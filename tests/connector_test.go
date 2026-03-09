package tests

import (
	"testing"
	"time"

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
