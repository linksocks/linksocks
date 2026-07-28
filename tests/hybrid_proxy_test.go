package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestForwardHybridHTTPProxy(t *testing.T) {
	env := forwardProxy(t)
	defer env.Close()

	// SOCKS5 on the same port must keep working.
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Client.SocksPort}))

	// HTTP proxy (absolute-form GET for plain HTTP targets) on the same port.
	require.NoError(t, testWebConnectionHTTPProxy(globalHTTPServer, &ProxyConfig{Port: env.Client.SocksPort}))
}

func TestReverseHybridHTTPProxy(t *testing.T) {
	env := reverseProxy(t)
	defer env.Close()

	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: env.Server.SocksPort}))
	require.NoError(t, testWebConnectionHTTPProxy(globalHTTPServer, &ProxyConfig{Port: env.Server.SocksPort}))
}

func TestForwardHybridHTTPProxyAuth(t *testing.T) {
	const (
		username = "proxy-user"
		password = "proxy-pass"
	)

	env := forwardProxyWithOptions(t, nil, &ProxyTestClientOption{
		SocksUsername: username,
		SocksPassword: password,
	})
	defer env.Close()

	// Wrong credentials should fail.
	require.Error(t, testWebConnectionHTTPProxy(globalHTTPServer, &ProxyConfig{
		Port:     env.Client.SocksPort,
		Username: "wrong",
		Password: "wrong",
	}))

	// Correct credentials should succeed for both protocols.
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{
		Port:     env.Client.SocksPort,
		Username: username,
		Password: password,
	}))
	require.NoError(t, testWebConnectionHTTPProxy(globalHTTPServer, &ProxyConfig{
		Port:     env.Client.SocksPort,
		Username: username,
		Password: password,
	}))
}
