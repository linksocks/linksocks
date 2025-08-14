package tests

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestForwardProxy(t *testing.T) {
	env := forwardProxy(t)
	defer env.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Client.SocksPort}))
}

func TestForwardProxyV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	env := forwardProxy(t)
	defer env.Close()
	require.NoError(t, testWebConnection(globalHTTPServerV6, &ProxyConfig{Port: env.Client.SocksPort}))
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

func TestForwardProxyCloseMultipleTimes(t *testing.T) {
	env := forwardProxy(t)
	defer env.Close()
	env.Server.Close()
	env.Server.Close()
	env.Client.Close()
	env.Client.Close()
}

func TestForwardReconnect(t *testing.T) {
	server := forwardServer(t, nil)
	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT0",
		Reconnect:    true,
	})
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

func TestClientThread(t *testing.T) {
	server := forwardServer(t, nil)
	defer server.Close()

	socksPort, err := getFreePort()
	require.NoError(t, err)

	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT0",
		SocksPort:    socksPort,
		Threads:      2,
	})
	defer client.Close()

	// Execute web connection test three times
	for i := 0; i < 3; i++ {
		require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: socksPort}))
	}
}

func TestFastOpenForward(t *testing.T) {
	server := forwardServer(t, nil)
	defer server.Close()

	socksPort, err := getFreePort()
	require.NoError(t, err)

	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT0",
		SocksPort:    socksPort,
		FastOpen:     true,
	})
	defer client.Close()

	// Execute web connection test three times
	for i := 0; i < 3; i++ {
		require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: socksPort}))
	}
}
