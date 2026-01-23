package tests

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"

	"github.com/stretchr/testify/require"
)

func TestProxyAuth(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		SocksUser:     "test_user",
		SocksPassword: "test_pass",
	})
	defer server.Close()

	client := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT0",
	})
	defer client.Close()

	require.Error(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort, Username: "test_user", Password: "test_pass"}))
}

func TestReverseProxy(t *testing.T) {
	env := reverseProxy(t)
	defer env.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Server.SocksPort}))
}

func TestReverseProxyV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	env := reverseProxy(t)
	defer env.Close()
	require.NoError(t, testWebConnection(globalHTTPServerV6, &ProxyConfig{Port: env.Server.SocksPort}))
}

func TestUDPReverseProxy(t *testing.T) {
	env := reverseProxy(t)
	defer env.Close()
	require.NoError(t, testUDPConnection(t, globalUDPServer, &ProxyConfig{Port: env.Server.SocksPort}))
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
	require.NoError(t, testUDPConnection(t, serverAddr, &ProxyConfig{Port: env.Server.SocksPort}))
}

func TestUDPReverseProxyV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	env := reverseProxy(t)
	defer env.Close()
	require.NoError(t, testUDPConnection(t, globalUDPServerV6, &ProxyConfig{Port: env.Server.SocksPort}))
}

func TestReverseProxyCloseMultipleTimes(t *testing.T) {
	env := reverseProxy(t)
	defer env.Close()
	env.Server.Close()
	env.Server.Close()
	env.Client.Close()
	env.Client.Close()
}

func TestReverseReconnect(t *testing.T) {
	server := reverseServer(t, nil)
	client := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT1",
	})
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
	newClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT2",
	})
	defer newClient.Close()

	// Wait for client to establish connection by checking token clients
	require.Eventually(t, func() bool {
		return server.Server.HasClients()
	}, 5*time.Second, 100*time.Millisecond, "Server failed to detect client disconnection")

	// Test connection with new client
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
}

func TestReverseRemoveToken(t *testing.T) {
	// Get two ports for testing
	socksPort, err := getFreePort()
	require.NoError(t, err)

	wsPort, err := getFreePort()
	require.NoError(t, err)

	// Create server with specific ports pool
	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithPortPool(linksocks.NewPortPool([]int{socksPort}))
	server := linksocks.NewLinkSocksServer(serverOpt)
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))
	defer server.Close()

	// Add first token
	result1, err := server.AddReverseToken(&linksocks.ReverseTokenOptions{
		Port:     socksPort,
		Token:    "",
		Username: "",
		Password: "",
	})
	require.NoError(t, err)
	token1 := result1.Token
	port1 := result1.Port
	require.NotZero(t, port1)

	// Try to add second token (should fail due to port being in use)
	result2, err := server.AddReverseToken(&linksocks.ReverseTokenOptions{
		Port:     socksPort,
		Token:    "",
		Username: "",
		Password: "",
	})
	require.Error(t, err)
	var port2 int
	if result2 != nil {
		port2 = result2.Port
	}
	require.Zero(t, port2)

	// Start first client and test
	client1 := reverseClient(t, &ProxyTestClientOption{
		WSPort:       wsPort,
		Token:        token1,
		LoggerPrefix: "CLT1",
	})
	defer client1.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: port1}))

	// Remove first token
	server.RemoveToken(token1)

	// Add second token
	result3, err := server.AddReverseToken(&linksocks.ReverseTokenOptions{
		Port:     socksPort,
		Token:    "",
		Username: "",
		Password: "",
	})
	require.NoError(t, err)
	token3 := result3.Token
	port3 := result3.Port
	require.NotZero(t, port3)

	// Wait for client to detect disconnection with timeout
	select {
	case <-client1.Client.DisconnectedChan():
		// Disconnection detected
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for client disconnection")
	}

	// Start second client and test
	client2 := reverseClient(t, &ProxyTestClientOption{
		WSPort:       wsPort,
		Token:        token3,
		LoggerPrefix: "CLT2",
	})
	defer client2.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: port3}))
}

func TestFastOpenReverse(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		FastOpen: true,
	})
	defer server.Close()

	client := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT0",
	})
	defer client.Close()

	// Execute web connection test three times
	for i := 0; i < 3; i++ {
		require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
	}
}
