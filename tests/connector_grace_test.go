package tests

import (
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// connectorEnv builds a complete connector/provider environment: a reverse
// server, a provider client, and a connector client. Channels are established
// through the connector's local SOCKS port.
func connectorEnv(t *testing.T, grace time.Duration) (*ProxyTestServer, *ProxyTestClient, *ProxyTestClient) {
	t.Helper()
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorToken: "CONNECTOR",
		LoggerPrefix:   "SRV0",
		TransportGrace: grace,
	})
	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          server.Token,
		LoggerPrefix:   "CLT1",
		Reconnect:      true,
		TransportGrace: grace,
	})
	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          "CONNECTOR",
		LoggerPrefix:   "CLT2",
		Reconnect:      true,
		TransportGrace: grace,
	})
	return server, provider, connector
}

// TestConnectorChannelSurvivesProviderWSDisconnect verifies that when the
// provider's WebSocket drops and reconnects, an established channel is
// rebound by the server pump (with a Resume message) and keeps flowing.
func TestConnectorChannelSurvivesProviderWSDisconnect(t *testing.T) {
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

	// Baseline traffic through the connector/provider chain.
	echoOnce(t, tunnel, "ping-before-drop", 10*time.Second)

	// Drop the provider link; the connector side must not notice.
	provider.Client.DisconnectWebSockets()
	waitForChan(t, provider.Client.DisconnectedChan(), 5*time.Second, "provider disconnection")
	waitForChan(t, provider.Client.ConnectedChan(), 15*time.Second, "provider reconnection")

	// New traffic flows again: the channel was rebound onto the new link.
	echoOnce(t, tunnel, "ping-after-provider-reconnect", 10*time.Second)
	require.NoError(t, tunnel.Close())
}

// TestConnectorChannelSurvivesConnectorWSDisconnect verifies that when the
// connector's WebSocket drops and reconnects, the established channel is
// rebound (the connector resumes it on the new link) and keeps flowing.
func TestConnectorChannelSurvivesConnectorWSDisconnect(t *testing.T) {
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

	echoOnce(t, tunnel, "ping-before-drop", 10*time.Second)

	// Drop the connector link; it reconnects and resumes the channel.
	connector.Client.DisconnectWebSockets()
	waitForChan(t, connector.Client.DisconnectedChan(), 5*time.Second, "connector disconnection")
	waitForChan(t, connector.Client.ConnectedChan(), 15*time.Second, "connector reconnection")

	echoOnce(t, tunnel, "ping-after-connector-reconnect", 10*time.Second)
	require.NoError(t, tunnel.Close())
}

// TestConnectorChannelExpiresAfterProviderGrace verifies that when the
// provider does not return within the grace window, the server tears the
// channel down and notifies the connector, closing the tunnel.
func TestConnectorChannelExpiresAfterProviderGrace(t *testing.T) {
	grace := 1500 * time.Millisecond
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorToken: "CONNECTOR",
		LoggerPrefix:   "SRV0",
		TransportGrace: grace,
	})
	defer server.Close()
	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          "CONNECTOR",
		LoggerPrefix:   "CLT2",
		Reconnect:      true,
		TransportGrace: grace,
	})
	defer connector.Close()
	// The provider must NOT reconnect: it stays down so the grace expires.
	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          server.Token,
		LoggerPrefix:   "CLT1",
		TransportGrace: grace,
	})
	defer provider.Close()

	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	host, port := splitHostPort(t, echoAddr)
	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", connector.SocksPort), host, port)
	defer tunnel.Close()
	echoOnce(t, tunnel, "ping-before-drop", 10*time.Second)

	provider.Client.DisconnectWebSockets()
	waitForChan(t, provider.Client.DisconnectedChan(), 5*time.Second, "provider disconnection")

	// The server notifies the connector with a disconnect; the tunnel closes
	// shortly after the grace window.
	require.NoError(t, tunnel.SetReadDeadline(time.Now().Add(15*time.Second)))
	buf := make([]byte, 1)
	_, err := tunnel.Read(buf)
	require.Error(t, err, "expected the channel to be torn down after the grace window")
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		t.Fatal("tunnel still open after the transport grace window")
	}
	t.Logf("tunnel closed: %v", err)
}