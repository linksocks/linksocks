package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// A connector token used as a shared secret must still carry a normal proxy
// connection end to end, with both sides proving the secret over the sealed
// channel before any destination or payload crosses it.
func TestConnectorSecretEndToEnd(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{ConnectorAutonomy: true})
	defer server.Close()

	providerSink := &lockedLogSink{}
	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT-SECRET-PROVIDER",
		LogSink:      providerSink,
		LogLevel:     zerolog.DebugLevel,
	})
	defer provider.Close()

	secret, err := provider.Client.AddConnectorSecret("CONNECTOR")
	require.NoError(t, err)
	require.Equal(t, "CONNECTOR", secret)

	connectorSink := &lockedLogSink{}
	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:          server.WSPort,
		Token:           "CONNECTOR",
		ConnectorSecret: "CONNECTOR",
		LoggerPrefix:    "CLT-SECRET-CONNECTOR",
		LogSink:         connectorSink,
		LogLevel:        zerolog.DebugLevel,
	})
	defer connector.Close()

	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connector.SocksPort}))
	require.Contains(t, providerSink.String(), "Channel authenticated by connector secret")
	require.Contains(t, connectorSink.String(), "Channel authenticated by connector secret")
}

// A connector with the wrong secret authenticates with a handle the relay
// never saw; the connection must fail with guidance instead of silently
// reaching a provider.
func TestConnectorSecretWrongSecretRejected(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{ConnectorAutonomy: true})
	defer server.Close()

	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT-SECRET-MISMATCH-PROVIDER",
	})
	defer provider.Close()

	_, err := provider.Client.AddConnectorSecret("right-secret")
	require.NoError(t, err)

	socksPort, err := getFreePort()
	require.NoError(t, err)

	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", server.WSPort)).
		WithSocksPort(socksPort).
		WithConnectorTokenSecret("wrong-secret").
		WithNoEnvProxy(true)
	client := linksocks.NewLinkSocksClient("", clientOpt)
	defer client.Close()

	readyErr := client.WaitReady(context.Background(), 5*time.Second)
	require.Error(t, readyErr)
	require.Contains(t, readyErr.Error(), "connector secret")
}

// The direct data plane is keyed from the connector secret as well: a pairing
// backed by a secret must still build its QUIC plane (both ends mix the same
// key), and the channel on top of it must authenticate.
func TestConnectorSecretBindsDirectMode(t *testing.T) {
	stunAddr, stunClose := startTestStunServer(t)
	defer stunClose()

	wsPort, err := getFreePort()
	require.NoError(t, err)

	server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(createPrefixedLogger("SRV-SECRET-DIRECT")).
		WithAPI("TOKEN").
		WithDirectEnable(true))
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	reverse, err := server.AddReverseToken(&linksocks.ReverseTokenOptions{AllowManageConnector: true})
	require.NoError(t, err)

	providerSink := &lockedLogSink{}
	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverse.Token,
		LoggerPrefix:    "CLT-SECRET-DIRECT-PROVIDER",
		LogSink:         providerSink,
		LogLevel:        zerolog.DebugLevel,
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer provider.Close()

	secret, err := provider.Client.AddConnectorSecret("DIRECT-SECRET")
	require.NoError(t, err)

	connectorSink := &lockedLogSink{}
	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           secret,
		ConnectorSecret: secret,
		LoggerPrefix:    "CLT-SECRET-DIRECT-CONNECTOR",
		LogSink:         connectorSink,
		LogLevel:        zerolog.DebugLevel,
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer connector.Close()

	require.Eventually(t, func() bool {
		return provider.Client.IsDirectQUICActive() && connector.Client.IsDirectQUICActive()
	}, 25*time.Second, 200*time.Millisecond, "the secret-bound direct QUIC plane did not become active\nprovider:\n%s\nconnector:\n%s",
		sinkDigest(providerSink), sinkDigest(connectorSink))

	// A direct plane only comes up when both ends mixed the same secret into
	// the probe and QUIC key material, so an active plane plus a working
	// transfer is the assertion here.
	require.Contains(t, providerSink.String(), "Direct connectivity ready")
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connector.SocksPort}))
}
