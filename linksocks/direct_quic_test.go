package linksocks

import (
	"context"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog"
)

// startUDPForwarder relays raw datagrams between the given socket and the
// backend address. It is transparent packet routing: the peers still share one
// TLS session, so it must not affect authentication.
func startUDPForwarder(t *testing.T, sock net.PacketConn, backend net.Addr) {
	t.Helper()
	var mu sync.Mutex
	var clientAddr net.Addr
	buf := make([]byte, 64*1024)
	go func() {
		for {
			n, from, err := sock.ReadFrom(buf)
			if err != nil {
				return
			}
			mu.Lock()
			if from.String() == backend.String() {
				to := clientAddr
				mu.Unlock()
				if to != nil {
					_, _ = sock.WriteTo(buf[:n], to)
				}
				continue
			}
			clientAddr = from
			mu.Unlock()
			_, _ = sock.WriteTo(buf[:n], backend)
		}
	}()
}

// startQUICMediator accepts direct QUIC connections and terminates them: for
// every arriving connection it dials the real server, so each side completes
// TLS with the mediator (the client skips certificate verification, so any
// certificate is accepted). Every stream is copied between the two
// connections, which relays the authentication exchange byte for byte.
//
// This is the man-in-the-middle that connection binding must defeat: without
// binding, both peers would accept the relayed MACs and the mediator would sit
// inside a transport whose payload has no second encryption layer.
func startQUICMediator(t *testing.T, backendAddr net.Addr) (net.Addr, *atomic.Int64) {
	t.Helper()

	packet, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("mediator packet conn: %v", err)
	}
	tlsConf, err := newEphemeralDirectQUICTLSConfig()
	if err != nil {
		t.Fatalf("mediator tls config: %v", err)
	}
	conf := &quic.Config{
		HandshakeIdleTimeout: 500 * time.Millisecond,
		MaxIdleTimeout:       30 * time.Second,
		KeepAlivePeriod:      5 * time.Second,
	}
	tr := &quic.Transport{Conn: packet}
	ln, err := tr.Listen(tlsConf, conf)
	if err != nil {
		t.Fatalf("mediator listen: %v", err)
	}
	t.Cleanup(func() {
		_ = ln.Close()
		_ = tr.Close()
		_ = packet.Close()
	})

	clientTLS := newDirectQUICClientTLSConfig()
	proxied := &atomic.Int64{}
	go func() {
		for {
			victim, aerr := ln.Accept(context.Background())
			if aerr != nil {
				return
			}
			proxied.Add(1)
			go func(victim *quic.Conn) {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				upstream, derr := tr.Dial(ctx, backendAddr, clientTLS, conf)
				if derr != nil {
					_ = victim.CloseWithError(0, "mediator dial failed")
					return
				}
				defer upstream.CloseWithError(0, "mediator finished")
				for {
					vs, serr := victim.AcceptStream(ctx)
					if serr != nil {
						return
					}
					us, oerr := upstream.OpenStreamSync(ctx)
					if oerr != nil {
						return
					}
					go relayQUICStream(vs, us)
				}
			}(victim)
		}
	}()

	return packet.LocalAddr(), proxied
}

// relayQUICStream copies a stream in both directions, half-closing each side
// so the peer sees the fin.
func relayQUICStream(a, b *quic.Stream) {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(a, b)
		_ = a.Close()
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(b, a)
		_ = b.Close()
	}()
	wg.Wait()
}

// directQUICAuthRig starts one manager pair and dials it once. mediate, when
// non-nil, replaces the dial target; the callback receives the server's real
// address and returns the address the client must dial.
func directQUICAuthRig(t *testing.T, mediate func(serverAddr net.Addr) net.Addr) error {
	t.Helper()

	sessionID := uuid.New()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	serverPacket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("server packet conn: %v", err)
	}
	t.Cleanup(func() { _ = serverPacket.Close() })
	serverMgr, err := NewDirectQUICManagerWithHandshakeTimeout(serverPacket, sessionID, key, zerolog.Nop(), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("server manager: %v", err)
	}
	t.Cleanup(func() { _ = serverMgr.Close() })
	serverCtx, serverCancel := context.WithTimeout(context.Background(), 8*time.Second)
	t.Cleanup(serverCancel)
	go func() {
		_, _ = serverMgr.Connect(serverCtx, nil, true)
	}()

	dialTarget := serverPacket.LocalAddr().(*net.UDPAddr)
	if mediate != nil {
		dialTarget = mediate(serverPacket.LocalAddr()).(*net.UDPAddr)
	}

	clientPacket, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("client packet conn: %v", err)
	}
	t.Cleanup(func() { _ = clientPacket.Close() })
	clientMgr, err := NewDirectQUICManagerWithHandshakeTimeout(clientPacket, sessionID, key, zerolog.Nop(), 500*time.Millisecond)
	if err != nil {
		t.Fatalf("client manager: %v", err)
	}
	t.Cleanup(func() { _ = clientMgr.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	conn, err := clientMgr.Connect(ctx, []DirectCandidate{
		{Addr: "127.0.0.1", Port: dialTarget.Port, Kind: "host"},
	}, false)
	if err == nil {
		_ = conn.CloseWithError(0, "test finished")
	}
	return err
}

// A QUIC mediator that terminates one connection per peer relays the whole
// authentication exchange. The handshake proof is bound to the live TLS
// connection, so the relayed MACs must not verify.
func TestDirectQUICRejectsMediatedConnection(t *testing.T) {
	var proxied *atomic.Int64
	if err := directQUICAuthRig(t, func(serverAddr net.Addr) net.Addr {
		target, counter := startQUICMediator(t, serverAddr)
		proxied = counter
		return target
	}); err == nil {
		t.Fatal("authentication succeeded through a QUIC mediator; the handshake proof is not bound to the connection")
	}
	if proxied == nil || proxied.Load() == 0 {
		t.Fatal("the mediator never accepted a connection; the test did not exercise the attack path")
	}
}

// The same rig without a mediator must authenticate: connection binding must
// not break honest direct connections.
func TestDirectQUICAcceptsDirectConnection(t *testing.T) {
	if err := directQUICAuthRig(t, nil); err != nil {
		t.Fatalf("honest direct QUIC authentication failed: %v", err)
	}
}

// Transparent packet routing keeps one TLS session end to end, so it must
// still authenticate: the binding detects terminated connections, not relays.
func TestDirectQUICAcceptsTransparentForwarder(t *testing.T) {
	if err := directQUICAuthRig(t, func(serverAddr net.Addr) net.Addr {
		front, err := net.ListenPacket("udp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("forwarder packet conn: %v", err)
		}
		t.Cleanup(func() { _ = front.Close() })
		startUDPForwarder(t, front, serverAddr)
		return front.LocalAddr()
	}); err != nil {
		t.Fatalf("authentication failed through a transparent UDP forwarder: %v", err)
	}
}
