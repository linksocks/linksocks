package linksocks

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/linksocks/linksocks/internal/ech"
	"github.com/linksocks/linksocks/internal/echtest"
	"github.com/rs/zerolog"
)

// A load-balancer entry point answers the handshake with a redirect to a healthy
// backend, exactly like the linksocks.js lb-entry worker in front of l.zetx.tech.
func TestWebSocketDialFollowsRelayRedirect(t *testing.T) {
	received := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			received <- "upgrade failed: " + err.Error()
			return
		}
		defer conn.Close()
		received <- request.URL.RequestURI()
	}))
	defer backend.Close()

	entry := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		target := "ws" + strings.TrimPrefix(backend.URL, "http") + request.URL.RequestURI()
		http.Redirect(response, request, target, http.StatusFound)
	}))
	defer entry.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx,
		"ws"+strings.TrimPrefix(entry.URL, "http")+"/socket?token=abc&web=1",
		relayDialOptions{Logger: zerolog.Nop()})
	if err != nil {
		t.Fatalf("dial through redirect: %v", err)
	}
	defer conn.Close()

	select {
	case got := <-received:
		if !strings.Contains(got, "/socket?token=abc&web=1") {
			t.Fatalf("backend request URI = %q, want the original query string", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("backend never received the redirected handshake")
	}
}

func TestWebSocketDialReportsRedirectLoop(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "ws"+strings.TrimPrefix(server.URL, "http")+request.URL.RequestURI(), http.StatusFound)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := dialRelayWebSocket(ctx,
		"ws"+strings.TrimPrefix(server.URL, "http")+"/socket",
		relayDialOptions{Logger: zerolog.Nop()})
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("dial error = %v, want too many redirects", err)
	}
}

// A relay without ECH support rejects the offer; the dial must fall back to a
// plain ClientHello so a privacy feature never costs connectivity.
func TestWebSocketDialFallsBackWhenECHIsRejected(t *testing.T) {
	upgraded := make(chan bool, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			upgraded <- false
			return
		}
		defer conn.Close()
		upgraded <- true
	}))
	defer server.Close()

	configList, _, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx, "wss://"+strings.TrimPrefix(server.URL, "https://")+"/socket", relayDialOptions{
		Insecure:    true,
		ECHMode:     ECHAuto,
		echOverride: configList,
		Logger:      zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("dial failed although the fallback should have connected: %v", err)
	}
	defer conn.Close()
	select {
	case ok := <-upgraded:
		if !ok {
			t.Fatal("server never saw a successful WebSocket upgrade")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the upgrade")
	}
}

// ECH off must not offer ECH at all.
func TestWebSocketDialECHAutoSkipsPlainWebSocket(t *testing.T) {
	conn, err := dialRelayWebSocket(context.Background(), "ws://127.0.0.1:1/socket", relayDialOptions{
		ECHMode: ECHAuto,
		Logger:  zerolog.Nop(),
	})
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("dial to a closed port succeeded")
	}
}

// sniffedClientHello is what the middlebox stand-in below records per
// connection.
type sniffedClientHello struct {
	ech bool
	sni string
}

// resettingECHListener stands in front of the TLS server and mimics the
// middlebox from the field: a ClientHello without an ECH extension is reset
// before the handshake completes; one that carries the ECH extension is handed
// to the TLS server, with the already-read bytes replayed.
type resettingECHListener struct {
	net.Listener
	seen chan sniffedClientHello
}

func (l *resettingECHListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		header := make([]byte, 5)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(conn, header); err != nil {
			conn.Close()
			continue
		}
		payload := make([]byte, int(header[3])<<8|int(header[4]))
		if _, err := io.ReadFull(conn, payload); err != nil {
			conn.Close()
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		hasECH, sni := parseClientHello(payload)
		l.seen <- sniffedClientHello{ech: hasECH, sni: sni}
		if !hasECH {
			// Closing with unread data left the client a reset, which is what
			// the field middlebox produces.
			conn.Close()
			continue
		}
		return &prefixedConn{Conn: conn, prefix: append(header, payload...)}, nil
	}
}

type prefixedConn struct {
	net.Conn
	prefix []byte
}

func (c *prefixedConn) Read(p []byte) (int, error) {
	if len(c.prefix) > 0 {
		n := copy(p, c.prefix)
		c.prefix = c.prefix[n:]
		return n, nil
	}
	return c.Conn.Read(p)
}

// parseClientHello reports whether a ClientHello handshake message carries the
// ECH extension (0xfe0d) and returns the outer server name, if any.
func parseClientHello(payload []byte) (bool, string) {
	if len(payload) < 4 || payload[0] != 0x01 {
		return false, ""
	}
	body := payload[4:]
	if len(body) < 2+32+1 {
		return false, ""
	}
	body = body[2+32:] // legacy_version, random
	session := int(body[0])
	body = body[1:]
	if len(body) < session+2 {
		return false, ""
	}
	body = body[session:]
	ciphers := int(binary.BigEndian.Uint16(body))
	body = body[2:]
	if len(body) < ciphers+1 {
		return false, ""
	}
	body = body[ciphers:]
	compression := int(body[0])
	body = body[1:]
	if len(body) < compression+2 {
		return false, ""
	}
	body = body[compression:]
	extensions := int(binary.BigEndian.Uint16(body))
	body = body[2:]
	if len(body) < extensions {
		extensions = len(body)
	}
	body = body[:extensions]

	var hasECH bool
	var sni string
	for len(body) >= 4 {
		extType := binary.BigEndian.Uint16(body)
		extLen := int(binary.BigEndian.Uint16(body[2:]))
		body = body[4:]
		if len(body) < extLen {
			break
		}
		data := body[:extLen]
		body = body[extLen:]
		switch extType {
		case 0xfe0d:
			hasECH = true
		case 0x0000:
			if len(data) >= 5 {
				nameLen := int(binary.BigEndian.Uint16(data[3:]))
				if len(data) >= 5+nameLen {
					sni = string(data[5 : 5+nameLen])
				}
			}
		}
	}
	return hasECH, sni
}

// The field failure this covers: a network resets TLS connections to the relay
// hostname and no ECH config can be discovered. The dial must retry once with
// the bootstrap ECH offer; the relay rejects the throwaway key and hands out
// real retry configs, and the dial must then connect with those.
func TestWebSocketDialBootstrapsECHAfterReset(t *testing.T) {
	_, key, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}
	// A real edge publishes its config in ECH rejection responses
	// (retry_configs); Go only advertises keys marked SendAsRetry.
	key.SendAsRetry = true

	upgraded := make(chan bool, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			upgraded <- false
			return
		}
		defer conn.Close()
		upgraded <- true
	}))
	seen := make(chan sniffedClientHello, 8)
	server.Listener = &resettingECHListener{Listener: server.Listener, seen: seen}
	server.TLS = &tls.Config{EncryptedClientHelloKeys: []tls.EncryptedClientHelloKey{key}}
	server.StartTLS()
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx, "wss://"+server.Listener.Addr().String()+"/socket", relayDialOptions{
		Insecure: true,
		ECHMode:  ECHAuto,
		Logger:   zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("dial after bootstrap: %v", err)
	}
	defer conn.Close()

	readHello := func(number int) sniffedClientHello {
		t.Helper()
		select {
		case hello := <-seen:
			return hello
		case <-time.After(5 * time.Second):
			t.Fatalf("middlebox never saw ClientHello %d", number)
			return sniffedClientHello{}
		}
	}
	first := readHello(1)
	if first.ech {
		t.Fatal("the first ClientHello already carried ECH; the block was not simulated")
	}
	second := readHello(2)
	if !second.ech {
		t.Fatal("the retry after the reset did not carry the bootstrap ECH offer")
	}
	if second.sni != ech.BootstrapPublicName {
		t.Fatalf("bootstrap offer used server name %q, want %q", second.sni, ech.BootstrapPublicName)
	}
	third := readHello(3)
	if !third.ech || third.sni != "ech.test" {
		t.Fatalf("retry after the rejection used server name %q (ech=%v), want the server-provided config", third.sni, third.ech)
	}

	select {
	case ok := <-upgraded:
		if !ok {
			t.Fatal("server never saw a successful WebSocket upgrade")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the upgrade")
	}
}

// A network that resets even the bootstrap offer cannot be helped
// automatically; the dial must report the block, with the way out, instead of
// retrying forever.
func TestWebSocketDialBootstrapResetReportsHint(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var connections atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			conn.Close()
		}
	}()

	hint := new(string)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx, "wss://"+listener.Addr().String()+"/socket", relayDialOptions{
		ECHMode: ECHAuto,
		Logger:  zerolog.Nop(),
		echHint: hint,
	})
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("dial succeeded although every connection was reset")
	}
	if !strings.Contains(*hint, "--ech-config") {
		t.Fatalf("hint = %q, want the --ech-config way out", *hint)
	}
	deadline := time.Now().Add(3 * time.Second)
	for connections.Load() < 2 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want 2 (plain attempt plus one bootstrap attempt)", got)
	}
}

// With ECH off the client must not bootstrap either.
func TestWebSocketDialECHOffDoesNotBootstrap(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var connections atomic.Int32
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			connections.Add(1)
			conn.Close()
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx, "wss://"+listener.Addr().String()+"/socket", relayDialOptions{
		ECHMode: ECHOff,
		Logger:  zerolog.Nop(),
	})
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("dial succeeded although every connection was reset")
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want exactly 1 with ECH off", got)
	}
}
