package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/stretchr/testify/require"
)

func TestRetryAuthFailure(t *testing.T) {
	// Create server
	server := forwardServer(t, nil)
	defer server.Close()

	// Create client with wrong token and retryAuthFailure enabled
	socksPort, err := getFreePort()
	require.NoError(t, err)

	clientLogger := createPrefixedLogger("CLT0")
	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", server.WSPort)).
		WithSocksPort(socksPort).
		WithReconnect(true).
		WithRetryAuthFailure(true).
		WithReconnectDelay(100 * time.Millisecond).
		WithLogger(clientLogger).
		WithNoEnvProxy(true)

	client := linksocks.NewLinkSocksClient("wrong-token", clientOpt)

	// Give client some time to attempt connection and fail, then retry
	time.Sleep(500 * time.Millisecond)

	// Client should still be running (not exited) because retryAuthFailure is enabled
	client.Close()

	// Create client with correct token to verify server is still working
	correctClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:    server.WSPort,
		Token:     server.Token,
		SocksPort: socksPort,
		Reconnect: true,
	})
	defer correctClient.Close()

	// Verify correct client connects successfully
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: correctClient.SocksPort}))
}

func TestRetryAuthFailureDisabled(t *testing.T) {
	// Create server
	server := forwardServer(t, nil)
	defer server.Close()

	// Create client with wrong token and retryAuthFailure disabled (default)
	socksPort, err := getFreePort()
	require.NoError(t, err)

	clientLogger := createPrefixedLogger("CLT0")
	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", server.WSPort)).
		WithSocksPort(socksPort).
		WithReconnect(true).
		WithRetryAuthFailure(false).
		WithReconnectDelay(100 * time.Millisecond).
		WithLogger(clientLogger).
		WithNoEnvProxy(true)

	client := linksocks.NewLinkSocksClient("wrong-token", clientOpt)

	// Client should fail quickly because retryAuthFailure is disabled
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err = client.WaitReady(ctx, 1*time.Second)
	require.Error(t, err, "Expected authentication failure error")
}

func TestBackoffResetAfterEstablishedConnection(t *testing.T) {
	// This test verifies that the backoff resets after a connection is established
	// and then drops, rather than continuing to increase.

	// Create initial server
	server := forwardServer(t, nil)

	// Create client with reconnect enabled
	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:    server.WSPort,
		Token:     server.Token,
		Reconnect: true,
	})

	// Verify initial connection works
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client.SocksPort}))

	// Close server to simulate connection drop
	server.Close()

	// Wait for client to detect disconnection
	select {
	case <-client.Client.DisconnectedChan():
		// Disconnection detected
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for client disconnection")
	}

	// Record time before restart
	startTime := time.Now()

	// Restart server on same port
	newServer := forwardServer(t, &ProxyTestServerOption{
		WSPort: server.WSPort,
		Token:  server.Token,
	})
	defer newServer.Close()

	// Wait for client to reconnect
	select {
	case <-client.Client.ConnectedChan():
		// Connection established
	case <-time.After(5 * time.Second):
		t.Fatal("Timeout waiting for client reconnection")
	}

	// Verify reconnection happened quickly (within 3 seconds)
	// If backoff wasn't reset after the established connection dropped,
	// it would take much longer due to exponential backoff accumulation
	reconnectDuration := time.Since(startTime)
	require.Less(t, reconnectDuration, 3*time.Second,
		"Reconnection took too long (%v), backoff may not have been reset", reconnectDuration)

	// Verify connection works after reconnect
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client.SocksPort}))
}

func TestDefaultReconnectDelay(t *testing.T) {
	// Verify that the default reconnect delay is 1 second
	opt := linksocks.DefaultClientOption()
	require.Equal(t, 1*time.Second, opt.ReconnectDelay,
		"Default reconnect delay should be 1 second")
}
