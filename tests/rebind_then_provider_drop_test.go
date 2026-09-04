package tests

import (
	"fmt"
	"testing"
	"time"
)

// TestConnectorReturnPathAfterConnectorRebindThenProviderDrop verifies that
// return-path data keeps flowing after the connector reconnects (rebinding
// the channel onto the fresh link) and the provider link then drops and
// recovers. The provider-rebind loop must not point the channel back at the
// connector's dead WebSocket.
func TestConnectorReturnPathAfterConnectorRebindThenProviderDrop(t *testing.T) {
	grace := 30 * time.Second
	server, provider, connector := connectorEnv(t, grace)
	defer server.Close()
	defer provider.Close()
	defer connector.Close()

	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	host, port := splitHostPort(t, echoAddr)
	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", connector.SocksPort), host, port)
	defer tunnel.Close()

	// Warm up the channel.
	echoOnce(t, tunnel, "ping-before\n", 10*time.Second)

	// Drop the connector link; on reconnect the channel is rebound onto the
	// fresh connector WebSocket.
	connector.Client.DisconnectWebSockets()
	waitForChan(t, connector.Client.DisconnectedChan(), 5*time.Second, "connector disconnection")
	waitForChan(t, connector.Client.ConnectedChan(), 10*time.Second, "connector reconnection")

	// With the connector on a fresh link, drop the provider. The server's
	// per-channel pump rebinds the provider and re-publishes the connector
	// mapping; it must not overwrite the live connector link with the dead
	// one the pump captured at startup.
	provider.Client.DisconnectWebSockets()
	waitForChan(t, provider.Client.DisconnectedChan(), 5*time.Second, "provider disconnection")
	waitForChan(t, provider.Client.ConnectedChan(), 10*time.Second, "provider reconnection")

	// Return-path data must still arrive at the connector.
	echoOnce(t, tunnel, "ping-after\n", 10*time.Second)
}