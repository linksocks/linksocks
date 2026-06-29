package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"

	"github.com/stretchr/testify/require"
)

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

func TestConnectorAutonomyReplaysAfterReconnect(t *testing.T) {
	const connectorToken = "CONNECTOR"

	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorAutonomy: true,
	})

	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT1",
		Reconnect:    true,
	})
	defer provider.Close()

	token, err := provider.Client.AddConnector(connectorToken)
	require.NoError(t, err)
	require.Equal(t, connectorToken, token)

	server.Server.RemoveToken(connectorToken)

	wsPort := server.WSPort
	socksPort := server.SocksPort
	reverseToken := server.Token
	server.Close()

	server = reverseServer(t, &ProxyTestServerOption{
		WSPort:            wsPort,
		SocksPort:         socksPort,
		Token:             reverseToken,
		ConnectorAutonomy: true,
	})
	defer server.Close()

	require.Eventually(t, func() bool {
		return connectorTokenReady(wsPort, connectorToken)
	}, 10*time.Second, 100*time.Millisecond)

	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:       wsPort,
		Token:        connectorToken,
		LoggerPrefix: "CLT2",
	})
	defer connector.Close()

	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connector.SocksPort}))
}

func connectorTokenReady(wsPort int, connectorToken string) bool {
	socksPort, err := getFreePort()
	if err != nil {
		return false
	}

	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithSocksPort(socksPort).
		WithReconnect(false).
		WithNoEnvProxy(true).
		WithLogger(createPrefixedLogger("CHK"))
	client := linksocks.NewLinkSocksClient(connectorToken, clientOpt)
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	defer client.Close()

	return client.WaitReady(ctx, 500*time.Millisecond) == nil
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

func TestConnectorWaitsForProviderReconnect(t *testing.T) {
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorToken: "CONNECTOR",
		ConnectorWait:  2 * time.Second,
	})
	defer server.Close()

	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT1",
	})

	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        "CONNECTOR",
		LoggerPrefix: "CLT2",
	})
	defer connector.Close()

	provider.Close()
	require.Eventually(t, func() bool {
		return server.Server.GetTokenClientCount(server.Token) == 0
	}, 5*time.Second, 50*time.Millisecond)

	errCh := make(chan error, 1)
	go func() {
		errCh <- testWebConnection(globalHTTPServer, &ProxyConfig{Port: connector.SocksPort})
	}()

	time.Sleep(300 * time.Millisecond)
	provider = reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT3",
	})
	defer provider.Close()

	select {
	case err := <-errCh:
		require.NoError(t, err)
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for connector request to complete")
	}
}
