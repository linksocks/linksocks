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
