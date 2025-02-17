package tests

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"wssocks/wssocks"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ProxyTestServer encapsulates the server-side test environment
type ProxyTestServer struct {
	Server    *wssocks.WSSocksServer
	WSPort    int
	SocksPort int
	Token     string
	Close     func()
}

type ProxyTestServerOption struct {
	WSPort        int
	SocksPort     int
	SocksUser     string
	SocksPassword string
	Token         string
	PortPool      *wssocks.PortPool
	LoggerPrefix  string
	Reconnect     bool
}

// ProxyTestClient encapsulates the client-side test environment
type ProxyTestClient struct {
	Client    *wssocks.WSSocksClient
	SocksPort int
	Close     func()
}

// ProxyTestEnv encapsulates both server and client test environments
type ProxyTestEnv struct {
	Server    *ProxyTestServer
	Client    *ProxyTestClient
	WSPort    int // WebSocket server port
	SocksPort int // SOCKS proxy port (client port for forward mode, server port for reverse mode)
	Close     func()
}

// ProxyConfig contains proxy configuration options
type ProxyConfig struct {
	Port     int
	Username string
	Password string
}

var (
	// Global test servers
	globalUDPServer         string // IP-based address
	globalUDPServerDomain   string // Domain-based address
	globalUDPServerV6       string // IPv6-based address
	globalUDPServerV6Domain string // IPv6 domain-based address
	globalHTTPServer        string
	globalHTTPServerV6      string
	globalCleanupFuncs      []func()

	// Global test logger
	testLogger zerolog.Logger
)

const (
	udpTestAttempts = 10 // Number of UDP test attempts
)

// forwardServer creates a WSS server in forward mode
func forwardServer(t *testing.T, opt *ProxyTestServerOption) *ProxyTestServer {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	token := ""

	var serverOpt *wssocks.ServerOption
	if opt == nil {
		logger := createPrefixedLogger("SRV0")
		serverOpt = wssocks.DefaultServerOption().
			WithWSPort(wsPort).
			WithLogger(logger)
	} else {
		// Set Token
		token = opt.Token

		// Use provided options or defaults
		if opt.LoggerPrefix != "" {
			logger := createPrefixedLogger(opt.LoggerPrefix)
			serverOpt = wssocks.DefaultServerOption().WithLogger(logger)
		} else {
			logger := createPrefixedLogger("SRV0")
			serverOpt = wssocks.DefaultServerOption().WithLogger(logger)
		}

		// Set WSPort
		if opt.WSPort != 0 {
			wsPort = opt.WSPort
		}
		serverOpt.WithWSPort(wsPort)

		// Set PortPool if provided
		if opt.PortPool != nil {
			serverOpt.WithPortPool(opt.PortPool)
		}
	}
	server := wssocks.NewWSSocksServer(serverOpt)
	token = server.AddForwardToken(token)

	require.NoError(t, server.WaitReady(5*time.Second))

	return &ProxyTestServer{
		Server: server,
		WSPort: wsPort,
		Token:  token,
		Close:  server.Close,
	}
}

// forwardClient creates a WSS client in forward mode
func forwardClient(t *testing.T, wsPort int, token string) *ProxyTestClient {
	socksPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("CLT0")

	clientOpt := wssocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://localhost:%d", wsPort)).
		WithSocksPort(socksPort).
		WithReconnectDelay(1 * time.Second).
		WithLogger(logger)
	client := wssocks.NewWSSocksClient(token, clientOpt)

	require.NoError(t, client.WaitReady(5*time.Second))

	return &ProxyTestClient{
		Client:    client,
		SocksPort: socksPort,
		Close:     client.Close,
	}
}

// reverseServer creates a WSS server in reverse mode
func reverseServer(t *testing.T, opt *ProxyTestServerOption) *ProxyTestServer {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	token := ""
	socksUser := ""
	socksPassword := ""

	socksPort, err := getFreePort()
	require.NoError(t, err)

	var serverOpt *wssocks.ServerOption
	if opt == nil {
		logger := createPrefixedLogger("SRV0")
		serverOpt = wssocks.DefaultServerOption().
			WithWSPort(wsPort).
			WithLogger(logger)
	} else {
		token = opt.Token
		socksUser = opt.SocksUser
		socksPassword = opt.SocksPassword

		// Use provided options or defaults
		if opt.LoggerPrefix != "" {
			logger := createPrefixedLogger(opt.LoggerPrefix)
			serverOpt = wssocks.DefaultServerOption().WithLogger(logger)
		} else {
			logger := createPrefixedLogger("SRV0")
			serverOpt = wssocks.DefaultServerOption().WithLogger(logger)
		}

		// Set WSPort
		if opt.WSPort != 0 {
			wsPort = opt.WSPort
		}
		serverOpt.WithWSPort(wsPort)

		// Set PortPool if provided
		if opt.PortPool != nil {
			serverOpt.WithPortPool(opt.PortPool)
		}

		// Set SocksPort if provided
		if opt.SocksPort != 0 {
			socksPort = opt.SocksPort
		}
	}

	server := wssocks.NewWSSocksServer(serverOpt)
	token, socksPort = server.AddReverseToken(&wssocks.ReverseTokenOptions{
		Port:     socksPort,
		Token:    token,
		Username: socksUser,
		Password: socksPassword,
	})
	require.NotZero(t, socksPort)

	require.NoError(t, server.WaitReady(5*time.Second))

	return &ProxyTestServer{
		Server:    server,
		WSPort:    wsPort,
		SocksPort: socksPort,
		Token:     token,
		Close:     server.Close,
	}
}

// reverseClient creates a WSS client in reverse mode
func reverseClient(t *testing.T, wsPort int, token string, prefix string) *ProxyTestClient {
	logger := createPrefixedLogger(prefix)

	clientOpt := wssocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://localhost:%d", wsPort)).
		WithReconnectDelay(1 * time.Second).
		WithReverse(true).
		WithLogger(logger)
	client := wssocks.NewWSSocksClient(token, clientOpt)

	require.NoError(t, client.WaitReady(5*time.Second))

	return &ProxyTestClient{
		Client: client,
		Close:  client.Close,
	}
}

// forwardProxy creates a complete forward proxy test environment
func forwardProxy(t *testing.T) *ProxyTestEnv {
	server := forwardServer(t, nil)
	client := forwardClient(t, server.WSPort, server.Token)

	return &ProxyTestEnv{
		Server:    server,
		Client:    client,
		WSPort:    server.WSPort,
		SocksPort: client.SocksPort, // In forward mode, use client's SOCKS port
		Close: func() {
			client.Close()
			server.Close()
		},
	}
}

// reverseProxy creates a complete reverse proxy test environment
func reverseProxy(t *testing.T) *ProxyTestEnv {
	server := reverseServer(t, nil)
	client := reverseClient(t, server.WSPort, server.Token, "CLT0")

	return &ProxyTestEnv{
		Server:    server,
		Client:    client,
		WSPort:    server.WSPort,
		SocksPort: server.SocksPort, // In reverse mode, use server's SOCKS port
		Close: func() {
			client.Close()
			server.Close()
		},
	}
}

// testWebConnection tests HTTP connection through the proxy
func testWebConnection(targetURL string, proxyConfig *ProxyConfig) error {
	var httpClient *http.Client

	if proxyConfig != nil {
		proxyURL := fmt.Sprintf("socks5://%s", net.JoinHostPort("localhost", fmt.Sprint(proxyConfig.Port)))
		if proxyConfig.Username != "" || proxyConfig.Password != "" {
			proxyURL = fmt.Sprintf("socks5://%s:%s@%s",
				url.QueryEscape(proxyConfig.Username),
				url.QueryEscape(proxyConfig.Password),
				net.JoinHostPort("localhost", fmt.Sprint(proxyConfig.Port)))
		}

		parsedURL, err := url.Parse(proxyURL)
		if err != nil {
			return err
		}

		httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(parsedURL),
			},
		}
	} else {
		httpClient = &http.Client{}
	}

	// Log test start
	if proxyConfig != nil {
		testLogger.Info().
			Str("url", targetURL).
			Int("proxy_port", proxyConfig.Port).
			Msg("Starting web connection test with proxy")
	} else {
		testLogger.Info().
			Str("url", targetURL).
			Msg("Starting web connection test without proxy")
	}

	resp, err := httpClient.Get(targetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Log test completion
	testLogger.Info().
		Str("url", targetURL).
		Int("status", resp.StatusCode).
		Msg("Web connection test completed")

	return nil
}

// assertUDPConnection tests UDP connection through the proxy
func assertUDPConnection(t *testing.T, serverAddr string, proxyConfig *ProxyConfig) {
	testData := []byte("Hello UDP")
	successCount := 0

	var proxyAddr string
	if proxyConfig != nil {
		proxyAddr = net.JoinHostPort("localhost", fmt.Sprint(proxyConfig.Port))
	} else {
		proxyAddr = serverAddr
	}

	var conn net.Conn
	if proxyConfig != nil {
		// Create TCP connection for SOCKS5 negotiation
		tcpConn, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer tcpConn.Close()

		// SOCKS5 handshake
		_, err = tcpConn.Write([]byte{0x05, 0x01, 0x00})
		if err != nil {
			t.Fatal(err)
		}

		// Read handshake response
		resp := make([]byte, 2)
		_, err = io.ReadFull(tcpConn, resp)
		if err != nil {
			t.Fatal(err)
		}
		if resp[0] != 0x05 || resp[1] != 0x00 {
			t.Fatal("SOCKS5 handshake failed")
		}

		// UDP ASSOCIATE request
		_, err = tcpConn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		if err != nil {
			t.Fatal(err)
		}

		// Read UDP ASSOCIATE response
		resp = make([]byte, 10)
		_, err = io.ReadFull(tcpConn, resp)
		if err != nil {
			t.Fatal(err)
		}
		if resp[1] != 0x00 {
			t.Fatal("UDP ASSOCIATE failed")
		}

		// Get UDP relay address and port
		relayPort := binary.BigEndian.Uint16(resp[8:10])
		proxyAddr = fmt.Sprintf("localhost:%d", relayPort)

		// Create UDP connection
		conn, err = net.Dial("udp", proxyAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		// Parse target address
		host, portStr, err := net.SplitHostPort(serverAddr)
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			t.Fatal(err)
		}

		// Test UDP communication
		for i := 0; i < udpTestAttempts; i++ {
			conn.SetDeadline(time.Now().Add(time.Second))

			// Create SOCKS5 UDP header
			var header []byte
			ip := net.ParseIP(host)
			if ip == nil {
				// Domain name
				header = []byte{0, 0, 0, 0x03, byte(len(host))}
				header = append(header, []byte(host)...)
			} else if ip4 := ip.To4(); ip4 != nil {
				// IPv4
				header = []byte{0, 0, 0, 0x01}
				header = append(header, ip4...)
			} else {
				// IPv6
				header = []byte{0, 0, 0, 0x04}
				header = append(header, ip.To16()...)
			}
			header = append(header, byte(port>>8), byte(port))

			// Send data with UDP header
			sendData := append(header, testData...)
			_, err = conn.Write(sendData)
			if err != nil {
				continue
			}
			testLogger.Info().Int("bytes", len(sendData)).Msg("UDP tester sent")

			// Read response
			buf := make([]byte, len(header)+len(testData))
			n, err := conn.Read(buf)
			if err != nil {
				continue
			}
			testLogger.Info().Int("bytes", n).Msg("UDP tester received")

			// Find the actual data after the UDP header
			var responseData []byte
			if n > 10 { // Minimum SOCKS5 UDP header size is 10 bytes
				switch buf[3] { // Address type
				case 0x01: // IPv4
					responseData = buf[10:n]
				case 0x03: // Domain
					domainLen := int(buf[4])
					responseData = buf[5+domainLen+2 : n]
				case 0x04: // IPv6
					responseData = buf[22:n]
				}
			}

			if responseData != nil && bytes.Equal(responseData, testData) {
				successCount++
			}
		}
	} else {
		conn, err := net.Dial("udp", proxyAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		for i := 0; i < udpTestAttempts; i++ {
			conn.SetDeadline(time.Now().Add(time.Second))
			_, err = conn.Write(testData)
			if err != nil {
				continue
			}
			testLogger.Info().Int("data", len(testData)).Msg("UDP tester sent")

			buf := make([]byte, len(testData))
			n, err := conn.Read(buf)
			if err != nil {
				continue
			}
			testLogger.Info().Int("bytes", n).Msg("UDP tester received")

			if n == len(testData) && string(buf[:n]) == string(testData) {
				successCount++
			}
		}
	}

	if successCount < udpTestAttempts {
		t.Errorf("UDP test failed: only %d/%d packets were successfully echoed", successCount, udpTestAttempts)
	}
}

// apiRequest is a helper function to send API requests to the test server
func apiRequest(t *testing.T, method, url string, apiKey string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	require.NoError(t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	client := &http.Client{}
	return client.Do(req)
}

func TestHTTPServer(t *testing.T) {
	require.NoError(t, testWebConnection(globalHTTPServer, nil))
}

func TestHTTPServerV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	require.NoError(t, testWebConnection(globalHTTPServerV6, nil))
}

func TestUDPServer(t *testing.T) {
	assertUDPConnection(t, globalUDPServer, nil)
}

func TestUDPServerV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	assertUDPConnection(t, globalUDPServerV6, nil)
}

func TestForwardProxy(t *testing.T) {
	env := forwardProxy(t)
	defer env.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Client.SocksPort}))
}

func TestProxyAuth(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		SocksUser:     "test_user",
		SocksPassword: "test_pass",
	})
	defer server.Close()
	client := reverseClient(t, server.WSPort, server.Token, "CLT0")
	defer client.Close()
	require.Error(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort, Username: "test_user", Password: "test_pass"}))
}

func TestReverseProxy(t *testing.T) {
	env := reverseProxy(t)
	defer env.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Server.SocksPort}))
}

func TestUDPForwardProxy(t *testing.T) {
	env := forwardProxy(t)
	defer env.Close()
	assertUDPConnection(t, globalUDPServer, &ProxyConfig{Port: env.Client.SocksPort})
}

func TestUDPForwardProxyDomain(t *testing.T) {
	// Check localhost resolution first
	addrs, err := net.LookupHost("localhost")
	require.NoError(t, err)
	require.NotEmpty(t, addrs)

	var serverAddr string
	if strings.Contains(addrs[0], ":") {
		// localhost resolves to IPv6 first
		if !hasIPv6Support() {
			t.Skip("localhost resolves to IPv6 but IPv6 is not supported")
		}
		serverAddr = globalUDPServerV6Domain
	} else {
		// localhost resolves to IPv4 first
		serverAddr = globalUDPServerDomain
	}

	env := forwardProxy(t)
	defer env.Close()
	assertUDPConnection(t, serverAddr, &ProxyConfig{Port: env.Client.SocksPort})
}

func TestUDPForwardProxyV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	env := forwardProxy(t)
	defer env.Close()
	assertUDPConnection(t, globalUDPServerV6, &ProxyConfig{Port: env.Client.SocksPort})
}

func TestUDPReverseProxy(t *testing.T) {
	env := reverseProxy(t)
	defer env.Close()
	assertUDPConnection(t, globalUDPServer, &ProxyConfig{Port: env.Server.SocksPort})
}

func TestUDPReverseProxyDomain(t *testing.T) {
	env := reverseProxy(t)
	defer env.Close()
	assertUDPConnection(t, globalUDPServerDomain, &ProxyConfig{Port: env.Server.SocksPort})
}

func TestUDPReverseProxyV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	env := reverseProxy(t)
	defer env.Close()
	assertUDPConnection(t, globalUDPServerV6, &ProxyConfig{Port: env.Server.SocksPort})
}

func TestForwardReconnect(t *testing.T) {
	server := forwardServer(t, nil)
	client := forwardClient(t, server.WSPort, server.Token)
	defer client.Close()

	// Test initial connection
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client.SocksPort}))

	// Close the server
	server.Close()

	// Stop the server and wait for client to detect disconnection with timeout
	select {
	case <-client.Client.Disconnected:
		// Disconnection detected
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for client disconnection")
	}

	// Start new server with same port
	newServer := forwardServer(t, &ProxyTestServerOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "SRV1",
	})
	defer newServer.Close()

	// Wait for client to reconnect with timeout
	select {
	case <-client.Client.Connected:
		// Connection established
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for client connection")
	}

	// Test connection after reconnect
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client.SocksPort}))
}

func TestReverseReconnect(t *testing.T) {
	server := reverseServer(t, nil)
	client := reverseClient(t, server.WSPort, server.Token, "CLT1")
	defer server.Close()

	// Test initial connection
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))

	// Stop the client
	client.Close()

	// Wait for server to detect disconnection by checking token clients
	require.Eventually(t, func() bool {
		return !server.Server.HasClients()
	}, 5*time.Second, 100*time.Millisecond, "Server failed to detect client disconnection")

	// Start new client with same port
	newClient := reverseClient(t, server.WSPort, server.Token, "CLT2")
	defer newClient.Close()

	// Wait for client to establish connection by checking token clients
	require.Eventually(t, func() bool {
		return server.Server.HasClients()
	}, 5*time.Second, 100*time.Millisecond, "Server failed to detect client disconnection")

	// Test connection with new client
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
}

func TestForwardRemoveToken(t *testing.T) {
	server := forwardServer(t, nil)
	client := forwardClient(t, server.WSPort, server.Token)
	defer func() {
		client.Close()
		server.Close()
	}()

	// Test initial connection
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client.SocksPort}))

	// Remove token
	server.Server.RemoveToken(server.Token)

	// Wait for client to detect disconnection with timeout
	select {
	case <-client.Client.Disconnected:
		// Disconnection detected
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for client disconnection")
	}

	// Connection should fail
	assert.Error(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client.SocksPort}))

	// Add token back
	server.Token = server.Server.AddForwardToken(server.Token)

	// Start new client with same port
	newClient := forwardClient(t, server.WSPort, server.Token)
	defer newClient.Close()

	// Connection should work again
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: newClient.SocksPort}))
}

func TestReverseRemoveToken(t *testing.T) {
	// Get two ports for testing
	socksPort, err := getFreePort()
	require.NoError(t, err)

	wsPort, err := getFreePort()
	require.NoError(t, err)

	// Create server with specific ports pool
	logger := createPrefixedLogger("SRV0")
	serverOpt := wssocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithPortPool(wssocks.NewPortPool([]int{socksPort}))
	server := wssocks.NewWSSocksServer(serverOpt)
	require.NoError(t, server.WaitReady(5*time.Second))
	defer server.Close()

	// Add first token
	token1, port1 := server.AddReverseToken(nil)
	require.NotZero(t, port1)

	// Try to add second token (should fail due to port being in use)
	_, port2 := server.AddReverseToken(nil)
	require.Zero(t, port2)

	// Start first client and test
	client1 := reverseClient(t, wsPort, token1, "CLT1")
	defer client1.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: port1}))

	// Remove first token
	server.RemoveToken(token1)

	// Add second token
	token2, port2 := server.AddReverseToken(nil)
	require.NotZero(t, port2)

	// Wait for client to detect disconnection with timeout
	select {
	case <-client1.Client.Disconnected:
		// Disconnection detected
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for client disconnection")
	}

	// Start second client and test
	client2 := reverseClient(t, wsPort, token2, "CLT2")
	defer client2.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: port2}))
}

func TestApi(t *testing.T) {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := wssocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithAPI("TOKEN")
	server := wssocks.NewWSSocksServer(serverOpt)
	require.NoError(t, server.WaitReady(5*time.Second))
	defer server.Close()

	baseURL := fmt.Sprintf("http://localhost:%d", wsPort)

	// Test access /
	resp1, err1 := apiRequest(t, "GET", baseURL+"/", "", nil)
	require.NoError(t, err1)
	defer resp1.Body.Close()

	body, err := io.ReadAll(resp1.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "API endpoints available")

	// Test creating a forward token via API
	resp2, err2 := apiRequest(t, "POST", baseURL+"/api/token", "TOKEN", wssocks.TokenRequest{
		Type: "forward",
	})
	require.NoError(t, err2)
	defer resp2.Body.Close()

	require.Equal(t, http.StatusOK, resp2.StatusCode)
	var tokenResp wssocks.TokenResponse
	require.NoError(t, json.NewDecoder(resp2.Body).Decode(&tokenResp))
	require.True(t, tokenResp.Success)
	require.NotEmpty(t, tokenResp.Token)

	// Start client and test connection
	client := forwardClient(t, wsPort, tokenResp.Token)
	defer client.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client.SocksPort}))

	// Test server status via API
	resp3, err3 := apiRequest(t, "GET", baseURL+"/api/status", "TOKEN", nil)
	require.NoError(t, err3)
	defer resp3.Body.Close()

	require.Equal(t, http.StatusOK, resp3.StatusCode)
	var statusResp wssocks.StatusResponse
	require.NoError(t, json.NewDecoder(resp3.Body).Decode(&statusResp))
	require.NotEmpty(t, statusResp.Tokens)

	// Test unauthorized access
	resp4, err4 := apiRequest(t, "GET", baseURL+"/api/status", "WRONG_KEY", nil)
	require.NoError(t, err4)
	defer resp4.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp4.StatusCode)
}

func TestMain(m *testing.M) {
	// Initialize test logger
	testLogger = createPrefixedLogger("TEST")

	var cleanup func()
	var err error

	// Start UDP echo server
	globalUDPServer, globalUDPServerDomain, cleanup, err = startUDPEchoServer(false)
	if err != nil {
		testLogger.Fatal().Err(err).Msg("Failed to start UDP server")
	}
	globalCleanupFuncs = append(globalCleanupFuncs, cleanup)

	// Start UDP echo server (IPv6)
	if hasIPv6Support() {
		globalUDPServerV6, globalUDPServerV6Domain, cleanup, err = startUDPEchoServer(true)
		if err != nil {
			testLogger.Warn().Err(err).Msg("Failed to start IPv6 UDP server")
		} else {
			globalCleanupFuncs = append(globalCleanupFuncs, cleanup)
		}
	}

	// Start HTTP test server
	globalHTTPServer, cleanup, err = startTestHTTPServer(false)
	if err != nil {
		testLogger.Fatal().Err(err).Msg("Failed to start HTTP server")
	}
	globalCleanupFuncs = append(globalCleanupFuncs, cleanup)

	// Start HTTP test server (IPv6)
	if hasIPv6Support() {
		globalHTTPServerV6, cleanup, err = startTestHTTPServer(true)
		if err != nil {
			testLogger.Warn().Err(err).Msg("Failed to start IPv6 HTTP server")
		} else {
			globalCleanupFuncs = append(globalCleanupFuncs, cleanup)
		}
	}

	// Run tests
	code := m.Run()

	// Cleanup
	for _, cleanup := range globalCleanupFuncs {
		cleanup()
	}

	os.Exit(code)
}

// hasIPv6Support checks if IPv6 is supported on the system
func hasIPv6Support() bool {
	conn, err := net.Dial("udp6", "[::1]:0")
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// getFreePort returns a free port number
func getFreePort() (int, error) {
	addr, err := net.ResolveTCPAddr("tcp", "localhost:0")
	if err != nil {
		return 0, err
	}

	l, err := net.ListenTCP("tcp", addr)
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// startUDPEchoServer starts a UDP echo server (IPv4 or IPv6) and returns both IP and domain-based addresses
func startUDPEchoServer(useIPv6 bool) (string, string, func(), error) {
	port, err := getFreePort()
	if err != nil {
		return "", "", nil, err
	}

	network := "udp"
	ip := "127.0.0.1"
	if useIPv6 {
		network = "udp6"
		ip = "::1"
		if conn, err := net.Dial("udp6", "[::1]:0"); err != nil {
			return "", "", nil, fmt.Errorf("IPv6 not supported: %w", err)
		} else {
			conn.Close()
		}
	}

	// Create both IP and domain-based addresses
	ipAddr := ip
	if useIPv6 {
		ipAddr = fmt.Sprintf("[%s]", ip)
	}
	ipBasedAddr := fmt.Sprintf("%s:%d", ipAddr, port)
	domainBasedAddr := fmt.Sprintf("localhost:%d", port)

	conn, err := net.ListenUDP(network, &net.UDPAddr{
		IP:   net.ParseIP(ip),
		Port: port,
	})
	if err != nil {
		return "", "", nil, err
	}

	done := make(chan struct{})
	go func() {
		defer conn.Close()
		buf := make([]byte, 1024)
		for {
			select {
			case <-done:
				return
			default:
				n, remoteAddr, err := conn.ReadFromUDP(buf)
				if err != nil {
					continue
				}
				testLogger.Info().Int("bytes", n).Msg("UDP echo server received")

				_, err = conn.WriteToUDP(buf[:n], remoteAddr)
				if err != nil {
					continue
				}
				testLogger.Info().Int("bytes", n).Msg("UDP echo server sent")
			}
		}
	}()

	cleanup := func() {
		close(done)
		conn.Close()
	}

	return ipBasedAddr, domainBasedAddr, cleanup, nil
}

// startTestHTTPServer starts a test HTTP server that returns 204 for /generate_204
func startTestHTTPServer(useIPv6 bool) (string, func(), error) {
	port, err := getFreePort()
	if err != nil {
		return "", nil, err
	}

	ip := "127.0.0.1"
	if useIPv6 {
		ip = "::1"
	}

	addr := fmt.Sprintf("%s:%d", ip, port)
	if useIPv6 {
		addr = fmt.Sprintf("[%s]:%d", ip, port)
	}

	server := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/generate_204" {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.WriteHeader(http.StatusNotFound)
		}),
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			testLogger.Error().Err(err).Msg("HTTP server error")
		}
	}()

	// Wait for server to start
	urlAddr := fmt.Sprintf("http://%s/generate_204", addr)
	var startupErr error
	for i := 0; i < 10; i++ {
		if _, err := http.Get(urlAddr); err == nil {
			startupErr = nil
			break
		}
		startupErr = err
		time.Sleep(100 * time.Millisecond)
	}
	if startupErr != nil {
		return "", nil, fmt.Errorf("server failed to start: %w", startupErr)
	}

	cleanup := func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		server.Shutdown(ctx)
		wg.Wait()
	}

	return urlAddr, cleanup, nil
}

// createPrefixedLogger creates a zerolog.Logger with customized level prefixes
func createPrefixedLogger(prefix string) zerolog.Logger {
	return zerolog.New(zerolog.ConsoleWriter{
		Out: os.Stdout,
		FormatLevel: func(i interface{}) string {
			level := i.(string)
			switch level {
			case "debug":
				return fmt.Sprintf("%s DBG", prefix)
			case "info":
				return fmt.Sprintf("%s INF", prefix)
			case "warn":
				return fmt.Sprintf("%s WRN", prefix)
			case "error":
				return fmt.Sprintf("%s ERR", prefix)
			default:
				return fmt.Sprintf("%s %s", prefix, level[:3])
			}
		},
	}).With().Timestamp().Logger()
}
