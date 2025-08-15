package linksocks

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// LinkSocksServer represents a SOCKS5 over WebSocket protocol server
type LinkSocksServer struct {
	// Core components
	relay *Relay
	log   zerolog.Logger

	// Synchronization primitives
	mu         sync.RWMutex
	ready      chan struct{}
	cancelFunc context.CancelFunc
	closed     bool // Flag to track if server has been closed

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
	sha256TokenMap  map[string]string               // Maps SHA256 tokens to original tokens

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
	channelIDToClient    map[uuid.UUID]*WSConn  // Maps channel_id to reverse client WebSocket connection
	channelIDToConnector map[uuid.UUID]*WSConn  // Maps channel_id to connector WebSocket connection
	tokenCache           map[string][]uuid.UUID // Maps token to list of channel_ids
	mu                   sync.RWMutex
}

// newConnectorCache creates a new connector cache
func newConnectorCache() *connectorCache {
	return &connectorCache{
		channelIDToClient:    make(map[uuid.UUID]*WSConn),
		channelIDToConnector: make(map[uuid.UUID]*WSConn),
		tokenCache:           make(map[string][]uuid.UUID),
	}
}

// ServerOption represents configuration options for LinkSocksServer
type ServerOption struct {
	WSHost           string
	WSPort           int
	SocksHost        string
	PortPool         *PortPool
	SocksWaitClient  bool
	Logger           zerolog.Logger
	BufferSize       int
	APIKey           string
	ChannelTimeout   time.Duration
	ConnectTimeout   time.Duration
	FastOpen         bool
	UpstreamProxy    string
	UpstreamUsername string
	UpstreamPassword string
}

// DefaultServerOption returns default server options
func DefaultServerOption() *ServerOption {
	return &ServerOption{
		WSHost:           "0.0.0.0",
		WSPort:           8765,
		SocksHost:        "127.0.0.1",
		PortPool:         NewPortPoolFromRange(1024, 10240),
		SocksWaitClient:  true,
		Logger:           zerolog.New(os.Stdout).With().Timestamp().Logger(),
		BufferSize:       DefaultBufferSize,
		APIKey:           "",
		ChannelTimeout:   DefaultChannelTimeout,
		ConnectTimeout:   DefaultConnectTimeout,
		FastOpen:         false,
		UpstreamProxy:    "",
		UpstreamUsername: "",
		UpstreamPassword: "",
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

// WithFastOpen controls whether to wait for connect success response
func (o *ServerOption) WithFastOpen(fastOpen bool) *ServerOption {
	o.FastOpen = fastOpen
	return o
}

// WithUpstreamProxy sets the upstream SOCKS5 proxy
func (o *ServerOption) WithUpstreamProxy(proxy string) *ServerOption {
	o.UpstreamProxy = proxy
	return o
}

// WithUpstreamAuth sets the upstream SOCKS5 proxy authentication
func (o *ServerOption) WithUpstreamAuth(username, password string) *ServerOption {
	o.UpstreamUsername = username
	o.UpstreamPassword = password
	return o
}

// NewLinkSocksServer creates a new LinkSocksServer instance
func NewLinkSocksServer(opt *ServerOption) *LinkSocksServer {
	if opt == nil {
		opt = DefaultServerOption()
	}

	relayOpt := NewDefaultRelayOption().
		WithBufferSize(opt.BufferSize).
		WithChannelTimeout(opt.ChannelTimeout).
		WithConnectTimeout(opt.ConnectTimeout).
		WithFastOpen(opt.FastOpen).
		WithUpstreamProxy(opt.UpstreamProxy).
		WithUpstreamAuth(opt.UpstreamUsername, opt.UpstreamPassword)

	s := &LinkSocksServer{
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
		sha256TokenMap:  make(map[string]string),
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

// ReverseTokenResult represents the result of adding a reverse token
type ReverseTokenResult struct {
	Token string // The token that was created or used
	Port  int    // The port assigned to the token
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
func (s *LinkSocksServer) tokenExists(token string) bool {
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
func (s *LinkSocksServer) AddReverseToken(opts *ReverseTokenOptions) (*ReverseTokenResult, error) {
	if opts == nil {
		opts = DefaultReverseTokenOptions()
	}

	// If token is provided, check if it already exists
	if opts.Token != "" && s.tokenExists(opts.Token) {
		return nil, fmt.Errorf("token already exists")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate random token if not provided
	token := opts.Token
	if token == "" {
		token = generateRandomToken(16)
	}

	// Generate SHA256 version of the token
	hash := sha256.Sum256([]byte(token))
	sha256Token := hex.EncodeToString(hash[:])
	s.sha256TokenMap[sha256Token] = token

	// For autonomy tokens, don't allocate a port
	if opts.AllowManageConnector {
		s.tokens[token] = -1 // Use -1 to indicate no SOCKS port
		s.tokenOptions[token] = opts
		s.log.Info().Msg("New autonomy reverse token added")
		return &ReverseTokenResult{
			Token: token,
			Port:  -1,
		}, nil
	}

	// Check if token already exists
	if existingPort, exists := s.tokens[token]; exists {
		return &ReverseTokenResult{
			Token: token,
			Port:  existingPort,
		}, nil
	}

	// Get port from pool
	assignedPort := s.portPool.Get(opts.Port)
	if assignedPort == 0 {
		return nil, fmt.Errorf("cannot allocate port: %d", opts.Port)
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
	s.log.Debug().Str("sha256Token", sha256Token).Msg("SHA256 for the token")
	return &ReverseTokenResult{
		Token: token,
		Port:  assignedPort,
	}, nil
}

// AddForwardToken adds a new token for forward socks proxy
func (s *LinkSocksServer) AddForwardToken(token string) (string, error) {
	// Check if token already exists
	if token != "" && s.tokenExists(token) {
		return "", fmt.Errorf("token already exists")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if token == "" {
		token = generateRandomToken(16)
	}

	// Generate SHA256 version of the token
	hash := sha256.Sum256([]byte(token))
	sha256Token := hex.EncodeToString(hash[:])
	s.sha256TokenMap[sha256Token] = token

	s.forwardTokens[token] = struct{}{}
	s.log.Info().Msg("New forward proxy token added")
	s.log.Debug().Str("sha256Token", sha256Token).Msg("SHA256 for the token")
	return token, nil
}

// AddConnectorToken adds a new connector token that forwards requests to a reverse token
func (s *LinkSocksServer) AddConnectorToken(connectorToken string, reverseToken string) (string, error) {
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

	// Generate SHA256 version of the token
	hash := sha256.Sum256([]byte(connectorToken))
	sha256Token := hex.EncodeToString(hash[:])
	s.sha256TokenMap[sha256Token] = connectorToken

	// Store connector token mapping
	s.connectorTokens[connectorToken] = reverseToken

	s.log.Info().Msg("New connector token added")

	return connectorToken, nil
}

// RemoveToken removes a token and disconnects all its clients
func (s *LinkSocksServer) RemoveToken(token string) bool {
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
func (s *LinkSocksServer) handlePendingToken(ctx context.Context, token string) error {
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
func (s *LinkSocksServer) Serve(ctx context.Context) error {
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

	handleWSUpgrade := func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			s.log.Warn().Err(err).Msg("Failed to upgrade connection")
			return
		}
		go s.handleWebSocket(ctx, conn, r)
	}

	// Register WebSocket handlers
	mux.HandleFunc("/socket/", handleWSUpgrade)
	mux.HandleFunc("/socket", handleWSUpgrade)

	// Update root handler
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			if s.apiKey != "" {
				fmt.Fprintf(w, "LinkSocks %s is running. API endpoints available at /api/*\n", Version)
			} else {
				fmt.Fprintf(w, "LinkSocks %s is running but API is not enabled.\n", Version)
			}
			return
		}
		http.NotFound(w, r)
	})

	s.wsServer = &http.Server{
		Addr:    net.JoinHostPort(s.wsHost, fmt.Sprintf("%d", s.wsPort)),
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

	// Start server in background goroutine
	go func() {
		if err := s.wsServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			s.errors <- err
		}
	}()

	// Wait for server to actually start listening
	go func() {
		for {
			// Check if we can connect to the server
			conn, err := net.DialTimeout("tcp", s.wsServer.Addr, 10*time.Millisecond)
			if err == nil {
				conn.Close()
				// Server is listening, signal ready
				s.log.Info().
					Str("listen", s.wsServer.Addr).
					Str("url", fmt.Sprintf("http://localhost:%d", s.wsPort)).
					Msg("LinkSocks server started")
				close(s.ready)
				return
			}
			// Wait a bit before trying again
			time.Sleep(10 * time.Millisecond)
		}
	}()

	// Block until context is done
	<-ctx.Done()
	return ctx.Err()
}

// WaitReady starts the server and waits for the server to be ready with optional timeout
func (s *LinkSocksServer) WaitReady(ctx context.Context, timeout time.Duration) error {
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
func (s *LinkSocksServer) handleWebSocket(ctx context.Context, ws *websocket.Conn, r *http.Request) {
	// Wrap the websocket connection
	wsConn := NewWSConn(ws, "", s.log)
	wsConn.SetClientIPFromRequest(r) // Extract and set client IP

	var clientID uuid.UUID
	var token string
	var internalToken string
	var isValidReverse, isValidForward, isValidConnector bool
	var reverseToken string
	var isUrlAuth bool
	var authMsg AuthMessage

	defer func() {
		wsConn.Close()
		if clientID != uuid.Nil {
			s.cleanupConnection(clientID, internalToken)
		}
	}()

	// Check if using URL query parameters for authentication
	query := r.URL.Query()
	if tokenParam := query.Get("token"); tokenParam != "" {
		s.log.Debug().Str("token_hash", tokenParam).Msg("Client is using token from URL query")
		if len(tokenParam) == 64 { // SHA256 hash is 64 characters
			s.mu.RLock()
			originalToken, exists := s.sha256TokenMap[tokenParam]
			if exists {
				token = originalToken
				isValidReverse = s.tokens[token] != 0
				_, hasForwardToken := s.forwardTokens[token]
				isValidForward = hasForwardToken
				tmpToken, isConnectorToken := s.connectorTokens[token]
				reverseToken = tmpToken
				isValidConnector = isConnectorToken && s.tokens[reverseToken] != 0

				// Check reverse parameter
				if reverseStr := query.Get("reverse"); reverseStr != "" {
					isReverse := reverseStr == "true" || reverseStr == "1"
					if isReverse && !isValidReverse {
						isValidReverse = false
						isValidForward = false
						isValidConnector = false
					} else if !isReverse && !isValidForward && !isValidConnector {
						isValidReverse = false
						isValidForward = false
						isValidConnector = false
					}
				}

				isUrlAuth = true
			}
			s.mu.RUnlock()
			if !exists || (!isValidReverse && !isValidForward && !isValidConnector) {
				authResponse := AuthResponseMessage{Success: false, Error: "invalid token"}
				s.relay.logMessage(authResponse, "send", wsConn.Label())
				wsConn.WriteMessage(authResponse)
				return
			}
		} else {
			authResponse := AuthResponseMessage{Success: false, Error: "invalid token format"}
			s.relay.logMessage(authResponse, "send", wsConn.Label())
			wsConn.WriteMessage(authResponse)
			return
		}
	} else {
		// Traditional authentication for requests without query parameters
		msg, err := wsConn.ReadMessage()
		if err != nil {
			s.log.Debug().Err(err).Msg("Failed to read auth message")
			authResponse := AuthResponseMessage{Success: false, Error: "invalid auth message"}
			s.relay.logMessage(authResponse, "send", wsConn.Label())
			wsConn.WriteMessage(authResponse)
			return
		}

		s.relay.logMessage(msg, "recv", wsConn.Label())
		authMsg, ok := msg.(AuthMessage)
		if !ok {
			authResponse := AuthResponseMessage{Success: false, Error: "invalid auth message"}
			s.relay.logMessage(authResponse, "send", wsConn.Label())
			wsConn.WriteMessage(authResponse)
			return
		}

		token = authMsg.Token
		s.mu.RLock()
		isValidReverse = authMsg.Reverse && s.tokens[token] != 0
		_, hasForwardToken := s.forwardTokens[token]
		isValidForward = !authMsg.Reverse && hasForwardToken
		tmpToken, isConnectorToken := s.connectorTokens[token]
		reverseToken = tmpToken
		isValidConnector = isConnectorToken && !authMsg.Reverse && s.tokens[reverseToken] != 0
		s.mu.RUnlock()

		if !isValidReverse && !isValidForward && !isValidConnector {
			authResponse := AuthResponseMessage{Success: false, Error: "invalid token"}
			s.relay.logMessage(authResponse, "send", wsConn.Label())
			wsConn.WriteMessage(authResponse)
			return
		}
	}

	clientID = uuid.New()
	wsConn.setLabel(clientID.String())

	s.mu.Lock()
	// For reverse tokens with AllowManageConnector, generate a unique internal token
	if isValidReverse {
		opts, exists := s.tokenOptions[token]
		if exists && opts.AllowManageConnector {
			if isUrlAuth {
				internalToken = uuid.New().String()
			} else {
				internalToken = authMsg.Instance.String()
			}
			s.tokenIndexes[internalToken] = 0
			s.tokenOptions[internalToken] = opts
			s.tokens[internalToken] = -1
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

	if isValidReverse {
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
		s.log.Info().Str("client_id", clientID.String()).Str("client_ip", wsConn.GetClientIP()).Msg("Reverse client authenticated")
		// Notify connectors about new reverse client
		s.broadcastPartnersToConnectors()
	} else if isValidConnector {
		// Handle connector proxy client
		s.log.Info().Str("client_id", clientID.String()).Str("client_ip", wsConn.GetClientIP()).Msg("Connector client authenticated")
		// Notify reverse clients about new connector
		s.broadcastPartnersToReverseClients(reverseToken)
	} else {
		// Handle forward proxy client
		s.log.Info().Str("client_id", clientID.String()).Str("client_ip", wsConn.GetClientIP()).Msg("Forward client authenticated")
	}

	authResponse := AuthResponseMessage{Success: true}
	s.relay.logMessage(authResponse, "send", wsConn.Label())
	if err := wsConn.WriteMessage(authResponse); err != nil {
		s.log.Debug().Err(err).Msg("Failed to send auth response")
		return
	}

	// Send initial partner status for connector clients
	if isValidConnector {
		// Count total reverse clients for this token
		reverseCount := 0
		s.mu.RLock()
		for token := range s.tokens {
			if clients, ok := s.tokenClients[token]; ok {
				reverseCount += len(clients)
			}
		}
		s.mu.RUnlock()

		// Send partners message
		partnersMsg := PartnersMessage{
			Count: reverseCount,
		}
		s.relay.logMessage(partnersMsg, "send", wsConn.Label())
		if err := wsConn.WriteMessage(partnersMsg); err != nil {
			s.log.Debug().Err(err).Msg("Failed to send initial partners status")
		}
	}

	// Start message handling goroutines
	errChan := make(chan error, 2)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start message dispatcher
	if isValidConnector {
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
func (s *LinkSocksServer) messageDispatcher(ctx context.Context, ws *WSConn, clientID uuid.UUID) error {
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

			// Handle message sequentially (not in a goroutine)
			switch m := msg.(type) {
			case DataMessage:
				// Use non-blocking send for data messages
				if queue, ok := s.relay.messageQueues.Load(m.ChannelID); ok {
					select {
					case queue.(chan BaseMessage) <- m:
						s.log.Trace().Str("channel_id", m.ChannelID.String()).Msg("Message forwarded to channel")
					default:
						s.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Message queue full, dropping message")
					}
					continue
				}

				// Forward to connector if exists
				s.connCache.mu.RLock()
				targetWS, exists := s.connCache.channelIDToConnector[m.ChannelID]
				s.connCache.mu.RUnlock()

				if exists {
					s.relay.logMessage(m, "send", ws.Label())
					if err := targetWS.WriteMessage(m); err != nil {
						s.log.Debug().Err(err).Msg("Failed to forward data message to connector client")
					}
				} else {
					s.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Received data for unknown channel, dropping message")
				}

			case ConnectMessage:
				var isForwardClient bool
				s.mu.RLock()
				_, isForwardClient = s.clients[clientID]
				s.mu.RUnlock()

				if isForwardClient {
					// Create buffered channel with larger capacity SYNCHRONOUSLY
					// This prevents race condition where DataMessage arrives before queue creation
					msgChan := make(chan BaseMessage, 1000)
					s.relay.messageQueues.Store(m.ChannelID, msgChan)
				}

				go func(m ConnectMessage) {
					if isForwardClient {
						go func() {
							if err := s.relay.HandleNetworkConnection(ctx, ws, m); err != nil && !errors.Is(err, context.Canceled) {
								s.log.Debug().Err(err).Msg("Error handling network connection")
							}
						}()
					}
				}(m)

			case ConnectResponseMessage:
				go func(m ConnectResponseMessage) {
					if queue, ok := s.relay.messageQueues.Load(m.ChannelID); ok {
						if s.relay.option.FastOpen {
							if m.Success {
								s.relay.SetConnectionSuccess(m.ChannelID)
							} else {
								s.disconnectChannel(m.ChannelID, ws, m)
							}
							return
						}

						select {
						case queue.(chan BaseMessage) <- m:
							s.log.Trace().Str("channel_id", m.ChannelID.String()).Msg("Delivered connect response to queue")
						case <-time.After(2 * time.Second):
							s.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Timeout delivering connect response")
						}
					} else {
						// Forward to connector
						s.connCache.mu.RLock()
						if connectorWS, exists := s.connCache.channelIDToConnector[m.ChannelID]; exists {
							s.relay.logMessage(m, "send", ws.Label())
							if err := connectorWS.WriteMessage(m); err != nil {
								s.log.Debug().Err(err).Msg("Failed to forward connect response")
							}
							s.log.Trace().Str("channel_id", m.ChannelID.String()).Msg("Forwarded connect response to connector")
						} else {
							s.log.Debug().Str("channel_id", m.ChannelID.String()).Msg("No queue and no connector for connect response")
						}
						s.connCache.mu.RUnlock()
					}
				}(m)

			case DisconnectMessage:
				go s.disconnectChannel(m.ChannelID, ws, m)

			case ConnectorMessage:
				go s.handleConnectorMessage(m, ws, clientID)
			}
		}
	}
}

// New helper method to handle connector messages
func (s *LinkSocksServer) handleConnectorMessage(m ConnectorMessage, ws *WSConn, clientID uuid.UUID) {
	// Check permissions
	s.mu.RLock()
	var token string
	var hasPermission bool
	for t, clients := range s.tokenClients {
		for _, client := range clients {
			if client.ID == clientID {
				token = t
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
		ChannelID: m.ChannelID,
	}

	if !hasPermission {
		response.Success = false
		response.Error = "Unauthorized connector management attempt"
		s.log.Warn().Str("client_id", clientID.String()).Msg("Unauthorized connector management attempt")
	} else {
		switch m.Operation {
		case "add":
			newToken, err := s.AddConnectorToken(m.ConnectorToken, token)
			if err != nil {
				response.Success = false
				response.Error = err.Error()
			} else {
				response.Success = true
				response.ConnectorToken = newToken
			}
		case "remove":
			if removed := s.RemoveToken(m.ConnectorToken); !removed {
				response.Success = false
				response.Error = "Failed to remove connector token"
			} else {
				response.Success = true
			}
		default:
			response.Success = false
			response.Error = fmt.Sprintf("Unknown connector operation: %s", m.Operation)
		}
	}

	// Send response asynchronously
	s.relay.logMessage(response, "send", ws.Label())
	if err := ws.WriteMessage(response); err != nil {
		s.log.Warn().Err(err).Msg("Failed to send connector response")
	}
}

// connectorMessageDispatcher handles WebSocket message distribution for connector tokens
func (s *LinkSocksServer) connectorMessageDispatcher(ctx context.Context, ws *WSConn, reverseToken string) error {
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
				go func(m ConnectMessage) {
					reverseWS, err := s.getNextWebSocket(reverseToken)
					if err != nil {
						s.log.Debug().Err(err).Msg("Refusing connector connect")
						// Send failure response back to connector
						response := ConnectResponseMessage{
							ChannelID: m.ChannelID,
							Success:   false,
							Error:     "no available reverse clients",
						}
						s.relay.logMessage(response, "send", ws.Label())
						if err := ws.WriteMessage(response); err != nil {
							s.log.Debug().Err(err).Msg("Failed to send connect failure response")
						}
						return
					}

					// Store channel_id mapping for connector
					s.connCache.mu.Lock()
					s.connCache.channelIDToConnector[m.ChannelID] = ws
					s.connCache.channelIDToClient[m.ChannelID] = reverseWS
					if ids, exists := s.connCache.tokenCache[reverseToken]; exists {
						s.connCache.tokenCache[reverseToken] = append(ids, m.ChannelID)
					} else {
						s.connCache.tokenCache[reverseToken] = []uuid.UUID{m.ChannelID}
					}
					s.connCache.mu.Unlock()

					s.relay.logMessage(m, "send", ws.Label())
					if err := reverseWS.WriteMessage(m); err != nil {
						s.log.Debug().Err(err).Msg("Failed to forward connect message")
					}
				}(m)

			case DataMessage:
				// Route data message based on channel_id
				s.connCache.mu.RLock()
				targetWS, exists := s.connCache.channelIDToClient[m.ChannelID]
				if exists {
					s.relay.logMessage(m, "send", ws.Label())
					if err := targetWS.WriteMessage(m); err != nil {
						s.log.Debug().Err(err).Msg("Failed to forward data message")
					}
				} else {
					s.log.Debug().Str("channel_id", m.ChannelID.String()).Msg("Received data for unknown channel")
				}
				s.connCache.mu.RUnlock()

			case DisconnectMessage:
				go func(m DisconnectMessage) {
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
				}(m)
			}
		}
	}
}

// disconnectChannel handles forwarding disconnect message and cleanup of channel resources
func (s *LinkSocksServer) disconnectChannel(channelID uuid.UUID, ws *WSConn, msg BaseMessage) {
	// Forward disconnect message to connector if exists
	s.connCache.mu.Lock()
	if targetWS, exists := s.connCache.channelIDToConnector[channelID]; exists {
		s.relay.logMessage(msg, "send", ws.Label())
		if err := targetWS.WriteMessage(msg); err != nil {
			s.log.Debug().Err(err).Msg("Failed to forward disconnect message")
		}
	}
	delete(s.connCache.channelIDToClient, channelID)
	delete(s.connCache.channelIDToConnector, channelID)
	s.connCache.mu.Unlock()

	s.relay.disconnectChannel(channelID)
}

// cleanupConnection cleans up resources when a client disconnects
func (s *LinkSocksServer) cleanupConnection(clientID uuid.UUID, token string) {
	if clientID == uuid.Nil {
		return
	}

	var shouldBroadcast bool
	var clientIP string

	s.mu.Lock()
	// Get client IP from the connection
	if ws, exists := s.clients[clientID]; exists {
		clientIP = ws.GetClientIP()
	}

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
			// Check if this was a reverse client - we'll broadcast after releasing the lock
			if _, isReverse := s.tokens[token]; isReverse {
				shouldBroadcast = true
			}
		} else {
			s.tokenClients[token] = clients
		}
	}

	// Clean up client connection
	delete(s.clients, clientID)
	s.mu.Unlock()

	// Broadcast outside the lock to avoid deadlock
	if shouldBroadcast {
		s.broadcastPartnersToConnectors()
	}

	s.log.Info().Str("client_id", clientID.String()).Str("client_ip", clientIP).Msg("Client disconnected")
}

// broadcastPartnersToConnectors sends the current number of reverse clients to all connectors
func (s *LinkSocksServer) broadcastPartnersToConnectors() {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Count total reverse clients
	reverseCount := 0
	for token := range s.tokens {
		if clients, ok := s.tokenClients[token]; ok {
			reverseCount += len(clients)
		}
	}

	// Create partners message
	partnersMsg := PartnersMessage{
		Count: reverseCount,
	}

	// Send to all connector clients
	for connectorToken := range s.connectorTokens {
		if clients, ok := s.tokenClients[connectorToken]; ok {
			for _, client := range clients {
				s.relay.logMessage(partnersMsg, "send", client.Conn.Label())
				if err := client.Conn.WriteMessage(partnersMsg); err != nil {
					s.log.Debug().Err(err).Msg("Failed to send partners update to connector")
				}
			}
		}
	}
}

// broadcastPartnersToReverseClients sends the current number of connectors to all reverse clients for a given token
func (s *LinkSocksServer) broadcastPartnersToReverseClients(reverseToken string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Count total connectors for this reverse token
	connectorCount := 0
	for connectorToken, rt := range s.connectorTokens {
		if rt == reverseToken {
			if clients, ok := s.tokenClients[connectorToken]; ok {
				connectorCount += len(clients)
			}
		}
	}

	// Create partners message
	partnersMsg := PartnersMessage{
		Count: connectorCount,
	}

	// Send to all reverse clients
	if clients, ok := s.tokenClients[reverseToken]; ok {
		for _, client := range clients {
			s.relay.logMessage(partnersMsg, "send", client.Conn.Label())
			if err := client.Conn.WriteMessage(partnersMsg); err != nil {
				s.log.Debug().Err(err).Msg("Failed to send partners update to reverse client")
			}
		}
	}
}

// getNextWebSocket gets next available WebSocket connection using round-robin
func (s *LinkSocksServer) getNextWebSocket(token string) (*WSConn, error) {
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

// waitForClients waits for clients to be available for the given token
func (s *LinkSocksServer) waitForClients(token string, addr net.Addr) error {
	s.mu.RLock()
	_, hasClients := s.tokenClients[token]
	s.mu.RUnlock()

	// If clients are already available, return immediately
	if hasClients {
		return nil
	}

	// Wait up to 10 seconds for clients to connect if needed
	timeout := time.After(10 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			s.log.Debug().Str("addr", addr.String()).Msg("No valid clients after timeout")
			return fmt.Errorf("no valid clients after timeout")
		case <-ticker.C:
			s.mu.RLock()
			clients, ok := s.tokenClients[token]
			hasValidClients := ok && len(clients) > 0
			s.mu.RUnlock()
			if hasValidClients {
				return nil
			}
		}
	}
}

// handleSocksRequest handles incoming SOCKS5 connection
func (s *LinkSocksServer) handleSocksRequest(ctx context.Context, socksConn net.Conn, addr net.Addr, token string) error {
	// Wait for clients to be available
	if err := s.waitForClients(token, addr); err != nil {
		return s.relay.RefuseSocksRequest(socksConn, 3)
	}
	// Get WebSocket connection using round-robin with basic liveness check (ping)
	var ws *WSConn
	var err error
	// Determine number of attempts based on current clients
	s.mu.RLock()
	maxAttempts := len(s.tokenClients[token])
	s.mu.RUnlock()
	if maxAttempts == 0 {
		s.log.Warn().Int("port", s.tokens[token]).Msg("No available client for SOCKS5 port")
		return s.relay.RefuseSocksRequest(socksConn, 3)
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		ws, err = s.getNextWebSocket(token)
		if err != nil {
			s.log.Warn().Int("port", s.tokens[token]).Msg("No available client for SOCKS5 port")
			return s.relay.RefuseSocksRequest(socksConn, 3)
		}
		// Quick liveness probe to avoid choosing a dead socket
		if pingErr := ws.SyncWriteControl(websocket.PingMessage, nil, time.Now().Add(1*time.Second)); pingErr != nil {
			s.log.Debug().Str("ws_label", ws.Label()).Msg("WS ping failed, trying next client")
			continue
		}
		s.log.Trace().Str("ws_label", ws.Label()).Msg("Selected reverse client for SOCKS request")
		break
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
	return s.relay.HandleSocksRequest(ctx, ws, socksConn, username, password)
}

// runSocksServer runs a SOCKS5 server for a specific token and port
func (s *LinkSocksServer) runSocksServer(ctx context.Context, token string, socksPort int) error {
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
				if errors.Is(err, io.EOF) {
					s.log.Debug().Err(err).Msg("Error handling SOCKS request")
				} else {
					s.log.Warn().Err(err).Msg("Error handling SOCKS request")
				}
			}
		}()
	}
}

// Close gracefully shuts down the LinkSocksServer
func (s *LinkSocksServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if already closed
	if s.closed {
		return
	}
	s.closed = true

	// Close relay if it exists
	if s.relay != nil {
		s.relay.Close()
	}

	// Clean up all waiting sockets
	if s.waitingSockets != nil {
		s.waitingMu.Lock()
		for port, waiting := range s.waitingSockets {
			if waiting != nil {
				if waiting.cancelTimer != nil {
					waiting.cancelTimer.Stop()
				}
				if waiting.listener != nil {
					waiting.listener.Close()
				}
			}
			delete(s.waitingSockets, port)
		}
		s.waitingMu.Unlock()
	}

	// Clean up all SOCKS servers
	if s.socksTasks != nil {
		for port, cancel := range s.socksTasks {
			if cancel != nil {
				cancel()
			}
			delete(s.socksTasks, port)
		}
	}

	// Clean up all client connections
	if s.clients != nil {
		for clientID, ws := range s.clients {
			if ws != nil {
				ws.Close()
			}
			delete(s.clients, clientID)
		}
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

	// Close socket manager if it exists
	if s.socketManager != nil {
		s.socketManager.Close()
	}

	s.log.Info().Msg("Server stopped")
}

// GetClientCount returns the total number of connected clients
func (s *LinkSocksServer) GetClientCount() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients)
}

// HasClients returns true if there are any connected clients
func (s *LinkSocksServer) HasClients() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients) > 0
}

// GetTokenClientCount counts clients connected for a given token
func (s *LinkSocksServer) GetTokenClientCount(token string) int {
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
