package tests

import (
	"bytes"
	"fmt"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

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
	const numRequests = 250
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
	const numRequests = 250
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
	const numRequests = 250
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numRequests)

	time.Sleep(500 * time.Millisecond)

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

	// High volume UDP test - each test creates a complete UDP connection and sends 10 packets
	const numUDPTests = 25
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numUDPTests)

	time.Sleep(500 * time.Millisecond)

	// Launch concurrent UDP tests
	for i := 0; i < numUDPTests; i++ {
		go func(testID int) {
			defer func() {
				if r := recover(); r != nil {
					results <- fmt.Errorf("UDP test %d panicked: %v", testID, r)
				}
			}()

			// Each test establishes UDP ASSOCIATE and sends 10 packets
			err := testUDPConnection(t, globalUDPServer, &ProxyConfig{Port: env.Client.SocksPort})
			results <- err
		}(i)
	}

	// Collect all test results
	var errors []error
	for i := 0; i < numUDPTests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Verify all UDP tests completed successfully (no packet loss)
	assert.Empty(t, errors, "Some UDP tests failed: %v", errors)

	// Verify timing
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"UDP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("udp_tests", numUDPTests).
		Int("total_packets", numUDPTests*10). // 10 packets per test from udpTestAttempts
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("UDP forward stress test completed")
}

func TestTCPStressReverse(t *testing.T) {
	// TCP stress test for reverse proxy with high volume traffic
	env := reverseProxyWithOptions(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel}, &ProxyTestClientOption{LogLevel: zerolog.DebugLevel})
	defer env.Close()

	// High volume concurrent requests through reverse proxy
	const numRequests = 250
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numRequests)

	time.Sleep(500 * time.Millisecond)

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
	const numUDPTests = 25
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numUDPTests)

	time.Sleep(500 * time.Millisecond)

	// Launch concurrent UDP tests
	for i := 0; i < numUDPTests; i++ {
		go func(testID int) {
			defer func() {
				if r := recover(); r != nil {
					results <- fmt.Errorf("UDP test %d panicked: %v", testID, r)
				}
			}()

			// Each test establishes UDP ASSOCIATE and sends 10 packets
			err := testUDPConnection(t, globalUDPServer, &ProxyConfig{Port: env.Server.SocksPort})
			results <- err
		}(i)
	}

	// Collect all test results
	var errors []error
	for i := 0; i < numUDPTests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	duration := time.Since(start)

	// Verify all tests completed successfully
	assert.Empty(t, errors, "Some reverse UDP tests failed: %v", errors)

	// Verify timing
	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Reverse UDP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("udp_tests", numUDPTests).
		Int("total_packets", numUDPTests*10). // 10 packets per test from udpTestAttempts
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("UDP reverse stress test completed")
}

func TestMultiClientTCPStressReverse(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{LogLevel: zerolog.TraceLevel})
	defer server.Close()

	const numClients = 10
	const requestsPerClient = 25
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numClients)
	var wg sync.WaitGroup
	var clients []*ProxyTestClient

	// Start all reverse clients first
	for i := 0; i < numClients; i++ {
		client := reverseClient(t, &ProxyTestClientOption{
			WSPort:       server.WSPort,
			Token:        server.Token,
			LoggerPrefix: fmt.Sprintf("CLT%d", i),
			LogLevel:     zerolog.TraceLevel,
		})
		clients = append(clients, client)
	}

	// Wait at least 500ms after starting reverseServer and reverseClients
	time.Sleep(500 * time.Millisecond)

	// Now concurrently issue requests for each client
	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int, client *ProxyTestClient) {
			defer wg.Done()

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
		}(i, clients[i])
	}

	go func() {
		wg.Wait()
		for _, c := range clients {
			c.Close()
		}
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
	const totalUDPTests = 25
	const timeoutSeconds = 90

	// Each client will handle totalUDPTests/numClients UDP tests
	testsPerClient := totalUDPTests / numClients

	start := time.Now()
	results := make(chan error, numClients)
	var wg sync.WaitGroup

	var clients []*ProxyTestClient

	// Start all reverse clients first
	for i := 0; i < numClients; i++ {
		client := reverseClient(t, &ProxyTestClientOption{
			WSPort:       server.WSPort,
			Token:        server.Token,
			LoggerPrefix: fmt.Sprintf("CLT%d", i),
			LogLevel:     zerolog.DebugLevel,
		})
		clients = append(clients, client)
	}

	// Wait at least 500ms after starting reverseServer and reverseClients
	time.Sleep(500 * time.Millisecond)

	for i := 0; i < numClients; i++ {
		wg.Add(1)
		go func(clientID int, client *ProxyTestClient) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					results <- fmt.Errorf("client %d panicked: %v", clientID, r)
				}
			}()

			// Each client handles its share of the total UDP tests
			var clientErrors []error
			for test := 0; test < testsPerClient; test++ {
				if err := testUDPConnection(t, globalUDPServer, &ProxyConfig{Port: server.SocksPort}); err != nil {
					clientErrors = append(clientErrors, err)
				}
			}

			if len(clientErrors) > 0 {
				results <- fmt.Errorf("client %d had %d UDP test failures: %v", clientID, len(clientErrors), clientErrors[0])
			} else {
				results <- nil
			}

		}(i, clients[i])
	}

	go func() {
		wg.Wait()
		for _, c := range clients {
			c.Close()
		}
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
		Int("total_udp_tests", totalUDPTests).
		Int("total_packets", totalUDPTests*10). // 10 packets per test from udpTestAttempts
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("Multi-client UDP stress test completed")
}
