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

// TestAuto_DirectDisconnectReconnect verifies the full direct lifecycle:
// establish → disconnect → relay fallback → reconnect → direct active again.
func TestAuto_DirectDisconnectReconnect(t *testing.T) {
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
		Reconnect:       true,
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT2",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
		Reconnect:       true,
	})
	defer connectorClient.Close()

	// --- Phase 1: Wait for direct to become ready and QUIC active ---
	t.Log("Phase 1: waiting for direct QUIC dataplane to become active")
	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 15*time.Second, 200*time.Millisecond, "direct QUIC dataplane did not become active")

	// Verify proxy works while direct is active.
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}))
	t.Log("Phase 1: direct QUIC active, proxy works")

	// --- Phase 2: Force disconnect and verify relay fallback ---
	t.Log("Phase 2: forcing direct disconnect")
	reverseClient.Client.ForceCloseDirectQUIC()
	connectorClient.Client.ForceCloseDirectQUIC()

	// Verify QUIC is actually down — must check immediately, not with Eventually,
	// because the QUIC agent may reconnect quickly.
	time.Sleep(100 * time.Millisecond)
	require.False(t, reverseClient.Client.IsDirectQUICActive(), "reverse QUIC still active immediately after close")
	require.False(t, connectorClient.Client.IsDirectQUICActive(), "connector QUIC still active immediately after close")

	// Proxy must still work via relay after direct drops.
	require.Eventually(t, func() bool {
		return testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}) == nil
	}, 10*time.Second, 200*time.Millisecond, "relay fallback did not work after direct disconnect")
	t.Log("Phase 2: relay fallback works after direct disconnect")

	// --- Phase 3: Wait for automatic direct recovery ---
	// Backoff starts at reconnectDelay (1s in test helpers), then 1.5s, 2.25s ...
	// Allow enough time for a few retry cycles.
	t.Log("Phase 3: waiting for direct QUIC to recover automatically")
	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 60*time.Second, 500*time.Millisecond, "direct QUIC did not recover after disconnect")

	// Verify proxy works through recovered direct.
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}))
	t.Log("Phase 3: direct QUIC recovered, proxy works again")
}

// TestAuto_DirectRepeatedDisconnect verifies that direct can recover through
// multiple disconnect cycles without getting stuck.
func TestAuto_DirectRepeatedDisconnect(t *testing.T) {
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
		Reconnect:       true,
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT2",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
		Reconnect:       true,
	})
	defer connectorClient.Close()

	// Wait for initial direct connection.
	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 15*time.Second, 200*time.Millisecond, "initial direct QUIC did not become active")

	// Run 3 disconnect-reconnect cycles.
	for cycle := 1; cycle <= 3; cycle++ {
		t.Logf("Cycle %d: disconnecting", cycle)

		reverseClient.Client.ForceCloseDirectQUIC()
		connectorClient.Client.ForceCloseDirectQUIC()

		time.Sleep(100 * time.Millisecond)
		require.False(t, reverseClient.Client.IsDirectQUICActive(), "cycle %d: reverse QUIC still active after close", cycle)
		require.False(t, connectorClient.Client.IsDirectQUICActive(), "cycle %d: connector QUIC still active after close", cycle)

		// Relay must work during degraded state.
		require.Eventually(t, func() bool {
			return testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}) == nil
		}, 10*time.Second, 200*time.Millisecond, "cycle %d: relay fallback failed", cycle)

		t.Logf("Cycle %d: waiting for recovery", cycle)
		require.Eventually(t, func() bool {
			return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
		}, 60*time.Second, 500*time.Millisecond, "cycle %d: direct QUIC did not recover", cycle)

		require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}))
		t.Logf("Cycle %d: recovered", cycle)
	}
}

// TestAuto_DirectDisconnectRelayKeepsWorking verifies that existing relay
// connections survive a direct disconnect and new connections also work.
func TestAuto_DirectDisconnectRelayKeepsWorking(t *testing.T) {
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
		Reconnect:       true,
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT2",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
		Reconnect:       true,
	})
	defer connectorClient.Close()

	// Wait for direct to be active.
	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 15*time.Second, 200*time.Millisecond, "direct QUIC did not become active")

	// Verify proxy works before disconnect.
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}))

	// Disconnect direct.
	reverseClient.Client.ForceCloseDirectQUIC()
	connectorClient.Client.ForceCloseDirectQUIC()

	// Verify QUIC is actually down.
	time.Sleep(100 * time.Millisecond)
	require.False(t, reverseClient.Client.IsDirectQUICActive(), "reverse QUIC still active after close")
	require.False(t, connectorClient.Client.IsDirectQUICActive(), "connector QUIC still active after close")

	// Immediately after disconnect, relay should work.
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}))

	// And continue working for subsequent requests.
	for i := 0; i < 5; i++ {
		require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: connectorClient.SocksPort}))
	}
}

// TestAuto_DirectDegradedStatusReportedToServer verifies that the server API
// reports degraded state when direct drops and ready state when it recovers.
func TestAuto_DirectDegradedStatusReportedToServer(t *testing.T) {
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
		Reconnect:       true,
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT2",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
		Reconnect:       true,
	})
	defer connectorClient.Close()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", wsPort)

	// Wait for ready state.
	require.Eventually(t, func() bool {
		return getDirectPeerState(t, baseURL, "ready") >= 2
	}, 12*time.Second, 200*time.Millisecond, "direct peers did not reach ready state")

	// Force degraded with a short cooldown. Don't close QUIC — the QUIC agent
	// will recover automatically after the cooldown via directMarkRecovered().
	reverseClient.Client.ForceDirectDegraded(5*time.Second, "test")
	connectorClient.Client.ForceDirectDegraded(5*time.Second, "test")

	// Verify degraded state reported.
	require.Eventually(t, func() bool {
		return getDirectPeerState(t, baseURL, "degraded") >= 2
	}, 3*time.Second, 100*time.Millisecond, "direct peers did not report degraded state")

	// Wait for automatic recovery to ready — the QUIC agent detects the active
	// connection after the cooldown and sends a ready status.
	require.Eventually(t, func() bool {
		return getDirectPeerState(t, baseURL, "ready") >= 2
	}, 15*time.Second, 200*time.Millisecond, "direct peers did not recover to ready state")
}

// getDirectPeerState queries the server API and counts peers in the given direct state.
func getDirectPeerState(t *testing.T, baseURL, wantState string) int {
	t.Helper()
	resp, err := apiRequest(t, "GET", baseURL+"/api/status", "TOKEN", nil)
	if err != nil {
		return 0
	}
	defer resp.Body.Close()

	var status linksocks.StatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		return 0
	}
	if status.Direct == nil {
		return 0
	}
	count := 0
	for _, p := range status.Direct.Peers {
		if !p.SupportsDirect {
			continue
		}
		if p.Role != "reverse" && p.Role != "connector" {
			continue
		}
		if p.LastDirectState == wantState {
			count++
		}
	}
	return count
}
