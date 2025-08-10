package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/rs/zerolog"
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
	LogLevel          zerolog.Level
	Reconnect         bool
	FastOpen          bool
}

// ProxyTestClient encapsulates the client-side test environment
type ProxyTestClient struct {
	Client    *wssocks.WSSocksClient
	SocksPort int
	Close     func()
}

type ProxyTestClientOption struct {
	WSPort       int           // WebSocket server port
	Token        string        // Client token
	SocksPort    int           // Custom SOCKS port
	Threads      int           // Number of client threads
	LoggerPrefix string        // Logger prefix for the client
	LogLevel     zerolog.Level // Log level for the client logger
	Reverse      bool          // Whether to use reverse mode
	FastOpen     bool          // Whether to enable fast-open mode
	Reconnect    bool          // Whether to enable auto-reconnection
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
		var logger zerolog.Logger
		prefix := opt.LoggerPrefix
		if prefix == "" {
			prefix = "SRV0"
		}

		if opt.LogLevel != 0 {
			logger = createPrefixedLoggerWithLevel(prefix, opt.LogLevel)
		} else {
			logger = createPrefixedLogger(prefix)
		}
		serverOpt = wssocks.DefaultServerOption().WithLogger(logger)

		// Set WSPort
		if opt.WSPort != 0 {
			wsPort = opt.WSPort
		}
		serverOpt.WithWSPort(wsPort)

		// Set PortPool if provided
		if opt.PortPool != nil {
			serverOpt.WithPortPool(opt.PortPool)
		}

		// Set FastOpen
		serverOpt.WithFastOpen(opt.FastOpen)
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
func forwardClient(t *testing.T, opt *ProxyTestClientOption) *ProxyTestClient {
	if opt == nil {
		opt = &ProxyTestClientOption{}
	}

	if opt.LoggerPrefix == "" {
		opt.LoggerPrefix = "CLT0"
	}

	socksPort := opt.SocksPort
	if socksPort == 0 {
		var err error
		socksPort, err = getFreePort()
		require.NoError(t, err)
	}

	var logger zerolog.Logger
	if opt.LogLevel != 0 {
		logger = createPrefixedLoggerWithLevel(opt.LoggerPrefix, opt.LogLevel)
	} else {
		logger = createPrefixedLogger(opt.LoggerPrefix)
	}
	clientOpt := wssocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://localhost:%d", opt.WSPort)).
		WithSocksPort(socksPort).
		WithReconnectDelay(1 * time.Second).
		WithFastOpen(opt.FastOpen).
		WithLogger(logger)

	if opt.Reconnect {
		clientOpt.WithReconnect(true)
	}

	if opt.Threads > 0 {
		clientOpt.WithThreads(opt.Threads)
	}

	client := wssocks.NewWSSocksClient(opt.Token, clientOpt)
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
		var logger zerolog.Logger
		prefix := opt.LoggerPrefix
		if prefix == "" {
			prefix = "SRV0"
		}

		if opt.LogLevel != 0 {
			logger = createPrefixedLoggerWithLevel(prefix, opt.LogLevel)
		} else {
			logger = createPrefixedLogger(prefix)
		}
		serverOpt = wssocks.DefaultServerOption().WithLogger(logger)

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

		// Set FastOpen
		serverOpt.WithFastOpen(opt.FastOpen)
	}

	server := wssocks.NewWSSocksServer(serverOpt)
	result, err := server.AddReverseToken(&wssocks.ReverseTokenOptions{
		Port:                 socksPort,
		Token:                token,
		Username:             socksUser,
		Password:             socksPassword,
		AllowManageConnector: connectorAutonomy,
	})
	if err == nil {
		token = result.Token
		socksPort = result.Port
	}
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
func reverseClient(t *testing.T, opt *ProxyTestClientOption) *ProxyTestClient {
	if opt == nil {
		opt = &ProxyTestClientOption{}
	}

	if opt.LoggerPrefix == "" {
		opt.LoggerPrefix = "CLT0"
	}

	var logger zerolog.Logger
	if opt.LogLevel != 0 {
		logger = createPrefixedLoggerWithLevel(opt.LoggerPrefix, opt.LogLevel)
	} else {
		logger = createPrefixedLogger(opt.LoggerPrefix)
	}
	clientOpt := wssocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://localhost:%d", opt.WSPort)).
		WithReconnectDelay(1 * time.Second).
		WithReverse(true).
		WithFastOpen(opt.FastOpen).
		WithLogger(logger)

	if opt.Reconnect {
		clientOpt.WithReconnect(true)
	}

	if opt.Threads > 0 {
		clientOpt.WithThreads(opt.Threads)
	}

	client := wssocks.NewWSSocksClient(opt.Token, clientOpt)
	require.NoError(t, client.WaitReady(context.Background(), 5*time.Second))

	return &ProxyTestClient{
		Client: client,
		Close:  client.Close,
	}
}

// forwardProxy creates a complete forward proxy test environment
func forwardProxy(t *testing.T) *ProxyTestEnv {
	return forwardProxyWithOptions(t, nil, nil)
}

// forwardProxyWithOptions creates a complete forward proxy test environment with custom options
func forwardProxyWithOptions(t *testing.T, serverOpt *ProxyTestServerOption, clientOpt *ProxyTestClientOption) *ProxyTestEnv {
	server := forwardServer(t, serverOpt)

	if clientOpt == nil {
		clientOpt = &ProxyTestClientOption{}
	}
	clientOpt.WSPort = server.WSPort
	clientOpt.Token = server.Token
	if clientOpt.LoggerPrefix == "" {
		clientOpt.LoggerPrefix = "CLT0"
	}

	client := forwardClient(t, clientOpt)

	return &ProxyTestEnv{
		Server:    server,
		Client:    client,
		WSPort:    server.WSPort,
		SocksPort: client.SocksPort,
		Close: func() {
			client.Close()
			server.Close()
		},
	}
}

// reverseProxy creates a complete reverse proxy test environment
func reverseProxy(t *testing.T) *ProxyTestEnv {
	return reverseProxyWithOptions(t, nil, nil)
}

// reverseProxyWithOptions creates a complete reverse proxy test environment with custom options
func reverseProxyWithOptions(t *testing.T, serverOpt *ProxyTestServerOption, clientOpt *ProxyTestClientOption) *ProxyTestEnv {
	server := reverseServer(t, serverOpt)

	if clientOpt == nil {
		clientOpt = &ProxyTestClientOption{}
	}
	clientOpt.WSPort = server.WSPort
	clientOpt.Token = server.Token
	if clientOpt.LoggerPrefix == "" {
		clientOpt.LoggerPrefix = "CLT0"
	}

	client := reverseClient(t, clientOpt)

	return &ProxyTestEnv{
		Server:    server,
		Client:    client,
		WSPort:    server.WSPort,
		SocksPort: server.SocksPort,
		Close: func() {
			client.Close()
			server.Close()
		},
	}
}
