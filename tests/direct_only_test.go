package tests

import (
	"testing"

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

	err := testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Client.SocksPort})
	require.Error(t, err)
}
