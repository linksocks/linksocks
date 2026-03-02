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

func TestDirectDiscovery_ServerRendezvous_Works(t *testing.T) {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithAPI("TOKEN").
		WithDirectEnable(true).
		WithDirectRendezvousUDP(true).
		WithDirectRendezvousHost("127.0.0.1").
		WithDirectRendezvousPort(0)
	server := linksocks.NewLinkSocksServer(serverOpt)
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	rev, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	reverseToken := rev.Token

	_, err = server.AddConnectorToken("CONNECTOR", reverseToken)
	require.NoError(t, err)

	reverseClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverseToken,
		LoggerPrefix:    "CLT1",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoveryServer,
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT2",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoveryServer,
	})
	defer connectorClient.Close()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", wsPort)
	require.Eventually(t, func() bool {
		resp, err := apiRequest(t, "GET", baseURL+"/api/status", "TOKEN", nil)
		if err != nil {
			return false
		}
		defer resp.Body.Close()

		var status linksocks.StatusResponse
		if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
			return false
		}
		if status.Direct == nil {
			return false
		}
		ready := 0
		for _, p := range status.Direct.Peers {
			if !p.SupportsDirect {
				continue
			}
			if p.Role != "reverse" && p.Role != "connector" {
				continue
			}
			if p.LastDirectState == "ready" {
				ready++
			}
		}
		return ready >= 2
	}, 8*time.Second, 100*time.Millisecond, "direct probing did not become ready via server rendezvous")

	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 6*time.Second, 100*time.Millisecond, "direct quic dataplane did not become active via server rendezvous")
}
