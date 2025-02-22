package wssocks

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

// WSSocksClient represents a SOCKS5 over WebSocket protocol client
type WSSocksClient struct {
	Connected    chan struct{} // Channel that is closed when connection is established
	Disconnected chan struct{} // Channel that is closed when connection is lost
	errors       chan error    // Channel for errors

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

	websocket      *WSConn
	socksListener  net.Listener
	reconnect      bool
	reconnectDelay time.Duration

	mu sync.RWMutex

	cancelFunc context.CancelFunc
}

// ClientOption represents configuration options for WSSocksClient
type ClientOption struct {
	WSURL           string
	Reverse         bool
	SocksHost       string
	SocksPort       int
	SocksUsername   string
	SocksPassword   string
	SocksWaitServer bool
	Reconnect       bool
	ReconnectDelay  time.Duration
	Logger          zerolog.Logger
	BufferSize      int
	ChannelTimeout  time.Duration
	ConnectTimeout  time.Duration
}

// DefaultClientOption returns default client options
func DefaultClientOption() *ClientOption {
	return &ClientOption{
		WSURL:           "ws://localhost:8765",
		Reverse:         false,
		SocksHost:       "127.0.0.1",
		SocksPort:       1080,
		SocksWaitServer: true,
		Reconnect:       true,
		ReconnectDelay:  5 * time.Second,
		Logger:          zerolog.New(os.Stdout).With().Timestamp().Logger(),
		BufferSize:      DefaultBufferSize,
		ChannelTimeout:  DefaultChannelTimeout,
		ConnectTimeout:  DefaultConnectTimeout,
	}
}

// WithWSURL sets the WebSocket server URL
func (o *ClientOption) WithWSURL(url string) *ClientOption {
	o.WSURL = convertWSPath(url)
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

// NewWSSocksClient creates a new WSSocksClient instance
func NewWSSocksClient(token string, opt *ClientOption) *WSSocksClient {
	if opt == nil {
		opt = DefaultClientOption()
	}

	disconnected := make(chan struct{})
	close(disconnected)

	relayOpt := NewDefaultRelayOption().
		WithBufferSize(opt.BufferSize).
		WithChannelTimeout(opt.ChannelTimeout).
		WithConnectTimeout(opt.ConnectTimeout)

	client := &WSSocksClient{
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
		Disconnected:    make(chan struct{}),
	}

	return client
}

// convertWSPath converts HTTP(S) URLs to WS(S) URLs and ensures proper path
func convertWSPath(wsURL string) string {
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

	if u.Path == "" || u.Path == "/" {
		u.Path = "/socket"
	}

	return u.String()
}

// WaitReady waits for the client to be ready with optional timeout
func (c *WSSocksClient) WaitReady(ctx context.Context, timeout time.Duration) error {
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
		return nil
	case err := <-c.errors:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Connect starts the client operation
func (c *WSSocksClient) Connect(ctx context.Context) error {
	c.log.Info().Str("url", c.wsURL).Msg("WSSocks Client is connecting to")

	if c.reverse {
		return c.startReverse(ctx)
	}
	return c.startForward(ctx)
}

// startForward connects to WebSocket server in forward proxy mode
func (c *WSSocksClient) startForward(ctx context.Context) error {
	if !c.socksWaitServer {
		go c.runSocksServer(ctx, nil)
	}

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			// Establish WebSocket connection
			ws, _, err := websocket.DefaultDialer.Dial(c.wsURL, nil)
			if err != nil {
				if c.reconnect {
					c.log.Warn().Err(err).Msg("WebSocket connection closed. Retrying in 5 seconds...")
					time.Sleep(c.reconnectDelay)
					continue
				}
				c.log.Error().Err(err).Msg("WebSocket connection closed. Exiting...")
				return err
			}

			wsConn := NewWSConn(ws)

			c.mu.Lock()
			c.websocket = wsConn
			c.mu.Unlock()

			var socksReady chan struct{}
			var socksServerTask *errgroup.Group
			if c.socksWaitServer {
				socksReady = make(chan struct{})
				socksServerTask, _ = errgroup.WithContext(ctx)
				socksServerTask.Go(func() error {
					return c.runSocksServer(ctx, socksReady)
				})
			}

			// Send authentication
			authMsg := AuthMessage{
				Reverse: false,
				Token:   c.token,
			}

			c.relay.logMessage(authMsg, "send")
			if err := wsConn.WriteMessage(authMsg); err != nil {
				wsConn.Close()
				if c.reconnect {
					c.log.Warn().Err(err).Msg("Failed to send auth message. Retrying...")
					time.Sleep(c.reconnectDelay)
					continue
				}
				return err
			}

			// Read auth response
			msg, err := wsConn.ReadMessage()
			if err != nil {
				wsConn.Close()
				if c.reconnect {
					c.log.Warn().Err(err).Msg("Failed to read auth response. Retrying...")
					time.Sleep(c.reconnectDelay)
					continue
				}
				return err
			}

			authResponse, ok := msg.(AuthResponseMessage)
			if !ok {
				wsConn.Close()
				return errors.New("unexpected message type for auth response")
			}

			c.relay.logMessage(authResponse, "recv")
			if !authResponse.Success {
				c.log.Error().Msg("Authentication failed")
				wsConn.Close()
				return errors.New("authentication failed")
			}

			c.log.Info().Msg("Authentication successful for forward proxy")

			if c.socksWaitServer {
				select {
				case <-socksReady:
				case <-ctx.Done():
					return ctx.Err()
				}
			}

			c.Disconnected = make(chan struct{})
			close(c.Connected)

			errChan := make(chan error, 2)

			// Start message dispatcher
			go func() {
				errChan <- c.messageDispatcher(ctx, wsConn)
			}()

			// Start heartbeat
			go func() {
				errChan <- c.heartbeatHandler(ctx, wsConn)
			}()

			// Wait for first error
			err = <-errChan

			c.mu.Lock()
			if c.websocket != nil {
				c.websocket.Close()
				c.websocket = nil
			}
			c.mu.Unlock()

			c.Connected = make(chan struct{})
			close(c.Disconnected)

			if err != nil && err != context.Canceled {
				if errors.Is(err, net.ErrClosed) || websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					if c.reconnect {
						c.log.Warn().Msg("WebSocket connection closed. Retrying in 5 seconds...")
						time.Sleep(c.reconnectDelay)
						continue
					}
					c.log.Error().Msg("WebSocket connection closed. Exiting...")
					return err
				}
				if c.reconnect {
					c.log.Warn().Err(err).Msg("Operation error. Retrying in 5 seconds...")
					time.Sleep(c.reconnectDelay)
					continue
				}
				c.log.Error().Err(err).Msg("Operation error. Exiting...")
				return err
			}
		}
	}
}

// startReverse connects to WebSocket server in reverse proxy mode
func (c *WSSocksClient) startReverse(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			ws, _, err := websocket.DefaultDialer.Dial(c.wsURL, nil)
			if err != nil {
				if c.reconnect {
					c.log.Warn().Err(err).Msg("WebSocket connection closed. Retrying in 5 seconds...")
					time.Sleep(c.reconnectDelay)
					continue
				}
				c.log.Error().Err(err).Msg("WebSocket connection closed. Exiting...")
				return err
			}

			wsConn := NewWSConn(ws)

			c.mu.Lock()
			c.websocket = wsConn
			c.mu.Unlock()

			// Send authentication
			authMsg := AuthMessage{
				Reverse: true,
				Token:   c.token,
			}

			c.relay.logMessage(authMsg, "send")
			if err := wsConn.WriteMessage(authMsg); err != nil {
				wsConn.Close()
				if c.reconnect {
					c.log.Warn().Err(err).Msg("Failed to send auth message. Retrying...")
					time.Sleep(c.reconnectDelay)
					continue
				}
				return err
			}

			// Read auth response
			msg, err := wsConn.ReadMessage()
			if err != nil {
				wsConn.Close()
				if c.reconnect {
					c.log.Warn().Err(err).Msg("Failed to read auth response. Retrying...")
					time.Sleep(c.reconnectDelay)
					continue
				}
				return err
			}

			c.relay.logMessage(msg, "recv")
			authResponse, ok := msg.(AuthResponseMessage)
			if !ok {
				wsConn.Close()
				return errors.New("unexpected message type for auth response")
			}

			if !authResponse.Success {
				c.log.Error().Msg("Authentication failed")
				wsConn.Close()
				return errors.New("authentication failed")
			}

			c.log.Info().Msg("Authentication successful for reverse proxy")

			errChan := make(chan error, 2)

			// Start message dispatcher
			go func() {
				errChan <- c.messageDispatcher(ctx, wsConn)
			}()

			// Start heartbeat
			go func() {
				errChan <- c.heartbeatHandler(ctx, wsConn)
			}()

			c.Disconnected = make(chan struct{})
			close(c.Connected)

			// Wait for first error
			err = <-errChan

			c.mu.Lock()
			if c.websocket != nil {
				c.websocket.Close()
				c.websocket = nil
			}
			c.mu.Unlock()

			c.Connected = make(chan struct{})
			close(c.Disconnected)

			if err != nil && err != context.Canceled {
				if errors.Is(err, net.ErrClosed) || websocket.IsCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					if c.reconnect {
						c.log.Warn().Msg("WebSocket connection closed. Retrying in 5 seconds...")
						time.Sleep(c.reconnectDelay)
						continue
					}
					c.log.Error().Msg("WebSocket connection closed. Exiting...")
					return err
				}
				if c.reconnect {
					c.log.Warn().Err(err).Msg("Operation error. Retrying in 5 seconds...")
					time.Sleep(c.reconnectDelay)
					continue
				}
				c.log.Error().Err(err).Msg("Operation error. Exiting...")
				return err
			}
		}
	}
}

// messageDispatcher handles global WebSocket message dispatching
func (c *WSSocksClient) messageDispatcher(ctx context.Context, ws *WSConn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msg, err := ws.ReadMessage()
			if err != nil {
				return err
			}

			c.relay.logMessage(msg, "recv")

			switch m := msg.(type) {
			case DataMessage:
				channelID := m.ChannelID
				if queue, ok := c.relay.messageQueues.Load(channelID); ok {
					select {
					case queue.(chan DataMessage) <- m:
						c.log.Trace().Str("channel_id", channelID.String()).Msg("Message forwarded to channel")
					default:
						c.log.Debug().Str("channel_id", channelID.String()).Msg("Message queue full")
					}
				} else {
					c.log.Debug().Str("channel_id", channelID.String()).Msg("Received data for unknown channel")
				}

			case ConnectMessage:
				go func() {
					if err := c.relay.HandleNetworkConnection(ctx, ws, m); err != nil && !errors.Is(err, context.Canceled) {
						c.log.Debug().Err(err).Msg("Network connection handler error")
					}
				}()

			case ConnectResponseMessage:
				if queue, ok := c.relay.messageQueues.Load(m.ConnectID); ok {
					queue.(chan ConnectResponseMessage) <- m
				} else {
					c.log.Debug().Str("connect_id", m.ConnectID.String()).Msg("Received connect response for unknown channel")
				}

			case DisconnectMessage:
				if cancelVal, ok := c.relay.tcpChannels.LoadAndDelete(m.ChannelID); ok {
					if cancel, ok := cancelVal.(context.CancelFunc); ok {
						cancel()
					}
				}
				if cancelVal, ok := c.relay.udpChannels.LoadAndDelete(m.ChannelID); ok {
					if cancel, ok := cancelVal.(context.CancelFunc); ok {
						cancel()
					}
				}
				c.relay.udpClientAddrs.Delete(m.ChannelID)
				c.relay.messageQueues.Delete(m.ChannelID)

			case ConnectorResponseMessage:
				if queue, ok := c.relay.messageQueues.Load(m.ConnectID); ok {
					queue.(chan ConnectorResponseMessage) <- m
				} else {
					c.log.Debug().Str("connect_id", m.ConnectID.String()).Msg("Received connector response for unknown channel")
				}

			default:
				c.log.Debug().Str("type", msg.GetType()).Msg("Received unknown message type")
			}
		}
	}
}

// heartbeatHandler maintains WebSocket connection with periodic pings
func (c *WSSocksClient) heartbeatHandler(ctx context.Context, ws *WSConn) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			err := ws.SyncWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway) {
					c.log.Trace().Msg("WebSocket connection closed, stopping heartbeat")
				} else {
					c.log.Debug().Err(err).Msg("Heartbeat error")
				}
				return err
			}
			c.log.Trace().Msg("Heartbeat: Sent ping, received pong")
		}
	}
}

// runSocksServer runs local SOCKS5 server
func (c *WSSocksClient) runSocksServer(ctx context.Context, readyEvent chan<- struct{}) error {
	c.mu.Lock()
	if c.socksListener != nil {
		c.mu.Unlock()
		if readyEvent != nil {
			close(readyEvent)
		}
		return nil
	}
	c.mu.Unlock()

	// Create TCP listener
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", c.socksHost, c.socksPort))
	if err != nil {
		return fmt.Errorf("failed to start SOCKS server: %w", err)
	}

	c.mu.Lock()
	c.socksListener = listener
	c.mu.Unlock()

	c.log.Info().Str("addr", listener.Addr().String()).Msg("SOCKS5 server started")

	if readyEvent != nil {
		close(readyEvent)
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
func (c *WSSocksClient) handleSocksRequest(ctx context.Context, socksConn net.Conn) {
	defer socksConn.Close()

	// Wait up to 10 seconds for WebSocket connection
	startTime := time.Now()
	for time.Since(startTime) < 10*time.Second {
		c.mu.RLock()
		ws := c.websocket
		c.mu.RUnlock()

		if ws != nil {
			if err := c.relay.HandleSocksRequest(ctx, ws, socksConn, c.socksUsername, c.socksPassword); err != nil && !errors.Is(err, context.Canceled) {
				c.log.Warn().Err(err).Msg("Error handling SOCKS request")
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

// Close gracefully shuts down the WSSocksClient
func (c *WSSocksClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Close relay
	c.relay.Close()

	// Close SOCKS listener if it exists
	if c.socksListener != nil {
		if err := c.socksListener.Close(); err != nil {
			c.log.Warn().Err(err).Msg("Error closing SOCKS listener")
		}
		c.socksListener = nil
	}

	// Close WebSocket connection if it exists
	if c.websocket != nil {
		if err := c.websocket.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			c.log.Warn().Err(err).Msg("Error closing WebSocket connection")
		}
		c.websocket = nil
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
func (c *WSSocksClient) AddConnector(connectorToken string) (string, error) {
	if !c.reverse {
		return "", errors.New("add connector is only available in reverse proxy mode")
	}

	c.mu.RLock()
	ws := c.websocket
	c.mu.RUnlock()

	if ws == nil {
		return "", errors.New("client not connected")
	}

	connectID := uuid.New()
	msg := ConnectorMessage{
		Operation:      "add",
		ConnectID:      connectID,
		ConnectorToken: connectorToken,
	}

	// Create response channel
	respChan := make(chan ConnectorResponseMessage, 1)
	c.relay.messageQueues.Store(connectID, respChan)
	defer c.relay.messageQueues.Delete(connectID)

	// Send request
	c.relay.logMessage(msg, "send")
	if err := ws.WriteMessage(msg); err != nil {
		return "", fmt.Errorf("failed to send connector request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-respChan:
		if !resp.Success {
			return "", fmt.Errorf("connector request failed: %s", resp.Error)
		}
		return resp.ConnectorToken, nil
	case <-time.After(10 * time.Second):
		return "", errors.New("timeout waiting for connector response")
	}
}

// RemoveConnector sends a request to remove a connector token and waits for response.
// This function is only available in reverse proxy mode.
func (c *WSSocksClient) RemoveConnector(connectorToken string) error {
	if !c.reverse {
		return errors.New("remove connector is only available in reverse proxy mode")
	}

	c.mu.RLock()
	ws := c.websocket
	c.mu.RUnlock()

	if ws == nil {
		return errors.New("client not connected")
	}

	connectID := uuid.New()
	msg := ConnectorMessage{
		Operation:      "remove",
		ConnectID:      connectID,
		ConnectorToken: connectorToken,
	}

	// Create response channel
	respChan := make(chan ConnectorResponseMessage, 1)
	c.relay.messageQueues.Store(connectID, respChan)
	defer c.relay.messageQueues.Delete(connectID)

	// Send request
	c.relay.logMessage(msg, "send")
	if err := ws.WriteMessage(msg); err != nil {
		return fmt.Errorf("failed to send connector request: %w", err)
	}

	// Wait for response with timeout
	select {
	case resp := <-respChan:
		if !resp.Success {
			return fmt.Errorf("connector request failed: %s", resp.Error)
		}
		return nil
	case <-time.After(10 * time.Second):
		return errors.New("timeout waiting for connector response")
	}
}
