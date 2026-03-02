package linksocks

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/rs/zerolog"
)

const (
	stunDefaultPort = "3478"

	stunTypeBindingRequest  = 0x0001
	stunTypeBindingResponse = 0x0101

	stunAttrMappedAddress           = 0x0001
	stunAttrXorMappedAddress        = 0x0020
	stunMagicCookie          uint32 = 0x2112A442
)

type StunResult struct {
	Server    string
	Addr      string
	Port      int
	RTT       time.Duration
	Candidate DirectCandidate
}

func extractStunTxID(b []byte) ([12]byte, bool) {
	var txID [12]byte
	if len(b) < 20 {
		return txID, false
	}
	if binary.BigEndian.Uint32(b[4:8]) != stunMagicCookie {
		return txID, false
	}
	copy(txID[:], b[8:20])
	return txID, true
}

func normalizeStunServer(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		// Keep as-is; net.ResolveUDPAddr will fail on scheme and we treat it as invalid.
		return s
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		// Bracketed IPv6 without a port.
		return s + ":" + stunDefaultPort
	}
	// If host:port is missing port, add default.
	host, port, err := net.SplitHostPort(s)
	if err == nil {
		if host == "" {
			return ""
		}
		if port == "" {
			port = stunDefaultPort
		}
		return net.JoinHostPort(host, port)
	}
	// Try as bare host (IPv4/hostname), or IPv6 without port.
	if strings.Contains(s, ":") {
		// Likely IPv6 without port.
		return net.JoinHostPort(s, stunDefaultPort)
	}
	return net.JoinHostPort(s, stunDefaultPort)
}

func BuildStunBindingRequest(txID [12]byte) []byte {
	buf := make([]byte, 20)
	binary.BigEndian.PutUint16(buf[0:2], stunTypeBindingRequest)
	binary.BigEndian.PutUint16(buf[2:4], 0)
	binary.BigEndian.PutUint32(buf[4:8], stunMagicCookie)
	copy(buf[8:20], txID[:])
	return buf
}

func ParseStunBindingResponse(b []byte, txID [12]byte) (string, int, error) {
	if len(b) < 20 {
		return "", 0, errors.New("stun: response too short")
	}
	mt := binary.BigEndian.Uint16(b[0:2])
	if mt != stunTypeBindingResponse {
		return "", 0, fmt.Errorf("stun: unexpected message type: 0x%04x", mt)
	}
	msgLen := int(binary.BigEndian.Uint16(b[2:4]))
	if len(b) < 20+msgLen {
		return "", 0, errors.New("stun: invalid message length")
	}
	cookie := binary.BigEndian.Uint32(b[4:8])
	if cookie != stunMagicCookie {
		return "", 0, errors.New("stun: invalid magic cookie")
	}
	if !equalBytes(b[8:20], txID[:]) {
		return "", 0, errors.New("stun: transaction id mismatch")
	}

	attrs := b[20 : 20+msgLen]
	for len(attrs) >= 4 {
		at := binary.BigEndian.Uint16(attrs[0:2])
		al := int(binary.BigEndian.Uint16(attrs[2:4]))
		if len(attrs) < 4+al {
			return "", 0, errors.New("stun: truncated attribute")
		}
		av := attrs[4 : 4+al]
		// Attributes are padded to 4-byte boundary.
		pad := (4 - (al % 4)) % 4
		if len(attrs) < 4+al+pad {
			return "", 0, errors.New("stun: truncated attribute padding")
		}
		attrs = attrs[4+al+pad:]

		switch at {
		case stunAttrXorMappedAddress:
			addr, port, err := parseXorMappedAddress(av, txID)
			if err != nil {
				return "", 0, err
			}
			return addr, port, nil
		case stunAttrMappedAddress:
			addr, port, err := parseMappedAddress(av)
			if err != nil {
				return "", 0, err
			}
			return addr, port, nil
		}
	}

	return "", 0, errors.New("stun: missing mapped address")
}

func parseMappedAddress(av []byte) (string, int, error) {
	if len(av) < 4 {
		return "", 0, errors.New("stun: mapped address too short")
	}
	fam := av[1]
	port := int(binary.BigEndian.Uint16(av[2:4]))
	switch fam {
	case 0x01:
		if len(av) < 8 {
			return "", 0, errors.New("stun: mapped address invalid ipv4")
		}
		ip := net.IPv4(av[4], av[5], av[6], av[7])
		return ip.String(), port, nil
	case 0x02:
		if len(av) < 20 {
			return "", 0, errors.New("stun: mapped address invalid ipv6")
		}
		ip := net.IP(av[4:20])
		return ip.String(), port, nil
	default:
		return "", 0, fmt.Errorf("stun: unknown address family: %d", fam)
	}
}

func parseXorMappedAddress(av []byte, txID [12]byte) (string, int, error) {
	if len(av) < 4 {
		return "", 0, errors.New("stun: xor-mapped address too short")
	}
	fam := av[1]
	xPort := binary.BigEndian.Uint16(av[2:4])
	port := int(xPort ^ uint16(stunMagicCookie>>16))

	switch fam {
	case 0x01:
		if len(av) < 8 {
			return "", 0, errors.New("stun: xor-mapped address invalid ipv4")
		}
		cookie := make([]byte, 4)
		binary.BigEndian.PutUint32(cookie, stunMagicCookie)
		ip := net.IPv4(
			av[4]^cookie[0],
			av[5]^cookie[1],
			av[6]^cookie[2],
			av[7]^cookie[3],
		)
		return ip.String(), port, nil

	case 0x02:
		if len(av) < 20 {
			return "", 0, errors.New("stun: xor-mapped address invalid ipv6")
		}
		mask := make([]byte, 16)
		binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
		copy(mask[4:16], txID[:])
		out := make([]byte, 16)
		for i := 0; i < 16; i++ {
			out[i] = av[4+i] ^ mask[i]
		}
		return net.IP(out).String(), port, nil

	default:
		return "", 0, fmt.Errorf("stun: unknown address family: %d", fam)
	}
}

func equalBytes(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func StunDiscoverOne(ctx context.Context, server string, timeout time.Duration) (*StunResult, error) {
	server = normalizeStunServer(server)
	if server == "" {
		return nil, errors.New("stun: empty server")
	}
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, fmt.Errorf("stun: resolve server: %w", err)
	}

	conn, err := net.DialUDP("udp", nil, addr)
	if err != nil {
		return nil, fmt.Errorf("stun: dial: %w", err)
	}
	defer conn.Close()

	var txID [12]byte
	if _, err := rand.Read(txID[:]); err != nil {
		return nil, fmt.Errorf("stun: rand: %w", err)
	}

	req := BuildStunBindingRequest(txID)
	start := time.Now()

	deadline := start.Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, fmt.Errorf("stun: set deadline: %w", err)
	}

	if _, err := conn.Write(req); err != nil {
		return nil, fmt.Errorf("stun: write: %w", err)
	}

	buf := make([]byte, 1500)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("stun: read: %w", err)
	}

	rtt := time.Since(start)
	mappedAddr, mappedPort, err := ParseStunBindingResponse(buf[:n], txID)
	if err != nil {
		return nil, err
	}

	res := &StunResult{
		Server: server,
		Addr:   mappedAddr,
		Port:   mappedPort,
		RTT:    rtt,
		Candidate: DirectCandidate{
			Addr: mappedAddr,
			Port: mappedPort,
			Kind: "srflx",
		},
	}
	return res, nil
}

type StunDiscoverOption struct {
	Servers []string
	Timeout time.Duration
	Logger  zerolog.Logger
}

func defaultStunServers() []string {
	return []string{
		"stun.miwifi.com:3478",
		"stun.chat.bilibili.com:3478",
		"stun.cloudflare.com:3478",
		"stun.l.google.com:19302",
		"stun.qq.com:3478",
	}
}

func DefaultStunDiscoverOption() *StunDiscoverOption {
	return &StunDiscoverOption{
		Servers: defaultStunServers(),
		Timeout: 2 * time.Second,
		Logger:  zerolog.Nop(),
	}
}

func StunDiscover(ctx context.Context, opt *StunDiscoverOption) (*StunResult, error) {
	if opt == nil {
		opt = DefaultStunDiscoverOption()
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 2 * time.Second
	}
	servers := opt.Servers
	if len(servers) == 0 {
		servers = defaultStunServers()
	}
	if len(servers) == 0 {
		return nil, errors.New("stun: no servers")
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	resultCh := make(chan *StunResult, len(servers))
	errCh := make(chan error, len(servers))

	for _, s := range servers {
		server := s
		go func() {
			res, err := StunDiscoverOne(ctx, server, opt.Timeout)
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
			select {
			case resultCh <- res:
			default:
			}
		}()
	}

	var lastErr error
	remaining := len(servers)

	for remaining > 0 {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, ctx.Err()

		case res := <-resultCh:
			remaining--
			if res == nil {
				continue
			}
			opt.Logger.Debug().
				Str("server", res.Server).
				Str("addr", res.Addr).
				Int("port", res.Port).
				Int64("rtt_ms", res.RTT.Milliseconds()).
				Msg("STUN srflx discovered")
			// Return the first successful response to minimize startup latency.
			return res, nil

		case err := <-errCh:
			remaining--
			if err != nil {
				lastErr = err
			}
		}
	}

	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("stun: no results")
}

// StunDiscoverFromConn performs STUN discovery using an existing UDP socket.
// This is required when the caller needs the mapped address/port to match the
// socket that will later be used for NAT hole punching.
func StunDiscoverFromConn(ctx context.Context, conn *net.UDPConn, opt *StunDiscoverOption) (*StunResult, error) {
	if conn == nil {
		return nil, errors.New("stun: nil conn")
	}
	if opt == nil {
		opt = DefaultStunDiscoverOption()
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 2 * time.Second
	}
	servers := opt.Servers
	if len(servers) == 0 {
		servers = defaultStunServers()
	}
	if len(servers) == 0 {
		return nil, errors.New("stun: no servers")
	}

	type pendingReq struct {
		server   string
		addr     *net.UDPAddr
		txID     [12]byte
		sentAt   time.Time
		disabled bool
	}

	pendingCh := make(chan pendingReq, len(servers))
	var wg sync.WaitGroup
	for _, s := range servers {
		serverRaw := s
		wg.Add(1)
		go func() {
			defer wg.Done()
			server := normalizeStunServer(serverRaw)
			if server == "" {
				return
			}
			addr, err := net.ResolveUDPAddr("udp", server)
			if err != nil {
				return
			}
			var txID [12]byte
			if _, err := rand.Read(txID[:]); err != nil {
				return
			}
			req := BuildStunBindingRequest(txID)
			sentAt := time.Now()
			if _, err := conn.WriteToUDP(req, addr); err != nil {
				return
			}
			pendingCh <- pendingReq{server: server, addr: addr, txID: txID, sentAt: sentAt}
		}()
	}
	wg.Wait()
	close(pendingCh)

	pendings := make([]pendingReq, 0, len(servers))
	idxByTxID := make(map[[12]byte]int, len(servers))
	for p := range pendingCh {
		idxByTxID[p.txID] = len(pendings)
		pendings = append(pendings, p)
	}

	if len(pendings) == 0 {
		return nil, errors.New("stun: no valid servers")
	}

	start := time.Now()
	deadline := start.Add(opt.Timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("stun: set deadline: %w", err)
	}
	defer func() {
		// Ensure callers can safely reuse this UDP socket (e.g. for direct probing or QUIC).
		_ = conn.SetReadDeadline(time.Time{})
	}()

	buf := make([]byte, 1500)
	var lastErr error
	remaining := len(pendings)

	for remaining > 0 {
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return nil, lastErr
			}
			return nil, ctx.Err()
		default:
		}

		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			ne, ok := err.(net.Error)
			if ok && ne.Timeout() {
				if lastErr != nil {
					return nil, lastErr
				}
				return nil, errors.New("stun: timeout")
			}
			return nil, fmt.Errorf("stun: read: %w", err)
		}

		rxTxID, ok := extractStunTxID(buf[:n])
		if !ok {
			continue
		}

		matched, ok := idxByTxID[rxTxID]
		if !ok {
			continue
		}

		pending := &pendings[matched]
		if pending.disabled {
			continue
		}
		pending.disabled = true
		remaining--

		mappedAddr, mappedPort, err := ParseStunBindingResponse(buf[:n], pending.txID)
		if err != nil {
			lastErr = err
			continue
		}

		rtt := time.Since(pending.sentAt)
		res := &StunResult{
			Server: pending.server,
			Addr:   mappedAddr,
			Port:   mappedPort,
			RTT:    rtt,
			Candidate: DirectCandidate{
				Addr: mappedAddr,
				Port: mappedPort,
				Kind: "srflx",
			},
		}
		opt.Logger.Debug().
			Str("server", res.Server).
			Str("addr", res.Addr).
			Int("port", res.Port).
			Int64("rtt_ms", res.RTT.Milliseconds()).
			Msg("STUN srflx discovered")
		return res, nil
	}
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New("stun: no results")
}
