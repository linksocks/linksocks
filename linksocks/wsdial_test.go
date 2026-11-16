package linksocks

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/linksocks/linksocks/internal/echtest"
	utls "github.com/refraction-networking/utls"
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

// sniffedClientHello records the fields relevant to the browser-compatible retry.
type sniffedClientHello struct {
	ech   bool
	sni   string
	alpns []string
}

// resettingClientHelloListener resets the ordinary Go ClientHello, then lets a
// Chrome GREASE-ECH ClientHello continue to the local TLS/WebSocket server.
type resettingClientHelloListener struct {
	net.Listener
	seen chan sniffedClientHello
}

func (l *resettingClientHelloListener) Accept() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		header := make([]byte, 5)
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		if _, err := io.ReadFull(conn, header); err != nil {
			_ = conn.Close()
			continue
		}
		payload := make([]byte, int(header[3])<<8|int(header[4]))
		if _, err := io.ReadFull(conn, payload); err != nil {
			_ = conn.Close()
			continue
		}
		_ = conn.SetReadDeadline(time.Time{})
		hello := parseClientHello(payload)
		l.seen <- hello
		if !hello.ech {
			_ = conn.Close()
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

func parseClientHello(payload []byte) sniffedClientHello {
	var result sniffedClientHello
	if len(payload) < 4 || payload[0] != 0x01 {
		return result
	}
	body := payload[4:]
	if len(body) < 2+32+1 {
		return result
	}
	body = body[2+32:]
	session := int(body[0])
	body = body[1:]
	if len(body) < session+2 {
		return result
	}
	body = body[session:]
	ciphers := int(binary.BigEndian.Uint16(body))
	body = body[2:]
	if len(body) < ciphers+1 {
		return result
	}
	body = body[ciphers:]
	compression := int(body[0])
	body = body[1:]
	if len(body) < compression+2 {
		return result
	}
	body = body[compression:]
	extensions := int(binary.BigEndian.Uint16(body))
	body = body[2:]
	if extensions > len(body) {
		extensions = len(body)
	}
	body = body[:extensions]

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
			result.ech = true
		case 0x0000:
			if len(data) >= 5 {
				nameLen := int(binary.BigEndian.Uint16(data[3:]))
				if len(data) >= 5+nameLen {
					result.sni = string(data[5 : 5+nameLen])
				}
			}
		case 0x0010:
			if len(data) >= 2 {
				list := data[2:]
				for len(list) > 0 {
					length := int(list[0])
					list = list[1:]
					if len(list) < length {
						break
					}
					result.alpns = append(result.alpns, string(list[:length]))
					list = list[length:]
				}
			}
		}
	}
	return result
}

func TestChromeWebSocketClientHelloProfile(t *testing.T) {
	spec, err := chromeWebSocketClientHelloSpec()
	if err != nil {
		t.Fatal(err)
	}
	var greaseECH, pqKeyShare, http1ALPN bool
	for _, ext := range spec.Extensions {
		switch value := ext.(type) {
		case *utls.GREASEEncryptedClientHelloExtension:
			greaseECH = true
		case *utls.KeyShareExtension:
			for _, share := range value.KeyShares {
				if share.Group == utls.X25519MLKEM768 {
					pqKeyShare = true
				}
			}
		case *utls.ALPNExtension:
			http1ALPN = len(value.AlpnProtocols) == 1 && value.AlpnProtocols[0] == "http/1.1"
		}
	}
	if !greaseECH || !pqKeyShare || !http1ALPN {
		t.Fatalf("Chrome profile fields: greaseECH=%v pqKeyShare=%v http1ALPN=%v", greaseECH, pqKeyShare, http1ALPN)
	}
}

func TestDialChromeTLSUsesHTTPConnectProxy(t *testing.T) {
	upgraded := make(chan bool, 1)
	target := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		upgraded <- err == nil
	}))
	defer target.Close()
	targetPort := target.Listener.Addr().(*net.TCPAddr).Port
	targetAddr := target.Listener.Addr().String()

	proxyServer := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodConnect {
			http.Error(response, "CONNECT required", http.StatusMethodNotAllowed)
			return
		}
		clientConn, buffered, err := response.(http.Hijacker).Hijack()
		if err != nil {
			return
		}
		upstream, err := net.Dial("tcp", targetAddr)
		if err != nil {
			_ = clientConn.Close()
			return
		}
		_, _ = fmt.Fprint(buffered, "HTTP/1.1 200 Connection Established\r\n\r\n")
		if err := buffered.Flush(); err != nil {
			_ = clientConn.Close()
			_ = upstream.Close()
			return
		}
		go func() {
			_, _ = io.Copy(upstream, buffered)
			_ = upstream.Close()
		}()
		go func() {
			_, _ = io.Copy(clientConn, upstream)
			_ = clientConn.Close()
		}()
	}))
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialChromeTLS(ctx, "tcp", net.JoinHostPort("localhost", strconv.Itoa(targetPort)), "localhost",
		"wss://localhost:"+strconv.Itoa(targetPort)+"/socket", true,
		func(*http.Request) (*url.URL, error) { return proxyURL, nil })
	if err != nil {
		t.Fatalf("Chrome TLS handshake through HTTP CONNECT proxy: %v", err)
	}
	if got := conn.(*utls.UConn).ConnectionState().NegotiatedProtocol; got != "http/1.1" {
		t.Fatalf("negotiated ALPN = %q, want http/1.1", got)
	}
	defer conn.Close()
	if _, err := fmt.Fprint(conn, "GET /socket HTTP/1.1\r\nHost: localhost\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: dGhlIHNhbXBsZSBub25jZQ==\r\n\r\n"); err != nil {
		t.Fatalf("write WebSocket request through proxy: %v", err)
	}
	select {
	case ok := <-upgraded:
		if !ok {
			t.Fatal("proxied server did not accept the WebSocket Upgrade")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("proxied WebSocket Upgrade was not received")
	}
}

// A reset of the ordinary Go hello retries with Chrome 133's GREASE-ECH
// fingerprint and still completes the HTTP/1.1 WebSocket Upgrade.
func TestWebSocketDialRetriesWithChromeClientHelloAfterReset(t *testing.T) {
	upgraded := make(chan bool, 1)
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err == nil {
			defer conn.Close()
		}
		upgraded <- err == nil
	}))
	seen := make(chan sniffedClientHello, 4)
	server.Listener = &resettingClientHelloListener{Listener: server.Listener, seen: seen}
	server.StartTLS()
	defer server.Close()

	port := server.Listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx, fmt.Sprintf("wss://localhost:%d/socket", port), relayDialOptions{
		Insecure:    true,
		ECHMode:     ECHAuto,
		echOverride: []byte{},
		NoEnvProxy:  true,
		Logger:      zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("dial after Chrome-compatible retry: %v", err)
	}
	defer conn.Close()

	readHello := func(number int) sniffedClientHello {
		t.Helper()
		select {
		case hello := <-seen:
			return hello
		case <-time.After(5 * time.Second):
			t.Fatalf("listener did not receive ClientHello %d", number)
			return sniffedClientHello{}
		}
	}
	first := readHello(1)
	if first.ech || first.sni != "localhost" {
		t.Fatalf("first ClientHello = %+v, want Go hello with plaintext localhost SNI", first)
	}
	second := readHello(2)
	if !second.ech || second.sni != "localhost" {
		t.Fatalf("retry ClientHello = %+v, want GREASE ECH and plaintext localhost SNI", second)
	}
	if len(second.alpns) != 1 || second.alpns[0] != "http/1.1" {
		t.Fatalf("retry ALPNs = %v, want only http/1.1", second.alpns)
	}
	select {
	case ok := <-upgraded:
		if !ok {
			t.Fatal("server did not complete the WebSocket Upgrade")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for WebSocket Upgrade")
	}
}

func TestWebSocketDialChromeRetryDoesNotLoop(t *testing.T) {
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
			_ = conn.Close()
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx, fmt.Sprintf("wss://localhost:%d/socket", port), relayDialOptions{
		ECHMode:     ECHAuto,
		echOverride: []byte{},
		NoEnvProxy:  true,
		Logger:      zerolog.Nop(),
	})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("dial succeeded although both ClientHellos were reset")
	}
	if got := connections.Load(); got != 2 {
		t.Fatalf("connections = %d, want one Go hello plus one Chrome-profile retry", got)
	}
}

func TestWebSocketDialECHOffDoesNotRetryChromeProfile(t *testing.T) {
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
			_ = conn.Close()
		}
	}()

	port := listener.Addr().(*net.TCPAddr).Port
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx, fmt.Sprintf("wss://localhost:%d/socket", port), relayDialOptions{
		ECHMode:    ECHOff,
		NoEnvProxy: true,
		Logger:     zerolog.Nop(),
	})
	if conn != nil {
		_ = conn.Close()
	}
	if err == nil {
		t.Fatal("dial succeeded although the only connection was reset")
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("connections = %d, want one plain attempt with ECH off", got)
	}
}
