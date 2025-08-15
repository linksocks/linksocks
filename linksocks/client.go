package linksocks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// nonRetriableError represents an error that should not be retried
type nonRetriableError struct {
	msg string
}

func (e *nonRetriableError) Error() string {
	return e.msg
}

// LinkSocksClient represents a SOCKS5 over WebSocket protocol client
type LinkSocksClient struct {
	Connected    chan struct{} // Channel that is closed when connection is established
	Disconnected chan struct{} // Channel that is closed when connection is lost
	IsConnected  bool          // Boolean flag indicating current connection status
	errors       chan error    // Channel for errors
	closed       bool          // Flag to track if client has been closed

	instanceID      uuid.UUID
	relay           *Relay
	log             zerolog.Logger
	token           string
	wsURL           string
	reverse         bool
	socksHost       string
	socksPort       int
	socksUsername   string
	socksPassword   string
	socksWaitServer bool
	socksReady      chan struct{}
	noEnvProxy      bool
	numPartners     int

	websockets     []*WSConn // Multiple WebSocket connections
	currentIndex   int       // Current WebSocket index for round-robin
	socksListener  net.Listener
	reconnect      bool
	reconnectDelay time.Duration
	threads        int // Number of concurrent WebSocket connections

	mu           sync.RWMutex // General mutex
	connectionMu sync.Mutex   // Dedicated mutex for connection status

	cancelFunc context.CancelFunc

	batchLogger *batchLogger
}

// ClientOption represents configuration options for LinkSocksClient
type ClientOption struct {
	WSURL            string
	Reverse          bool
	SocksHost        string
	SocksPort        int
	SocksUsername    string
	SocksPassword    string
	SocksWaitServer  bool
	Reconnect        bool
	ReconnectDelay   time.Duration
	Logger           zerolog.Logger
	BufferSize       int
	ChannelTimeout   time.Duration
	ConnectTimeout   time.Duration
	Threads          int
	FastOpen         bool
	UpstreamProxy    string
	UpstreamUsername string
	UpstreamPassword string
	NoEnvProxy       bool // Ignore environment proxy settings
}

// DefaultClientOption returns default client options
func DefaultClientOption() *ClientOption {
	return &ClientOption{
		WSURL:            "ws://localhost:8765",
		Reverse:          false,
		SocksHost:        "127.0.0.1",
		SocksPort:        9870,
		SocksWaitServer:  true,
		Reconnect:        false,
		ReconnectDelay:   5 * time.Second,
		Logger:           zerolog.New(os.Stdout).With().Timestamp().Logger(),
		BufferSize:       DefaultBufferSize,
		ChannelTimeout:   DefaultChannelTimeout,
		ConnectTimeout:   DefaultConnectTimeout,
		Threads:          1,
		FastOpen:         false,
		UpstreamProxy:    "",
		UpstreamUsername: "",
		UpstreamPassword: "",
		NoEnvProxy:       false,
	}
}

// WithWSURL sets the WebSocket server URL
func (o *ClientOption) WithWSURL(url string) *ClientOption {
	o.WSURL = url
	return o
}

// WithReverse sets the reverse proxy mode
func (o *ClientOption) WithReverse(reverse bool) *ClientOption {
	o.Reverse = reverse
	return o
}

// WithSocksHost sets the SOCKS5 server host
func (o *ClientOption) WithSocksHost(host string) *ClientOption {
	o.SocksHost = host
	return o
}

// WithSocksPort sets the SOCKS5 server port
func (o *ClientOption) WithSocksPort(port int) *ClientOption {
	o.SocksPort = port
	return o
}

// WithSocksUsername sets the SOCKS5 authentication username
func (o *ClientOption) WithSocksUsername(username string) *ClientOption {
	o.SocksUsername = username
	return o
}

// WithSocksPassword sets the SOCKS5 authentication password
func (o *ClientOption) WithSocksPassword(password string) *ClientOption {
	o.SocksPassword = password
	return o
}

// WithSocksWaitServer sets whether to wait for server connection before starting SOCKS server
func (o *ClientOption) WithSocksWaitServer(wait bool) *ClientOption {
	o.SocksWaitServer = wait
	return o
}

// WithReconnect sets the reconnect behavior
func (o *ClientOption) WithReconnect(reconnect bool) *ClientOption {
	o.Reconnect = reconnect
	return o
}

// WithReconnectDelay sets the reconnect delay duration
func (o *ClientOption) WithReconnectDelay(delay time.Duration) *ClientOption {
	o.ReconnectDelay = delay
	return o
}

// WithLogger sets the logger instance
func (o *ClientOption) WithLogger(logger zerolog.Logger) *ClientOption {
	o.Logger = logger
	return o
}

// WithBufferSize sets the buffer size for data transfer
func (o *ClientOption) WithBufferSize(size int) *ClientOption {
	o.BufferSize = size
	return o
}

// WithChannelTimeout sets the channel timeout duration
func (o *ClientOption) WithChannelTimeout(timeout time.Duration) *ClientOption {
	o.ChannelTimeout = timeout
	return o
}

// WithConnectTimeout sets the connect timeout duration
func (o *ClientOption) WithConnectTimeout(timeout time.Duration) *ClientOption {
	o.ConnectTimeout = timeout
	return o
}

// WithThreads sets the number of concurrent WebSocket connections
func (o *ClientOption) WithThreads(threads int) *ClientOption {
	o.Threads = threads
	return o
}

// WithFastOpen controls whether to wait for connect success response
func (o *ClientOption) WithFastOpen(fastOpen bool) *ClientOption {
	o.FastOpen = fastOpen
	return o
}

// WithUpstreamProxy sets the upstream SOCKS5 proxy
func (o *ClientOption) WithUpstreamProxy(proxy string) *ClientOption {
	o.UpstreamProxy = proxy
	return o
}

// WithUpstreamAuth sets the upstream SOCKS5 proxy authentication
func (o *ClientOption) WithUpstreamAuth(username, password string) *ClientOption {
	o.UpstreamUsername = username
	o.UpstreamPassword = password
	return o
}

// WithNoEnvProxy sets whether to ignore environment proxy settings
func (o *ClientOption) WithNoEnvProxy(noEnvProxy bool) *ClientOption {
	o.NoEnvProxy = noEnvProxy
	return o
}

// NewLinkSocksClient creates a new LinkSocksClient instance
func NewLinkSocksClient(token string, opt *ClientOption) *LinkSocksClient {
	if opt == nil {
		opt = DefaultClientOption()
	}

	// Validate and adjust threads parameter before using it
	numCPU := runtime.NumCPU()
	maxThreads := numCPU * 100

	if opt.Threads < 0 {
		// If negative, use all CPU cores
		opt.Threads = numCPU
		opt.Logger.Info().Int("threads", opt.Threads).Int("cpu_cores", numCPU).Msg("Negative threads value detected, using all CPU cores")
	} else if opt.Threads == 0 {
		// If zero, use default value of 1
		opt.Threads = 1
		opt.Logger.Info().Int("threads", opt.Threads).Msg("Zero threads value detected, using default value")
	} else if opt.Threads > maxThreads {
		// If greater than 100x CPU cores, limit to 100x CPU cores
		opt.Threads = maxThreads
		opt.Logger.Info().Int("threads", opt.Threads).Int("cpu_cores", numCPU).Msg("Large threads value detected, limiting to 100x CPU cores")
	}

	// Convert URL with token
	opt.WSURL = convertWSPath(opt.WSURL)

	disconnected := make(chan struct{})
	close(disconnected)

	relayOpt := NewDefaultRelayOption().
		WithBufferSize(opt.BufferSize).
		WithChannelTimeout(opt.ChannelTimeout).
		WithConnectTimeout(opt.ConnectTimeout).
		WithFastOpen(opt.FastOpen).
		WithUpstreamProxy(opt.UpstreamProxy).
		WithUpstreamAuth(opt.UpstreamUsername, opt.UpstreamPassword)

	client := &LinkSocksClient{
		instanceID:      uuid.New(),
		relay:           NewRelay(opt.Logger, relayOpt),
		log:             opt.Logger,
		token:           token,
		wsURL:           opt.WSURL,
		reverse:         opt.Reverse,
		socksHost:       opt.SocksHost,
		socksPort:       opt.SocksPort,
		socksUsername:   opt.SocksUsername,
		socksPassword:   opt.SocksPassword,
		socksWaitServer: opt.SocksWaitServer,
		reconnect:       opt.Reconnect,
		reconnectDelay:  opt.ReconnectDelay,
		errors:          make(chan error, 1),
		Connected:       make(chan struct{}),
		Disconnected:    disconnected,
		IsConnected:     false,
		numPartners:     0,
		threads:         opt.Threads,
		websockets:      make([]*WSConn, 0, opt.Threads),
		noEnvProxy:      opt.NoEnvProxy,
	}

	return client
}

// convertWSPath converts HTTP(S) URLs to WS(S) URLs and ensures proper path
func convertWSPath(wsURL string) string {
	if !strings.Contains(wsURL, "://") {
		wsURL = "wss://" + wsURL
	}

	u, err := url.Parse(wsURL)
	if err != nil {
		return wsURL
	}

	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}

	// Set path to /socket only if it's root path
	if u.Path == "" || u.Path == "/" {
		u.Path = "/socket"
	}

	return u.String()
}

// WaitReady start the client and waits for the client to be ready with optional timeout
func (c *LinkSocksClient) WaitReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)

	c.mu.Lock()
	c.cancelFunc = cancel
	c.mu.Unlock()

	go func() {
		if err := c.Connect(ctx); err != nil {
			c.errors <- err
		}
	}()

	if timeout > 0 {
		select {
		case <-c.Connected:
			if !c.reverse {
				// For forward proxy, also wait for SOCKS server
				select {
				case <-c.socksReady:
					return nil
				case err := <-c.errors:
					return err
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(timeout):
					return fmt.Errorf("timeout waiting for SOCKS server to be ready")
				}
			}
			return nil
		case err := <-c.errors:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timeout):
			return fmt.Errorf("timeout waiting for client to be ready")
		}
	}

	select {
	case <-c.Connected:
		if !c.reverse {
			// For forward proxy, also wait for SOCKS server
			select {
			case <-c.socksReady:
				return nil
			case err := <-c.errors:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	case err := <-c.errors:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Connect starts the client operation
func (c *LinkSocksClient) Connect(ctx context.Context) error {
	// threads parameter is already validated in NewLinkSocksClient
	c.log.Info().Str("url", c.wsURL).Msg("LinkSocks Client is connecting to")

	if c.reverse {
		return c.startReverse(ctx)
	}
	return c.startForward(ctx)
}

// getNextWebSocket returns the next available WebSocket connection in round-robin fashion
func (c *LinkSocksClient) getNextWebSocket() *WSConn {
	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.websockets) == 0 {
		return nil
	}

	// Find next available WebSocket
	for i := 0; i < len(c.websockets); i++ {
		c.currentIndex = (c.currentIndex + 1) % len(c.websockets)
		if c.websockets[c.currentIndex] != nil {
			return c.websockets[c.currentIndex]
		}
	}

	return nil
}

// startForward connects to WebSocket server in forward proxy mode
func (c *LinkSocksClient) startForward(ctx context.Context) error {
	// Initialize socksReady channel
	c.mu.Lock()
	c.socksReady = make(chan struct{})
	c.mu.Unlock()

	if !c.socksWaitServer {
		// Start SOCKS server immediately without waiting
		go func() {
			if err := c.runSocksServer(ctx); err != nil {
				c.errors <- fmt.Errorf("socks server error: %w", err)
			}
		}()
	}

	var wg sync.WaitGroup
	errChan := make(chan error, c.threads)

	// Start multiple WebSocket connections
	for i := 0; i < c.threads; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					if err := c.maintainWebSocketConnection(ctx, index); err != nil {
						var nrErr *nonRetriableError
						if !c.reconnect || errors.As(err, &nrErr) {
							errChan <- err
							return
						}
						c.batchLogger.log("retry_error", c.threads, func(count, total int) {
							c.log.Warn().Err(err).Msgf("Connection error, retrying... (%d/%d)", count, total)
						})
						time.Sleep(c.reconnectDelay)
					}
				}
			}
		}(i)
	}

	// If socksWaitServer is true, start SOCKS server after at least one WebSocket connection is established
	if c.socksWaitServer {
		select {
		case <-c.Connected:
			go func() {
				if err := c.runSocksServer(ctx); err != nil {
					c.errors <- fmt.Errorf("socks server error: %w", err)
				}
			}()
		case err := <-errChan:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	// Wait for all goroutines to finish or first error
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// Monitor both WebSocket errors and SOCKS server errors
	select {
	case err := <-errChan:
		return err
	case err := <-c.errors:
		return err
	}
}

// maintainWebSocketConnection maintains a single WebSocket connection
func (c *LinkSocksClient) maintainWebSocketConnection(ctx context.Context, index int) error {
	// Create batch logger if not exists
	c.mu.Lock()
	if c.batchLogger == nil {
		c.batchLogger = newBatchLogger(c.log)
	}
	c.mu.Unlock()

	if !c.noEnvProxy {
		if httpProxy, httpsProxy := os.Getenv("http_proxy"), os.Getenv("https_proxy"); httpProxy != "" || httpsProxy != "" {
			c.batchLogger.log("proxy_env", c.threads, func(count, total int) {
				c.log.Info().
					Str("http_proxy", httpProxy).
					Str("https_proxy", httpsProxy).
					Msgf("Using proxy from environment to connect to the server (%d/%d)", count, total)
			})
		}
	}

	dialer := websocket.DefaultDialer
	if c.noEnvProxy {
		dialer = &websocket.Dialer{
			Proxy:            nil, // Explicitly disable proxy
			HandshakeTimeout: websocket.DefaultDialer.HandshakeTimeout,
			ReadBufferSize:   websocket.DefaultDialer.ReadBufferSize,
			WriteBufferSize:  websocket.DefaultDialer.WriteBufferSize,
		}
	}

	// Parse URL to check path
	wsURLWithParams := c.wsURL
	u, err := url.Parse(wsURLWithParams)
	if err == nil && u.Path == "/socket" {
		// Only add query parameters for /socket path
		// Calculate SHA256 hash of the token
		hash := sha256.Sum256([]byte(c.token))
		// Convert to hex string
		hashStr := hex.EncodeToString(hash[:])

		q := u.Query()
		q.Set("token", hashStr)
		q.Set("reverse", strconv.FormatBool(c.reverse))
		q.Set("instance", c.instanceID.String())
		u.RawQuery = q.Encode()
		wsURLWithParams = u.String()
	}

	// Handle WebSocket connection with redirect support
	var ws *websocket.Conn
	var resp *http.Response
	currentURL := wsURLWithParams
	redirectsLeft := 5 // Maximum number of redirects to follow

	for redirectsLeft >= 0 {
		ws, resp, err = dialer.Dial(currentURL, nil)
		if err == nil {
			// Successfully connected
			break
		}

		// Check if this is a redirect
		if err == websocket.ErrBadHandshake && resp != nil && (resp.StatusCode >= 301 && resp.StatusCode <= 308) {
			redirect := resp.Header.Get("Location")
			if redirect == "" {
				// No redirect URL provided
				c.log.Warn().Int("status", resp.StatusCode).Msg("Received redirect status but no Location header")
				return errors.New("redirect response missing Location header")
			}

			// Track number of redirects
			redirectsLeft--
			if redirectsLeft < 0 {
				c.log.Warn().Msg("Too many redirects, giving up after 5 redirects")
				return errors.New("too many redirects (maximum 5)")
			}

			// Log the redirect
			c.log.Debug().
				Str("from", currentURL).
				Str("to", redirect).
				Int("status", resp.StatusCode).
				Int("redirects_left", redirectsLeft).
				Msg("Following WebSocket redirect")

			// Parse the redirect URL
			redirectURL, parseErr := url.Parse(redirect)
			if parseErr != nil {
				c.log.Warn().Err(parseErr).Msg("Failed to parse redirect URL")
				return parseErr
			}

			// Handle relative URLs
			if !redirectURL.IsAbs() {
				originalURL, _ := url.Parse(currentURL)
				redirectURL = originalURL.ResolveReference(redirectURL)
			}

			// Ensure proper scheme (ws/wss)
			if redirectURL.Scheme == "http" {
				redirectURL.Scheme = "ws"
			} else if redirectURL.Scheme == "https" {
				redirectURL.Scheme = "wss"
			}

			// Update URL for next attempt
			currentURL = redirectURL.String()
			continue
		}

		// Not a redirect or another error occurred
		if err == websocket.ErrBadHandshake {
			statusMsg := "unknown"
			if resp != nil {
				statusMsg = resp.Status
			}
			c.batchLogger.log("handshake_error", c.threads, func(count, total int) {
				c.log.Warn().Err(err).Str("status", statusMsg).Msgf("WebSocket handshake failed (%d/%d)", count, total)
			})
		} else {
			c.batchLogger.log("dial_error", c.threads, func(count, total int) {
				c.log.Warn().Err(err).Msgf("Failed to dial WebSocket (%d/%d)", count, total)
			})
		}
		return err
	}

	wsConn := NewWSConn(ws, strconv.Itoa(index), c.log)

	c.mu.Lock()
	if len(c.websockets) <= index {
		c.websockets = append(c.websockets, wsConn)
	} else {
		c.websockets[index] = wsConn
	}
	c.mu.Unlock()

	// Send authentication if not using /socket path
	if u.Path != "/socket" {
		authMsg := AuthMessage{
			Reverse:  c.reverse,
			Token:    c.token,
			Instance: c.instanceID,
		}

		c.relay.logMessage(authMsg, "send", wsConn.Label())
		if err := wsConn.WriteMessage(authMsg); err != nil {
			wsConn.Close()
			c.batchLogger.log("auth_send_error", c.threads, func(count, total int) {
				c.log.Warn().Err(err).Msgf("Failed to send auth message (%d/%d)", count, total)
			})
			return err
		}
	}

	// Read auth response
	msg, err := wsConn.ReadMessage()
	if err != nil {
		wsConn.Close()
		c.batchLogger.log("auth_read_error", c.threads, func(count, total int) {
			c.log.Warn().Err(err).Msgf("Failed to read auth response (%d/%d)", count, total)
		})
		return err
	}

	c.relay.logMessage(msg, "recv", wsConn.Label())
	authResponse, ok := msg.(AuthResponseMessage)
	if !ok {
		wsConn.Close()
		return errors.New("unexpected message type for auth response")
	}

	if !authResponse.Success {
		c.batchLogger.log("auth_failed", c.threads, func(count, total int) {
			c.log.Error().Msgf("Authentication failed (%d/%d)", count, total)
		})
		wsConn.Close()
		// Return a non-retriable error to prevent reconnection attempts
		return &nonRetriableError{msg: "authentication failed"}
	}

	c.batchLogger.log("auth_success", c.threads, func(count, total int) {
		mode := "forward"
		if c.reverse {
			mode = "reverse"
		}
		c.log.Info().Msgf("Authentication successful for %s proxy (%d/%d)", mode, count, total)
	})

	if index == 0 {
		c.setConnectionStatus(true)
	}

	if err := wsConn.SyncWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
		if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway) {
			c.log.Trace().Msg("WebSocket connection closed, stopping heartbeat")
		} else {
			c.log.Debug().Err(err).Msg("Heartbeat error")
		}
		return err
	}

	errChan := make(chan error, 2)

	// Start message dispatcher and heartbeat
	go func() {
		errChan <- c.messageDispatcher(ctx, wsConn)
	}()
	go func() {
		errChan <- c.heartbeatHandler(ctx, wsConn)
	}()

	// Wait for first error
	err = <-errChan

	c.mu.Lock()
	if index < len(c.websockets) {
		c.websockets[index] = nil
	}

	// Check if all connections are down
	allDown := true
	for _, ws := range c.websockets {
		if ws != nil {
			allDown = false
			break
		}
	}
	if allDown {
		c.setConnectionStatus(false)
	}
	c.mu.Unlock()

	return err
}

// startReverse connects to WebSocket server in reverse proxy mode
func (c *LinkSocksClient) startReverse(ctx context.Context) error {
	var wg sync.WaitGroup
	errChan := make(chan error, c.threads)

	// Start multiple WebSocket connections
	for i := 0; i < c.threads; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				default:
					if err := c.maintainWebSocketConnection(ctx, index); err != nil {
						var nrErr *nonRetriableError
						if !c.reconnect || errors.As(err, &nrErr) {
							errChan <- err
							return
						}
						c.batchLogger.log("retry_error", c.threads, func(count, total int) {
							c.log.Warn().Err(err).Msgf("Connection error, retrying... (%d/%d)", count, total)
						})
						time.Sleep(c.reconnectDelay)
					}
				}
			}
		}(i)
	}

	// Wait for all goroutines to finish or first error
	go func() {
		wg.Wait()
		close(errChan)
	}()

	// Return first error
	if err := <-errChan; err != nil {
		return err
	}
	return nil
}

// messageDispatcher handles global WebSocket message dispatching
func (c *LinkSocksClient) messageDispatcher(ctx context.Context, ws *WSConn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msg, err := ws.ReadMessage()
			if err != nil {
				return err
			}

			c.relay.logMessage(msg, "recv", ws.Label())

			switch m := msg.(type) {
			case DataMessage:
				if queue, ok := c.relay.messageQueues.Load(m.ChannelID); ok {
					select {
					case queue.(chan BaseMessage) <- m:
						c.log.Trace().Str("channel_id", m.ChannelID.String()).Msg("Message forwarded to channel")
					default:
						// Drop message if queue is full instead of blocking
						c.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Message queue full, dropping message")
					}
				} else {
					c.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Received data for unknown channel, dropping message")
				}

			case ConnectMessage:
				if c.reverse {
					msgChan := make(chan BaseMessage, 1000)
					c.relay.messageQueues.Store(m.ChannelID, msgChan)
					go func() {
						if err := c.relay.HandleNetworkConnection(ctx, ws, m); err != nil && !errors.Is(err, context.Canceled) {
							c.log.Debug().Err(err).Msg("Error handling network connection")
						}
					}()
				}

			case ConnectResponseMessage:
				if c.relay.option.FastOpen {
					if m.Success {
						c.relay.SetConnectionSuccess(m.ChannelID)
					} else {
						c.relay.disconnectChannel(m.ChannelID)
					}
				} else if queue, ok := c.relay.messageQueues.Load(m.ChannelID); ok {
					// Non-blocking send for connect response
					select {
					case queue.(chan BaseMessage) <- m:
					case <-time.After(2 * time.Second):
						c.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Timeout delivering connect response")
					}
				}

			case DisconnectMessage:
				if m.Error != "" {
					c.log.Debug().Str("channel_id", m.ChannelID.String()).Str("reason", m.Error).Msg("Disconnected by remote")
				} else {
					c.log.Debug().Str("channel_id", m.ChannelID.String()).Msg("Disconnected")
				}
				c.relay.disconnectChannel(m.ChannelID)

			case ConnectorResponseMessage:
				if queue, ok := c.relay.messageQueues.Load(m.ChannelID); ok {
					// Non-blocking send for connector response
					select {
					case queue.(chan BaseMessage) <- m:
					default:
						c.log.Debug().Str("channel_id", m.ChannelID.String()).Msg("Connector response queue full")
					}
				}

			case LogMessage:
				// Handle log message by outputting to server logs
				switch m.Level {
				case LogLevelTrace:
					c.log.Trace().Str("client", ws.Label()).Msg(m.Msg)
				case LogLevelDebug:
					c.log.Debug().Str("client", ws.Label()).Msg(m.Msg)
				case LogLevelInfo:
					c.log.Info().Str("client", ws.Label()).Msg(m.Msg)
				case LogLevelWarn:
					c.log.Warn().Str("client", ws.Label()).Msg(m.Msg)
				case LogLevelError:
					c.log.Error().Str("client", ws.Label()).Msg(m.Msg)
				default:
					c.log.Debug().Str("client", ws.Label()).Str("level", m.Level).Msg(m.Msg)
				}

			case PartnersMessage:
				c.mu.Lock()
				c.numPartners = m.Count
				c.mu.Unlock()
				c.log.Debug().Int("partners", m.Count).Msg("Updated partners count")

			default:
				c.log.Warn().Str("type", msg.GetType()).Msg("Received unknown message type, dropping message")
			}
		}
	}
}

// heartbeatHandler maintains WebSocket connection with periodic pings
func (c *LinkSocksClient) heartbeatHandler(ctx context.Context, ws *WSConn) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			// Just send the ping - RTT will be measured when pong is received
			if err := ws.SyncWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second)); err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway) {
					c.log.Trace().Msg("WebSocket connection closed, stopping heartbeat")
				} else {
					c.log.Debug().Err(err).Msg("Heartbeat error")
				}
				return err
			}
		}
	}
}

// runSocksServer runs local SOCKS5 server
func (c *LinkSocksClient) runSocksServer(ctx context.Context) error {
	c.mu.Lock()
	if c.socksListener != nil {
		c.mu.Unlock()
		return nil
	}
	c.mu.Unlock()

	// Create TCP listener
	listener, err := net.Listen("tcp", net.JoinHostPort(c.socksHost, fmt.Sprintf("%d", c.socksPort)))
	if err != nil {
		return fmt.Errorf("failed to start SOCKS server: %w", err)
	}

	c.mu.Lock()
	c.socksListener = listener
	c.mu.Unlock()

	c.log.Info().Str("addr", listener.Addr().String()).Msg("SOCKS5 server started")

	// Signal that SOCKS server is ready
	select {
	case <-c.socksReady:
	default:
		close(c.socksReady)
	}

	// Accept connections
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			conn, err := listener.Accept()
			if err != nil {
				if errors.Is(err, net.ErrClosed) {
					return nil
				}
				c.log.Warn().Err(err).Msg("Error accepting SOCKS connection")
				continue
			}

			c.log.Debug().Str("remote_addr", conn.RemoteAddr().String()).Msg("Accepted SOCKS5 connection")
			go c.handleSocksRequest(ctx, conn)
		}
	}
}

// handleSocksRequest handles SOCKS5 client request
func (c *LinkSocksClient) handleSocksRequest(ctx context.Context, socksConn net.Conn) {
	defer socksConn.Close()

	// Wait up to 10 seconds for WebSocket connection
	startTime := time.Now()
	for time.Since(startTime) < 10*time.Second {
		ws := c.getNextWebSocket()
		if ws != nil {
			if err := c.relay.HandleSocksRequest(ctx, ws, socksConn, c.socksUsername, c.socksPassword); err != nil && !errors.Is(err, context.Canceled) {
				if errors.Is(err, io.EOF) {
					c.log.Debug().Err(err).Msg("Error handling SOCKS request")
				} else {
					c.log.Warn().Err(err).Msg("Error handling SOCKS request")
				}
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}

	c.log.Warn().Msg("No valid websockets connection after waiting 10s, refusing socks request")
	if err := c.relay.RefuseSocksRequest(socksConn, 0x03); err != nil {
		c.log.Warn().Err(err).Msg("Error refusing SOCKS request")
	}
}

// Close gracefully shuts down the LinkSocksClient
func (c *LinkSocksClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if already closed
	if c.closed {
		return
	}
	c.closed = true

	// Close relay if it exists
	if c.relay != nil {
		c.relay.Close()
	}

	// Close SOCKS listener if it exists
	if c.socksListener != nil {
		if err := c.socksListener.Close(); err != nil {
			c.log.Warn().Err(err).Msg("Error closing SOCKS listener")
		}
		c.socksListener = nil
	}

	// Close WebSocket connections
	if c.websockets != nil {
		for _, ws := range c.websockets {
			if ws != nil {
				if err := ws.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
					c.log.Warn().Err(err).Msg("Error closing WebSocket connection")
				}
			}
		}
		c.websockets = nil
	}

	// Cancel main worker if it exists
	if c.cancelFunc != nil {
		c.cancelFunc()
		c.cancelFunc = nil
	}

	c.log.Info().Msg("Client stopped")
}

// AddConnector sends a request to add a new connector token and waits for response.
// This function is only available in reverse proxy mode.
func (c *LinkSocksClient) AddConnector(connectorToken string) (string, error) {
	if !c.reverse {
		return "", errors.New("add connector is only available in reverse proxy mode")
	}

	ws := c.getNextWebSocket()
	if ws == nil {
		return "", errors.New("client not connected")
	}

	channelID := uuid.New()
	msg := ConnectorMessage{
		Operation:      "add",
		ChannelID:      channelID,
		ConnectorToken: connectorToken,
	}

	// Create response channel
	respChan := make(chan BaseMessage, 1)
	c.relay.messageQueues.Store(channelID, respChan)
	defer c.relay.messageQueues.Delete(channelID)

	// Send request
	c.relay.logMessage(msg, "send", ws.Label())
	if err := ws.WriteMessage(msg); err != nil {
		return "", fmt.Errorf("failed to send connector request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-respChan:
		connResp, ok := resp.(ConnectorResponseMessage)
		if !ok {
			return "", errors.New("unexpected message type for connector response")
		}
		if !connResp.Success {
			return "", fmt.Errorf("connector request failed: %s", connResp.Error)
		}
		return connResp.ConnectorToken, nil
	case <-time.After(10 * time.Second):
		return "", errors.New("timeout waiting for connector response")
	}
}

// RemoveConnector sends a request to remove a connector token and waits for response.
// This function is only available in reverse proxy mode.
func (c *LinkSocksClient) RemoveConnector(connectorToken string) error {
	if !c.reverse {
		return errors.New("remove connector is only available in reverse proxy mode")
	}

	ws := c.getNextWebSocket()
	if ws == nil {
		return errors.New("client not connected")
	}

	channelID := uuid.New()
	msg := ConnectorMessage{
		Operation:      "remove",
		ChannelID:      channelID,
		ConnectorToken: connectorToken,
	}

	// Create response channel
	respChan := make(chan BaseMessage, 1)
	c.relay.messageQueues.Store(channelID, respChan)
	defer c.relay.messageQueues.Delete(channelID)

	// Send request
	c.relay.logMessage(msg, "send", ws.Label())
	if err := ws.WriteMessage(msg); err != nil {
		return fmt.Errorf("failed to send connector request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-respChan:
		connResp, ok := resp.(ConnectorResponseMessage)
		if !ok {
			return errors.New("unexpected message type for connector response")
		}
		if !connResp.Success {
			return fmt.Errorf("connector request failed: %s", connResp.Error)
		}
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("timeout waiting for connector response")
	}
}

// setConnectionStatus safely handle channel operations
func (c *LinkSocksClient) setConnectionStatus(connected bool) {
	c.connectionMu.Lock()
	defer c.connectionMu.Unlock()

	// Only update if status actually changed
	if c.IsConnected == connected {
		return
	}

	c.IsConnected = connected

	if connected {
		// Reset Connected channel if it was previously closed
		select {
		case <-c.Connected:
			c.Connected = make(chan struct{})
		default:
		}
		close(c.Connected)
		// Reset Disconnected channel
		c.Disconnected = make(chan struct{})
	} else {
		// Reset Connected channel
		c.Connected = make(chan struct{})
		// Reset Disconnected channel if it was previously closed
		select {
		case <-c.Disconnected:
			c.Disconnected = make(chan struct{})
		default:
		}
		close(c.Disconnected)
	}
}

// GetPartnersCount returns the current number of partners
func (c *LinkSocksClient) GetPartnersCount() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.numPartners
}
