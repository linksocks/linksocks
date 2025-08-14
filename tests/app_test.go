package tests

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/zetxtech/linksocks/linksocks"

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

func TestForwardProxyV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	env := forwardProxy(t)
	defer env.Close()
	require.NoError(t, testWebConnection(globalHTTPServerV6, &ProxyConfig{Port: env.Client.SocksPort}))
}

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

func TestForwardProxyCloseMultipleTimes(t *testing.T) {
	env := forwardProxy(t)
	defer env.Close()
	env.Server.Close()
	env.Server.Close()
	env.Client.Close()
	env.Client.Close()
}

func TestReverseProxyCloseMultipleTimes(t *testing.T) {
	env := reverseProxy(t)
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

func TestForwardRemoveToken(t *testing.T) {
	server := forwardServer(t, nil)
	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT0",
	})
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
	newClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT0",
	})
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
	case <-client1.Client.Disconnected:
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

func TestConnector(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorToken: "CONNECTOR",
	})
	defer server.Close()

	client1 := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT1",
	})
	defer client1.Close()

	client2 := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        "CONNECTOR",
		LoggerPrefix: "CLT2",
	})
	defer client2.Close()

	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client2.SocksPort}))
}

func TestConnectorAutonomy(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorAutonomy: true,
	})
	defer server.Close()

	client1 := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT1",
	})
	defer client1.Close()

	token, err := client1.Client.AddConnector("CONNECTOR")
	require.NoError(t, err)
	require.NotEmpty(t, token)

	client2 := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        "CONNECTOR",
		LoggerPrefix: "CLT2",
	})
	defer client2.Close()

	require.Error(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client2.SocksPort}))
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

func TestFastOpenConnector(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorToken: "CONNECTOR",
		FastOpen:       true,
	})
	defer server.Close()

	client1 := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT1",
	})
	defer client1.Close()

	client2 := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        "CONNECTOR",
		LoggerPrefix: "CLT2",
	})
	defer client2.Close()

	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client2.SocksPort}))
}

func TestMixedFastOpenConnector(t *testing.T) {
	// Test cases with different fast-open mode combinations
	testCases := []struct {
		name            string
		serverFastOpen  bool
		client2FastOpen bool
	}{
		{"ServerFastOpen", true, false},
		{"ConnectorFastOpen", false, true},
		{"AllFastOpen", true, true},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			server := reverseServer(t, &ProxyTestServerOption{
				ConnectorToken: "CONNECTOR",
				FastOpen:       tc.serverFastOpen,
			})
			defer server.Close()

			client1 := reverseClient(t, &ProxyTestClientOption{
				WSPort:       server.WSPort,
				Token:        server.Token,
				LoggerPrefix: "CLT1",
			})
			defer client1.Close()

			client2 := forwardClient(t, &ProxyTestClientOption{
				WSPort:       server.WSPort,
				Token:        "CONNECTOR",
				LoggerPrefix: "CLT2",
				FastOpen:     tc.client2FastOpen,
			})
			defer client2.Close()

			require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort}))
			require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client2.SocksPort}))
		})
	}
}

// testDirectUDPConnection tests UDP connection directly without proxy
func testDirectUDPConnection(serverAddr string) error {
	testData := []byte("Hello UDP")

	conn, err := net.Dial("udp", serverAddr)
	if err != nil {
		return err
	}
	defer conn.Close()

	conn.SetDeadline(time.Now().Add(5 * time.Second))

	// Send test data
	_, err = conn.Write(testData)
	if err != nil {
		return err
	}

	// Read echo response
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return err
	}

	// Verify echo
	if !bytes.Equal(buf[:n], testData) {
		return fmt.Errorf("UDP echo mismatch: sent %q, received %q", testData, buf[:n])
	}

	return nil
}

func TestDirectHTTPStress(t *testing.T) {
	// Direct HTTP stress test without proxy to establish baseline performance
	const numRequests = 100
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numRequests)

	// Launch concurrent requests directly to HTTP server
	for i := 0; i < numRequests; i++ {
		go func(reqID int) {
			err := testWebConnection(globalHTTPServer, nil) // nil = no proxy
			results <- err
		}(i)
	}

	// Collect all results
	var errors []error
	for i := 0; i < numRequests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Log results for comparison
	TestLogger.Info().
		Int("requests", numRequests).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("Direct HTTP stress test completed")

	// Verify all requests completed successfully
	assert.Empty(t, errors, "Some direct HTTP requests failed: %v", errors)

	// Verify timing (should be very fast for direct local server access)
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Direct HTTP stress test took too long: %v", duration)
}

func TestDirectUDPStress(t *testing.T) {
	// Direct UDP stress test without proxy to establish baseline performance
	const numRequests = 100
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numRequests)

	// Launch concurrent requests directly to UDP server
	for i := 0; i < numRequests; i++ {
		go func(reqID int) {
			err := testDirectUDPConnection(globalUDPServer)
			results <- err
		}(i)
	}

	// Collect all results
	var errors []error
	for i := 0; i < numRequests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Log results for comparison
	TestLogger.Info().
		Int("requests", numRequests).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("Direct UDP stress test completed")

	// Verify all requests completed successfully
	assert.Empty(t, errors, "Some direct UDP requests failed: %v", errors)

	// Verify timing (should be very fast for direct local server access)
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Direct UDP stress test took too long: %v", duration)
}

func TestTCPStressForward(t *testing.T) {
	// TCP stress test with high volume traffic, timing and content verification
	env := forwardProxyWithOptions(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel}, &ProxyTestClientOption{LogLevel: zerolog.DebugLevel})
	defer env.Close()

	// High volume concurrent requests
	const numRequests = 100
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numRequests)

	// Launch concurrent requests
	for i := 0; i < numRequests; i++ {
		go func(reqID int) {
			err := testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Client.SocksPort})
			results <- err
		}(i)
	}

	// Collect all results
	var errors []error
	for i := 0; i < numRequests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Verify all requests completed successfully (no packet loss)
	assert.Empty(t, errors, "Some TCP requests failed: %v", errors)

	// Verify timing (should complete within reasonable time for local server)
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"TCP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("requests", numRequests).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("TCP forward stress test completed")
}

func TestUDPStressForward(t *testing.T) {
	// UDP stress test with high volume traffic, timing and content verification
	env := forwardProxyWithOptions(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel}, &ProxyTestClientOption{LogLevel: zerolog.DebugLevel})
	defer env.Close()

	// High volume UDP packets with content verification
	const numBatches = 50
	const packetsPerBatch = 10
	const timeoutSeconds = 45

	start := time.Now()
	results := make(chan error, numBatches)

	// Launch concurrent UDP batches
	for i := 0; i < numBatches; i++ {
		go func(batchID int) {
			defer func() {
				if r := recover(); r != nil {
					results <- fmt.Errorf("batch %d panicked: %v", batchID, r)
				}
			}()

			// Send multiple UDP packets in this batch
			for j := 0; j < packetsPerBatch; j++ {
				assertUDPConnection(t, globalUDPServer, &ProxyConfig{Port: env.Client.SocksPort})
			}
			results <- nil // Success
		}(i)
	}

	// Collect all batch results
	var errors []error
	for i := 0; i < numBatches; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Verify all batches completed successfully (no packet loss)
	assert.Empty(t, errors, "Some UDP batches failed: %v", errors)

	// Verify timing
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"UDP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("batches", numBatches).
		Int("total_packets", numBatches*packetsPerBatch).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("UDP forward stress test completed")
}

func TestTCPStressReverse(t *testing.T) {
	// TCP stress test for reverse proxy with high volume traffic
	env := reverseProxyWithOptions(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel}, &ProxyTestClientOption{LogLevel: zerolog.DebugLevel})
	defer env.Close()

	// High volume concurrent requests through reverse proxy
	const numRequests = 100
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numRequests)

	// Launch concurrent requests
	for i := 0; i < numRequests; i++ {
		go func(reqID int) {
			err := testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Server.SocksPort})
			results <- err
		}(i)
	}

	// Collect all results
	var errors []error
	for i := 0; i < numRequests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Verify all requests completed successfully
	assert.Empty(t, errors, "Some reverse TCP requests failed: %v", errors)

	// Verify timing
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Reverse TCP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("requests", numRequests).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("TCP reverse stress test completed")
}

func TestUDPStressReverse(t *testing.T) {
	// UDP stress test for reverse proxy with high volume traffic
	env := reverseProxyWithOptions(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel}, &ProxyTestClientOption{LogLevel: zerolog.DebugLevel})
	defer env.Close()

	// Test high volume UDP through reverse proxy
	const numBatches = 50
	const packetsPerBatch = 10
	const timeoutSeconds = 45

	start := time.Now()
	results := make(chan error, numBatches)

	// Launch concurrent UDP batches
	for i := 0; i < numBatches; i++ {
		go func(batchID int) {
			defer func() {
				if r := recover(); r != nil {
					results <- fmt.Errorf("batch %d panicked: %v", batchID, r)
				}
			}()

			// Send multiple UDP packets in this batch
			for j := 0; j < packetsPerBatch; j++ {
				assertUDPConnection(t, globalUDPServer, &ProxyConfig{Port: env.Server.SocksPort})
			}
			results <- nil // Success
		}(i)
	}

	// Collect all batch results
	var errors []error
	for i := 0; i < numBatches; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Verify all batches completed
	assert.Empty(t, errors, "Some reverse UDP batches failed: %v", errors)

	// Verify timing
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Reverse UDP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("batches", numBatches).
		Int("total_packets", numBatches*packetsPerBatch).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("UDP reverse stress test completed")
}

func TestMultiClientTCPStressReverse(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{LogLevel: zerolog.TraceLevel})
	defer server.Close()

	const numClients = 10
	const requestsPerClient = 50
	const timeoutSeconds = 60

	start := time.Now()
	results := make(chan error, numClients)
	var wg sync.WaitGroup
	var clients []*ProxyTestClient
	var clientsMu sync.Mutex

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()

			client := reverseClient(t, &ProxyTestClientOption{
				WSPort:       server.WSPort,
				Token:        server.Token,
				LoggerPrefix: fmt.Sprintf("CLT%d", clientID),
				LogLevel:     zerolog.TraceLevel,
			})
			clientsMu.Lock()
			clients = append(clients, client)
			clientsMu.Unlock()

			clientResults := make(chan error, requestsPerClient)
			for req := 0; req < requestsPerClient; req++ {
				go func(reqID int) {
					err := testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort})
					clientResults <- err
				}(req)
			}
			var clientErrors []error
			for req := 0; req < requestsPerClient; req++ {
				if err := <-clientResults; err != nil {
					clientErrors = append(clientErrors, err)
				}
			}

			if len(clientErrors) > 0 {
				results <- fmt.Errorf("client %d had %d errors: %v", clientID, len(clientErrors), clientErrors[0])
			} else {
				results <- nil
			}
		}(i)
	}

	go func() {
		wg.Wait()
		clientsMu.Lock()
		for _, c := range clients {
			c.Close()
		}
		clientsMu.Unlock()
		close(results)
	}()

	var errors []error
	for err := range results {
		if err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	assert.Empty(t, errors, "Some multi-client TCP requests failed: %v", errors)

	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Multi-client TCP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("clients", numClients).
		Int("requests_per_client", requestsPerClient).
		Int("total_requests", numClients*requestsPerClient).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("Multi-client TCP stress test completed")
}

func TestMultiClientUDPStressReverse(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel})
	defer server.Close()

	const numClients = 5
	const batchesPerClient = 5
	const packetsPerBatch = 50
	const timeoutSeconds = 90

	start := time.Now()
	results := make(chan error, numClients)
	var wg sync.WaitGroup

	var clients []*ProxyTestClient
	var clientsMu sync.Mutex

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results <- fmt.Errorf("client %d panicked: %v", clientID, r)
				}
			}()

			client := reverseClient(t, &ProxyTestClientOption{
				WSPort:       server.WSPort,
				Token:        server.Token,
				LoggerPrefix: fmt.Sprintf("CLT%d", clientID),
				LogLevel:     zerolog.DebugLevel,
			})
			clientsMu.Lock()
			clients = append(clients, client)
			clientsMu.Unlock()

			for batch := 0; batch < batchesPerClient; batch++ {
				for packet := 0; packet < packetsPerBatch; packet++ {
					assertUDPConnection(t, globalUDPServer, &ProxyConfig{Port: server.SocksPort})
				}
			}

			results <- nil

		}(i)
	}

	go func() {
		wg.Wait()
		clientsMu.Lock()
		for _, c := range clients {
			c.Close()
		}
		clientsMu.Unlock()
		close(results)
	}()

	var errors []error
	for err := range results {
		if err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	assert.Empty(t, errors, "Some multi-client UDP requests failed: %v", errors)

	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Multi-client UDP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("clients", numClients).
		Int("batches_per_client", batchesPerClient).
		Int("packets_per_batch", packetsPerBatch).
		Int("total_packets", numClients*batchesPerClient*packetsPerBatch).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("Multi-client UDP stress test completed")
}

func TestMultiClientMixedStressReverse(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel})
	defer server.Close()

	const tcpClients = 5
	const udpClients = 5
	const tcpRequestsPerClient = 15
	const udpBatchesPerClient = 8
	const udpPacketsPerBatch = 5
	const timeoutSeconds = 120

	start := time.Now()
	results := make(chan error, tcpClients+udpClients)
	var wg sync.WaitGroup
	var allClients []*ProxyTestClient
	var clientsMu sync.Mutex
	for i := 0; i < tcpClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			client := reverseClient(t, &ProxyTestClientOption{
				WSPort:       server.WSPort,
				Token:        server.Token,
				LoggerPrefix: fmt.Sprintf("TCP%d", clientID),
				LogLevel:     zerolog.DebugLevel,
			})
			clientsMu.Lock()
			allClients = append(allClients, client)
			clientsMu.Unlock()
			clientResults := make(chan error, tcpRequestsPerClient)
			for req := 0; req < tcpRequestsPerClient; req++ {
				go func(reqID int) {
					err := testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort})
					clientResults <- err
				}(req)
			}
			var clientErrors []error
			for req := 0; req < tcpRequestsPerClient; req++ {
				if err := <-clientResults; err != nil {
					clientErrors = append(clientErrors, err)
				}
			}

			if len(clientErrors) > 0 {
				results <- fmt.Errorf("TCP client %d had %d errors: %v", clientID, len(clientErrors), clientErrors[0])
			} else {
				results <- nil
			}
		}(i)
	}

	// UDP clients
	for i := 0; i < udpClients; i++ {
		wg.Add(1)
		go func(clientID int) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results <- fmt.Errorf("UDP client %d panicked: %v", clientID, r)
				}
			}()

			// Create UDP client
			client := reverseClient(t, &ProxyTestClientOption{
				WSPort:       server.WSPort,
				Token:        server.Token,
				LoggerPrefix: fmt.Sprintf("UDP%d", clientID),
				LogLevel:     zerolog.DebugLevel,
			})
			// Add client to shared list for later cleanup
			clientsMu.Lock()
			allClients = append(allClients, client)
			clientsMu.Unlock()

			// Send UDP packets through server's SOCKS port
			for batch := 0; batch < udpBatchesPerClient; batch++ {
				for packet := 0; packet < udpPacketsPerBatch; packet++ {
					assertUDPConnection(t, globalUDPServer, &ProxyConfig{Port: server.SocksPort})
				}
			}

			results <- nil // Success
		}(i)
	}

	// Wait for all clients to complete
	go func() {
		wg.Wait()
		// Close all clients only after all requests are completed
		clientsMu.Lock()
		for _, c := range allClients {
			c.Close()
		}
		clientsMu.Unlock()
		close(results)
	}()

	// Collect all client results
	var errors []error
	for err := range results {
		if err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Verify all clients completed successfully
	assert.Empty(t, errors, "Some mixed multi-client requests failed: %v", errors)

	// Verify timing
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Mixed multi-client stress test took too long: %v", duration)

	TestLogger.Info().
		Int("tcp_clients", tcpClients).
		Int("udp_clients", udpClients).
		Int("tcp_requests_per_client", tcpRequestsPerClient).
		Int("udp_batches_per_client", udpBatchesPerClient).
		Int("udp_packets_per_batch", udpPacketsPerBatch).
		Int("total_tcp_requests", tcpClients*tcpRequestsPerClient).
		Int("total_udp_packets", udpClients*udpBatchesPerClient*udpPacketsPerBatch).
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("Mixed multi-client stress test completed")
}
