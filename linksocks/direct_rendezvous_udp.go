package linksocks

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	directRendezvousMagic   uint32 = 0x4C535255 // "LSRU"
	directRendezvousVersion byte   = 1

	directRendezvousTypeBindingRequest  byte = 1
	directRendezvousTypeBindingResponse byte = 2
)

// startDirectRendezvousUDP starts a minimal STUN-like UDP service.
// It is optional and must be explicitly enabled.
func (s *LinkSocksServer) startDirectRendezvousUDP(parent context.Context) error {
	if !s.directRendezvousEnable {
		return nil
	}

	s.mu.Lock()
	if s.directRendezvousConn != nil {
		s.mu.Unlock()
		return nil
	}

	host := strings.TrimSpace(s.directRendezvousHost)
	port := s.directRendezvousPort
	if host == "" {
		host = s.wsHost
		if host == "" {
			host = "0.0.0.0"
		}
	}
	if port <= 0 {
		// Default to the same port as the WS server for simpler deployments.
		if s.wsPort > 0 {
			port = s.wsPort
		} else {
			port = 0
		}
	}

	addr := &net.UDPAddr{IP: net.ParseIP(host), Port: port}
	if addr.IP == nil {
		// If host is not an IP, try resolving as UDP addr.
		ua, err := net.ResolveUDPAddr("udp", net.JoinHostPort(host, fmt.Sprintf("%d", port)))
		if err != nil {
			s.mu.Unlock()
			return fmt.Errorf("direct rendezvous: resolve udp addr: %w", err)
		}
		addr = ua
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("direct rendezvous: listen udp: %w", err)
	}

	ctx, cancel := context.WithCancel(parent)
	s.directRendezvousConn = conn
	s.directRendezvousCancel = cancel
	la := conn.LocalAddr().(*net.UDPAddr)
	s.directRendezvousPort = la.Port
	s.mu.Unlock()

	s.log.Info().Str("listen", la.String()).Msg("Direct UDP rendezvous enabled")
	go s.directRendezvousUDPServe(ctx, conn)
	return nil
}

type directRendezvousPacket struct {
	Type    byte
	Version byte
	Nonce   [8]byte
}

func packDirectRendezvousPacket(p directRendezvousPacket) []byte {
	b := make([]byte, 4+1+1+8)
	binary.BigEndian.PutUint32(b[0:4], directRendezvousMagic)
	b[4] = p.Version
	b[5] = p.Type
	copy(b[6:14], p.Nonce[:])
	return b
}

func parseDirectRendezvousPacket(b []byte) (directRendezvousPacket, bool) {
	if len(b) < 4+1+1+8 {
		return directRendezvousPacket{}, false
	}
	if binary.BigEndian.Uint32(b[0:4]) != directRendezvousMagic {
		return directRendezvousPacket{}, false
	}
	ver := b[4]
	if ver != directRendezvousVersion {
		return directRendezvousPacket{}, false
	}
	typ := b[5]
	if typ != directRendezvousTypeBindingRequest {
		return directRendezvousPacket{}, false
	}
	var nonce [8]byte
	copy(nonce[:], b[6:14])
	return directRendezvousPacket{Type: typ, Version: ver, Nonce: nonce}, true
}

func saddrToCandidate(from *net.UDPAddr) DirectCandidate {
	if from == nil {
		return DirectCandidate{}
	}
	ip := from.IP
	if ip == nil {
		return DirectCandidate{}
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	return DirectCandidate{Addr: ip.String(), Port: from.Port, Kind: "srflx"}
}

func (s *LinkSocksServer) directRendezvousUDPServe(ctx context.Context, conn *net.UDPConn) {
	if conn == nil {
		return
	}

	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		_ = conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
		n, from, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			return
		}

		pkt, ok := parseDirectRendezvousPacket(buf[:n])
		if !ok {
			continue
		}
		cand := saddrToCandidate(from)
		if cand.Addr == "" || cand.Port <= 0 {
			continue
		}

		resp := buildDirectRendezvousBindingResponse(pkt.Nonce, cand)
		_, _ = conn.WriteToUDP(resp, from)
	}
}

func buildDirectRendezvousBindingResponse(nonce [8]byte, cand DirectCandidate) []byte {
	ip := net.ParseIP(cand.Addr)
	if ip == nil {
		ip = net.IPv4(0, 0, 0, 0)
	}
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}

	// Format:
	// magic(4) ver(1) type(1) nonce(8) family(1) ipLen(1) ip(ipLen) port(2)
	ipBytes := []byte(ip)
	ipLen := len(ipBytes)
	if ipLen > 255 {
		ipLen = 0
		ipBytes = nil
	}
	family := byte(1) // ipv4 by default
	if len(ipBytes) == 16 {
		family = 2
	}

	b := make([]byte, 0, 4+1+1+8+1+1+ipLen+2)
	tmp := make([]byte, 4)
	binary.BigEndian.PutUint32(tmp, directRendezvousMagic)
	b = append(b, tmp...)
	b = append(b, directRendezvousVersion)
	b = append(b, directRendezvousTypeBindingResponse)
	b = append(b, nonce[:]...)
	b = append(b, family)
	b = append(b, byte(ipLen))
	b = append(b, ipBytes...)
	port := make([]byte, 2)
	binary.BigEndian.PutUint16(port, uint16(cand.Port))
	b = append(b, port...)
	return b
}

// DirectRendezvousDiscoverFromConn asks the server UDP rendezvous endpoint to
// reflect back the observed src ip:port, using the provided UDP socket.
func DirectRendezvousDiscoverFromConn(ctx context.Context, conn *net.UDPConn, server string, timeout time.Duration) (*DirectCandidate, error) {
	if conn == nil {
		return nil, errors.New("direct rendezvous: nil conn")
	}
	server = strings.TrimSpace(server)
	if server == "" {
		return nil, errors.New("direct rendezvous: empty server")
	}
	addr, err := net.ResolveUDPAddr("udp", server)
	if err != nil {
		return nil, fmt.Errorf("direct rendezvous: resolve server: %w", err)
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	req := packDirectRendezvousPacket(directRendezvousPacket{Type: directRendezvousTypeBindingRequest, Version: directRendezvousVersion, Nonce: nonce})
	if _, err := conn.WriteToUDP(req, addr); err != nil {
		return nil, fmt.Errorf("direct rendezvous: write: %w", err)
	}

	start := time.Now()
	deadline := start.Add(timeout)
	if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
		deadline = dl
	}
	if err := conn.SetReadDeadline(deadline); err != nil {
		return nil, fmt.Errorf("direct rendezvous: set read deadline: %w", err)
	}
	defer func() { _ = conn.SetReadDeadline(time.Time{}) }()

	buf := make([]byte, 2048)
	for {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			return nil, fmt.Errorf("direct rendezvous: read: %w", err)
		}
		cand, ok := parseDirectRendezvousBindingResponse(buf[:n], nonce)
		if !ok {
			continue
		}
		cand.Kind = "srflx"
		return &cand, nil
	}
}

func parseDirectRendezvousBindingResponse(b []byte, nonce [8]byte) (DirectCandidate, bool) {
	// magic(4) ver(1) type(1) nonce(8) family(1) ipLen(1) ip(ipLen) port(2)
	if len(b) < 4+1+1+8+1+1+2 {
		return DirectCandidate{}, false
	}
	if binary.BigEndian.Uint32(b[0:4]) != directRendezvousMagic {
		return DirectCandidate{}, false
	}
	if b[4] != directRendezvousVersion {
		return DirectCandidate{}, false
	}
	if b[5] != directRendezvousTypeBindingResponse {
		return DirectCandidate{}, false
	}
	if !equalBytes(b[6:14], nonce[:]) {
		return DirectCandidate{}, false
	}
	ipLen := int(b[15])
	if len(b) < 16+ipLen+2 {
		return DirectCandidate{}, false
	}
	ipBytes := b[16 : 16+ipLen]
	port := int(binary.BigEndian.Uint16(b[16+ipLen : 16+ipLen+2]))
	if port <= 0 || port > 65535 {
		return DirectCandidate{}, false
	}
	ip := net.IP(ipBytes)
	if ip4 := ip.To4(); ip4 != nil {
		ip = ip4
	}
	if len(ip) == 0 {
		return DirectCandidate{}, false
	}
	return DirectCandidate{Addr: ip.String(), Port: port}, true
}
