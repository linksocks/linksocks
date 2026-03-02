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

func TestAuto_DegradedForcesRelayFallback_NewChannels(t *testing.T) {
	stunAddr, stunClose := startTestStunServer(t)
	defer stunClose()

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

	reverseClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverseToken,
		LoggerPrefix:    "CLT1",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT2",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
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
	}, 8*time.Second, 100*time.Millisecond, "direct probing did not become ready")

	// Wait for QUIC dataplane to actually become active so the test covers the
	// transition from direct-ready to degraded fallback.
	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 6*time.Second, 100*time.Millisecond, "direct quic dataplane did not become active")

	// Force degraded state on both sides; new channels should fall back to relay.
	// We also force close QUIC so the route selection cannot pick direct even if it was ready.
	reverseClient.Client.ForceDirectDegraded(30*time.Second, "test")
	connectorClient.Client.ForceDirectDegraded(30*time.Second, "test")
	reverseClient.Client.ForceCloseDirectQUIC()
	connectorClient.Client.ForceCloseDirectQUIC()

	require.Eventually(t, func() bool {
		return !reverseClient.Client.IsDirectQUICActive() && !connectorClient.Client.IsDirectQUICActive()
	}, 2*time.Second, 50*time.Millisecond, "direct quic dataplane still active after forced close")

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
		degraded := 0
		for _, p := range status.Direct.Peers {
			if !p.SupportsDirect {
				continue
			}
			if p.Role != "reverse" && p.Role != "connector" {
				continue
			}
			if p.LastDirectState == "degraded" {
				degraded++
			}
		}
		return degraded >= 2
	}, 3*time.Second, 100*time.Millisecond, "direct state did not become degraded")

	// After degrading and closing QUIC, proxy must still work via relay.
	require.Eventually(t, func() bool {
		return testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}) == nil
	}, 12*time.Second, 200*time.Millisecond, "relay fallback did not become available in time")
}
