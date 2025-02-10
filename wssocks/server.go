package wssocks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
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

// Version represents the server version
const Version = "1.0.0" // Update with actual version

// WSSocksServer represents a SOCKS5 over WebSocket protocol server
type WSSocksServer struct {
	relay *Relay
	log   zerolog.Logger

	wsHost   string
	wsPort   int
	wsServer *http.Server

	socksHost string
	portPool  *PortPool

	// Synchronization
	ready chan struct{}
	mu    sync.RWMutex

	// Client management
	clients       map[uuid.UUID]*WSConn
	forwardTokens map[string]struct{}

	// Token management
	tokens       map[string]int
	tokenLocks   map[string]*sync.Mutex
	tokenClients map[string][]clientInfo
	tokenIndexes map[string]int
	socksAuth    map[string]authInfo

	// SOCKS server management
	socksTasks map[int]context.CancelFunc

	socksWaitClient bool
	cancelFunc      context.CancelFunc
}

type clientInfo struct {
	ID   uuid.UUID
	Conn *WSConn
}

type authInfo struct {
	Username string
	Password string
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
		BufferSize:      BufferSize,
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

// NewWSSocksServer creates a new WSSocksServer instance
func NewWSSocksServer(opt *ServerOption) *WSSocksServer {
	if opt == nil {
		opt = DefaultServerOption()
	}

	s := &WSSocksServer{
		relay:           NewRelay(opt.Logger, opt.BufferSize),
		log:             opt.Logger,
		wsHost:          opt.WSHost,
		wsPort:          opt.WSPort,
		socksHost:       opt.SocksHost,
		portPool:        opt.PortPool,
		ready:           make(chan struct{}),
		clients:         make(map[uuid.UUID]*WSConn),
		forwardTokens:   make(map[string]struct{}),
		tokens:          make(map[string]int),
		tokenLocks:      make(map[string]*sync.Mutex),
		tokenClients:    make(map[string][]clientInfo),
		tokenIndexes:    make(map[string]int),
		socksAuth:       make(map[string]authInfo),
		socksTasks:      make(map[int]context.CancelFunc),
		socksWaitClient: opt.SocksWaitClient,
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
	Token    string
	Port     int
	Username string
	Password string
}

// DefaultReverseTokenOptions returns default options for reverse token
func DefaultReverseTokenOptions() *ReverseTokenOptions {
	return &ReverseTokenOptions{
		Token: "", // Will be auto-generated
		Port:  0,  // Will be assigned from pool
	}
}

// AddReverseToken adds a new token for reverse socks and assigns a port
func (s *WSSocksServer) AddReverseToken(opts *ReverseTokenOptions) (string, int) {
	if opts == nil {
		opts = DefaultReverseTokenOptions()
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Generate random token if not provided
	token := opts.Token
	if token == "" {
		token = generateRandomToken(16)
	}

	// Check if token already exists
	if existingPort, exists := s.tokens[token]; exists {
		return token, existingPort
	}

	// Get port from pool
	assignedPort := s.portPool.Get(opts.Port)
	if assignedPort == 0 {
		return "", 0
	}

	// Store token information
	s.tokens[token] = assignedPort
	s.tokenLocks[token] = &sync.Mutex{}
	if opts.Username != "" && opts.Password != "" {
		s.socksAuth[token] = authInfo{
			Username: opts.Username,
			Password: opts.Password,
		}
	}

	// Start SOCKS server immediately if we're not waiting for clients
	if s.wsServer != nil {
		ctx, cancel := context.WithCancel(context.Background())
		s.socksTasks[assignedPort] = cancel
		go func() {
			if err := s.runSocksServer(ctx, token, assignedPort); err != nil {
				s.log.Error().Err(err).Int("port", assignedPort).Msg("SOCKS server error")
			}
		}()
	}

	s.log.Info().Int("port", assignedPort).Msg("New reverse proxy token added")
	return token, assignedPort
}

// AddForwardToken adds a new token for forward socks proxy
func (s *WSSocksServer) AddForwardToken(token string) string {
	s.mu.Lock()
	defer s.mu.Unlock()

	if token == "" {
		token = generateRandomToken(16)
	}

	s.forwardTokens[token] = struct{}{}
	s.log.Info().Msg("New forward proxy token added")
	return token
}

// RemoveToken removes a token and disconnects all its clients
func (s *WSSocksServer) RemoveToken(token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Check if token exists
	if _, isReverse := s.tokens[token]; !isReverse {
		if _, isForward := s.forwardTokens[token]; !isForward {
			return false
		}
	}

	// Handle reverse proxy token
	if port, exists := s.tokens[token]; exists {
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
		delete(s.tokenLocks, token)
		delete(s.tokenIndexes, token)
		delete(s.socksAuth, token)

		// Cancel and clean up SOCKS server if it exists
		if cancel, exists := s.socksTasks[port]; exists {
			cancel()
			delete(s.socksTasks, port)
		}

		// Return port to pool
		s.portPool.Put(port)

		s.log.Info().Str("token", token).Msg("Reverse token removed")
	}

	// Handle forward proxy token
	if _, exists := s.forwardTokens[token]; exists {
		// Close all forward client connections using this token
		for clientID, ws := range s.clients {
			ws.Close()
			delete(s.clients, clientID)
		}
		delete(s.forwardTokens, token)
		s.log.Info().Str("token", token).Msg("Forward token removed")
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
				s.log.Error().Err(err).Int("port", socksPort).Msg("SOCKS server error")
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
	mux.HandleFunc("/socket", func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			s.log.Error().Err(err).Msg("Failed to upgrade connection")
			return
		}
		go s.handleWebSocket(ctx, conn)
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprintf(w, "WSSocks %s is running but API is not enabled.\n", Version)
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

	s.log.Info().Str("addr", s.wsServer.Addr).Msg("WSSocks Server started")
	close(s.ready)

	return s.wsServer.ListenAndServe()
}

// WaitReady waits for the server to be ready with optional timeout
func (s *WSSocksServer) WaitReady(timeout time.Duration) error {
	ctx, cancel := context.WithCancel(context.Background())

	s.mu.Lock()
	s.cancelFunc = cancel
	s.mu.Unlock()

	task := make(chan error, 1)
	go func() {
		task <- s.Serve(ctx)
	}()

	if timeout > 0 {
		select {
		case <-s.ready:
			return nil
		case err := <-task:
			return err
		case <-time.After(timeout):
			return fmt.Errorf("timeout waiting for server to be ready")
		}
	}
	<-s.ready
	return nil
}

// handleWebSocket handles WebSocket connection
func (s *WSSocksServer) handleWebSocket(ctx context.Context, ws *websocket.Conn) {
	// Wrap the websocket connection
	wsConn := NewWSConn(ws)

	var clientID uuid.UUID
	var token string

	defer func() {
		wsConn.Close()
		if clientID != uuid.Nil {
			s.cleanupConnection(clientID, token)
		}
	}()

	// Handle authentication
	var authMsg AuthMessage
	if err := wsConn.ReadJSON(&authMsg); err != nil {
		s.log.Error().Err(err).Msg("Failed to read auth message")
		return
	}

	s.relay.logMessage(authMsg, "recv")
	if authMsg.Type != TypeAuth {
		authResponse := AuthResponseMessage{Type: TypeAuthResponse, Success: false}
		s.relay.logMessage(authResponse, "send")
		wsConn.SyncWriteJSON(authResponse)
		return
	}

	token = authMsg.Token
	s.mu.RLock()
	isValidReverse := authMsg.Reverse && s.tokens[token] > 0
	_, hasForwardToken := s.forwardTokens[token]
	isValidForward := !authMsg.Reverse && hasForwardToken
	s.mu.RUnlock()

	if !isValidReverse && !isValidForward {
		authResponse := AuthResponseMessage{Type: TypeAuthResponse, Success: false}
		s.relay.logMessage(authResponse, "send")
		wsConn.SyncWriteJSON(authResponse)
		return
	}

	clientID = uuid.New()

	if authMsg.Reverse {
		// Handle reverse proxy client
		lock := s.tokenLocks[token]
		lock.Lock()
		if _, exists := s.tokenClients[token]; !exists {
			s.tokenClients[token] = make([]clientInfo, 0)
		}
		s.tokenClients[token] = append(s.tokenClients[token], clientInfo{ID: clientID, Conn: wsConn})
		s.clients[clientID] = wsConn

		// Start SOCKS server if not already running
		socksPort := s.tokens[token]
		if _, exists := s.socksTasks[socksPort]; !exists {
			ctx, cancel := context.WithCancel(ctx)
			s.socksTasks[socksPort] = cancel
			go func() {
				if err := s.runSocksServer(ctx, token, socksPort); err != nil {
					s.log.Error().Err(err).Int("port", socksPort).Msg("SOCKS server error")
				}
			}()
		}
		lock.Unlock()

		s.log.Info().Str("client_id", clientID.String()).Msg("Reverse client authenticated")
	} else {
		// Handle forward proxy client
		s.mu.Lock()
		s.clients[clientID] = wsConn
		s.mu.Unlock()

		s.log.Info().Str("client_id", clientID.String()).Msg("Forward client authenticated")
	}

	// Send auth response
	authResponse := AuthResponseMessage{Type: TypeAuthResponse, Success: true}
	s.relay.logMessage(authResponse, "send")
	if err := wsConn.SyncWriteJSON(authResponse); err != nil {
		s.log.Error().Err(err).Msg("Failed to send auth response")
		return
	}

	// Start message handling goroutines
	errChan := make(chan error, 2)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Start message dispatcher
	go func() {
		errChan <- s.messageDispatcher(ctx, wsConn, clientID)
	}()

	// Start heartbeat
	go func() {
		errChan <- s.wsHeartbeat(ctx, wsConn, clientID)
	}()

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
			var rawMsg json.RawMessage
			err := ws.ReadJSON(&rawMsg)
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					s.log.Error().Err(err).Msg("WebSocket read error")
				}
				return err
			}

			msg, err := ParseMessage(rawMsg)
			if err != nil {
				s.log.Error().Err(err).Msg("Failed to parse message")
				continue
			}

			s.relay.logMessage(msg, "recv")

			switch m := msg.(type) {
			case *DataMessage:
				channelID := m.ChannelID
				if queue, ok := s.relay.messageQueues.Load(channelID); ok {
					select {
					case queue.(chan DataMessage) <- *m:
						s.log.Debug().Str("channel_id", channelID).Msg("Message forwarded to channel")
					default:
						s.log.Warn().Str("channel_id", channelID).Msg("Message queue full")
					}
				} else {
					s.log.Warn().Str("channel_id", channelID).Msg("Received data for unknown channel")
				}

			case *ConnectMessage:
				s.mu.RLock()
				_, isForwardClient := s.clients[clientID]
				s.mu.RUnlock()

				if isForwardClient {
					go func() {
						if err := s.relay.HandleNetworkConnection(ctx, ws, *m); err != nil && !errors.Is(err, context.Canceled) {
							s.log.Error().Err(err).Msg("Network connection handler error")
						}
					}()
				}

			case *ConnectResponseMessage:
				if queue, ok := s.relay.messageQueues.Load(m.ConnectID); ok {
					queue.(chan ConnectResponseMessage) <- *m
				} else {
					s.log.Debug().Str("connect_id", m.ConnectID).Msg("Received connect response for unknown channel")
				}

			default:
				s.log.Warn().Str("type", msg.GetType()).Msg("Received unknown message type")
			}
		}
	}
}

// wsHeartbeat maintains WebSocket connection with periodic pings
func (s *WSSocksServer) wsHeartbeat(ctx context.Context, ws *WSConn, clientID uuid.UUID) error {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			if err := ws.SyncWriteControl(websocket.PingMessage, []byte{}, time.Now().Add(10*time.Second)); err != nil {
				s.log.Info().Str("client_id", clientID.String()).Msg("Heartbeat detected disconnection")
				return err
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

	// Clean up client connection and its write mutex
	delete(s.clients, clientID)

	s.log.Debug().Str("client_id", clientID.String()).Msg("Cleaned up resources for client")
}

// getNextWebSocket gets next available WebSocket connection using round-robin
func (s *WSSocksServer) getNextWebSocket(token string) (*WSConn, error) {
	lock := s.tokenLocks[token]
	lock.Lock()
	defer lock.Unlock()

	if _, exists := s.tokenClients[token]; !exists || len(s.tokenClients[token]) == 0 {
		return nil, fmt.Errorf("no available clients for token")
	}

	clients := s.tokenClients[token]
	currentIndex := s.tokenIndexes[token]
	s.tokenIndexes[token] = (currentIndex + 1) % len(clients)

	s.log.Debug().Int("index", currentIndex).Msg("Using client index for request")

	if currentIndex < len(clients) {
		return clients[currentIndex].Conn, nil
	}
	return clients[0].Conn, nil
}

// handleSocksRequest handles incoming SOCKS5 connection
func (s *WSSocksServer) handleSocksRequest(ctx context.Context, socksConn net.Conn, addr net.Addr, token string) error {
	defer socksConn.Close()

	// Wait up to 10 seconds for clients to connect if needed
	if _, exists := s.tokenClients[token]; !exists {
		timeout := time.After(10 * time.Second)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-timeout:
				s.log.Debug().Str("addr", addr.String()).Msg("No valid clients after timeout")
				return s.relay.RefuseSocksRequest(socksConn, 3)
			case <-ticker.C:
				if clients, ok := s.tokenClients[token]; ok && len(clients) > 0 {
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
	if auth, ok := s.socksAuth[token]; ok {
		username = auth.Username
		password = auth.Password
	}

	// Handle SOCKS request using relay
	if err := s.relay.HandleSocksRequest(ctx, ws, socksConn, username, password); err != nil && !errors.Is(err, context.Canceled) {
		s.log.Error().Err(err).Msg("Error handling SOCKS request")
	}
	return nil
}

// runSocksServer runs a SOCKS5 server for a specific token and port
func (s *WSSocksServer) runSocksServer(ctx context.Context, token string, socksPort int) error {
	listener, err := net.Listen("tcp", fmt.Sprintf("%s:%d", s.socksHost, socksPort))
	if err != nil {
		return fmt.Errorf("failed to start SOCKS server: %w", err)
	}
	defer listener.Close()

	s.log.Info().Str("addr", listener.Addr().String()).Msg("SOCKS5 server started")

	go func() {
		<-ctx.Done()
		listener.Close()
	}()

	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil // Context cancelled
			}
			s.log.Error().Err(err).Msg("Failed to accept SOCKS connection")
			continue
		}

		go func() {
			if err := s.handleSocksRequest(ctx, conn, conn.RemoteAddr(), token); err != nil && !errors.Is(err, context.Canceled) {
				s.log.Error().Err(err).Msg("Error handling SOCKS request")
			}
		}()
	}
}

// Close gracefully shuts down the WSSocksServer
func (s *WSSocksServer) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()

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
			s.log.Error().Err(err).Msg("Error closing WebSocket server")
		}
		s.wsServer = nil
	}

	// Cancel main worker if it exists
	if s.cancelFunc != nil {
		s.cancelFunc()
		s.cancelFunc = nil
	}

	s.log.Debug().Msg("Server stopped")
}

// HasClients checks if there are any clients connected
func (s *WSSocksServer) HasClients() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.clients) > 0
}

// HasTokenClients checks if there are any clients connected for a given token
func (s *WSSocksServer) HasTokenClients(token string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Check reverse proxy clients
	if clients, exists := s.tokenClients[token]; exists && len(clients) > 0 {
		return true
	}

	// Check forward proxy clients
	if _, exists := s.forwardTokens[token]; exists {
		return len(s.clients) > 0
	}

	return false
}
