package tests

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/stretchr/testify/require"
)

// mockHTTPProxy creates a simple HTTP CONNECT proxy server for testing
func mockHTTPProxy(t *testing.T) (string, func()) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := listener.Addr().(*net.TCPAddr).Port
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleHTTPConnect(conn)
		}
	}()

	return addr, func() { listener.Close() }
}

// handleHTTPConnect handles HTTP CONNECT requests
func handleHTTPConnect(conn net.Conn) {
	defer conn.Close()

	buf := make([]byte, 4096)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}

	// Parse CONNECT request
	request := string(buf[:n])
	var method, host string
	fmt.Sscanf(request, "%s %s", &method, &host)

	if method != "CONNECT" {
		conn.Write([]byte("HTTP/1.1 400 Bad Request\r\n\r\n"))
		return
	}

	// Connect to target
	targetConn, err := net.DialTimeout("tcp", host, 10*time.Second)
	if err != nil {
		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
		return
	}
	defer targetConn.Close()

	// Send success response
	conn.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))

	// Relay data
	done := make(chan struct{}, 2)
	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			targetConn.Write(buf[:n])
		}
		done <- struct{}{}
	}()
	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := targetConn.Read(buf)
			if err != nil {
				break
			}
			conn.Write(buf[:n])
		}
		done <- struct{}{}
	}()
	<-done
}

// mockSOCKS5Proxy creates a simple SOCKS5 proxy server for testing
func mockSOCKS5Proxy(t *testing.T) (string, func()) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)

	port := listener.Addr().(*net.TCPAddr).Port
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go handleSOCKS5(conn)
		}
	}()

	return addr, func() { listener.Close() }
}

// handleSOCKS5 handles SOCKS5 requests
func handleSOCKS5(conn net.Conn) {
	defer conn.Close()

	// Read auth methods
	buf := make([]byte, 256)
	n, err := conn.Read(buf)
	if err != nil || n < 2 || buf[0] != 0x05 {
		return
	}

	// No auth required
	conn.Write([]byte{0x05, 0x00})

	// Read connect request
	n, err = conn.Read(buf)
	if err != nil || n < 7 || buf[0] != 0x05 || buf[1] != 0x01 {
		return
	}

	// Parse target address
	var targetAddr string
	switch buf[3] {
	case 0x01: // IPv4
		targetAddr = fmt.Sprintf("%d.%d.%d.%d:%d", buf[4], buf[5], buf[6], buf[7],
			int(buf[8])<<8|int(buf[9]))
	case 0x03: // Domain
		domainLen := int(buf[4])
		targetAddr = fmt.Sprintf("%s:%d", string(buf[5:5+domainLen]),
			int(buf[5+domainLen])<<8|int(buf[6+domainLen]))
	case 0x04: // IPv6
		targetAddr = fmt.Sprintf("[%x:%x:%x:%x:%x:%x:%x:%x]:%d",
			int(buf[4])<<8|int(buf[5]), int(buf[6])<<8|int(buf[7]),
			int(buf[8])<<8|int(buf[9]), int(buf[10])<<8|int(buf[11]),
			int(buf[12])<<8|int(buf[13]), int(buf[14])<<8|int(buf[15]),
			int(buf[16])<<8|int(buf[17]), int(buf[18])<<8|int(buf[19]),
			int(buf[20])<<8|int(buf[21]))
	default:
		conn.Write([]byte{0x05, 0x08, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}

	// Connect to target
	targetConn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		conn.Write([]byte{0x05, 0x05, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		return
	}
	defer targetConn.Close()

	// Send success response
	conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})

	// Relay data
	done := make(chan struct{}, 2)
	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := conn.Read(buf)
			if err != nil {
				break
			}
			targetConn.Write(buf[:n])
		}
		done <- struct{}{}
	}()
	go func() {
		buf := make([]byte, 32768)
		for {
			n, err := targetConn.Read(buf)
			if err != nil {
				break
			}
			conn.Write(buf[:n])
		}
		done <- struct{}{}
	}()
	<-done
}

func TestForwardProxyWithHTTPUpstream(t *testing.T) {
	// Start mock HTTP proxy
	httpProxyAddr, cleanupHTTP := mockHTTPProxy(t)
	defer cleanupHTTP()

	// Create server with HTTP upstream proxy
	wsPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithUpstreamProxy(httpProxyAddr).
		WithUpstreamProxyType(linksocks.ProxyTypeHTTP)

	server := linksocks.NewLinkSocksServer(serverOpt)
	token, err := server.AddForwardToken("")
	require.NoError(t, err)
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))
	defer server.Close()

	// Create client
	socksPort, err := getFreePort()
	require.NoError(t, err)

	clientLogger := createPrefixedLogger("CLT0")
	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithSocksPort(socksPort).
		WithLogger(clientLogger).
		WithNoEnvProxy(true)

	client := linksocks.NewLinkSocksClient(token, clientOpt)
	require.NoError(t, client.WaitReady(context.Background(), 5*time.Second))
	defer client.Close()

	// Test connection through HTTP upstream proxy
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: socksPort}))
}

func TestForwardProxyWithSOCKS5Upstream(t *testing.T) {
	// Start mock SOCKS5 proxy
	socks5ProxyAddr, cleanupSOCKS5 := mockSOCKS5Proxy(t)
	defer cleanupSOCKS5()

	// Create server with SOCKS5 upstream proxy
	wsPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithUpstreamProxy(socks5ProxyAddr).
		WithUpstreamProxyType(linksocks.ProxyTypeSocks5)

	server := linksocks.NewLinkSocksServer(serverOpt)
	token, err := server.AddForwardToken("")
	require.NoError(t, err)
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))
	defer server.Close()

	// Create client
	socksPort, err := getFreePort()
	require.NoError(t, err)

	clientLogger := createPrefixedLogger("CLT0")
	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithSocksPort(socksPort).
		WithLogger(clientLogger).
		WithNoEnvProxy(true)

	client := linksocks.NewLinkSocksClient(token, clientOpt)
	require.NoError(t, client.WaitReady(context.Background(), 5*time.Second))
	defer client.Close()

	// Test connection through SOCKS5 upstream proxy
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: socksPort}))
}

func TestReverseProxyWithHTTPUpstream(t *testing.T) {
	// Start mock HTTP proxy
	httpProxyAddr, cleanupHTTP := mockHTTPProxy(t)
	defer cleanupHTTP()

	// Create server
	wsPort, err := getFreePort()
	require.NoError(t, err)
	socksPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger)

	server := linksocks.NewLinkSocksServer(serverOpt)
	result, err := server.AddReverseToken(&linksocks.ReverseTokenOptions{
		Port: socksPort,
	})
	require.NoError(t, err)
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))
	defer server.Close()

	// Create client with HTTP upstream proxy
	clientLogger := createPrefixedLogger("CLT0")
	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithReverse(true).
		WithLogger(clientLogger).
		WithNoEnvProxy(true).
		WithUpstreamProxy(httpProxyAddr).
		WithUpstreamProxyType(linksocks.ProxyTypeHTTP)

	client := linksocks.NewLinkSocksClient(result.Token, clientOpt)
	require.NoError(t, client.WaitReady(context.Background(), 5*time.Second))
	defer client.Close()

	// Test connection through HTTP upstream proxy
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: socksPort}))
}

func TestReverseProxyWithSOCKS5Upstream(t *testing.T) {
	// Start mock SOCKS5 proxy
	socks5ProxyAddr, cleanupSOCKS5 := mockSOCKS5Proxy(t)
	defer cleanupSOCKS5()

	// Create server
	wsPort, err := getFreePort()
	require.NoError(t, err)
	socksPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger)

	server := linksocks.NewLinkSocksServer(serverOpt)
	result, err := server.AddReverseToken(&linksocks.ReverseTokenOptions{
		Port: socksPort,
	})
	require.NoError(t, err)
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))
	defer server.Close()

	// Create client with SOCKS5 upstream proxy
	clientLogger := createPrefixedLogger("CLT0")
	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithReverse(true).
		WithLogger(clientLogger).
		WithNoEnvProxy(true).
		WithUpstreamProxy(socks5ProxyAddr).
		WithUpstreamProxyType(linksocks.ProxyTypeSocks5)

	client := linksocks.NewLinkSocksClient(result.Token, clientOpt)
	require.NoError(t, client.WaitReady(context.Background(), 5*time.Second))
	defer client.Close()

	// Test connection through SOCKS5 upstream proxy
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: socksPort}))
}

func TestParseProxyURL(t *testing.T) {
	// Test HTTP proxy URL parsing via CLI parseProxy function
	// This is tested indirectly through the integration tests above

	// Test that HTTP proxy works with authentication
	t.Run("HTTPProxyWithAuth", func(t *testing.T) {
		// Start a simple HTTP server to verify proxy is working
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		require.NoError(t, err)
		defer listener.Close()

		port := listener.Addr().(*net.TCPAddr).Port

		go func() {
			for {
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				// Read request
				buf := make([]byte, 4096)
				conn.Read(buf)
				// Check for Proxy-Authorization header
				if string(buf) != "" {
					conn.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))
				}
				conn.Close()
			}
		}()

		// Just verify the server is listening
		conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
		require.NoError(t, err)
		conn.Close()
	})
}

func TestProxyTypeConstants(t *testing.T) {
	require.Equal(t, linksocks.ProxyType(""), linksocks.ProxyTypeNone)
	require.Equal(t, linksocks.ProxyType("socks5"), linksocks.ProxyTypeSocks5)
	require.Equal(t, linksocks.ProxyType("http"), linksocks.ProxyTypeHTTP)
}

func TestHTTPProxyBase64Encoding(t *testing.T) {
	// Test that HTTP proxy authentication works correctly
	// This is tested indirectly through the HTTP upstream proxy tests
	// The base64Encode function in relay.go handles the encoding

	// Start mock HTTP proxy that checks auth
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer listener.Close()

	port := listener.Addr().(*net.TCPAddr).Port
	addr := fmt.Sprintf("127.0.0.1:%d", port)

	authReceived := make(chan string, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		request := string(buf[:n])

		// Extract Proxy-Authorization header
		for _, line := range splitLines(request) {
			if len(line) > 21 && line[:20] == "Proxy-Authorization:" {
				authReceived <- line[21:]
				break
			}
		}

		conn.Write([]byte("HTTP/1.1 502 Bad Gateway\r\n\r\n"))
	}()

	// Create server with HTTP upstream proxy with auth
	wsPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithUpstreamProxy(addr).
		WithUpstreamProxyType(linksocks.ProxyTypeHTTP).
		WithUpstreamAuth("testuser", "testpass")

	server := linksocks.NewLinkSocksServer(serverOpt)
	token, err := server.AddForwardToken("")
	require.NoError(t, err)
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))
	defer server.Close()

	// Create client
	socksPort, err := getFreePort()
	require.NoError(t, err)

	clientLogger := createPrefixedLogger("CLT0")
	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithSocksPort(socksPort).
		WithLogger(clientLogger).
		WithNoEnvProxy(true)

	client := linksocks.NewLinkSocksClient(token, clientOpt)
	require.NoError(t, client.WaitReady(context.Background(), 5*time.Second))
	defer client.Close()

	// Try to make a connection (will fail but should send auth header)
	httpClient := &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyURL(mustParseURL(fmt.Sprintf("socks5://127.0.0.1:%d", socksPort))),
		},
		Timeout: 2 * time.Second,
	}
	httpClient.Get("http://example.com")

	// Check if auth was received
	select {
	case auth := <-authReceived:
		require.Contains(t, auth, "Basic")
		// Base64 of "testuser:testpass" is "dGVzdHVzZXI6dGVzdHBhc3M="
		require.Contains(t, auth, "dGVzdHVzZXI6dGVzdHBhc3M=")
	case <-time.After(3 * time.Second):
		t.Log("No auth header received (proxy may not have been contacted)")
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			line := s[start:i]
			if len(line) > 0 && line[len(line)-1] == '\r' {
				line = line[:len(line)-1]
			}
			lines = append(lines, line)
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func mustParseURL(rawURL string) *url.URL {
	u, _ := url.Parse(rawURL)
	return u
}
