package tests

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/zetxtech/wssocks/wssocks"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	// Global test servers
	globalUDPServer         string // IP-based address
	globalUDPServerDomain   string // Domain-based address
	globalUDPServerV6       string // IPv6-based address
	globalUDPServerV6Domain string // IPv6 domain-based address
	globalHTTPServer        string
	globalHTTPServerV6      string
	globalCleanupFuncs      []func()
)

func TestMain(m *testing.M) {
	// Initialize test logger
	TestLogger = createPrefixedLogger("TEST")

	var cleanup func()
	var err error

	// Start UDP echo server
	globalUDPServer, globalUDPServerDomain, cleanup, err = startUDPEchoServer(false)
	if err != nil {
		TestLogger.Fatal().Err(err).Msg("Failed to start UDP server")
	}
	globalCleanupFuncs = append(globalCleanupFuncs, cleanup)

	// Start UDP echo server (IPv6)
	if hasIPv6Support() {
		globalUDPServerV6, globalUDPServerV6Domain, cleanup, err = startUDPEchoServer(true)
		if err != nil {
			TestLogger.Warn().Err(err).Msg("Failed to start IPv6 UDP server")
		} else {
			globalCleanupFuncs = append(globalCleanupFuncs, cleanup)
		}
	}

	// Start HTTP test server
	globalHTTPServer, cleanup, err = startTestHTTPServer(false)
	if err != nil {
		TestLogger.Fatal().Err(err).Msg("Failed to start HTTP server")
	}
	globalCleanupFuncs = append(globalCleanupFuncs, cleanup)

	// Start HTTP test server (IPv6)
	if hasIPv6Support() {
		globalHTTPServerV6, cleanup, err = startTestHTTPServer(true)
		if err != nil {
			TestLogger.Warn().Err(err).Msg("Failed to start IPv6 HTTP server")
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
	ip := net.ParseIP(addrs[0])
	require.NotNil(t, ip, "Failed to parse resolved IP address")

	if ip.To4() == nil {
		// First IP is IPv6
		if !hasIPv6Support() {
			t.Skip("localhost resolves to IPv6 but IPv6 is not supported")
		}
		serverAddr = globalUDPServerV6Domain
	} else {
		// First IP is IPv4
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
	// Check localhost resolution first
	addrs, err := net.LookupHost("localhost")
	require.NoError(t, err)
	require.NotEmpty(t, addrs)

	var serverAddr string
	ip := net.ParseIP(addrs[0])
	require.NotNil(t, ip, "Failed to parse resolved IP address")

	if ip.To4() == nil {
		// First IP is IPv6
		if !hasIPv6Support() {
			t.Skip("localhost resolves to IPv6 but IPv6 is not supported")
		}
		serverAddr = globalUDPServerV6Domain
	} else {
		// First IP is IPv4
		serverAddr = globalUDPServerDomain
	}

	env := reverseProxy(t)
	defer env.Close()
	assertUDPConnection(t, serverAddr, &ProxyConfig{Port: env.Server.SocksPort})
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
	client := forwardClient(t, server.WSPort, server.Token, "CLT0")
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
	client := forwardClient(t, server.WSPort, server.Token, "CLT0")
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
	
	// Add token back
	var err error
	server.Token, err = server.Server.AddForwardToken(server.Token)
	require.NoError(t, err)
	assert.NotEmpty(t, server.Token)

	// Start new client with same port
	newClient := forwardClient(t, server.WSPort, server.Token, "CLT0")
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
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))
	defer server.Close()

	// Add first token
	token1, port1, err := server.AddReverseToken(&wssocks.ReverseTokenOptions{
		Port:     socksPort,
		Token:    "",
		Username: "",
		Password: "",
	})
	require.NoError(t, err)
	require.NotZero(t, port1)

	// Try to add second token (should fail due to port being in use)
	_, port2, err := server.AddReverseToken(&wssocks.ReverseTokenOptions{
		Port:     socksPort,
		Token:    "",
		Username: "",
		Password: "",
	})
	require.Error(t, err)
	require.Zero(t, port2)

	// Start first client and test
	client1 := reverseClient(t, wsPort, token1, "CLT1")
	defer client1.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: port1}))

	// Remove first token
	server.RemoveToken(token1)

	// Add second token
	token2, port2, err := server.AddReverseToken(&wssocks.ReverseTokenOptions{
		Port:     socksPort,
		Token:    "",
		Username: "",
		Password: "",
	})
	require.NoError(t, err)
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

func TestConnector(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorToken: "CONNECTOR",
	})
	defer server.Close()
	client1 := reverseClient(t, server.WSPort, server.Token, "CLT1")
	defer client1.Close()
	client2 := forwardClient(t, server.WSPort, "CONNECTOR", "CLT2")
	defer client2.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client2.SocksPort}))
}

func TestConnectorAutonomy(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorAutonomy: true,
	})
	defer server.Close()
	client1 := reverseClient(t, server.WSPort, server.Token, "CLT1")
	defer client1.Close()
	token, err := client1.Client.AddConnector("CONNECTOR")
	require.NoError(t, err)
	require.NotEmpty(t, token)
	client2 := forwardClient(t, server.WSPort, "CONNECTOR", "CLT2")
	defer client2.Close()
	require.Error(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client2.SocksPort}))
}
