package linksocks

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	directProbeMagic   uint32 = 0x4C534850 // "LSHP"
	directProbeVersion byte   = 1

	directProbeTypeRequest  byte = 1
	directProbeTypeResponse byte = 2
)

type directProbePacket struct {
	Type      byte
	SessionID uuid.UUID
	Nonce     [8]byte
}

func packDirectProbePacket(p directProbePacket) []byte {
	b := make([]byte, 4+1+1+16+8)
	binary.BigEndian.PutUint32(b[0:4], directProbeMagic)
	b[4] = directProbeVersion
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
	if b[4] != directProbeVersion {
		return directProbePacket{}, false
	}

	var sid uuid.UUID
	copy(sid[:], b[6:22])
	var nonce [8]byte
	copy(nonce[:], b[22:30])
	return directProbePacket{Type: b[5], SessionID: sid, Nonce: nonce}, true
}

type DirectProber struct {
	log  zerolog.Logger
	conn *net.UDPConn

	mu       sync.Mutex
	session  uuid.UUID
	ready    bool
	readyCh  chan struct{}
	lastFrom *net.UDPAddr
}

func NewDirectProber(conn *net.UDPConn, sessionID uuid.UUID, logger zerolog.Logger) *DirectProber {
	if logger.GetLevel() == zerolog.NoLevel {
		logger = zerolog.Nop()
	}
	return &DirectProber{
		log:     logger,
		conn:    conn,
		session: sessionID,
		ready:   false,
		readyCh: make(chan struct{}),
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

			switch pkt.Type {
			case directProbeTypeRequest:
				resp := directProbePacket{Type: directProbeTypeResponse, SessionID: p.session, Nonce: pkt.Nonce}
				_, _ = p.conn.WriteToUDP(packDirectProbePacket(resp), from)

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
		if c.Kind != "srflx" {
			continue
		}
		if c.Addr == "" || c.Port <= 0 {
			continue
		}

		addr, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", c.Addr, c.Port))
		if err != nil {
			continue
		}

		var nonce [8]byte
		_, _ = rand.Read(nonce[:])
		req := directProbePacket{Type: directProbeTypeRequest, SessionID: p.session, Nonce: nonce}
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
