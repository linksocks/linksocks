package tests

import (
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/stretchr/testify/require"
)

func TestDirectOnly_RefuseOnTimeoutWithoutRelay(t *testing.T) {
	// No direct-enable on server => direct signaling not supported, so direct-only must refuse.
	env := forwardProxyWithOptions(t, &ProxyTestServerOption{}, &ProxyTestClientOption{
		DirectMode:       linksocks.DirectModeDirectOnly,
		DirectDiscovery:  linksocks.DirectDiscoverySTUN,
		StunServers:      []string{"127.0.0.1:0"},
		DirectOnlyAction: linksocks.DirectOnlyActionRefuse,
	})
	defer env.Close()

	err := testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Client.SocksPort})
	require.Error(t, err)
}

func TestDirectOnly_ExitOnTimeoutWithoutRelay(t *testing.T) {
	env := forwardProxyWithOptions(t, &ProxyTestServerOption{}, &ProxyTestClientOption{
		DirectMode:       linksocks.DirectModeDirectOnly,
		DirectDiscovery:  linksocks.DirectDiscoverySTUN,
		StunServers:      []string{"127.0.0.1:0"},
		DirectOnlyAction: linksocks.DirectOnlyActionExit,
	})
	defer env.Close()

	// Wait for client to exit due to direct-only failure
	// The client should either fail during WaitReady (fast path) or close shortly after
	timeout := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()

	clientClosed := false
	for !clientClosed {
		select {
		case <-timeout:
			t.Fatal("timeout waiting for client to exit due to direct-only failure")
		case <-ticker.C:
			// Check if disconnected channel is closed (client stopped)
			select {
			case <-env.Client.Client.DisconnectedChan():
				clientClosed = true
			default:
			}
		}
	}

	// Client should be closed, connection attempts should fail
	err := testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Client.SocksPort})
	require.Error(t, err)
}
