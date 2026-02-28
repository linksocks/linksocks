package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/stretchr/testify/require"
)

func TestAuto_STUNUnreachable_StillRelays(t *testing.T) {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithAPI("TOKEN").
		WithDirectEnable(true)
	server := linksocks.NewLinkSocksServer(serverOpt)
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	rev, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	reverseToken := rev.Token

	_, err = server.AddConnectorToken("CONNECTOR", reverseToken)
	require.NoError(t, err)

	// Port is intentionally unreachable (no STUN server bound).
	stunUnreachable := "127.0.0.1:1"

	reverseClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverseToken,
		LoggerPrefix:    "CLT1",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoveryAuto,
		StunServers:     []string{stunUnreachable},
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT2",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoveryAuto,
		StunServers:     []string{stunUnreachable},
	})
	defer connectorClient.Close()

	// Direct dataplane must not become active.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		require.False(t, reverseClient.Client.IsDirectQUICActive())
		require.False(t, connectorClient.Client.IsDirectQUICActive())
		time.Sleep(50 * time.Millisecond)
	}

	// Proxy must still work via relay even if STUN discovery fails.
	require.Eventually(t, func() bool {
		return testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}) == nil
	}, 10*time.Second, 200*time.Millisecond, "relay should remain usable when STUN is unreachable")

	// Optional: server status should not report direct ready.
	baseURL := fmt.Sprintf("http://127.0.0.1:%d", wsPort)
	resp, err := apiRequest(t, "GET", baseURL+"/api/status", "TOKEN", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	var status linksocks.StatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
	if status.Direct != nil {
		for _, p := range status.Direct.Peers {
			require.NotEqual(t, "ready", p.LastDirectState)
		}
	}
}
