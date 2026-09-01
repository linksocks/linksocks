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
	"strings"
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
	connectorWait   time.Duration

	// Client connections
	clients map[uuid.UUID]*WSConn // Maps client ID to WebSocket connection

	// Token management
	forwardTokens     map[string]struct{}             // Set of valid forward proxy tokens
	tokens            map[string]int                  // Maps reverse proxy tokens to ports
	tokenClients      map[string][]clientInfo         // Maps tokens to their connected clients
	tokenIndexes      map[string]int                  // Round-robin indexes for load balancing
	tokenOptions      map[string]*ReverseTokenOptions // options per token
	tokenAvailability map[string]chan struct{}        // Per-token provider availability notification channels
	tokenWaiters      map[string]int                  // Connector requests currently waiting for a provider
	connectorTokens   map[string]string               // Maps connector tokens to their reverse tokens
	internalTokens    map[string][]string             // Maps original token to list of internal tokens
	sha256TokenMap    map[string]string               // Maps SHA256 tokens to original tokens

	// Per-token access control (checked on the server before dialing/forwarding)
	forwardTokenAC   map[string]*AccessControl // Forward token -> destination rules
	connectorTokenAC map[string]*AccessControl // Connector token -> destination rules

	// Connector management
	connCache *connectorCache

	// Active SOCKS servers
	socksTasks map[int]context.CancelFunc // Active SOCKS server tasks

	// Socket reuse management
	waitingSockets map[int]*waitingSocket // Sockets waiting for reuse
	waitingMu      sync.RWMutex           // Mutex for waiting sockets management
	socketManager  *SocketManager

	// API server
	apiKey  string
	apiKeys map[string]struct{} // Additional API keys; authentication only

	// Error channel
	errors chan error // Channel for errors

	// Direct signaling (experimental; WS-only, no UDP dependencies)
	directEnable bool
	clientMeta   map[uuid.UUID]*directClientMeta

	// Optional server-side UDP rendezvous (minimal STUN subset). Disabled by default.
	directRendezvousEnable bool
	directRendezvousHost   string
	directRendezvousPort   int
	directRendezvousConn   *net.UDPConn
	directRendezvousCancel context.CancelFunc

	// Start/ready guards (important for language bindings calling WaitReady repeatedly)
	startOnce sync.Once
	readyOnce sync.Once
}

type directClientRole string

const (
	directClientRoleForward   directClientRole = "forward"
	directClientRoleReverse   directClientRole = "reverse"
	directClientRoleConnector directClientRole = "connector"
)

type directClientMeta struct {
	InternalToken string
	ReverseToken  string // For connector clients only
	Role          directClientRole

	SupportsDirect bool

	LastCapabilities *DirectCapabilitiesMessage
	LastRendezvous   *DirectRendezvousMessage
	LastStatus       *DirectStatusMessage
	UpdatedAt        time.Time
}

type clientInfo struct {
	ID   uuid.UUID
	Conn *WSConn
}

type waitingSocket struct {
	listener    net.Listener
	cancelTimer *time.Timer
}

// connectorPendingQueueSize bounds the per-channel buffer for data that
// arrives before the channel is bound (fast-open pipelining, out-of-band
// signaling, etc.).
const connectorPendingQueueSize = 1000

// connectorPendingTTL bounds how long data for an unbound channel is
// retained before it is dropped.
const connectorPendingTTL = 30 * time.Second

type pendingConnectorData struct {
	queue chan BaseMessage
	timer *time.Timer
}

type connectorCache struct {
	channelIDToClient    map[uuid.UUID]*WSConn               // Maps channel_id to reverse client WebSocket connection
	channelIDToConnector map[uuid.UUID]*WSConn               // Maps channel_id to connector WebSocket connection
	pendingQueues        map[uuid.UUID]*pendingConnectorData // Data for channels that are not bound yet
	tokenCache           map[string][]uuid.UUID              // Maps token to list of channel_ids
	mu                   sync.RWMutex
}

// newConnectorCache creates a new connector cache
func newConnectorCache() *connectorCache {
	return &connectorCache{
		channelIDToClient:    make(map[uuid.UUID]*WSConn),
		channelIDToConnector: make(map[uuid.UUID]*WSConn),
		pendingQueues:        make(map[uuid.UUID]*pendingConnectorData),
		tokenCache:           make(map[string][]uuid.UUID),
	}
}

// ServerOption represents configuration options for LinkSocksServer
type ServerOption struct {
	WSHost          string
	WSPort          int
	SocksHost       string
	PortPool        *PortPool
	SocksWaitClient bool
	ConnectorWait   time.Duration
	Logger          zerolog.Logger
	BufferSize      int
	APIKey          string
	// APIKeys registers additional API keys accepted for API authentication.
	// Keys are credentials only and never carry access rules; rules are bound
	// to tokens (per-token) and to the server (entry/dial access control).
	APIKeys           []string
	ChannelTimeout    time.Duration
	ConnectTimeout    time.Duration
	FastOpen          bool
	UpstreamProxy     string
	UpstreamUsername  string
	UpstreamPassword  string
	UpstreamProxyType ProxyType

	// Direct connection options (experimental; default disabled).
	DirectEnable bool

	// Optional server-side UDP rendezvous (minimal STUN subset).
	DirectRendezvousEnable bool
	DirectRendezvousHost   string
	DirectRendezvousPort   int

	// AccessControl restricts which destinations local proxies started by this
	// server may request at the entry. Nil means no restriction.
	AccessControl *AccessControl

	// DialAccessControl restricts which destinations this server may actually
	// connect to when it performs the dial (forward proxy mode). Nil means no
	// restriction.
	DialAccessControl *AccessControl
}

// DefaultServerOption returns default server options
func DefaultServerOption() *ServerOption {
	return &ServerOption{
		WSHost:                 "0.0.0.0",
		WSPort:                 8765,
		SocksHost:              "127.0.0.1",
		PortPool:               NewPortPoolFromRange(1024, 10240),
		SocksWaitClient:        true,
		ConnectorWait:          5 * time.Second,
		Logger:                 zerolog.New(os.Stdout).With().Timestamp().Logger(),
		BufferSize:             DefaultBufferSize,
		APIKey:                 "",
		APIKeys:                nil,
		ChannelTimeout:         DefaultChannelTimeout,
		ConnectTimeout:         DefaultConnectTimeout,
		FastOpen:               false,
		UpstreamProxy:          "",
		UpstreamUsername:       "",
		UpstreamPassword:       "",
		DirectEnable:           false,
		DirectRendezvousEnable: false,
		DirectRendezvousHost:   "",
		DirectRendezvousPort:   0,
		AccessControl:          nil,
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

// WithConnectorWait sets how long connector requests wait for a reverse client.
func (o *ServerOption) WithConnectorWait(timeout time.Duration) *ServerOption {
	o.ConnectorWait = timeout
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

// WithAPIKeys registers additional API keys accepted for API authentication.
// Keys only authenticate: access rules are attached per token (token creation
// API) or at the server level (entry/dial access control), never to a key.
func (o *ServerOption) WithAPIKeys(keys ...string) *ServerOption {
	o.APIKeys = append([]string(nil), keys...)
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

// WithUpstreamProxy sets the upstream proxy address
func (o *ServerOption) WithUpstreamProxy(proxy string) *ServerOption {
	o.UpstreamProxy = proxy
	return o
}

// WithUpstreamProxyType sets the upstream proxy type (socks5 or http)
func (o *ServerOption) WithUpstreamProxyType(proxyType ProxyType) *ServerOption {
	o.UpstreamProxyType = proxyType
	return o
}

// WithUpstreamAuth sets the upstream proxy authentication
func (o *ServerOption) WithUpstreamAuth(username, password string) *ServerOption {
	o.UpstreamUsername = username
	o.UpstreamPassword = password
	return o
}

func (o *ServerOption) WithDirectEnable(enable bool) *ServerOption {
	o.DirectEnable = enable
	return o
}

func (o *ServerOption) WithDirectRendezvousUDP(enable bool) *ServerOption {
	o.DirectRendezvousEnable = enable
	return o
}

func (o *ServerOption) WithDirectRendezvousHost(host string) *ServerOption {
	o.DirectRendezvousHost = host
	return o
}

func (o *ServerOption) WithDirectRendezvousPort(port int) *ServerOption {
	o.DirectRendezvousPort = port
	return o
}

// WithEntryAccessControl sets the default destination restrictions at the
// local proxy entry for proxies started by this server.
func (o *ServerOption) WithEntryAccessControl(ac *AccessControl) *ServerOption {
	o.AccessControl = ac
	return o
}

// WithDialAccessControl sets the destination restrictions applied when this
// server performs the actual connection to the target.
func (o *ServerOption) WithDialAccessControl(ac *AccessControl) *ServerOption {
	o.DialAccessControl = ac
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
		WithUpstreamAuth(opt.UpstreamUsername, opt.UpstreamPassword).
		WithUpstreamProxyType(opt.UpstreamProxyType).
		WithEntryAccessControl(opt.AccessControl).
		WithDialAccessControl(opt.DialAccessControl)

	var apiKeys map[string]struct{}
	if len(opt.APIKeys) > 0 {
		apiKeys = make(map[string]struct{}, len(opt.APIKeys))
		for _, k := range opt.APIKeys {
			apiKeys[k] = struct{}{}
		}
	}

	s := &LinkSocksServer{
		relay:                  NewRelay(opt.Logger, relayOpt),
		log:                    opt.Logger,
		wsHost:                 opt.WSHost,
		wsPort:                 opt.WSPort,
		socksHost:              opt.SocksHost,
		portPool:               opt.PortPool,
		ready:                  make(chan struct{}),
		clients:                make(map[uuid.UUID]*WSConn),
		forwardTokens:          make(map[string]struct{}),
		tokens:                 make(map[string]int),
		tokenClients:           make(map[string][]clientInfo),
		tokenIndexes:           make(map[string]int),
		tokenAvailability:      make(map[string]chan struct{}),
		tokenWaiters:           make(map[string]int),
		connectorTokens:        make(map[string]string),
		connCache:              newConnectorCache(),
		tokenOptions:           make(map[string]*ReverseTokenOptions),
		forwardTokenAC:         make(map[string]*AccessControl),
		connectorTokenAC:       make(map[string]*AccessControl),
		socksTasks:             make(map[int]context.CancelFunc),
		socksWaitClient:        opt.SocksWaitClient,
		connectorWait:          opt.ConnectorWait,
		waitingSockets:         make(map[int]*waitingSocket),
		socketManager:          NewSocketManager(opt.SocksHost, opt.Logger),
		apiKey:                 opt.APIKey,
		apiKeys:                apiKeys,
		internalTokens:         make(map[string][]string),
		sha256TokenMap:         make(map[string]string),
		errors:                 make(chan error, 16),
		directEnable:           opt.DirectEnable,
		clientMeta:             make(map[uuid.UUID]*directClientMeta),
		directRendezvousEnable: opt.DirectRendezvousEnable,
		directRendezvousHost:   opt.DirectRendezvousHost,
		directRendezvousPort:   opt.DirectRendezvousPort,
	}

	return s
}

func (s *LinkSocksServer) clientHelloIndicatesDirectSupport(m LogMessage) bool {
	if !strings.HasPrefix(m.Msg, DirectClientHelloPrefix) {
		return false
	}
	// Minimal parsing: treat direct_signaling=1 as the marker.
	return strings.Contains(m.Msg, "direct_signaling=1")
}

func (s *LinkSocksServer) markClientSupportsDirect(clientID uuid.UUID) {
	s.mu.Lock()
	meta := s.clientMeta[clientID]
	if meta != nil {
		meta.SupportsDirect = true
		meta.UpdatedAt = time.Now()
	}
	s.mu.Unlock()
}

func (s *LinkSocksServer) sendKnownPeerDirectCapabilities(clientID uuid.UUID, ws *WSConn) {
	if !s.directEnable {
		return
	}

	var capsToSend []DirectCapabilitiesMessage

	s.mu.RLock()
	meta := s.clientMeta[clientID]
	if meta == nil {
		s.mu.RUnlock()
		return
	}

	switch meta.Role {
	case directClientRoleConnector:
		// Connector peers are reverse clients under meta.ReverseToken.
		if clients, ok := s.tokenClients[meta.ReverseToken]; ok {
			for _, ci := range clients {
				pm := s.clientMeta[ci.ID]
				if pm == nil || !pm.SupportsDirect || pm.LastCapabilities == nil {
					continue
				}
				capsToSend = append(capsToSend, *pm.LastCapabilities)
			}
		}

	case directClientRoleReverse:
		// Reverse peers are all connector clients mapped to this internal token.
		for connectorToken, rt := range s.connectorTokens {
			if rt != meta.InternalToken {
				continue
			}
			if clients, ok := s.tokenClients[connectorToken]; ok {
				for _, ci := range clients {
					pm := s.clientMeta[ci.ID]
					if pm == nil || !pm.SupportsDirect || pm.LastCapabilities == nil {
						continue
					}
					capsToSend = append(capsToSend, *pm.LastCapabilities)
				}
			}
		}
	}
	s.mu.RUnlock()

	for _, capMsg := range capsToSend {
		s.relay.logMessage(capMsg, "send", ws.Label())
		if err := ws.WriteMessage(capMsg); err != nil {
			s.log.Debug().Err(err).Msg("Failed to send cached direct capabilities")
		}
	}
}

func (s *LinkSocksServer) forwardDirectMessageFromClient(clientID uuid.UUID, msg BaseMessage) {
	if !s.directEnable {
		return
	}

	var targets []*WSConn

	s.mu.RLock()
	meta := s.clientMeta[clientID]
	if meta == nil {
		s.mu.RUnlock()
		return
	}

	switch meta.Role {
	case directClientRoleConnector:
		if clients, ok := s.tokenClients[meta.ReverseToken]; ok {
			for _, ci := range clients {
				pm := s.clientMeta[ci.ID]
				if pm == nil || !pm.SupportsDirect {
					continue
				}
				targets = append(targets, ci.Conn)
			}
		}

	case directClientRoleReverse:
		for connectorToken, rt := range s.connectorTokens {
			if rt != meta.InternalToken {
				continue
			}
			if clients, ok := s.tokenClients[connectorToken]; ok {
				for _, ci := range clients {
					pm := s.clientMeta[ci.ID]
					if pm == nil || !pm.SupportsDirect {
						continue
					}
					targets = append(targets, ci.Conn)
				}
			}
		}
	}
	s.mu.RUnlock()

	for _, t := range targets {
		s.relay.logMessage(msg, "send", t.Label())
		if err := t.WriteMessage(msg); err != nil {
			s.log.Debug().Err(err).Msg("Failed to forward direct signaling message")
		}
	}
}

func (s *LinkSocksServer) forwardChannelControl(channelID uuid.UUID, source *WSConn, msg BaseMessage) {
	s.connCache.mu.RLock()
	connector := s.connCache.channelIDToConnector[channelID]
	client := s.connCache.channelIDToClient[channelID]
	var target *WSConn
	if source == connector {
		target = client
	} else if source == client {
		target = connector
	} else if connector != nil {
		target = connector
	} else {
		target = client
	}
	s.connCache.mu.RUnlock()
	if target == nil || target == source {
		return
	}
	if err := target.WriteMessage(msg); err != nil {
		s.log.Debug().Err(err).Str("channel_id", channelID.String()).Msg("Failed to forward channel control message")
	}
}

func (s *LinkSocksServer) bindChannelPeer(channelID uuid.UUID, source *WSConn, role directClientRole, token string) {
	s.connCache.mu.Lock()
	switch role {
	case directClientRoleConnector:
		s.connCache.channelIDToConnector[channelID] = source
	case directClientRoleReverse:
		s.connCache.channelIDToClient[channelID] = source
	default:
		if token != "" {
			if ids := s.connCache.tokenCache[token]; len(ids) > 0 {
				for _, id := range ids {
					if id == channelID {
						continue
					}
					if peer := s.connCache.channelIDToConnector[id]; peer != nil {
						s.connCache.channelIDToClient[channelID] = peer
						break
					}
				}
			}
		}
	}
	s.connCache.mu.Unlock()
}

func (s *LinkSocksServer) findDirectPeer(sessionID uuid.UUID, source *WSConn) *WSConn {
	if sessionID == uuid.Nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for clientID, meta := range s.clientMeta {
		if meta == nil || meta.LastCapabilities == nil || meta.LastCapabilities.SessionID != sessionID {
			continue
		}
		peer := s.clients[clientID]
		if peer != nil && peer != source {
			return peer
		}
	}
	return nil
}

func (s *LinkSocksServer) bindDirectChannel(channelID, peerSessionID uuid.UUID, source *WSConn, role directClientRole, token string) {
	peer := s.findDirectPeer(peerSessionID, source)

	s.connCache.mu.Lock()
	switch role {
	case directClientRoleConnector:
		if _, exists := s.connCache.channelIDToConnector[channelID]; !exists {
			s.connCache.channelIDToConnector[channelID] = source
		}
		if peer != nil {
			if _, exists := s.connCache.channelIDToClient[channelID]; !exists {
				s.connCache.channelIDToClient[channelID] = peer
			}
		}
	case directClientRoleReverse:
		if _, exists := s.connCache.channelIDToClient[channelID]; !exists {
			s.connCache.channelIDToClient[channelID] = source
		}
		if peer != nil {
			if _, exists := s.connCache.channelIDToConnector[channelID]; !exists {
				s.connCache.channelIDToConnector[channelID] = peer
			}
		}
	default:
		if _, exists := s.connCache.channelIDToConnector[channelID]; !exists {
			s.connCache.channelIDToConnector[channelID] = source
		}
		if peer != nil {
			if _, exists := s.connCache.channelIDToClient[channelID]; !exists {
				s.connCache.channelIDToClient[channelID] = peer
			}
		}
	}
	if token != "" {
		ids := s.connCache.tokenCache[token]
		found := false
		for _, id := range ids {
			if id == channelID {
				found = true
				break
			}
		}
		if !found {
			s.connCache.tokenCache[token] = append(ids, channelID)
		}
	}
	s.connCache.mu.Unlock()
}

func (s *LinkSocksServer) reportError(err error) {
	if err == nil {
		return
	}
	select {
	case s.errors <- err:
	default:
		// Best-effort: never block background goroutines.
	}
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
	// AccessControl restricts which destinations this token's local proxy may
	// connect to. Nil falls back to the server-wide default.
	AccessControl *AccessControl
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

	// Reject "anonymous" as a token
	if opts.Token == "anonymous" {
		return nil, fmt.Errorf("'anonymous' is reserved and cannot be used as a token")
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
	// Reject "anonymous" as a token
	if token == "anonymous" {
		return "", fmt.Errorf("'anonymous' is reserved and cannot be used as a token")
	}

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
	// Reject "anonymous" as a connector token
	if connectorToken == "anonymous" {
		return "", fmt.Errorf("'anonymous' is reserved and cannot be used as a connector token")
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

	// If the connector token already exists, allow idempotent add when it maps
	// to the same reverse token; otherwise reject.
	if rt, exists := s.connectorTokens[connectorToken]; exists {
		if rt == reverseToken {
			return connectorToken, nil
		}
		return "", fmt.Errorf("connector token already exists")
	}

	// Disallow collisions with existing forward/reverse tokens.
	if _, exists := s.forwardTokens[connectorToken]; exists {
		return "", fmt.Errorf("connector token already exists")
	}
	if _, exists := s.tokens[connectorToken]; exists {
		return "", fmt.Errorf("connector token already exists")
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

// AddForwardTokenWithRules adds a forward token restricted to the given
// destinations. Rules are checked on the server before dialing on behalf of
// this token; when nil the server-level dial control applies.
func (s *LinkSocksServer) AddForwardTokenWithRules(token string, rules []AccessRule) (string, error) {
	ac, err := NewAccessControl(rules)
	if err != nil {
		return "", err
	}
	tok, err := s.AddForwardToken(token)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.forwardTokenAC[tok] = ac
	s.mu.Unlock()
	return tok, nil
}

// AddConnectorTokenWithRules adds a connector token restricted to the given
// destinations. Rules are checked on the server before a request from this
// connector is forwarded to a reverse provider.
func (s *LinkSocksServer) AddConnectorTokenWithRules(connectorToken string, reverseToken string, rules []AccessRule) (string, error) {
	ac, err := NewAccessControl(rules)
	if err != nil {
		return "", err
	}
	tok, err := s.AddConnectorToken(connectorToken, reverseToken)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.connectorTokenAC[tok] = ac
	s.mu.Unlock()
	return tok, nil
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
			delete(s.tokenAvailability, internalToken)
			delete(s.tokenWaiters, internalToken)
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
				delete(s.connCache.pendingQueues, id)
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
		delete(s.connectorTokenAC, token)

		s.log.Info().Str("token", token).Msg("Connector token removed")

		return true
	}

	// Handle reverse proxy token
	if port, isReverse := s.tokens[token]; isReverse {
		// Remove all connector tokens using this reverse token
		for connectorToken, rt := range s.connectorTokens {
			if rt == token {
				s.connCache.mu.Lock()
				if ids, exists := s.connCache.tokenCache[connectorToken]; exists {
					for _, id := range ids {
						delete(s.connCache.channelIDToClient, id)
						delete(s.connCache.channelIDToConnector, id)
						delete(s.connCache.pendingQueues, id)
					}
					delete(s.connCache.tokenCache, connectorToken)
				}
				s.connCache.mu.Unlock()

				if clients, ok := s.tokenClients[connectorToken]; ok {
					for _, client := range clients {
						client.Conn.Close()
						delete(s.clients, client.ID)
					}
					delete(s.tokenClients, connectorToken)
				}
				delete(s.connectorTokens, connectorToken)
				delete(s.connectorTokenAC, connectorToken)
				s.log.Info().Str("token", connectorToken).Msg("Connector token removed")
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
		delete(s.tokenAvailability, token)
		delete(s.tokenWaiters, token)

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
		delete(s.forwardTokenAC, token)

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
	// Optional UDP rendezvous server.
	if s.directRendezvousEnable {
		if err := s.startDirectRendezvousUDP(ctx); err != nil {
			return err
		}
	}

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

	srv := &http.Server{
		Addr:    net.JoinHostPort(s.wsHost, fmt.Sprintf("%d", s.wsPort)),
		Handler: mux,
	}

	// Assign once. Background goroutines must not rely on this pointer remaining non-nil.
	s.wsServer = srv

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

	// Listen explicitly so readiness is signaled by the listener itself
	// instead of a dial-polling loop.
	listener, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		s.reportError(fmt.Errorf("failed to listen on %s: %w", srv.Addr, err))
		return ctx.Err()
	}

	s.log.Info().
		Str("listen", srv.Addr).
		Str("url", fmt.Sprintf("http://localhost:%d", s.wsPort)).
		Msg("LinkSocks server started")
	s.readyOnce.Do(func() {
		close(s.ready)
	})

	// Serve on the already-open listener in the background
	go func(srv *http.Server, listener net.Listener) {
		defer func() {
			if r := recover(); r != nil {
				s.reportError(fmt.Errorf("panic in server ListenAndServe: %v", r))
			}
		}()
		if err := srv.Serve(listener); err != nil && err != http.ErrServerClosed {
			s.reportError(err)
		}
	}(srv, listener)

	// Block until context is done
	<-ctx.Done()
	return ctx.Err()
}

// WaitReady starts the server and waits for the server to be ready with optional timeout
func (s *LinkSocksServer) WaitReady(ctx context.Context, timeout time.Duration) error {
	s.mu.RLock()
	if s.closed {
		s.mu.RUnlock()
		return errors.New("server is closed")
	}
	s.mu.RUnlock()

	// Start Serve only once; subsequent WaitReady calls just wait.
	var serveCtx context.Context
	s.startOnce.Do(func() {
		serveCtx, s.cancelFunc = context.WithCancel(ctx)
		go func() {
			defer func() {
				if r := recover(); r != nil {
					s.reportError(fmt.Errorf("panic in server Serve: %v", r))
				}
			}()
			if err := s.Serve(serveCtx); err != nil {
				s.reportError(err)
			}
		}()
	})

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
		msg, peerVersion, err := wsConn.ReadMessageWithVersion()
		if err != nil {
			s.log.Debug().Err(err).Msg("Failed to read auth message")
			authResponse := AuthResponseMessage{Success: false, Error: "invalid auth message: " + err.Error()}
			s.relay.logMessage(authResponse, "send", wsConn.Label())
			wsConn.WriteMessage(authResponse)
			return
		}

		if (peerVersion & 0x0f) != (ProtocolVersion & 0x0f) {
			s.log.Warn().
				Uint8("peer_minor_version", peerVersion&0x0f).
				Uint8("server_minor_version", ProtocolVersion&0x0f).
				Msg("Minor protocol version mismatch detected; connection allowed but features may differ")
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
	s.mu.Unlock()

	if isValidReverse {
		// Start the SOCKS server before the auth response is sent so the
		// local proxy port accepts connections by the time the provider is
		// told it is authenticated. GetListener synchronously ensures the
		// port is bound; runSocksServer reuses the same listener.
		s.mu.Lock()
		socksPort := s.tokens[token]
		_, exists := s.socksTasks[socksPort]
		if socksPort > 0 && !exists {
			if _, err := s.socketManager.GetListener(socksPort); err != nil {
				s.log.Warn().Err(err).Int("port", socksPort).Msg("Failed to pre-bind SOCKS listener")
			}
			ctx, cancel := context.WithCancel(ctx)
			s.socksTasks[socksPort] = cancel
			go func() {
				if err := s.runSocksServer(ctx, token, socksPort); err != nil {
					s.log.Debug().Err(err).Int("port", socksPort).Msg("SOCKS server error")
				}
			}()
		}
		s.mu.Unlock()
	}

	authResponse := AuthResponseMessage{Success: true}
	s.relay.logMessage(authResponse, "send", wsConn.Label())
	if err := wsConn.WriteMessage(authResponse); err != nil {
		s.log.Debug().Err(err).Msg("Failed to send auth response")
		return
	}

	// Publish the connection as available only after the auth response is
	// written, so pending connects can never overtake it on the wire.
	s.mu.Lock()
	if _, exists := s.tokenClients[internalToken]; !exists {
		s.tokenClients[internalToken] = make([]clientInfo, 0)
	}
	s.tokenClients[internalToken] = append(s.tokenClients[internalToken], clientInfo{ID: clientID, Conn: wsConn})
	s.clients[clientID] = wsConn
	role := directClientRoleForward
	metaReverseToken := ""
	if isValidReverse {
		role = directClientRoleReverse
		s.notifyTokenAvailabilityLocked(internalToken)
	} else if isValidConnector {
		role = directClientRoleConnector
		metaReverseToken = reverseToken
	}
	s.clientMeta[clientID] = &directClientMeta{
		InternalToken:    internalToken,
		ReverseToken:     metaReverseToken,
		Role:             role,
		SupportsDirect:   false,
		UpdatedAt:        time.Now(),
		LastCapabilities: nil,
		LastRendezvous:   nil,
		LastStatus:       nil,
	}
	s.mu.Unlock()

	if isValidReverse {
		// Handle reverse proxy client
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

	if s.directEnable {
		hello := LogMessage{Level: LogLevelDebug, Msg: DirectServerHelloPrefix + "direct_signaling=1"}
		s.relay.logMessage(hello, "send", wsConn.Label())
		if err := wsConn.WriteMessage(hello); err != nil {
			s.log.Debug().Err(err).Msg("Failed to send direct signaling server hello")
		}
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
			errChan <- s.connectorMessageDispatcher(ctx, wsConn, reverseToken, clientID)
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
			case LogMessage:
				if s.directEnable && s.clientHelloIndicatesDirectSupport(m) {
					s.markClientSupportsDirect(clientID)
					s.sendKnownPeerDirectCapabilities(clientID, ws)
				}

			case DirectCapabilitiesMessage:
				if s.directEnable {
					s.mu.Lock()
					if meta := s.clientMeta[clientID]; meta != nil {
						meta.SupportsDirect = true
						tmp := m
						meta.LastCapabilities = &tmp
						meta.UpdatedAt = time.Now()
					}
					s.mu.Unlock()
					s.forwardDirectMessageFromClient(clientID, m)
				}

			case DirectRendezvousMessage:
				if s.directEnable {
					s.mu.Lock()
					if meta := s.clientMeta[clientID]; meta != nil {
						meta.SupportsDirect = true
						tmp := m
						meta.LastRendezvous = &tmp
						meta.UpdatedAt = time.Now()
					}
					s.mu.Unlock()
					s.forwardDirectMessageFromClient(clientID, m)
				}

			case DirectStatusMessage:
				if s.directEnable {
					s.mu.Lock()
					if meta := s.clientMeta[clientID]; meta != nil {
						meta.SupportsDirect = true
						tmp := m
						meta.LastStatus = &tmp
						meta.UpdatedAt = time.Now()
					}
					s.mu.Unlock()
					s.forwardDirectMessageFromClient(clientID, m)
				}

			case ChannelBindMessage:
				s.mu.RLock()
				meta := s.clientMeta[clientID]
				role := directClientRoleForward
				if meta != nil {
					role = meta.Role
				}
				internalToken := ""
				if meta != nil {
					internalToken = meta.InternalToken
				}
				s.mu.RUnlock()
				s.bindDirectChannel(m.ChannelID, m.PeerSessionID, ws, role, internalToken)
				s.bindChannelPeer(m.ChannelID, ws, role, internalToken)
				s.forwardChannelControl(m.ChannelID, ws, m)

			case ChannelMigrateMessage:
				s.forwardChannelControl(m.ChannelID, ws, m)

			case ChannelMigrateAckMessage:
				s.forwardChannelControl(m.ChannelID, ws, m)

			case ChannelDataAckMessage:
				s.forwardChannelControl(m.ChannelID, ws, m)

			case UnknownMessage:
				// Forward compatibility: ignore.

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
					// Enforce per-token forward access control before dialing
					s.mu.RLock()
					var fwdAC *AccessControl
					if meta := s.clientMeta[clientID]; meta != nil {
						fwdAC = s.forwardTokenAC[meta.InternalToken]
					}
					s.mu.RUnlock()
					if fwdAC != nil && !fwdAC.Empty() && !fwdAC.Allow(m.Address, m.Port) {
						s.log.Warn().Str("address", m.Address).Int("port", m.Port).Msg("Forward connect blocked by token access control")
						s.rejectConnectRequest(ws, m, "destination blocked by access control")
						continue
					}

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
						connectorWS, exists := s.connCache.channelIDToConnector[m.ChannelID]
						s.connCache.mu.RUnlock()
						if exists {
							s.relay.logMessage(m, "send", ws.Label())
							if err := connectorWS.WriteMessage(m); err != nil {
								s.log.Debug().Err(err).Msg("Failed to forward connect response")
							}
							s.log.Trace().Str("channel_id", m.ChannelID.String()).Msg("Forwarded connect response to connector")
						} else {
							s.log.Debug().Str("channel_id", m.ChannelID.String()).Msg("No queue and no connector for connect response")
						}
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
func (s *LinkSocksServer) connectorMessageDispatcher(ctx context.Context, ws *WSConn, reverseToken string, clientID uuid.UUID) error {
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
			case LogMessage:
				if s.directEnable && s.clientHelloIndicatesDirectSupport(m) {
					s.markClientSupportsDirect(clientID)
					s.sendKnownPeerDirectCapabilities(clientID, ws)
				}

			case DirectCapabilitiesMessage:
				if s.directEnable {
					s.mu.Lock()
					if meta := s.clientMeta[clientID]; meta != nil {
						meta.SupportsDirect = true
						tmp := m
						meta.LastCapabilities = &tmp
						meta.UpdatedAt = time.Now()
					}
					s.mu.Unlock()
					s.forwardDirectMessageFromClient(clientID, m)
				}

			case DirectRendezvousMessage:
				if s.directEnable {
					s.mu.Lock()
					if meta := s.clientMeta[clientID]; meta != nil {
						meta.SupportsDirect = true
						tmp := m
						meta.LastRendezvous = &tmp
						meta.UpdatedAt = time.Now()
					}
					s.mu.Unlock()
					s.forwardDirectMessageFromClient(clientID, m)
				}

			case DirectStatusMessage:
				if s.directEnable {
					s.mu.Lock()
					if meta := s.clientMeta[clientID]; meta != nil {
						meta.SupportsDirect = true
						tmp := m
						meta.LastStatus = &tmp
						meta.UpdatedAt = time.Now()
					}
					s.mu.Unlock()
					s.forwardDirectMessageFromClient(clientID, m)
				}

			case UnknownMessage:
				// Forward compatibility: ignore.

			case ChannelBindMessage:
				s.bindDirectChannel(m.ChannelID, m.PeerSessionID, ws, directClientRoleConnector, reverseToken)
				s.bindChannelPeer(m.ChannelID, ws, directClientRoleConnector, reverseToken)
				s.forwardChannelControl(m.ChannelID, ws, m)

			case ChannelMigrateMessage:
				s.forwardChannelControl(m.ChannelID, ws, m)

			case ChannelMigrateAckMessage:
				s.forwardChannelControl(m.ChannelID, ws, m)

			case ChannelDataAckMessage:
				s.forwardChannelControl(m.ChannelID, ws, m)

			case ConnectMessage:
				// Ensure the channel has a buffer. Data may already have been
				// arriving for it (fast-open pipelining does not depend on the
				// message order), and the provider lookup below runs in a
				// goroutine. Reserve the buffer synchronously so no data is
				// dropped while the mapping is unpublished.
				s.reserveConnectorChannel(m.ChannelID)

				go func(m ConnectMessage) {
					defer s.cleanupConnectorChannel(m.ChannelID)
					// Enforce per-connector access control before forwarding to a provider
					s.mu.RLock()
					var connAC *AccessControl
					if meta := s.clientMeta[clientID]; meta != nil {
						connAC = s.connectorTokenAC[meta.InternalToken]
					}
					s.mu.RUnlock()
					if connAC != nil && !connAC.Empty() && !connAC.Allow(m.Address, m.Port) {
						s.log.Warn().Str("address", m.Address).Int("port", m.Port).Msg("Connector connect blocked by token access control")
						s.rejectConnectRequest(ws, m, "destination blocked by access control")
						return
					}

					reverseWS, err := s.waitForNextWebSocket(ctx, reverseToken, s.connectorWait)
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

					// Store channel_id mapping for connector. The mapping is
					// published only after ConnectMessage is written below, so
					// data drained from the buffer can never overtake it.
					s.connCache.mu.Lock()
					s.connCache.channelIDToConnector[m.ChannelID] = ws
					s.connCache.channelIDToClient[m.ChannelID] = reverseWS
					if ids, exists := s.connCache.tokenCache[reverseToken]; exists {
						s.connCache.tokenCache[reverseToken] = append(ids, m.ChannelID)
					} else {
						s.connCache.tokenCache[reverseToken] = []uuid.UUID{m.ChannelID}
					}
					var queue chan BaseMessage
					if pd, ok := s.connCache.pendingQueues[m.ChannelID]; ok {
						if pd.timer != nil {
							pd.timer.Stop()
						}
						queue = pd.queue
					}
					s.connCache.mu.Unlock()

					s.relay.logMessage(m, "send", ws.Label())
					if err := reverseWS.WriteMessage(m); err != nil {
						s.log.Debug().Err(err).Msg("Failed to forward connect message")
						return
					}
					if queue == nil {
						return
					}
					// Forward buffered (and subsequent) data in order. The
					// queue outlives the buffer phase so data is never sent
					// directly while draining. It is closed by channel cleanup.
					for {
						select {
						case <-ctx.Done():
							return
						case dm, ok := <-queue:
							if !ok {
								return
							}
							s.relay.logMessage(dm, "send", ws.Label())
							if err := reverseWS.WriteMessage(dm); err != nil {
								s.log.Debug().Err(err).Msg("Failed to forward data message")
								return
							}
						}
					}
				}(m)

			case DataMessage:
				s.routeConnectorData(m, ws)

			case DisconnectMessage:
				go func(m DisconnectMessage) {
					// Forward message and clean up channel mappings
					s.connCache.mu.RLock()
					targetWS, exists := s.connCache.channelIDToConnector[m.ChannelID]
					s.connCache.mu.RUnlock()
					if exists {
						s.relay.logMessage(m, "send", ws.Label())
						if err := targetWS.WriteMessage(m); err != nil {
							s.log.Debug().Err(err).Msg("Failed to forward disconnect message")
						}
					}
					s.cleanupConnectorChannel(m.ChannelID)
				}(m)
			}
		}
	}
}

// disconnectChannel handles forwarding disconnect message and cleanup of channel resources
func (s *LinkSocksServer) disconnectChannel(channelID uuid.UUID, ws *WSConn, msg BaseMessage) {
	// Forward disconnect message to connector if exists
	s.connCache.mu.RLock()
	targetWS, exists := s.connCache.channelIDToConnector[channelID]
	s.connCache.mu.RUnlock()
	if exists {
		s.relay.logMessage(msg, "send", ws.Label())
		if err := targetWS.WriteMessage(msg); err != nil {
			s.log.Debug().Err(err).Msg("Failed to forward disconnect message")
		}
	}
	s.cleanupConnectorChannel(channelID)

	s.relay.disconnectChannel(channelID)
}

// cleanupConnection cleans up resources when a client disconnects
func (s *LinkSocksServer) cleanupConnection(clientID uuid.UUID, token string) {
	if clientID == uuid.Nil {
		return
	}

	var clientIP string
	// Notify connectors when a reverse (provider) leaves; notify reverse clients when
	// a connector leaves. Partners counts must update on both connect and disconnect.
	var notifyConnectors bool
	var notifyReverseToken string

	s.mu.Lock()
	// Get client IP from the connection
	if ws, exists := s.clients[clientID]; exists {
		clientIP = ws.GetClientIP()
	}

	// Prefer role recorded at authentication time.
	if meta, ok := s.clientMeta[clientID]; ok && meta != nil {
		switch meta.Role {
		case directClientRoleReverse:
			notifyConnectors = true
		case directClientRoleConnector:
			notifyReverseToken = meta.ReverseToken
		}
	}

	// Clean up connection in tokenClients
	if token != "" && s.tokenClients[token] != nil {
		clients := make([]clientInfo, 0, len(s.tokenClients[token]))
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

		// Fallback when clientMeta is missing (should be rare for authenticated clients).
		if !notifyConnectors && notifyReverseToken == "" {
			if _, isReverse := s.tokens[token]; isReverse {
				notifyConnectors = true
			} else if reverseToken, isConnector := s.connectorTokens[token]; isConnector {
				notifyReverseToken = reverseToken
			}
		}
	}

	// Clean up client connection
	delete(s.clients, clientID)
	delete(s.clientMeta, clientID)
	s.mu.Unlock()

	// Broadcast outside the lock to avoid deadlock
	if notifyConnectors {
		s.broadcastPartnersToConnectors()
	}
	if notifyReverseToken != "" {
		s.broadcastPartnersToReverseClients(notifyReverseToken)
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

// getNextWebSocketLocked returns the next available connection for token
// using round-robin. Caller must hold s.mu.
func (s *LinkSocksServer) getNextWebSocketLocked(token string) (*WSConn, error) {
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

// getNextWebSocket gets next available WebSocket connection using round-robin
func (s *LinkSocksServer) getNextWebSocket(token string) (*WSConn, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.getNextWebSocketLocked(token)
}

// notifyTokenAvailabilityLocked wakes all requests waiting for a provider of
// token. Caller must hold s.mu.
func (s *LinkSocksServer) notifyTokenAvailabilityLocked(token string) {
	if ch, ok := s.tokenAvailability[token]; ok && ch != nil {
		close(ch)
	}
	s.tokenAvailability[token] = make(chan struct{})
}

// routeConnectorData forwards a connector DataMessage to the mapped reverse
// client. Data for a channel that is not bound yet is buffered with no
// preconditions: fast-open pipelining delivers data before the channel
// mapping is published, and the connect handler drains the buffer in order.
// Buffered sends happen under the cache lock so cleanup can never close a
// queue under a send; they are non-blocking and never stall the dispatcher.
func (s *LinkSocksServer) routeConnectorData(m DataMessage, src *WSConn) {
	s.connCache.mu.RLock()
	pd, queued := s.connCache.pendingQueues[m.ChannelID]
	targetWS := s.connCache.channelIDToClient[m.ChannelID]
	if queued {
		select {
		case pd.queue <- m:
			s.connCache.mu.RUnlock()
			return
		default:
			s.connCache.mu.RUnlock()
			s.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Pending data queue full, dropping message")
			return
		}
	}
	s.connCache.mu.RUnlock()
	if targetWS != nil {
		s.relay.logMessage(m, "send", src.Label())
		if err := targetWS.WriteMessage(m); err != nil {
			s.log.Debug().Err(err).Msg("Failed to forward data message")
		}
		return
	}
	s.bufferChannelData(m)
}

// bufferChannelData retains data for a channel that is not bound yet.
// There are no preconditions: any unknown channel's data is held until its
// ConnectMessage binds the channel (then drained in order) or until
// connectorPendingTTL expires and the data is dropped.
func (s *LinkSocksServer) bufferChannelData(m DataMessage) {
	s.connCache.mu.Lock()
	pd, ok := s.connCache.pendingQueues[m.ChannelID]
	if !ok {
		pd = &pendingConnectorData{queue: make(chan BaseMessage, connectorPendingQueueSize)}
		s.connCache.pendingQueues[m.ChannelID] = pd
		pd.timer = time.AfterFunc(connectorPendingTTL, func() { s.expirePendingConnectorData(m.ChannelID) })
	}
	select {
	case pd.queue <- m:
		s.connCache.mu.Unlock()
		s.log.Trace().Str("channel_id", m.ChannelID.String()).Msg("Buffered data for unbound channel")
	default:
		s.connCache.mu.Unlock()
		s.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Pending data queue full, dropping message")
	}
}

// reserveConnectorChannel ensures a per-channel buffer exists before the
// async provider lookup publishes the channel mapping, so data for the
// channel is never dropped in between.
func (s *LinkSocksServer) reserveConnectorChannel(channelID uuid.UUID) {
	s.connCache.mu.Lock()
	if _, ok := s.connCache.pendingQueues[channelID]; !ok {
		pd := &pendingConnectorData{queue: make(chan BaseMessage, connectorPendingQueueSize)}
		s.connCache.pendingQueues[channelID] = pd
		pd.timer = time.AfterFunc(connectorPendingTTL, func() { s.expirePendingConnectorData(channelID) })
	}
	s.connCache.mu.Unlock()
}

// expirePendingConnectorData drops buffered data whose channel never became
// bound within connectorPendingTTL.
func (s *LinkSocksServer) expirePendingConnectorData(channelID uuid.UUID) {
	s.connCache.mu.Lock()
	pd, ok := s.connCache.pendingQueues[channelID]
	if !ok {
		s.connCache.mu.Unlock()
		return
	}
	if _, bound := s.connCache.channelIDToClient[channelID]; bound {
		s.connCache.mu.Unlock()
		return
	}
	delete(s.connCache.pendingQueues, channelID)
	close(pd.queue)
	s.connCache.mu.Unlock()
	s.log.Warn().Str("channel_id", channelID.String()).Msg("Dropped buffered data for channel that never connected")
}

// cleanupConnectorChannel removes all server-side state for a connector
// channel and closes its pending queue, waking a goroutine draining it.
// Safe to call more than once; must not be called while holding connCache.mu.
func (s *LinkSocksServer) cleanupConnectorChannel(channelID uuid.UUID) {
	s.connCache.mu.Lock()
	delete(s.connCache.channelIDToClient, channelID)
	delete(s.connCache.channelIDToConnector, channelID)
	if pd, ok := s.connCache.pendingQueues[channelID]; ok {
		// Closing the queue is safe: buffered sends hold the cache lock, so
		// a concurrent sender cannot write to a closed channel.
		if pd.timer != nil {
			pd.timer.Stop()
		}
		delete(s.connCache.pendingQueues, channelID)
		close(pd.queue)
	}
	s.connCache.mu.Unlock()
}

func (s *LinkSocksServer) waitForNextWebSocket(ctx context.Context, token string, timeout time.Duration) (*WSConn, error) {
	if timeout <= 0 {
		return s.getNextWebSocket(token)
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	s.mu.Lock()
	s.tokenWaiters[token]++
	changed := s.tokenAvailability[token]
	if changed == nil {
		changed = make(chan struct{})
		s.tokenAvailability[token] = changed
	}
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.tokenWaiters[token]--
		if s.tokenWaiters[token] <= 0 {
			delete(s.tokenWaiters, token)
		}
		s.mu.Unlock()
	}()

	for {
		// Check availability and subscribe under the same lock so a provider
		// published in between cannot be missed.
		s.mu.Lock()
		ws, err := s.getNextWebSocketLocked(token)
		changed = s.tokenAvailability[token]
		if changed == nil {
			changed = make(chan struct{})
			s.tokenAvailability[token] = changed
		}
		s.mu.Unlock()

		if err == nil {
			return ws, nil
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
			return nil, err
		case <-changed:
			// A provider appeared; re-check.
		}
	}
}

// GetConnectorWaitCount returns the number of connector requests currently
// waiting for a provider of the given reverse token.
func (s *LinkSocksServer) GetConnectorWaitCount(token string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.tokenWaiters[token]
}

// waitForClients waits for clients to be available for the given token
func (s *LinkSocksServer) waitForClients(token string, addr net.Addr) error {
	s.mu.Lock()
	clients, ok := s.tokenClients[token]
	hasValidClients := ok && len(clients) > 0
	changed := s.tokenAvailability[token]
	if changed == nil {
		changed = make(chan struct{})
		s.tokenAvailability[token] = changed
	}
	s.mu.Unlock()

	// If clients are already available, return immediately
	if hasValidClients {
		return nil
	}

	// Wait up to 10 seconds for clients to connect if needed
	timer := time.NewTimer(10 * time.Second)
	defer timer.Stop()

	for {
		select {
		case <-timer.C:
			s.log.Debug().Str("addr", addr.String()).Msg("No valid clients after timeout")
			return fmt.Errorf("no valid clients after timeout")
		case <-changed:
			// Provider availability changed; re-check.
		}

		s.mu.Lock()
		clients, ok = s.tokenClients[token]
		hasValidClients = ok && len(clients) > 0
		changed = s.tokenAvailability[token]
		if changed == nil {
			changed = make(chan struct{})
			s.tokenAvailability[token] = changed
		}
		s.mu.Unlock()

		if hasValidClients {
			return nil
		}
	}
}

// handleSocksRequest handles incoming SOCKS5 connection
func (s *LinkSocksServer) handleSocksRequest(ctx context.Context, socksConn net.Conn, addr net.Addr, token string) error {
	// Wait for clients to be available
	if err := s.waitForClients(token, addr); err != nil {
		return s.relay.RefuseLocalProxyRequest(socksConn, 3)
	}
	// Get WebSocket connection using round-robin with basic liveness check (ping)
	var ws *WSConn
	var err error
	// Determine number of attempts based on current clients
	s.mu.RLock()
	maxAttempts := len(s.tokenClients[token])
	s.mu.RUnlock()
	if maxAttempts == 0 {
		s.log.Warn().Int("port", s.tokens[token]).Msg("No available client for local proxy port")
		return s.relay.RefuseLocalProxyRequest(socksConn, 3)
	}

	for attempt := 0; attempt < maxAttempts; attempt++ {
		ws, err = s.getNextWebSocket(token)
		if err != nil {
			s.log.Warn().Int("port", s.tokens[token]).Msg("No available client for local proxy port")
			return s.relay.RefuseLocalProxyRequest(socksConn, 3)
		}
		// Quick liveness probe to avoid choosing a dead socket
		if pingErr := ws.SyncWriteControl(websocket.PingMessage, nil, time.Now().Add(1*time.Second)); pingErr != nil {
			s.log.Debug().Str("ws_label", ws.Label()).Msg("WS ping failed, trying next client")
			continue
		}
		s.log.Trace().Str("ws_label", ws.Label()).Msg("Selected reverse client for local proxy request")
		break
	}

	// Get authentication info and token-level access control if configured
	var username, password string
	var tokenAC *AccessControl
	s.mu.RLock()
	if auth, ok := s.tokenOptions[token]; ok {
		username = auth.Username
		password = auth.Password
		tokenAC = auth.AccessControl
	}
	s.mu.RUnlock()

	// Attach token-level access control so relay checks apply per-token rules
	if tokenAC != nil {
		ctx = withAccessControl(ctx, tokenAC)
	}

	// Handle SOCKS5 / HTTP proxy request using hybrid local proxy demux
	return s.relay.HandleLocalProxyRequest(ctx, ws, socksConn, username, password)
}

// rejectConnectRequest replies a failed connection attempt to the requester.
func (s *LinkSocksServer) rejectConnectRequest(ws *WSConn, m ConnectMessage, reason string) {
	response := ConnectResponseMessage{
		ChannelID: m.ChannelID,
		Success:   false,
		Error:     reason,
	}
	s.relay.logMessage(response, "send", ws.Label())
	if err := ws.WriteMessage(response); err != nil {
		s.log.Debug().Err(err).Msg("Failed to send access control failure response")
	}
}

// runSocksServer runs a SOCKS5 server for a specific token and port
func (s *LinkSocksServer) runSocksServer(ctx context.Context, token string, socksPort int) error {
	listener, err := s.socketManager.GetListener(socksPort)
	if err != nil {
		return err
	}
	defer s.socketManager.ReleaseListener(socksPort)

	s.log.Debug().Str("addr", listener.Addr().String()).Msg("Local proxy server started (SOCKS5 + HTTP)")

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
			s.log.Warn().Err(err).Msg("Failed to accept local proxy connection")
			continue
		}

		go func(conn net.Conn) {
			defer conn.Close()
			if err := s.handleSocksRequest(ctx, conn, conn.RemoteAddr(), token); err != nil && !errors.Is(err, context.Canceled) {
				if errors.Is(err, io.EOF) {
					s.log.Debug().Err(err).Msg("Error handling local proxy request")
				} else {
					s.log.Warn().Err(err).Msg("Error handling local proxy request")
				}
			}
		}(conn)
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

	// Cancel main worker as early as possible to stop background goroutines.
	if s.cancelFunc != nil {
		s.cancelFunc()
		s.cancelFunc = nil
	}

	// Stop optional direct rendezvous UDP listener.
	if s.directRendezvousCancel != nil {
		s.directRendezvousCancel()
		s.directRendezvousCancel = nil
	}
	if s.directRendezvousConn != nil {
		_ = s.directRendezvousConn.Close()
		s.directRendezvousConn = nil
	}

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
	return s.getTokenClientCountLocked(token)
}

// getTokenClientCountLocked counts clients for a token; caller must hold s.mu
// (read or write). Kept separate so callers already holding the lock don't
// re-acquire it: sync.RWMutex is not reentrant and a second RLock while a
// writer is queued deadlocks (writer-preference RWMutex).
func (s *LinkSocksServer) getTokenClientCountLocked(token string) int {
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
