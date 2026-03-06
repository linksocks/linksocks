package linksocks

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	directProbeMagic   uint32 = 0x4C534850 // "LSHP"
	directProbeVersion byte   = 2

	directProbeTypeRequest  byte = 1
	directProbeTypeResponse byte = 2
)

type directProbePacket struct {
	Type      byte
	SessionID uuid.UUID
	Nonce     [8]byte
	MAC       [directProbeMACLen]byte
	HasMAC    bool
	Version   byte
}

func packDirectProbePacket(p directProbePacket) []byte {
	if p.Version == 0 {
		p.Version = directProbeVersion
	}
	if p.Version >= 2 {
		b := make([]byte, 4+1+1+16+8+directProbeMACLen)
		binary.BigEndian.PutUint32(b[0:4], directProbeMagic)
		b[4] = p.Version
		b[5] = p.Type
		copy(b[6:22], p.SessionID[:])
		copy(b[22:30], p.Nonce[:])
		copy(b[30:30+directProbeMACLen], p.MAC[:])
		return b
	}

	b := make([]byte, 4+1+1+16+8)
	binary.BigEndian.PutUint32(b[0:4], directProbeMagic)
	b[4] = p.Version
	b[5] = p.Type
	copy(b[6:22], p.SessionID[:])
	copy(b[22:30], p.Nonce[:])
	return b
}

func parseDirectProbePacket(b []byte) (directProbePacket, bool) {
	if len(b) < 4+1+1+16+8 {
		return directProbePacket{}, false
	}
	if binary.BigEndian.Uint32(b[0:4]) != directProbeMagic {
		return directProbePacket{}, false
	}
	ver := b[4]
	if ver != 1 && ver != 2 {
		return directProbePacket{}, false
	}

	var sid uuid.UUID
	copy(sid[:], b[6:22])
	var nonce [8]byte
	copy(nonce[:], b[22:30])
	if ver >= 2 {
		if len(b) < 4+1+1+16+8+directProbeMACLen {
			return directProbePacket{}, false
		}
		var mac [directProbeMACLen]byte
		copy(mac[:], b[30:30+directProbeMACLen])
		return directProbePacket{Type: b[5], SessionID: sid, Nonce: nonce, MAC: mac, HasMAC: true, Version: ver}, true
	}
	return directProbePacket{Type: b[5], SessionID: sid, Nonce: nonce, HasMAC: false, Version: ver}, true
}

type DirectProber struct {
	log        zerolog.Logger
	conn       *net.UDPConn
	sessionKey []byte

	mu       sync.Mutex
	session  uuid.UUID
	ready    bool
	readyCh  chan struct{}
	lastFrom *net.UDPAddr
}

func NewDirectProber(conn *net.UDPConn, sessionID uuid.UUID, sessionKey []byte, logger zerolog.Logger) *DirectProber {
	if logger.GetLevel() == zerolog.NoLevel {
		logger = zerolog.Nop()
	}
	return &DirectProber{
		log:        logger,
		conn:       conn,
		sessionKey: append([]byte(nil), sessionKey...),
		session:    sessionID,
		ready:      false,
		readyCh:    make(chan struct{}),
	}
}

func (p *DirectProber) ReadyChan() <-chan struct{} {
	p.mu.Lock()
	ch := p.readyCh
	p.mu.Unlock()
	return ch
}

func (p *DirectProber) markReady(from *net.UDPAddr) {
	p.mu.Lock()
	if p.ready {
		p.mu.Unlock()
		return
	}
	p.ready = true
	p.lastFrom = from
	ch := p.readyCh
	close(ch)
	p.mu.Unlock()
}

func (p *DirectProber) Start(ctx context.Context) {
	if p.conn == nil {
		return
	}
	// This prober temporarily uses read deadlines for polling. When the same UDP
	// socket is later reused by the QUIC transport, ensure we clear deadlines on
	// exit so quic-go reads don't get spurious timeouts.
	defer func() {
		_ = p.conn.SetReadDeadline(time.Time{})
	}()

	buf := make([]byte, 2048)
	for {
		select {
		case <-ctx.Done():
			return
		default:
			_ = p.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, from, err := p.conn.ReadFromUDP(buf)
			if err != nil {
				ne, ok := err.(net.Error)
				if ok && ne.Timeout() {
					continue
				}
				return
			}

			pkt, ok := parseDirectProbePacket(buf[:n])
			if !ok {
				continue
			}
			if pkt.SessionID != p.session {
				continue
			}

			if len(p.sessionKey) > 0 {
				// If we have a session key, require authenticated probe packets.
				if pkt.Version < 2 || !pkt.HasMAC {
					continue
				}
				if !verifyDirectProbeMAC(p.sessionKey, pkt.Version, pkt.Type, pkt.SessionID, pkt.Nonce, pkt.MAC) {
					continue
				}
			}

			switch pkt.Type {
			case directProbeTypeRequest:
				resp := directProbePacket{Type: directProbeTypeResponse, SessionID: p.session, Nonce: pkt.Nonce, Version: pkt.Version}
				if len(p.sessionKey) > 0 {
					resp.Version = 2
					resp.MAC = computeDirectProbeMAC(p.sessionKey, resp.Version, resp.Type, resp.SessionID, resp.Nonce)
					resp.HasMAC = true
				}
				_, _ = p.conn.WriteToUDP(packDirectProbePacket(resp), from)
				// If we can receive a valid probe request from the peer (and respond),
				// the UDP path is already usable. Mark ready so the higher-level direct
				// agent can stop retrying and proceed.
				p.markReady(from)

			case directProbeTypeResponse:
				p.markReady(from)
			}
		}
	}
}

type DirectProbeResult struct {
	From *net.UDPAddr
	RTT  time.Duration
}

func (p *DirectProber) Probe(ctx context.Context, candidates []DirectCandidate, perCandidateTimeout time.Duration) (*DirectProbeResult, error) {
	if p.conn == nil {
		return nil, errors.New("direct: nil udp conn")
	}
	if perCandidateTimeout <= 0 {
		perCandidateTimeout = 800 * time.Millisecond
	}

candidateLoop:
	for _, c := range candidates {
		// Historically only srflx candidates were used (from STUN). For local/LAN
		// testing we also allow host candidates.
		if c.Kind != "" && c.Kind != "srflx" && c.Kind != "host" {
			continue
		}
		if c.Addr == "" || c.Port <= 0 {
			continue
		}

		addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(c.Addr, strconv.Itoa(c.Port)))
		if err != nil {
			continue
		}

		var nonce [8]byte
		_, _ = rand.Read(nonce[:])
		req := directProbePacket{Type: directProbeTypeRequest, SessionID: p.session, Nonce: nonce, Version: 1}
		if len(p.sessionKey) > 0 {
			req.Version = 2
			req.MAC = computeDirectProbeMAC(p.sessionKey, req.Version, req.Type, req.SessionID, req.Nonce)
			req.HasMAC = true
		}
		payload := packDirectProbePacket(req)

		start := time.Now()
		deadline := start.Add(perCandidateTimeout)
		if dl, ok := ctx.Deadline(); ok && dl.Before(deadline) {
			deadline = dl
		}
		tick := time.NewTicker(120 * time.Millisecond)
		for {
			select {
			case <-ctx.Done():
				tick.Stop()
				return nil, ctx.Err()
			case <-p.ReadyChan():
				tick.Stop()
				p.mu.Lock()
				from := p.lastFrom
				p.mu.Unlock()
				return &DirectProbeResult{From: from, RTT: time.Since(start)}, nil
			case <-tick.C:
				if time.Now().After(deadline) {
					tick.Stop()
					continue candidateLoop
				}
				_, _ = p.conn.WriteToUDP(payload, addr)
			}
		}
	}

	return nil, errors.New("direct: no reachable candidate")
}
