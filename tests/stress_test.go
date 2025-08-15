package tests

import (
	"bytes"
	"fmt"
	"net"
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

func TestDirectTCPStress(t *testing.T) {
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
	const numRequests = 100
	const timeoutSeconds = 30

	start := time.Now()
	results := make(chan error, numRequests)

	time.Sleep(500 * time.Millisecond)

	// Launch concurrent requests with limited concurrency
	sem := make(chan bool, 10) // Limit to 10 concurrent requests
	for i := 0; i < numRequests; i++ {
		go func(reqID int) {
			sem <- true              // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

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

	// Moderate volume concurrent requests through reverse proxy
	const numRequests = 50
	const timeoutSeconds = 45

	start := time.Now()
	results := make(chan error, numRequests)

	time.Sleep(500 * time.Millisecond)

	// Launch concurrent requests with limited concurrency
	sem := make(chan bool, 10) // Limit to 10 concurrent requests
	for i := 0; i < numRequests; i++ {
		go func(reqID int) {
			sem <- true              // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

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
		Int("total_udp_packets", numUDPTests*10). // 10 packets per test from udpTestAttempts
		Dur("duration", duration).
		Int("errors", len(errors)).
		Msg("UDP reverse stress test completed")
}

func TestMultiClientTCPStressReverse(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel})
	defer server.Close()

	const numClients = 5
	const totalRequests = 100
	const timeoutSeconds = 60

	start := time.Now()
	results := make(chan error, totalRequests)
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

	// Launch all requests concurrently with limited concurrency
	sem := make(chan bool, 10) // Limit to 10 concurrent requests
	for i := 0; i < totalRequests; i++ {
		go func(reqID int) {
			sem <- true              // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			err := testWebConnection(globalHTTPServer, &ProxyConfig{Port: server.SocksPort})
			results <- err
		}(i)
	}

	// Collect all results
	var errors []error
	for i := 0; i < totalRequests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	// Close all clients
	for _, c := range clients {
		c.Close()
	}

	duration := time.Since(start)
	successCount := totalRequests - len(errors)

	assert.Empty(t, errors, "Some TCP requests failed: %v", errors)

	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Multi-client TCP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("clients", numClients).
		Int("total_requests", totalRequests).
		Int("successful_requests", successCount).
		Int("failed_requests", len(errors)).
		Dur("duration", duration).
		Msg("Multi-client TCP stress test completed")
}

func TestMultiClientUDPStressReverse(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{LogLevel: zerolog.DebugLevel})
	defer server.Close()

	const numClients = 5
	const totalUDPTests = 25
	const timeoutSeconds = 90

	start := time.Now()
	results := make(chan error, totalUDPTests)
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

	// Launch all UDP test batches concurrently
	// Each testUDPConnection is one batch (internally tests 10 UDP packets)
	sem := make(chan bool, 5) // Limit to 5 concurrent UDP tests
	for i := 0; i < totalUDPTests; i++ {
		go func(testID int) {
			sem <- true              // Acquire semaphore
			defer func() { <-sem }() // Release semaphore

			// Each testUDPConnection is one batch (10 UDP packets internally)
			err := testUDPConnection(t, globalUDPServer, &ProxyConfig{Port: server.SocksPort})
			results <- err
		}(i)
	}

	// Collect all results
	var errors []error
	for i := 0; i < totalUDPTests; i++ {
		if err := <-results; err != nil {
			errors = append(errors, err)
		}
	}

	// Close all clients
	for _, c := range clients {
		c.Close()
	}

	duration := time.Since(start)
	successCount := totalUDPTests - len(errors)

	assert.Empty(t, errors, "Some UDP test batches failed: %v", errors)

	assert.Less(t, duration, time.Duration(timeoutSeconds)*time.Second,
		"Multi-client UDP stress test took too long: %v", duration)

	TestLogger.Info().
		Int("clients", numClients).
		Int("total_udp_tests", totalUDPTests).
		Int("successful_tests", successCount).
		Int("failed_tests", len(errors)).
		Int("total_udp_packets", totalUDPTests*10). // 10 packets per test from udpTestAttempts
		Dur("duration", duration).
		Msg("Multi-client UDP stress test completed")
}
