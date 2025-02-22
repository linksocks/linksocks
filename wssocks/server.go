package wssocks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// WSSocksServer represents a SOCKS5 over WebSocket protocol server
type WSSocksServer struct {
	// Core components
	relay *Relay
	log   zerolog.Logger

	// Synchronization primitives
	mu         sync.RWMutex
	ready      chan struct{}
	cancelFunc context.CancelFunc

	// WebSocket server configuration
	wsHost   string
	wsPort   int
	wsServer *http.Server

	// SOCKS server configuration
	socksHost       string
	portPool        *PortPool
	socksWaitClient bool

	// Client connections
	clients map[uuid.UUID]*WSConn // Maps client ID to WebSocket connection

	// Token management
	forwardTokens   map[string]struct{}             // Set of valid forward proxy tokens
	tokens          map[string]int                  // Maps reverse proxy tokens to ports
	tokenClients    map[string][]clientInfo         // Maps tokens to their connected clients
	tokenIndexes    map[string]int                  // Round-robin indexes for load balancing
	tokenOptions    map[string]*ReverseTokenOptions // options per token
	connectorTokens map[string]string               // Maps connector tokens to their reverse tokens
	internalTokens  map[string][]string             // Maps original token to list of internal tokens

	// Connector management
	connCache *connectorCache

	// Active SOCKS servers
	socksTasks map[int]context.CancelFunc // Active SOCKS server tasks

	// Socket reuse management
	waitingSockets map[int]*waitingSocket // Sockets waiting for reuse
	waitingMu      sync.RWMutex           // Mutex for waiting sockets management
	socketManager  *SocketManager

	// API server
	apiKey string

	// Error channel
	errors chan error // Channel for errors
}

type clientInfo struct {
	ID   uuid.UUID
	Conn *WSConn
}

type waitingSocket struct {
	listener    net.Listener
	cancelTimer *time.Timer
}

type connectorCache struct {
	connectIDToConnector map[uuid.UUID]*WSConn  // Maps connect_id to reverse client WebSocket connection
	channelIDToClient    map[uuid.UUID]*WSConn  // Maps channel_id to reverse client WebSocket connection
	channelIDToConnector map[uuid.UUID]*WSConn  // Maps channel_id to connector WebSocket connection
	tokenCache           map[string][]uuid.UUID // Maps token to list of connect_ids and channel_ids
	mu                   sync.RWMutex
}

// newConnectorCache creates a new connector cache
func newConnectorCache() *connectorCache {
	return &connectorCache{
		connectIDToConnector: make(map[uuid.UUID]*WSConn),
		channelIDToClient:    make(map[uuid.UUID]*WSConn),
		channelIDToConnector: make(map[uuid.UUID]*WSConn),
		tokenCache:           make(map[string][]uuid.UUID),
	}
}

// ServerOption represents configuration options for WSSocksServer
type ServerOption struct {
	WSHost          string
	WSPort          int
	SocksHost       string
	PortPool        *PortPool
	SocksWaitClient bool
	Logger          zerolog.Logger
	BufferSize      int
	APIKey          string
	ChannelTimeout  time.Duration
	ConnectTimeout  time.Duration
}

// DefaultServerOption returns default server options
func DefaultServerOption() *ServerOption {
	return &ServerOption{
		WSHost:          "0.0.0.0",
		WSPort:          8765,
		SocksHost:       "127.0.0.1",
		PortPool:        NewPortPoolFromRange(1024, 10240),
		SocksWaitClient: true,
		Logger:          zerolog.New(os.Stdout).With().Timestamp().Logger(),
		BufferSize:      DefaultBufferSize,
		APIKey:          "",
		ChannelTimeout:  DefaultChannelTimeout,
		ConnectTimeout:  DefaultConnectTimeout,
	}
}

// WithWSHost sets the WebSocket host
func (o *ServerOption) WithWSHost(host string) *ServerOption {
	o.WSHost = host
	return o
}

// WithWSPort sets the WebSocket port
func (o *ServerOption) WithWSPort(port int) *ServerOption {
	o.WSPort = port
	return o
}

// WithSocksHost sets the SOCKS host
func (o *ServerOption) WithSocksHost(host string) *ServerOption {
	o.SocksHost = host
	return o
}

// WithPortPool sets the port pool
func (o *ServerOption) WithPortPool(pool *PortPool) *ServerOption {
	o.PortPool = pool
	return o
}

// WithSocksWaitClient sets whether to wait for client before starting SOCKS server
func (o *ServerOption) WithSocksWaitClient(wait bool) *ServerOption {
	o.SocksWaitClient = wait
	return o
}

// WithLogger sets the logger
func (o *ServerOption) WithLogger(logger zerolog.Logger) *ServerOption {
	o.Logger = logger
	return o
}

// WithBufferSize sets the buffer size for data transfer
func (o *ServerOption) WithBufferSize(size int) *ServerOption {
	o.BufferSize = size
	return o
}

// WithAPI sets apiKey to enable the HTTP API
func (o *ServerOption) WithAPI(apiKey string) *ServerOption {
	o.APIKey = apiKey
	return o
}

// WithChannelTimeout sets the channel timeout duration
func (o *ServerOption) WithChannelTimeout(timeout time.Duration) *ServerOption {
	o.ChannelTimeout = timeout
	return o
}

// WithConnectTimeout sets the connect timeout duration
func (o *ServerOption) WithConnectTimeout(timeout time.Duration) *ServerOption {
	o.ConnectTimeout = timeout
	return o
}

// NewWSSocksServer creates a new WSSocksServer instance
func NewWSSocksServer(opt *ServerOption) *WSSocksServer {
	if opt == nil {
		opt = DefaultServerOption()
	}

	relayOpt := NewDefaultRelayOption().
		WithBufferSize(opt.BufferSize).
		WithChannelTimeout(opt.ChannelTimeout).
		WithConnectTimeout(opt.ConnectTimeout)

	s := &WSSocksServer{
		relay:           NewRelay(opt.Logger, relayOpt),
		log:             opt.Logger,
		wsHost:          opt.WSHost,
		wsPort:          opt.WSPort,
		socksHost:       opt.SocksHost,
		portPool:        opt.PortPool,
		ready:           make(chan struct{}),
		clients:         make(map[uuid.UUID]*WSConn),
		forwardTokens:   make(map[string]struct{}),
		tokens:          make(map[string]int),
		tokenClients:    make(map[string][]clientInfo),
		tokenIndexes:    make(map[string]int),
		connectorTokens: make(map[string]string),
		connCache:       newConnectorCache(),
		tokenOptions:    make(map[string]*ReverseTokenOptions),
		socksTasks:      make(map[int]context.CancelFunc),
		socksWaitClient: opt.SocksWaitClient,
		waitingSockets:  make(map[int]*waitingSocket),
		socketManager:   NewSocketManager(opt.SocksHost, opt.Logger),
		apiKey:          opt.APIKey,
		internalTokens:  make(map[string][]string),
		errors:          make(chan error, 1),
	}

	return s
}

// generateRandomToken generates a random token string
func generateRandomToken(length int) string {
	b := make([]byte, length/2)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ReverseTokenOptions represents configuration options for reverse token
type ReverseTokenOptions struct {
	Token                string
	Port                 int
	Username             string
	Password             string
	AllowManageConnector bool // Allows managing connectors via WebSocket messages
}

// DefaultReverseTokenOptions returns default options for reverse token
func DefaultReverseTokenOptions() *ReverseTokenOptions {
	return &ReverseTokenOptions{
		Token:                "",    // Will be auto-generated
		Port:                 0,     // Will be assigned from pool
		AllowManageConnector: false, // Default to false for security
	}
}

// tokenExists checks if a token already exists in any form (forward, reverse, or connector)
func (s *WSSocksServer) tokenExists(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check if token exists as a forward token
	if _, exists := s.forwardTokens[token]; exists {
		return true
	}

	// Check if token exists as a reverse token
	if _, exists := s.tokens[token]; exists {
		return true
	}

	// Check if token exists as a connector token
	if _, exists := s.connectorTokens[token]; exists {
		return true
	}

	return false
}

// AddReverseToken adds a new token for reverse socks and assigns a port
func (s *WSSocksServer) AddReverseToken(opts *ReverseTokenOptions) (string, int, error) {
	if opts == nil {
		opts = DefaultReverseTokenOptions()
	}

	// If token is provided, check if it already exists
	if opts.Token != "" && s.tokenExists(opts.Token) {
		return "", 0, fmt.Errorf("token already exists")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate random token if not provided
	token := opts.Token
	if token == "" {
		token = generateRandomToken(16)
	}

	// For autonomy tokens, don't allocate a port
	if opts.AllowManageConnector {
		s.tokens[token] = -1 // Use -1 to indicate no SOCKS port
		s.tokenOptions[token] = opts
		s.log.Info().Msg("New autonomy reverse token added")
		return token, -1, nil
	}

	// Check if token already exists
	if existingPort, exists := s.tokens[token]; exists {
		return token, existingPort, nil
	}

	// Get port from pool
	assignedPort := s.portPool.Get(opts.Port)
	if assignedPort == 0 {
		return "", 0, fmt.Errorf("cannot allocate port: %d", opts.Port)
	}

	// Store token information
	s.tokens[token] = assignedPort
	s.tokenOptions[token] = opts

	// Start SOCKS server immediately if we're not waiting for clients
	if s.wsServer != nil && !s.socksWaitClient {
		ctx, cancel := context.WithCancel(context.Background())
		s.socksTasks[assignedPort] = cancel
		go func() {
			if err := s.runSocksServer(ctx, token, assignedPort); err != nil {
				s.log.Warn().Err(err).Int("port", assignedPort).Msg("SOCKS server error")
			}
		}()
	}

	s.log.Info().Int("port", assignedPort).Msg("New reverse proxy token added")
	return token, assignedPort, nil
}

// AddForwardToken adds a new token for forward socks proxy
func (s *WSSocksServer) AddForwardToken(token string) (string, error) {
	// Check if token already exists
	if token != "" && s.tokenExists(token) {
		return "", fmt.Errorf("token already exists")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if token == "" {
		token = generateRandomToken(16)
	}

	s.forwardTokens[token] = struct{}{}
	s.log.Info().Msg("New forward proxy token added")
	return token, nil
}

// AddConnectorToken adds a new connector token that forwards requests to a reverse token
func (s *WSSocksServer) AddConnectorToken(connectorToken string, reverseToken string) (string, error) {
	// Check if connector token already exists
	if connectorToken != "" && s.tokenExists(connectorToken) {
		return "", fmt.Errorf("connector token already exists")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate random token if not provided
	if connectorToken == "" {
		connectorToken = generateRandomToken(16)
	}

	// Verify reverse token exists
	if _, exists := s.tokens[reverseToken]; !exists {
		return "", fmt.Errorf("reverse token does not exist")
	}

	// Store connector token mapping
	s.connectorTokens[connectorToken] = reverseToken

	s.log.Info().Msg("New connector token added")

	return connectorToken, nil
}

// RemoveToken removes a token and disconnects all its clients
func (s *WSSocksServer) RemoveToken(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Clean up any internal tokens first
	if internalTokens, exists := s.internalTokens[token]; exists {
		for _, internalToken := range internalTokens {
			// Clean up internal token data
			if clients, ok := s.tokenClients[internalToken]; ok {
				for _, client := range clients {
					client.Conn.Close()
					delete(s.clients, client.ID)
				}
				delete(s.tokenClients, internalToken)
			}
			delete(s.tokens, internalToken)
			delete(s.tokenIndexes, internalToken)
			delete(s.tokenOptions, internalToken)
		}
		delete(s.internalTokens, token)
	}

	// Handle connector proxy token
	if _, isConnector := s.connectorTokens[token]; isConnector {
		// Clean up connector cache
		s.connCache.mu.Lock()
		if ids, exists := s.connCache.tokenCache[token]; exists {
			for _, id := range ids {
				delete(s.connCache.connectIDToConnector, id)
				delete(s.connCache.channelIDToClient, id)
				delete(s.connCache.channelIDToConnector, id)
			}
			delete(s.connCache.tokenCache, token)
		}
		s.connCache.mu.Unlock()

		// Close all client connections for this token
		if clients, ok := s.tokenClients[token]; ok {
			for _, client := range clients {
				client.Conn.Close()
				delete(s.clients, client.ID)
			}
			delete(s.tokenClients, token)
		}

		// Clean up token related data
		delete(s.connectorTokens, token)

		s.log.Info().Str("token", token).Msg("Connector token removed")

		return true
	}

	// Handle reverse proxy token
	if port, isReverse := s.tokens[token]; isReverse {
		// Remove all connector tokens using this reverse token
		for connectorToken, rt := range s.connectorTokens {
			if rt == token {
				s.RemoveToken(connectorToken)
			}
		}

		// Close all client connections for this token
		if clients, ok := s.tokenClients[token]; ok {
			for _, client := range clients {
				client.Conn.Close()
				delete(s.clients, client.ID)
			}
			delete(s.tokenClients, token)
		}

		// Clean up token related data
		delete(s.tokens, token)
		delete(s.tokenIndexes, token)
		delete(s.tokenOptions, token)

		// Cancel and clean up SOCKS server if it exists
		if cancel, exists := s.socksTasks[port]; exists {
			cancel()
			delete(s.socksTasks, port)
		}

		// Return port to pool
		s.portPool.Put(port)

		s.log.Info().Str("token", token).Msg("Reverse token removed")

		return true
	}

	// Handle forward proxy token
	if _, isForward := s.forwardTokens[token]; isForward {
		// Close all client connections for this token
		if clients, ok := s.tokenClients[token]; ok {
			for _, client := range clients {
				client.Conn.Close()
				delete(s.clients, client.ID)
			}
			delete(s.tokenClients, token)
		}

		// Clean up token related data
		delete(s.forwardTokens, token)

		s.log.Info().Str("token", token).Msg("Forward token removed")

		return true
	}

	return true
}

// handlePendingToken handles starting SOCKS server for a token
func (s *WSSocksServer) handlePendingToken(ctx context.Context, token string) error {
	if s.socksWaitClient {
		return nil // Don't start SOCKS server if waiting for client
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	socksPort, exists := s.tokens[token]
	if !exists {
		return nil
	}

	if _, running := s.socksTasks[socksPort]; !running {
		ctx, cancel := context.WithCancel(ctx)
		s.socksTasks[socksPort] = cancel
		go func() {
			if err := s.runSocksServer(ctx, token, socksPort); err != nil {
				s.log.Warn().Err(err).Int("port", socksPort).Msg("SOCKS server error")
			}
		}()
	}
	return nil
}

// Serve starts the WebSocket server and waits for clients
func (s *WSSocksServer) Serve(ctx context.Context) error {
	upgrader := websocket.Upgrader{
		CheckOrigin: func(r *http.Request) bool {
			return true // Allow all origins
		},
	}

	mux := http.NewServeMux()

	// Register API handlers if enabled
	if s.apiKey != "" {
		apiHandler := NewAPIHandler(s, s.apiKey)
		apiHandler.RegisterHandlers(mux)
		s.log.Info().Int("port", s.wsPort).Msg("API endpoints enabled")
	}

	// Register WebSocket handler
	mux.HandleFunc("/socket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			s.log.Warn().Err(err).Msg("Failed to upgrade connection")
			return
		}
		go s.handleWebSocket(ctx, conn)
	})

	// Update root handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if s.apiKey != "" {
				fmt.Fprintf(w, "WSSocks %s is running. API endpoints available at /api/*\n", Version)
			} else {
				fmt.Fprintf(w, "WSSocks %s is running but API is not enabled.\n", Version)
			}
			return
		}
		http.NotFound(w, r)
	})

	s.wsServer = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", s.wsHost, s.wsPort),
		Handler: mux,
	}

	// Handle all pending tokens
	s.mu.RLock()
	tokens := make([]string, 0, len(s.tokens))
	for token := range s.tokens {
		tokens = append(tokens, token)
	}
	s.mu.RUnlock()

	for _, token := range tokens {
		if err := s.handlePendingToken(ctx, token); err != nil {
			s.log.Error().Err(err).Str("token", token).Msg("Failed to handle pending token")
		}
	}

	s.log.Info().
		Str("listen", s.wsServer.Addr).
		Str("url", fmt.Sprintf("http://localhost:%d", s.wsPort)).
		Msg("WSSocks Server started")
	close(s.ready)

	return s.wsServer.ListenAndServe()
}

// WaitReady waits for the server to be ready with optional timeout
func (s *WSSocksServer) WaitReady(ctx context.Context, timeout time.Duration) error {
	ctx, cancel := context.WithCancel(ctx)

	s.mu.Lock()
	s.cancelFunc = cancel
	s.mu.Unlock()

	go func() {
		if err := s.Serve(ctx); err != nil {
			s.errors <- err
		}
	}()

	if timeout > 0 {
		select {
		case <-s.ready:
			return nil
		case err := <-s.errors:
			return err
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(timeout):
			return fmt.Errorf("timeout waiting for server to be ready")
		}
	}

	select {
	case <-s.ready:
		return nil
	case err := <-s.errors:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// handleWebSocket handles WebSocket connection
func (s *WSSocksServer) handleWebSocket(ctx context.Context, ws *websocket.Conn) {
	// Wrap the websocket connection
	wsConn := NewWSConn(ws, "", s.log)

	var clientID uuid.UUID
	var token string
	var internalToken string

	defer func() {
		wsConn.Close()
		if clientID != uuid.Nil {
			s.cleanupConnection(clientID, internalToken)
		}
	}()

	// Handle authentication
	msg, err := wsConn.ReadMessage()
	if err != nil {
		s.log.Debug().Err(err).Msg("Failed to read auth message")
		authResponse := AuthResponseMessage{Success: false}
		s.relay.logMessage(authResponse, "send", wsConn.Label())
		wsConn.WriteMessage(authResponse)
		wsConn.flushBatch()
		return
	}

	s.relay.logMessage(msg, "recv", wsConn.Label())
	authMsg, ok := msg.(AuthMessage)
	if !ok {
		authResponse := AuthResponseMessage{Success: false}
		s.relay.logMessage(authResponse, "send", wsConn.Label())
		wsConn.WriteMessage(authResponse)
		wsConn.flushBatch()
		return
	}

	token = authMsg.Token
	s.mu.RLock()
	isValidReverse := authMsg.Reverse && s.tokens[token] != 0
	_, hasForwardToken := s.forwardTokens[token]
	isValidForward := !authMsg.Reverse && hasForwardToken
	reverseToken, isConnectorToken := s.connectorTokens[token]
	isValidConnector := isConnectorToken && !authMsg.Reverse && s.tokens[reverseToken] != 0
	s.mu.RUnlock()

	if !isValidReverse && !isValidForward && !isValidConnector {
		authResponse := AuthResponseMessage{Success: false}
		s.relay.logMessage(authResponse, "send", wsConn.Label())
		wsConn.WriteMessage(authResponse)
		wsConn.flushBatch()
		return
	}

	clientID = uuid.New()
	wsConn.setLabel(clientID.String())

	s.mu.Lock()
	// For reverse tokens with AllowManageConnector, generate a unique internal token
	if isValidReverse {
		opts, exists := s.tokenOptions[token]
		if exists && opts.AllowManageConnector {
			internalToken = uuid.New().String()
			// Initialize structures for this internal token
			s.tokenClients[internalToken] = make([]clientInfo, 0)
			s.tokenIndexes[internalToken] = 0
			s.tokenOptions[internalToken] = opts
			s.tokens[internalToken] = -1

			// Add to internalTokens mapping
			s.internalTokens[token] = append(s.internalTokens[token], internalToken)
		} else {
			internalToken = token
		}
	} else {
		internalToken = token
	}

	if _, exists := s.tokenClients[internalToken]; !exists {
		s.tokenClients[internalToken] = make([]clientInfo, 0)
	}
	s.tokenClients[internalToken] = append(s.tokenClients[internalToken], clientInfo{ID: clientID, Conn: wsConn})
	s.clients[clientID] = wsConn
	s.mu.Unlock()

	if authMsg.Reverse {
		// Handle reverse proxy client
		s.mu.Lock()
		// Start SOCKS server if not already running
		socksPort := s.tokens[token]
		_, exists := s.socksTasks[socksPort]
		if socksPort > 0 && !exists {
			ctx, cancel := context.WithCancel(ctx)
			s.socksTasks[socksPort] = cancel
			go func() {
				if err := s.runSocksServer(ctx, token, socksPort); err != nil {
					s.log.Debug().Err(err).Int("port", socksPort).Msg("SOCKS server error")
				}
			}()
		}
		s.mu.Unlock()
		s.log.Debug().Str("client_id", clientID.String()).Msg("Reverse client authenticated")
	} else if isConnectorToken {
		// Handle connector proxy client
		s.log.Debug().Str("client_id", clientID.String()).Msg("Connector client authenticated")
	} else {
		// Handle forward proxy client
		s.log.Debug().Str("client_id", clientID.String()).Msg("Forward client authenticated")
	}

	// Send auth response
	authResponse := AuthResponseMessage{Success: true}
	s.relay.logMessage(authResponse, "send", wsConn.Label())
	if err := wsConn.WriteMessage(authResponse); err != nil {
		s.log.Debug().Err(err).Msg("Failed to send auth response")
		wsConn.flushBatch()
		return
	}

	// Start message handling goroutines
	errChan := make(chan error, 2)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start message dispatcher
	if isConnectorToken {
		go func() {
			errChan <- s.connectorMessageDispatcher(ctx, wsConn, reverseToken)
		}()
	} else {
		go func() {
			errChan <- s.messageDispatcher(ctx, wsConn, clientID)
		}()
	}

	// Wait for either routine to finish
	<-errChan
}

// messageDispatcher handles WebSocket message distribution
func (s *WSSocksServer) messageDispatcher(ctx context.Context, ws *WSConn, clientID uuid.UUID) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msg, err := ws.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					s.log.Debug().Err(err).Msg("WebSocket read error")
				}
				return err
			}

			s.relay.logMessage(msg, "recv", ws.Label())

			switch m := msg.(type) {
			case DataMessage:
				channelID := m.ChannelID
				// First try existing message queues
				if queue, ok := s.relay.messageQueues.Load(channelID); ok {
					select {
					case queue.(chan DataMessage) <- m:
						s.log.Trace().Str("channel_id", channelID.String()).Msg("Message forwarded to channel")
					default:
						s.log.Debug().Str("channel_id", channelID.String()).Msg("Message queue full")
					}
					continue
				}

				// If not in message queues, check connector channels
				s.connCache.mu.RLock()
				targetWS, exists := s.connCache.channelIDToClient[channelID]
				s.connCache.mu.RUnlock()

				if exists {
					s.relay.logMessage(m, "send", ws.Label())
					if err := targetWS.WriteMessage(m); err != nil {
						s.log.Debug().Err(err).Msg("Failed to forward data message to connector client")
					}
				} else {
					s.log.Debug().Str("channel_id", channelID.String()).Msg("Received data for unknown channel")
				}

			case ConnectMessage:
				var isForwardClient bool
				s.mu.RLock()
				_, isForwardClient = s.clients[clientID]
				s.mu.RUnlock()

				if isForwardClient {
					go func() {
						if err := s.relay.HandleNetworkConnection(ctx, ws, m); err != nil && !errors.Is(err, context.Canceled) {
							s.log.Debug().Err(err).Msg("Network connection handler error")
						}
					}()
				}

			case ConnectResponseMessage:
				if queue, ok := s.relay.messageQueues.Load(m.ConnectID); ok {
					queue.(chan ConnectResponseMessage) <- m
				} else {
					// Store channel mappings for connector routing
					s.connCache.mu.Lock()
					if connectorWS, exists := s.connCache.connectIDToConnector[m.ConnectID]; exists {
						// Store both client and connector mappings for the new channel
						s.connCache.channelIDToClient[m.ChannelID] = connectorWS
						s.connCache.channelIDToConnector[m.ChannelID] = ws
						// Forward the response to the connector
						s.relay.logMessage(m, "send", ws.Label())
						if err := connectorWS.WriteMessage(m); err != nil {
							s.log.Debug().Err(err).Msg("Failed to forward connect response to connector")
						}
					} else {
						s.log.Debug().Str("connect_id", m.ConnectID.String()).Msg("Received connect response for unknown channel")
					}
					s.connCache.mu.Unlock()
				}

			case DisconnectMessage:
				// Forward disconnect message to connector if exists
				s.connCache.mu.Lock()
				if targetWS, exists := s.connCache.channelIDToConnector[m.ChannelID]; exists {
					s.relay.logMessage(m, "send", ws.Label())
					if err := targetWS.WriteMessage(m); err != nil {
						s.log.Debug().Err(err).Msg("Failed to forward disconnect message")
					}
				}
				s.connCache.mu.Unlock()

				// Clean up relay channels
				if cancelVal, ok := s.relay.tcpChannels.LoadAndDelete(m.ChannelID); ok {
					if cancel, ok := cancelVal.(context.CancelFunc); ok {
						cancel()
					}
				}
				if cancelVal, ok := s.relay.udpChannels.LoadAndDelete(m.ChannelID); ok {
					if cancel, ok := cancelVal.(context.CancelFunc); ok {
						cancel()
					}
				}
				s.relay.udpClientAddrs.Delete(m.ChannelID)
				s.relay.messageQueues.Delete(m.ChannelID)

				// Clean up connector channel mappings
				s.connCache.mu.Lock()
				delete(s.connCache.channelIDToClient, m.ChannelID)
				delete(s.connCache.channelIDToConnector, m.ChannelID)
				s.connCache.mu.Unlock()

			case ConnectorMessage:
				// Check if this client has permission to manage connectors
				s.mu.RLock()
				var token string
				var hasPermission bool
				for t, clients := range s.tokenClients {
					for _, client := range clients {
						if client.ID == clientID {
							token = t
							// Check if this token has connector management permission
							if opts, exists := s.tokenOptions[t]; exists {
								hasPermission = opts.AllowManageConnector
							}
							break
						}
					}
					if token != "" {
						break
					}
				}
				s.mu.RUnlock()

				// Prepare response
				response := ConnectorResponseMessage{
					ConnectID: m.ConnectID,
				}

				if !hasPermission {
					response.Success = false
					response.Error = "Unauthorized connector management attempt"
					s.log.Warn().
						Str("client_id", clientID.String()).
						Msg("Unauthorized connector management attempt")
				} else {
					switch m.Operation {
					case "add":
						newToken, err := s.AddConnectorToken(m.ConnectorToken, token)
						if err != nil {
							response.Success = false
							response.Error = err.Error()
							s.log.Warn().
								Err(err).
								Str("connector_token", m.ConnectorToken).
								Msg("Failed to add connector token")
						} else {
							response.Success = true
							response.ConnectorToken = newToken
							s.log.Info().
								Str("connector_token", newToken).
								Msg("Added new connector token via WebSocket")
						}

					case "remove":
						if removed := s.RemoveToken(m.ConnectorToken); !removed {
							response.Success = false
							response.Error = "Failed to remove connector token"
							s.log.Warn().
								Str("connector_token", m.ConnectorToken).
								Msg("Failed to remove connector token")
						} else {
							response.Success = true
							s.log.Info().
								Str("connector_token", m.ConnectorToken).
								Msg("Removed connector token via WebSocket")
						}

					default:
						response.Success = false
						response.Error = fmt.Sprintf("Unknown connector operation: %s", m.Operation)
						s.log.Info().
							Str("operation", m.Operation).
							Msg("Unknown connector operation")
					}
				}

				// Send response
				s.relay.logMessage(response, "send", ws.Label())
				if err := ws.WriteMessage(response); err != nil {
					s.log.Warn().Err(err).Msg("Failed to send connector response")
					return err
				}

			default:
				s.log.Debug().Str("type", msg.GetType()).Msg("Received unknown message type")
			}
		}
	}
}

// connectorMessageDispatcher handles WebSocket message distribution for connector tokens
func (s *WSSocksServer) connectorMessageDispatcher(ctx context.Context, ws *WSConn, reverseToken string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			msg, err := ws.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					s.log.Debug().Err(err).Msg("WebSocket read error")
				}
				return err
			}

			s.relay.logMessage(msg, "recv", ws.Label())

			switch m := msg.(type) {
			case ConnectMessage:
				// Store connect_id mapping for connector
				s.connCache.mu.Lock()
				s.connCache.connectIDToConnector[m.ConnectID] = ws
				if ids, exists := s.connCache.tokenCache[reverseToken]; exists {
					s.connCache.tokenCache[reverseToken] = append(ids, m.ConnectID)
				} else {
					s.connCache.tokenCache[reverseToken] = []uuid.UUID{m.ConnectID}
				}
				s.connCache.mu.Unlock()

				// Forward to reverse client
				reverseWS, err := s.getNextWebSocket(reverseToken)
				if err != nil {
					s.log.Debug().Err(err).Msg("Failed to get reverse client")
					continue
				}
				s.relay.logMessage(m, "send", ws.Label())
				if err := reverseWS.WriteMessage(m); err != nil {
					s.log.Debug().Err(err).Msg("Failed to forward connect message")
				}

			case DataMessage:
				// Route data message based on channel_id
				s.connCache.mu.RLock()
				targetWS, exists := s.connCache.channelIDToConnector[m.ChannelID]
				s.connCache.mu.RUnlock()

				if exists {
					s.relay.logMessage(m, "send", ws.Label())
					if err := targetWS.WriteMessage(m); err != nil {
						s.log.Debug().Err(err).Msg("Failed to forward data message")
					}
				}

			case DisconnectMessage:
				// Clean up channel mappings and forward message
				s.connCache.mu.Lock()
				if targetWS, exists := s.connCache.channelIDToConnector[m.ChannelID]; exists {
					s.relay.logMessage(m, "send", ws.Label())
					if err := targetWS.WriteMessage(m); err != nil {
						s.log.Debug().Err(err).Msg("Failed to forward disconnect message")
					}
					delete(s.connCache.channelIDToConnector, m.ChannelID)
					delete(s.connCache.channelIDToClient, m.ChannelID)
				}
				s.connCache.mu.Unlock()
			}
		}
	}
}

// cleanupConnection cleans up resources when a client disconnects
func (s *WSSocksServer) cleanupConnection(clientID uuid.UUID, token string) {
	if clientID == uuid.Nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Clean up connection in tokenClients
	if token != "" && s.tokenClients[token] != nil {
		clients := make([]clientInfo, 0)
		for _, client := range s.tokenClients[token] {
			if client.ID != clientID {
				clients = append(clients, client)
			}
		}
		if len(clients) == 0 {
			delete(s.tokenClients, token)
			delete(s.tokenIndexes, token)
		} else {
			s.tokenClients[token] = clients
		}
	}

	// Clean up client connection
	delete(s.clients, clientID)

	s.log.Debug().Str("client_id", clientID.String()).Msg("Client disconnected")
}

// getNextWebSocket gets next available WebSocket connection using round-robin
func (s *WSSocksServer) getNextWebSocket(token string) (*WSConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if _, exists := s.tokenClients[token]; !exists || len(s.tokenClients[token]) == 0 {
		return nil, fmt.Errorf("no available clients for token")
	}

	clients := s.tokenClients[token]
	currentIndex := s.tokenIndexes[token]
	s.tokenIndexes[token] = (currentIndex + 1) % len(clients)

	s.log.Trace().Int("index", currentIndex).Msg("Using client index for request")

	if currentIndex < len(clients) {
		return clients[currentIndex].Conn, nil
	}
	return clients[0].Conn, nil
}

// handleSocksRequest handles incoming SOCKS5 connection
func (s *WSSocksServer) handleSocksRequest(ctx context.Context, socksConn net.Conn, addr net.Addr, token string) error {
	s.mu.RLock()
	_, hasClients := s.tokenClients[token]
	s.mu.RUnlock()

	// Wait up to 10 seconds for clients to connect if needed
	if !hasClients {
		timeout := time.After(10 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout:
				s.log.Debug().Str("addr", addr.String()).Msg("No valid clients after timeout")
				return s.relay.RefuseSocksRequest(socksConn, 3)
			case <-ticker.C:
				s.mu.RLock()
				clients, ok := s.tokenClients[token]
				hasValidClients := ok && len(clients) > 0
				s.mu.RUnlock()
				if hasValidClients {
					goto ClientFound
				}
			}
		}
	}

ClientFound:
	// Get WebSocket connection using round-robin
	ws, err := s.getNextWebSocket(token)
	if err != nil {
		s.log.Warn().Int("port", s.tokens[token]).Msg("No available client for SOCKS5 port")
		return s.relay.RefuseSocksRequest(socksConn, 3)
	}

	// Get authentication info if configured
	var username, password string
	s.mu.RLock()
	if auth, ok := s.tokenOptions[token]; ok {
		username = auth.Username
		password = auth.Password
	}
	s.mu.RUnlock()

	// Handle SOCKS request using relay
	if err := s.relay.HandleSocksRequest(ctx, ws, socksConn, username, password); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Warn().Err(err).Msg("Error handling SOCKS request")
	}
	return nil
}

// runSocksServer runs a SOCKS5 server for a specific token and port
func (s *WSSocksServer) runSocksServer(ctx context.Context, token string, socksPort int) error {
	listener, err := s.socketManager.GetListener(socksPort)
	if err != nil {
		return err
	}
	defer s.socketManager.ReleaseListener(socksPort)

	s.log.Debug().Str("addr", listener.Addr().String()).Msg("SOCKS5 server started")

	go func() {
		<-ctx.Done()
		listener.(*net.TCPListener).SetDeadline(time.Now())
		s.socketManager.ReleaseListener(socksPort)
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				listener.(*net.TCPListener).SetDeadline(time.Time{})
				return nil // Context cancelled
			}
			s.log.Warn().Err(err).Msg("Failed to accept SOCKS connection")
			continue
		}

		go func() {
			if err := s.handleSocksRequest(ctx, conn, conn.RemoteAddr(), token); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Warn().Err(err).Msg("Error handling SOCKS request")
			}
		}()
	}
}

// Close gracefully shuts down the WSSocksServer
func (s *WSSocksServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Close relay
	s.relay.Close()

	// Clean up all waiting sockets
	s.waitingMu.Lock()
	for port, waiting := range s.waitingSockets {
		waiting.cancelTimer.Stop()
		waiting.listener.Close()
		delete(s.waitingSockets, port)
	}
	s.waitingMu.Unlock()

	// Clean up all SOCKS servers
	for port, cancel := range s.socksTasks {
		cancel()
		delete(s.socksTasks, port)
	}

	// Clean up all client connections
	for clientID, ws := range s.clients {
		ws.Close()
		delete(s.clients, clientID)
	}

	// Close WebSocket server if it exists
	if s.wsServer != nil {
		if err := s.wsServer.Close(); err != nil {
			s.log.Warn().Err(err).Msg("Error closing WebSocket server")
		}
		s.wsServer = nil
	}

	// Cancel main worker if it exists
	if s.cancelFunc != nil {
		s.cancelFunc()
		s.cancelFunc = nil
	}

	s.socketManager.Close()
	s.log.Info().Msg("Server stopped")
}

// GetClientCount returns the total number of connected clients
func (s *WSSocksServer) GetClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// HasClients returns true if there are any connected clients
func (s *WSSocksServer) HasClients() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients) > 0
}

// GetTokenClientCount counts clients connected for a given token
func (s *WSSocksServer) GetTokenClientCount(token string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check reverse proxy clients
	if clients, exists := s.tokenClients[token]; exists {
		return len(clients)
	}

	// Check forward proxy clients
	if _, exists := s.forwardTokens[token]; exists {
		return len(s.clients)
	}

	return 0
}
