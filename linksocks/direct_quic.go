package linksocks

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog"
)

const (
	directQUICHandshakeMagic   uint32 = 0x4C535148 // "LSQH"
	directQUICHandshakeVersion byte   = 1

	directQUICHandshakeTypeHello byte = 1
	directQUICHandshakeTypeAck   byte = 2
)

const directQUICHandshakeLen = 4 + 1 + 1 + 16 + 8 + directProbeMACLen

type directQUICHandshake struct {
	Version   byte
	Type      byte
	SessionID uuid.UUID
	Nonce     [8]byte
	MAC       [directProbeMACLen]byte
}

func computeDirectQUICHandshakeMAC(sessionKey []byte, version byte, typ byte, sessionID uuid.UUID, nonce [8]byte) [directProbeMACLen]byte {
	var out [directProbeMACLen]byte
	if len(sessionKey) == 0 {
		return out
	}

	b := make([]byte, 0, 1+1+16+8)
	b = append(b, version)
	b = append(b, typ)
	b = append(b, sessionID[:]...)
	b = append(b, nonce[:]...)

	m := hmac.New(sha256.New, sessionKey)
	_, _ = m.Write(b)
	sum := m.Sum(nil)
	copy(out[:], sum[:directProbeMACLen])
	return out
}

func verifyDirectQUICHandshakeMAC(sessionKey []byte, version byte, typ byte, sessionID uuid.UUID, nonce [8]byte, mac [directProbeMACLen]byte) bool {
	if len(sessionKey) == 0 {
		return false
	}
	expected := computeDirectQUICHandshakeMAC(sessionKey, version, typ, sessionID, nonce)
	return subtle.ConstantTimeCompare(expected[:], mac[:]) == 1
}

func packDirectQUICHandshake(h directQUICHandshake) []byte {
	b := make([]byte, directQUICHandshakeLen)
	binary.BigEndian.PutUint32(b[0:4], directQUICHandshakeMagic)
	b[4] = h.Version
	b[5] = h.Type
	copy(b[6:22], h.SessionID[:])
	copy(b[22:30], h.Nonce[:])
	copy(b[30:30+directProbeMACLen], h.MAC[:])
	return b
}

func parseDirectQUICHandshake(b []byte) (directQUICHandshake, bool) {
	if len(b) < directQUICHandshakeLen {
		return directQUICHandshake{}, false
	}
	if binary.BigEndian.Uint32(b[0:4]) != directQUICHandshakeMagic {
		return directQUICHandshake{}, false
	}
	ver := b[4]
	if ver != directQUICHandshakeVersion {
		return directQUICHandshake{}, false
	}
	typ := b[5]
	if typ != directQUICHandshakeTypeHello && typ != directQUICHandshakeTypeAck {
		return directQUICHandshake{}, false
	}
	var sid uuid.UUID
	copy(sid[:], b[6:22])
	var nonce [8]byte
	copy(nonce[:], b[22:30])
	var mac [directProbeMACLen]byte
	copy(mac[:], b[30:30+directProbeMACLen])
	return directQUICHandshake{Version: ver, Type: typ, SessionID: sid, Nonce: nonce, MAC: mac}, true
}

func newEphemeralDirectQUICTLSConfig() (*tls.Config, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, err
	}

	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return nil, err
	}

	now := time.Now()
	tmpl := x509.Certificate{
		SerialNumber: serial,
		NotBefore:    now.Add(-1 * time.Hour),
		NotAfter:     now.Add(24 * time.Hour),

		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, key.Public(), key)
	if err != nil {
		return nil, err
	}

	cert := tls.Certificate{
		Certificate: [][]byte{der},
		PrivateKey:  key,
	}

	return &tls.Config{
		Certificates: []tls.Certificate{cert},
		NextProtos:   []string{"linksocks-direct"},
		MinVersion:   tls.VersionTLS13,
	}, nil
}

func newDirectQUICClientTLSConfig() *tls.Config {
	return &tls.Config{
		InsecureSkipVerify: true,
		NextProtos:         []string{"linksocks-direct"},
		MinVersion:         tls.VersionTLS13,
		ServerName:         "linksocks",
	}
}

func directQUICClientAuth(ctx context.Context, conn *quic.Conn, sessionID uuid.UUID, sessionKey []byte) error {
	s, err := conn.OpenStreamSync(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	var nonce [8]byte
	_, _ = rand.Read(nonce[:])
	h := directQUICHandshake{Version: directQUICHandshakeVersion, Type: directQUICHandshakeTypeHello, SessionID: sessionID, Nonce: nonce}
	h.MAC = computeDirectQUICHandshakeMAC(sessionKey, h.Version, h.Type, h.SessionID, h.Nonce)

	if _, err := s.Write(packDirectQUICHandshake(h)); err != nil {
		return err
	}

	buf := make([]byte, directQUICHandshakeLen)
	if _, err := io.ReadFull(s, buf); err != nil {
		return err
	}
	ack, ok := parseDirectQUICHandshake(buf)
	if !ok {
		return errors.New("direct quic: invalid handshake ack")
	}
	if ack.Type != directQUICHandshakeTypeAck {
		return errors.New("direct quic: unexpected handshake type")
	}
	if ack.SessionID != sessionID {
		return errors.New("direct quic: session_id mismatch")
	}
	if ack.Nonce != nonce {
		return errors.New("direct quic: nonce mismatch")
	}
	if !verifyDirectQUICHandshakeMAC(sessionKey, ack.Version, ack.Type, ack.SessionID, ack.Nonce, ack.MAC) {
		return errors.New("direct quic: invalid ack mac")
	}
	return nil
}

func directQUICServerAuth(ctx context.Context, conn *quic.Conn, sessionID uuid.UUID, sessionKey []byte) error {
	s, err := conn.AcceptStream(ctx)
	if err != nil {
		return err
	}
	defer s.Close()

	buf := make([]byte, directQUICHandshakeLen)
	if _, err := io.ReadFull(s, buf); err != nil {
		return err
	}
	h, ok := parseDirectQUICHandshake(buf)
	if !ok {
		return errors.New("direct quic: invalid handshake")
	}
	if h.Type != directQUICHandshakeTypeHello {
		return errors.New("direct quic: unexpected handshake type")
	}
	if h.SessionID != sessionID {
		return errors.New("direct quic: session_id mismatch")
	}
	if !verifyDirectQUICHandshakeMAC(sessionKey, h.Version, h.Type, h.SessionID, h.Nonce, h.MAC) {
		return errors.New("direct quic: invalid hello mac")
	}

	ack := directQUICHandshake{Version: directQUICHandshakeVersion, Type: directQUICHandshakeTypeAck, SessionID: sessionID, Nonce: h.Nonce}
	ack.MAC = computeDirectQUICHandshakeMAC(sessionKey, ack.Version, ack.Type, ack.SessionID, ack.Nonce)
	if _, err := s.Write(packDirectQUICHandshake(ack)); err != nil {
		return err
	}
	return nil
}

type DirectQUICManager struct {
	log zerolog.Logger

	packetConn net.PacketConn
	transport  *quic.Transport
	listener   *quic.Listener
	quicConf   *quic.Config
	serverTLS  *tls.Config
	clientTLS  *tls.Config

	sessionID  uuid.UUID
	sessionKey []byte

	mu     sync.Mutex
	active *quic.Conn
	closed bool
}

func NewDirectQUICManager(conn net.PacketConn, sessionID uuid.UUID, sessionKey []byte, logger zerolog.Logger) (*DirectQUICManager, error) {
	if conn == nil {
		return nil, errors.New("direct quic: nil packet conn")
	}
	if logger.GetLevel() == zerolog.NoLevel {
		logger = zerolog.Nop()
	}
	serverTLS, err := newEphemeralDirectQUICTLSConfig()
	if err != nil {
		return nil, err
	}

	return &DirectQUICManager{
		log:        logger,
		packetConn: conn,
		sessionID:  sessionID,
		sessionKey: append([]byte(nil), sessionKey...),
		serverTLS:  serverTLS,
		clientTLS:  newDirectQUICClientTLSConfig(),
		quicConf: &quic.Config{
			HandshakeIdleTimeout: 2 * time.Second,
			MaxIdleTimeout:       30 * time.Second,
			KeepAlivePeriod:      5 * time.Second,
		},
	}, nil
}

func (m *DirectQUICManager) Connect(ctx context.Context, candidates []DirectCandidate) (*quic.Conn, error) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil, errors.New("direct quic: manager closed")
	}
	if m.active != nil {
		c := m.active
		m.mu.Unlock()
		return c, nil
	}
	m.mu.Unlock()

	if m.sessionID == uuid.Nil {
		return nil, errors.New("direct quic: empty session_id")
	}
	if len(m.sessionKey) == 0 {
		return nil, errors.New("direct quic: empty session_key")
	}

	tr := &quic.Transport{Conn: m.packetConn}
	ln, err := tr.Listen(m.serverTLS, m.quicConf)
	if err != nil {
		_ = tr.Close()
		return nil, err
	}

	m.mu.Lock()
	m.transport = tr
	m.listener = ln
	m.mu.Unlock()

	ctxConn, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		once sync.Once
		win  *quic.Conn
		werr error
		done = make(chan struct{})
	)

	setWinner := func(c *quic.Conn, err error) {
		once.Do(func() {
			win = c
			werr = err
			close(done)
			cancel()
		})
	}

	go func() {
		for {
			c, err := ln.Accept(ctxConn)
			if err != nil {
				return
			}
			go func(conn *quic.Conn) {
				authCtx, cancelAuth := context.WithTimeout(ctxConn, 2*time.Second)
				err := directQUICServerAuth(authCtx, conn, m.sessionID, m.sessionKey)
				cancelAuth()
				if err != nil {
					_ = conn.CloseWithError(0, "auth failed")
					return
				}
				setWinner(conn, nil)
			}(c)
		}
	}()

	for _, cand := range candidates {
		if cand.Addr == "" || cand.Port <= 0 {
			continue
		}
		addr, err := net.ResolveUDPAddr("udp", net.JoinHostPort(cand.Addr, fmt.Sprintf("%d", cand.Port)))
		if err != nil {
			continue
		}

		go func(raddr net.Addr) {
			dialCtx, cancelDial := context.WithTimeout(ctxConn, 3*time.Second)
			c, err := tr.Dial(dialCtx, raddr, m.clientTLS, m.quicConf)
			cancelDial()
			if err != nil {
				return
			}
			authCtx, cancelAuth := context.WithTimeout(ctxConn, 2*time.Second)
			err = directQUICClientAuth(authCtx, c, m.sessionID, m.sessionKey)
			cancelAuth()
			if err != nil {
				_ = c.CloseWithError(0, "auth failed")
				return
			}
			setWinner(c, nil)
		}(addr)
	}

	select {
	case <-done:
		if werr != nil {
			m.Close()
			return nil, werr
		}
		if win == nil {
			m.Close()
			return nil, errors.New("direct quic: no connection")
		}
		m.mu.Lock()
		m.active = win
		m.mu.Unlock()
		_ = ln.Close()
		return win, nil

	case <-ctx.Done():
		m.Close()
		return nil, ctx.Err()
	}
}

func (m *DirectQUICManager) Active() *quic.Conn {
	m.mu.Lock()
	c := m.active
	m.mu.Unlock()
	if c == nil {
		return nil
	}
	// Treat closed connections as inactive so callers can reconnect.
	select {
	case <-c.Context().Done():
		return nil
	default:
		return c
	}
}

func (m *DirectQUICManager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	ln := m.listener
	tr := m.transport
	c := m.active
	m.mu.Unlock()

	if c != nil {
		_ = c.CloseWithError(0, "closing")
	}
	if ln != nil {
		_ = ln.Close()
	}
	if tr != nil {
		return tr.Close()
	}
	return nil
}
