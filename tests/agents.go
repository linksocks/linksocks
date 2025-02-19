package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/zetxtech/wssocks/wssocks"

	"github.com/stretchr/testify/require"
)

// ProxyTestServer encapsulates the server-side test environment
type ProxyTestServer struct {
	Server         *wssocks.WSSocksServer
	WSPort         int
	SocksPort      int
	Token          string
	ConnectorToken string
	Close          func()
}

type ProxyTestServerOption struct {
	WSPort            int
	SocksPort         int
	SocksUser         string
	SocksPassword     string
	Token             string
	ConnectorToken    string
	ConnectorAutonomy bool
	PortPool          *wssocks.PortPool
	LoggerPrefix      string
	Reconnect         bool
}

// ProxyTestClient encapsulates the client-side test environment
type ProxyTestClient struct {
	Client    *wssocks.WSSocksClient
	SocksPort int
	Close     func()
}

// ProxyTestEnv encapsulates both server and client test environments
type ProxyTestEnv struct {
	Server    *ProxyTestServer
	Client    *ProxyTestClient
	WSPort    int // WebSocket server port
	SocksPort int // SOCKS proxy port (client port for forward mode, server port for reverse mode)
	Close     func()
}

// forwardServer creates a WSS server in forward mode
func forwardServer(t *testing.T, opt *ProxyTestServerOption) *ProxyTestServer {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	token := ""

	var serverOpt *wssocks.ServerOption
	if opt == nil {
		logger := createPrefixedLogger("SRV0")
		serverOpt = wssocks.DefaultServerOption().
			WithWSPort(wsPort).
			WithLogger(logger)
	} else {
		// Set Token
		token = opt.Token

		// Use provided options or defaults
		if opt.LoggerPrefix != "" {
			logger := createPrefixedLogger(opt.LoggerPrefix)
			serverOpt = wssocks.DefaultServerOption().WithLogger(logger)
		} else {
			logger := createPrefixedLogger("SRV0")
			serverOpt = wssocks.DefaultServerOption().WithLogger(logger)
		}

		// Set WSPort
		if opt.WSPort != 0 {
			wsPort = opt.WSPort
		}
		serverOpt.WithWSPort(wsPort)

		// Set PortPool if provided
		if opt.PortPool != nil {
			serverOpt.WithPortPool(opt.PortPool)
		}
	}
	server := wssocks.NewWSSocksServer(serverOpt)
	token, err = server.AddForwardToken(token)
	require.NoError(t, err)
	require.NotEmpty(t, token)

	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	return &ProxyTestServer{
		Server: server,
		WSPort: wsPort,
		Token:  token,
		Close:  server.Close,
	}
}

// forwardClient creates a WSS client in forward mode
func forwardClient(t *testing.T, wsPort int, token string, prefix string) *ProxyTestClient {
	socksPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger(prefix)

	clientOpt := wssocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://localhost:%d", wsPort)).
		WithSocksPort(socksPort).
		WithReconnectDelay(1 * time.Second).
		WithLogger(logger)
	client := wssocks.NewWSSocksClient(token, clientOpt)

	require.NoError(t, client.WaitReady(context.Background(), 5*time.Second))

	return &ProxyTestClient{
		Client:    client,
		SocksPort: socksPort,
		Close:     client.Close,
	}
}

// reverseServer creates a WSS server in reverse mode
func reverseServer(t *testing.T, opt *ProxyTestServerOption) *ProxyTestServer {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	token := ""
	connectorToken := ""
	socksUser := ""
	socksPassword := ""
	connectorAutonomy := false

	socksPort, err := getFreePort()
	require.NoError(t, err)

	var serverOpt *wssocks.ServerOption
	if opt == nil {
		logger := createPrefixedLogger("SRV0")
		serverOpt = wssocks.DefaultServerOption().
			WithWSPort(wsPort).
			WithLogger(logger)
	} else {
		token = opt.Token
		socksUser = opt.SocksUser
		socksPassword = opt.SocksPassword
		connectorAutonomy = opt.ConnectorAutonomy

		// Use provided options or defaults
		if opt.LoggerPrefix != "" {
			logger := createPrefixedLogger(opt.LoggerPrefix)
			serverOpt = wssocks.DefaultServerOption().WithLogger(logger)
		} else {
			logger := createPrefixedLogger("SRV0")
			serverOpt = wssocks.DefaultServerOption().WithLogger(logger)
		}

		// Set WSPort
		if opt.WSPort != 0 {
			wsPort = opt.WSPort
		}
		serverOpt.WithWSPort(wsPort)

		// Set PortPool if provided
		if opt.PortPool != nil {
			serverOpt.WithPortPool(opt.PortPool)
		}

		// Set SocksPort if provided
		if opt.SocksPort != 0 {
			socksPort = opt.SocksPort
		}
	}

	server := wssocks.NewWSSocksServer(serverOpt)
	token, socksPort, err = server.AddReverseToken(&wssocks.ReverseTokenOptions{
		Port:                 socksPort,
		Token:                token,
		Username:             socksUser,
		Password:             socksPassword,
		AllowManageConnector: connectorAutonomy,
	})
	require.NoError(t, err)
	require.NotZero(t, socksPort)

	if opt != nil && opt.ConnectorToken != "" {
		connectorToken, err := server.AddConnectorToken(opt.ConnectorToken, token)
		require.NoError(t, err)
		require.NotEmpty(t, connectorToken)
	}

	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	return &ProxyTestServer{
		Server:         server,
		WSPort:         wsPort,
		SocksPort:      socksPort,
		Token:          token,
		ConnectorToken: connectorToken,
		Close:          server.Close,
	}
}

// reverseClient creates a WSS client in reverse mode
func reverseClient(t *testing.T, wsPort int, token string, prefix string) *ProxyTestClient {
	logger := createPrefixedLogger(prefix)

	clientOpt := wssocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://localhost:%d", wsPort)).
		WithReconnectDelay(1 * time.Second).
		WithReverse(true).
		WithLogger(logger)
	client := wssocks.NewWSSocksClient(token, clientOpt)

	require.NoError(t, client.WaitReady(context.Background(), 5*time.Second))

	return &ProxyTestClient{
		Client: client,
		Close:  client.Close,
	}
}

// forwardProxy creates a complete forward proxy test environment
func forwardProxy(t *testing.T) *ProxyTestEnv {
	server := forwardServer(t, nil)
	client := forwardClient(t, server.WSPort, server.Token, "CLT0")

	return &ProxyTestEnv{
		Server:    server,
		Client:    client,
		WSPort:    server.WSPort,
		SocksPort: client.SocksPort, // In forward mode, use client's SOCKS port
		Close: func() {
			client.Close()
			server.Close()
		},
	}
}

// reverseProxy creates a complete reverse proxy test environment
func reverseProxy(t *testing.T) *ProxyTestEnv {
	server := reverseServer(t, nil)
	client := reverseClient(t, server.WSPort, server.Token, "CLT0")

	return &ProxyTestEnv{
		Server:    server,
		Client:    client,
		WSPort:    server.WSPort,
		SocksPort: server.SocksPort, // In reverse mode, use server's SOCKS port
		Close: func() {
			client.Close()
			server.Close()
		},
	}
}
