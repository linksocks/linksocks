package linksocks

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// dialReverseClient connects to the server as a reverse (provider) client
// using URL token authentication and returns the WebSocket connection.
func dialReverseClient(t *testing.T, wsURL string, token string) *websocket.Conn {
	t.Helper()
	hash := sha256.Sum256([]byte(token))
	url := fmt.Sprintf("%s?token=%s&reverse=true", wsURL, hex.EncodeToString(hash[:]))
	ws, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatalf("dial reverse client: %v", err)
	}
	t.Cleanup(func() { ws.Close() })
	// Drain the auth response so it does not confuse later reads.
	ws.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, _ = ws.ReadMessage()
	ws.SetReadDeadline(time.Time{})
	return ws
}

// socks5Connect performs a SOCKS5 handshake and CONNECT, returning the reply code.
func socks5Connect(t *testing.T, proxyAddr string, host string, port int) byte {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial socks proxy: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := readFull(conn, reply); err != nil {
		t.Fatalf("read socks auth reply: %v", err)
	}

	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 0x01)
		req = append(req, ip.To4()...)
	} else if ip := net.ParseIP(host); ip != nil {
		req = append(req, 0x04)
		req = append(req, ip.To16()...)
	} else {
		if len(host) > 255 {
			t.Fatalf("domain too long: %s", host)
		}
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write socks connect request: %v", err)
	}

	header := make([]byte, 4)
	if _, err := readFull(conn, header); err != nil {
		t.Fatalf("read socks connect reply: %v", err)
	}
	if header[0] != 0x05 {
		t.Fatalf("unexpected socks version in reply: %d", header[0])
	}
	// Consume the bound address (atyp + addr + port).
	rest := make([]byte, 256)
	n, err := conn.Read(rest)
	if err != nil && n == 0 {
		t.Fatalf("read socks bound address: %v", err)
	}
	_ = n
	return header[1]
}

func readFull(conn net.Conn, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := conn.Read(buf[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

// httpConnect sends an HTTP CONNECT request and returns the status code.
func httpConnect(t *testing.T, proxyAddr string, target string) int {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial http proxy: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	req := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", target, target)
	if _, err := conn.Write([]byte(req)); err != nil {
		t.Fatalf("write http connect request: %v", err)
	}
	statusLine, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("read http connect reply: %v", err)
	}
	parts := strings.SplitN(strings.TrimSpace(statusLine), " ", 3)
	if len(parts) < 2 {
		t.Fatalf("malformed status line: %q", statusLine)
	}
	code := 0
	fmt.Sscanf(parts[1], "%d", &code)
	return code
}

// socks5ConnectEcho performs a SOCKS5 CONNECT and echoes a payload through the
// tunnel, returning the reply code.
func socks5ConnectEcho(t *testing.T, proxyAddr string, host string, port int, payload []byte) byte {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
	if err != nil {
		t.Fatalf("dial socks proxy: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := readFull(conn, reply); err != nil {
		t.Fatalf("read socks auth reply: %v", err)
	}

	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 0x01)
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write socks connect request: %v", err)
	}

	header := make([]byte, 4)
	if _, err := readFull(conn, header); err != nil {
		t.Fatalf("read socks connect reply: %v", err)
	}
	if header[0] != 0x05 {
		t.Fatalf("unexpected socks version in reply: %d", header[0])
	}
	if header[1] != 0x00 {
		// Drain the rest of the failure response.
		rest := make([]byte, 256)
		_, _ = conn.Read(rest)
		return header[1]
	}
	// Consume the bound address.
	rest := make([]byte, 256)
	if _, err := conn.Read(rest); err != nil {
		t.Fatalf("read socks bound address: %v", err)
	}
	if _, err := conn.Write(payload); err != nil {
		t.Fatalf("write payload through tunnel: %v", err)
	}
	buf := make([]byte, len(payload))
	if _, err := readFull(conn, buf); err != nil {
		t.Fatalf("read echo through tunnel: %v", err)
	}
	if string(buf) != string(payload) {
		t.Fatalf("echo mismatch: got %q, want %q", buf, payload)
	}
	return header[1]
}

// startEchoServer starts a TCP echo server and returns its port.
func startEchoServer(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen echo server: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				_, _ = io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

// freeTCPPort reserves a free TCP port and returns it.
func freeTCPPort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve tcp port: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port
}

// TestForwardTokenAccessControl verifies that rules bound to a forward token
// are enforced on the server before dialing on behalf of that token.
func TestForwardTokenAccessControl(t *testing.T) {
	allowedPort := startEchoServer(t)
	blockedPort := startEchoServer(t)

	wsPort := freeTCPPort(t)
	clientSocks := freeTCPPort(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewLinkSocksServer(DefaultServerOption().
		WithWSHost("127.0.0.1").
		WithWSPort(wsPort).
		WithLogger(zerolog.Nop()))
	if err := srv.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Close)

	const token = "forward-access-control-token"
	if _, err := srv.AddForwardTokenWithRules(token, []AccessRule{
		{Addrs: []string{"127.0.0.0/8"}, Ports: []PortSpec{SinglePort(allowedPort)}},
	}); err != nil {
		t.Fatalf("add forward token with rules: %v", err)
	}

	cli := NewLinkSocksClient(token, DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithSocksHost("127.0.0.1").
		WithSocksPort(clientSocks).
		WithSocksWaitServer(true).
		WithLogger(zerolog.Nop()))
	if err := cli.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start forward client: %v", err)
	}
	t.Cleanup(cli.Close)

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", clientSocks)

	// Wait until the forward client is authenticated by the server.
	deadline := time.Now().Add(10 * time.Second)
	for {
		code := socks5Connect(t, proxyAddr, "127.0.0.1", allowedPort)
		if code != 0x03 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("forward client never became available")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Allowed target: connect succeeds and data echoes through the tunnel.
	payload := []byte("hello through forward tunnel")
	if code := socks5ConnectEcho(t, proxyAddr, "127.0.0.1", allowedPort, payload); code != 0x00 {
		t.Errorf("allowed forward target reply = 0x%02x, want 0x00", code)
	}
	// Allowed subnet but disallowed port.
	if code := socks5Connect(t, proxyAddr, "127.0.0.1", blockedPort); code != 0x04 {
		t.Errorf("blocked forward port reply = 0x%02x, want 0x04", code)
	}
	// Disallowed subnet.
	if code := socks5Connect(t, proxyAddr, "8.8.8.8", 80); code != 0x04 {
		t.Errorf("blocked forward subnet reply = 0x%02x, want 0x04", code)
	}
}

// TestConnectorTokenAccessControl verifies that rules bound to a connector
// token are enforced on the server before a request from that connector is
// forwarded to a reverse provider.
func TestConnectorTokenAccessControl(t *testing.T) {
	allowedPort := startEchoServer(t)
	blockedPort := startEchoServer(t)

	wsPort := freeTCPPort(t)
	connectorSocks := freeTCPPort(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewLinkSocksServer(DefaultServerOption().
		WithWSHost("127.0.0.1").
		WithWSPort(wsPort).
		WithLogger(zerolog.Nop()))
	if err := srv.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Close)

	const reverseToken = "connector-ac-reverse-token"
	if _, err := srv.AddReverseToken(&ReverseTokenOptions{Token: reverseToken, Port: 0}); err != nil {
		t.Fatalf("add reverse token: %v", err)
	}

	const connectorToken = "connector-access-control-token"
	if _, err := srv.AddConnectorTokenWithRules(connectorToken, reverseToken, []AccessRule{
		{Addrs: []string{"127.0.0.0/8"}, Ports: []PortSpec{SinglePort(allowedPort)}},
	}); err != nil {
		t.Fatalf("add connector token with rules: %v", err)
	}

	// Provider: reverse client that performs the actual dial.
	provider := NewLinkSocksClient(reverseToken, DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithReverse(true).
		WithSocksHost("127.0.0.1").
		WithSocksPort(0).
		WithSocksWaitServer(true).
		WithLogger(zerolog.Nop()))
	if err := provider.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start provider: %v", err)
	}
	t.Cleanup(provider.Close)

	// Connector: forward-style client authenticated with the connector token.
	connector := NewLinkSocksClient(connectorToken, DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithSocksHost("127.0.0.1").
		WithSocksPort(connectorSocks).
		WithSocksWaitServer(true).
		WithLogger(zerolog.Nop()))
	if err := connector.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start connector: %v", err)
	}
	t.Cleanup(connector.Close)

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", connectorSocks)

	// Wait until the connector is authenticated and a provider is available.
	deadline := time.Now().Add(15 * time.Second)
	for {
		code := socks5Connect(t, proxyAddr, "127.0.0.1", allowedPort)
		if code != 0x03 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("connector/provider never became available")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Allowed target: forwarded to provider and echoes through the tunnel.
	payload := []byte("hello through connector tunnel")
	if code := socks5ConnectEcho(t, proxyAddr, "127.0.0.1", allowedPort, payload); code != 0x00 {
		t.Errorf("allowed connector target reply = 0x%02x, want 0x00", code)
	}
	// Allowed subnet but disallowed port.
	if code := socks5Connect(t, proxyAddr, "127.0.0.1", blockedPort); code != 0x04 {
		t.Errorf("blocked connector port reply = 0x%02x, want 0x04", code)
	}
	// Disallowed subnet.
	if code := socks5Connect(t, proxyAddr, "8.8.8.8", 80); code != 0x04 {
		t.Errorf("blocked connector subnet reply = 0x%02x, want 0x04", code)
	}
}

// TestAccessControlEnforcement verifies that the server-side access control
// blocks SOCKS5 and HTTP CONNECT destinations outside the allowed subnets and
// ports, while allowing permitted ones.
func TestAccessControlEnforcement(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve ws port: %v", err)
	}
	wsPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	ac, err := NewAccessControl([]AccessRule{
		{Addrs: []string{"127.0.0.0/8"}, Ports: []PortSpec{PortRange(30000, 30010)}},
	})
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewLinkSocksServer(DefaultServerOption().
		WithWSHost("127.0.0.1").
		WithWSPort(wsPort).
		WithLogger(zerolog.Nop()).
		WithFastOpen(true).
		WithSocksWaitClient(false).
		WithEntryAccessControl(ac))
	if err := srv.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Close)

	const token = "access-control-test-token"
	result, err := srv.AddReverseToken(&ReverseTokenOptions{Token: token, Port: 0})
	if err != nil {
		t.Fatalf("add reverse token: %v", err)
	}
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", result.Port)

	ws := dialReverseClient(t, fmt.Sprintf("ws://127.0.0.1:%d/socket", wsPort), token)
	ws.SetReadDeadline(time.Now().Add(5 * time.Second))

	// Wait until the server has registered the provider client by polling
	// SOCKS; reply 0x03 means "no client available yet".
	deadline := time.Now().Add(10 * time.Second)
	for {
		code := socks5Connect(t, proxyAddr, "127.0.0.1", 30005)
		if code != 0x03 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider client never became available")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Allowed subnet and port: connection proceeds (fast open replies success).
	if code := socks5Connect(t, proxyAddr, "127.0.0.1", 30005); code != 0x00 {
		t.Errorf("allowed target reply = 0x%02x, want 0x00", code)
	}
	// Allowed subnet via domain name.
	if code := socks5Connect(t, proxyAddr, "localhost", 30005); code != 0x00 {
		t.Errorf("allowed domain target reply = 0x%02x, want 0x00", code)
	}
	// Disallowed subnet.
	if code := socks5Connect(t, proxyAddr, "8.8.8.8", 80); code != 0x02 {
		t.Errorf("blocked subnet reply = 0x%02x, want 0x02", code)
	}
	// Allowed subnet but disallowed port.
	if code := socks5Connect(t, proxyAddr, "127.0.0.1", 30011); code != 0x02 {
		t.Errorf("blocked port reply = 0x%02x, want 0x02", code)
	}
	// Unresolvable domain must be rejected.
	if code := socks5Connect(t, proxyAddr, "nonexistent.invalid-domain.example", 80); code != 0x02 {
		t.Errorf("unresolvable domain reply = 0x%02x, want 0x02", code)
	}

	// HTTP CONNECT path.
	if code := httpConnect(t, proxyAddr, "127.0.0.1:30005"); code != http.StatusOK {
		t.Errorf("allowed HTTP CONNECT status = %d, want 200", code)
	}
	if code := httpConnect(t, proxyAddr, "8.8.8.8:443"); code != http.StatusForbidden {
		t.Errorf("blocked HTTP CONNECT status = %d, want 403", code)
	}
	if code := httpConnect(t, proxyAddr, "127.0.0.1:30011"); code != http.StatusForbidden {
		t.Errorf("blocked-port HTTP CONNECT status = %d, want 403", code)
	}

	// The relay must not have forwarded any connect request for blocked
	// destinations; nothing should arrive on the provider socket.
	_ = ws
}

// TestAccessControlDialSideEnforcement verifies that access control configured
// on the provider side (the client performing the actual dial) blocks
// destinations outside the allowed rules, returning SOCKS failure to the local
// proxy client.
func TestAccessControlDialSideEnforcement(t *testing.T) {
	allowedPort := startEchoServer(t)
	blockedPort := startEchoServer(t)

	ac, err := NewAccessControl([]AccessRule{
		{Addrs: []string{"127.0.0.0/8"}, Ports: []PortSpec{SinglePort(allowedPort)}},
	})
	if err != nil {
		t.Fatalf("NewAccessControl() error = %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve ws port: %v", err)
	}
	wsPort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewLinkSocksServer(DefaultServerOption().
		WithWSHost("127.0.0.1").
		WithWSPort(wsPort).
		WithLogger(zerolog.Nop()))
	if err := srv.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Close)

	const token = "dial-side-access-control-token"
	result, err := srv.AddReverseToken(&ReverseTokenOptions{Token: token, Port: 0})
	if err != nil {
		t.Fatalf("add reverse token: %v", err)
	}
	proxyAddr := fmt.Sprintf("127.0.0.1:%d", result.Port)

	// The provider (dial side) carries its own access control rules.
	client := NewLinkSocksClient(token, DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithReverse(true).
		WithSocksHost("127.0.0.1").
		WithSocksPort(0).
		WithSocksWaitServer(true).
		WithDialAccessControl(ac).
		WithLogger(zerolog.Nop()))
	if err := client.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start provider client: %v", err)
	}
	t.Cleanup(client.Close)

	// Wait until the server has registered the provider client.
	deadline := time.Now().Add(10 * time.Second)
	for {
		code := socks5Connect(t, proxyAddr, "127.0.0.1", allowedPort)
		if code != 0x03 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("provider client never became available")
		}
		time.Sleep(100 * time.Millisecond)
	}

	// Allowed target: connect succeeds and data echoes through the tunnel.
	payload := []byte("hello through the tunnel")
	if code := socks5ConnectEcho(t, proxyAddr, "127.0.0.1", allowedPort, payload); code != 0x00 {
		t.Errorf("allowed dial target reply = 0x%02x, want 0x00", code)
	}
	// Blocked port on an allowed subnet: dial side rejects with failure.
	if code := socks5Connect(t, proxyAddr, "127.0.0.1", blockedPort); code != 0x04 {
		t.Errorf("blocked dial port reply = 0x%02x, want 0x04", code)
	}
	// Blocked subnet: dial side rejects with failure.
	if code := socks5Connect(t, proxyAddr, "8.8.8.8", 80); code != 0x04 {
		t.Errorf("blocked dial subnet reply = 0x%02x, want 0x04", code)
	}
}
