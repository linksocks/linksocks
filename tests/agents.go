package tests

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"

	"github.com/stretchr/testify/require"
)

// maxPortRetries bounds how many times a test server/client rebuilds itself
// after a transient port conflict (EADDRINUSE).
const maxPortRetries = 5

// isPortConflict reports whether err is a bind port-already-in-use failure.
// getFreePort releases its probe socket before the server binds, so a
// parallel test process can grab the port in that window; rebuild with a
// fresh port instead of failing.
func isPortConflict(err error) bool {
	return err != nil && strings.Contains(err.Error(), "address already in use")
}

// ProxyTestServer encapsulates the server-side test environment
type ProxyTestServer struct {
	Server         *linksocks.LinkSocksServer
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
	ConnectorWait     time.Duration
	PortPool          *linksocks.PortPool
	LoggerPrefix      string
	LogLevel          zerolog.Level
	Reconnect         bool
	FastOpen          bool
	TransportGrace    time.Duration
}

// ProxyTestClient encapsulates the client-side test environment
type ProxyTestClient struct {
	Client    *linksocks.LinkSocksClient
	SocksPort int
	Close     func()
}

type ProxyTestClientOption struct {
	WSPort        int           // WebSocket server port
	Token         string        // Client token
	SocksPort     int           // Custom SOCKS port
	Threads       int           // Number of client threads
	LoggerPrefix  string        // Logger prefix for the client
	LogLevel      zerolog.Level // Log level for the client logger
	Reverse       bool          // Whether to use reverse mode
	FastOpen      bool          // Whether to enable fast-open mode
	Reconnect     bool          // Whether to enable auto-reconnection
	TransportGrace time.Duration // How long established channels survive a WebSocket drop
	SocksUsername string        // Local proxy username (SOCKS5 + HTTP Basic)
	SocksPassword string        // Local proxy password (SOCKS5 + HTTP Basic)

	// Direct signaling options (optional; defaults keep relay-only behavior).
	DirectMode       linksocks.DirectMode
	DirectDiscovery  linksocks.DirectDiscovery
	StunServers      []string
	DirectOnlyAction linksocks.DirectOnlyAction
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
	for attempt := 0; attempt < maxPortRetries; attempt++ {
		wsPort, err := getFreePort()
		require.NoError(t, err)

		token := ""

		var serverOpt *linksocks.ServerOption
		if opt == nil {
			logger := createPrefixedLogger("SRV0")
			serverOpt = linksocks.DefaultServerOption().
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
			serverOpt = linksocks.DefaultServerOption().WithLogger(logger)

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
			serverOpt.WithConnectorWait(opt.ConnectorWait)
			if opt.TransportGrace != 0 {
				serverOpt.WithTransportGrace(opt.TransportGrace)
			}
		}
		server := linksocks.NewLinkSocksServer(serverOpt)
		token, err = server.AddForwardToken(token)
		require.NoError(t, err)
		require.NotEmpty(t, token)

		readyErr := server.WaitReady(context.Background(), 5*time.Second)
		if readyErr == nil {
			return &ProxyTestServer{
				Server: server,
				WSPort: wsPort,
				Token:  token,
				Close:  server.Close,
			}
		}
		server.Close()
		if !isPortConflict(readyErr) {
			require.NoError(t, readyErr, "forwardServer: WaitReady failed")
		}
	}
	require.Fail(t, "forwardServer: failed to bind a free port after %d attempts", maxPortRetries)
	return nil
}

// forwardClient creates a WSS client in forward mode
func forwardClient(t *testing.T, opt *ProxyTestClientOption) *ProxyTestClient {
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

	socksPort := opt.SocksPort
	var client *linksocks.LinkSocksClient
	var readyErr error
	for attempt := 0; attempt < maxPortRetries; attempt++ {
		// Pick a fresh port per attempt so a transient EADDRINUSE (a parallel
		// test process racing getFreePort) retries on a new port instead of the same one.
		if opt.SocksPort == 0 {
			var err error
			socksPort, err = getFreePort()
			require.NoError(t, err)
		}

		clientOpt := linksocks.DefaultClientOption().
			WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", opt.WSPort)).
			WithSocksPort(socksPort).
			WithReconnectDelay(1 * time.Second).
			WithFastOpen(opt.FastOpen).
			WithLogger(logger).
			WithNoEnvProxy(true)
		if opt.TransportGrace != 0 {
			clientOpt.WithTransportGrace(opt.TransportGrace)
		}

		if opt.DirectMode != "" {
			clientOpt.WithDirectMode(opt.DirectMode)
		}
		if opt.DirectDiscovery != "" {
			clientOpt.WithDirectDiscovery(opt.DirectDiscovery)
		}
		if len(opt.StunServers) > 0 {
			clientOpt.WithStunServers(opt.StunServers)
		}
		if opt.DirectOnlyAction != "" {
			clientOpt.WithDirectOnlyAction(opt.DirectOnlyAction)
		}

		if opt.Reconnect {
			clientOpt.WithReconnect(true)
		}

		if opt.Threads > 0 {
			clientOpt.WithThreads(opt.Threads)
		}

		if opt.SocksUsername != "" {
			clientOpt.WithSocksUsername(opt.SocksUsername)
		}
		if opt.SocksPassword != "" {
			clientOpt.WithSocksPassword(opt.SocksPassword)
		}

		client = linksocks.NewLinkSocksClient(opt.Token, clientOpt)
		readyErr = client.WaitReady(context.Background(), 5*time.Second)
		if readyErr == nil {
			break
		}
		if opt.DirectMode == linksocks.DirectModeDirectOnly &&
			opt.DirectOnlyAction == linksocks.DirectOnlyActionExit &&
			strings.HasPrefix(readyErr.Error(), "direct-only:") {
			readyErr = nil
			break
		}
		client.Close()
		if !isPortConflict(readyErr) {
			break
		}
	}
	require.NoError(t, readyErr, "forwardClient: WaitReady failed")

	return &ProxyTestClient{
		Client:    client,
		SocksPort: socksPort,
		Close:     client.Close,
	}
}

// reverseServer creates a WSS server in reverse mode
func reverseServer(t *testing.T, opt *ProxyTestServerOption) *ProxyTestServer {
	token := ""
	connectorToken := ""
	socksUser := ""
	socksPassword := ""
	connectorAutonomy := false

	for attempt := 0; attempt < maxPortRetries; attempt++ {
		wsPort, err := getFreePort()
		require.NoError(t, err)

		socksPort, err := getFreePort()
		require.NoError(t, err)

		var serverOpt *linksocks.ServerOption
		if opt == nil {
			logger := createPrefixedLogger("SRV0")
			serverOpt = linksocks.DefaultServerOption().
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
			serverOpt = linksocks.DefaultServerOption().WithLogger(logger)

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
			serverOpt.WithConnectorWait(opt.ConnectorWait)
			if opt.TransportGrace != 0 {
				serverOpt.WithTransportGrace(opt.TransportGrace)
			}
		}

		server := linksocks.NewLinkSocksServer(serverOpt)
		result, err := server.AddReverseToken(&linksocks.ReverseTokenOptions{
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

		readyErr := server.WaitReady(context.Background(), 5*time.Second)
		if readyErr == nil {
			return &ProxyTestServer{
				Server:         server,
				WSPort:         wsPort,
				SocksPort:      socksPort,
				Token:          token,
				ConnectorToken: connectorToken,
				Close:          server.Close,
			}
		}
		server.Close()
		if !isPortConflict(readyErr) {
			require.NoError(t, readyErr, "reverseServer: WaitReady failed")
		}
	}
	require.Fail(t, "reverseServer: failed to bind a free port after %d attempts", maxPortRetries)
	return nil
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
	clientOpt := linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", opt.WSPort)).
		WithReconnectDelay(1 * time.Second).
		WithReverse(true).
		WithFastOpen(opt.FastOpen).
		WithLogger(logger).
		WithNoEnvProxy(true)
	if opt.TransportGrace != 0 {
		clientOpt.WithTransportGrace(opt.TransportGrace)
	}

	if opt.DirectMode != "" {
		clientOpt.WithDirectMode(opt.DirectMode)
	}
	if opt.DirectDiscovery != "" {
		clientOpt.WithDirectDiscovery(opt.DirectDiscovery)
	}
	if len(opt.StunServers) > 0 {
		clientOpt.WithStunServers(opt.StunServers)
	}
	if opt.DirectOnlyAction != "" {
		clientOpt.WithDirectOnlyAction(opt.DirectOnlyAction)
	}

	if opt.Reconnect {
		clientOpt.WithReconnect(true)
	}

	if opt.Threads > 0 {
		clientOpt.WithThreads(opt.Threads)
	}

	client := linksocks.NewLinkSocksClient(opt.Token, clientOpt)
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
