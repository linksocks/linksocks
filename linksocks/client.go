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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

func init() {
	// quic-go attempts to increase UDP socket buffer sizes for better throughput.
	// On systems with low rmem/wmem limits or without CAP_NET_ADMIN, it prints a
	// one-time warning via the standard library logger. We silence that warning
	// here and rely on our own diagnostics (direct-quic buffer sizing).
	//
	// Ref: https://github.com/quic-go/quic-go/wiki/UDP-Buffer-Sizes
	if _, ok := os.LookupEnv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING"); !ok {
		_ = os.Setenv("QUIC_GO_DISABLE_RECEIVE_BUFFER_WARNING", "true")
	}
}

const (
	MaxRedirects = 5 // Maximum number of WebSocket redirects to follow
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

	// Direct signaling (experimental)
	directMode       DirectMode
	directDiscovery  DirectDiscovery
	stunServers      []string
	directOnlyAction DirectOnlyAction
	directHostCands  DirectHostCandidatesMode

	directHostCandsEnabled bool

	directUPnP        bool
	directUPnPLease   time.Duration
	directUPnPKeep    bool
	directUPnPExtPort int

	directStartOnce sync.Once
	directMu        sync.Mutex
	directNotifyCh  chan struct{}

	directLocalSessionID     uuid.UUID
	directLocalCandidates    []DirectCandidate
	directLocalCandidatesSig string
	directLocalPublicKey     []byte
	directLocalKeyPair       *directKeyPair
	directProbeConn          *net.UDPConn
	directRemotePublicKey    []byte
	directLocalReady         bool

	directRemoteSessionID  uuid.UUID
	directRemoteCandidates []DirectCandidate
	// directProbePeer is the observed UDP endpoint of the peer that responded
	// to the last successful direct probe. It is used to pick a single QUIC dial
	// target and avoid creating multiple concurrent QUIC connections that may
	// lead to each side selecting a different active connection.
	directProbePeer *net.UDPAddr

	directPairSessionID  uuid.UUID
	directPairSessionKey []byte
	directPairKeyReady   bool
	directPairSessionSet bool

	directPendingRendezvous   map[uuid.UUID]struct{}
	directProbing             bool
	directReady               bool
	directProbeBackoffSess    uuid.UUID
	directProbeFailCount      int
	directProbeNext           time.Time
	directProbeRetryArmed     bool
	directProbeLastFailSess   uuid.UUID
	directProbeLastFailAt     time.Time
	directProbeLastFailReason string

	directAwaitHostSess  uuid.UUID
	directAwaitHostUntil time.Time
	directAwaitHostArmed bool

	directUserLogSess   uuid.UUID
	directUserLogState  string
	directUserLogReason string

	directPeerLastStatus        string
	directPeerLastStatusSession uuid.UUID
	directPeerLastStatusAt      time.Time
	directPeerReady             bool
	directPeerStatusSession     uuid.UUID
	directDegradedUntil         time.Time
	directRefuseSocks           bool

	directQUICStartOnce sync.Once
	directQUICMu        sync.Mutex
	directQUICManager   *DirectQUICManager
	directQUICPlane     *DirectQUICDataPlane
	directQUICReadyCh   chan struct{}
	// Reserved for future fine-grained QUIC health tracking.
	directQUICBadUntil time.Time
	directQUICBadCount int

	websockets     []*WSConn // Multiple WebSocket connections
	currentIndex   int       // Current WebSocket index for round-robin
	socksListener  net.Listener
	reconnect      bool
	reconnectDelay time.Duration
	threads        int // Number of concurrent WebSocket connections

	mu           sync.RWMutex // General mutex
	connectionMu sync.Mutex   // Dedicated mutex for connection status

	cancelFunc context.CancelFunc
	startOnce  sync.Once

	batchLogger *batchLogger
}

func (c *LinkSocksClient) directIsUsable(now time.Time) bool {
	c.directMu.Lock()
	ready := c.directReady
	degraded := !c.directDegradedUntil.IsZero() && now.Before(c.directDegradedUntil)
	c.directMu.Unlock()
	return ready && !degraded
}

func (c *LinkSocksClient) directQUICIsActive() bool {
	c.directQUICMu.Lock()
	mgr := c.directQUICManager
	pl := c.directQUICPlane
	c.directQUICMu.Unlock()
	if mgr == nil || pl == nil {
		return false
	}
	return mgr.Active() != nil
}

// IsDirectQUICActive reports whether the direct QUIC dataplane currently has an active connection.
// Intended for tests and diagnostics.
func (c *LinkSocksClient) IsDirectQUICActive() bool {
	return c.directQUICIsActive()
}

// ForceCloseDirectQUIC closes and clears the current direct QUIC dataplane (if any).
// Intended for tests and controlled fault injection.
func (c *LinkSocksClient) ForceCloseDirectQUIC() {
	c.directQUICMu.Lock()
	pl := c.directQUICPlane
	mgr := c.directQUICManager
	c.directQUICPlane = nil
	c.directQUICManager = nil
	c.directQUICMu.Unlock()

	if pl != nil {
		_ = pl.Close()
	}
	if mgr != nil {
		_ = mgr.Close()
	}
}

func (c *LinkSocksClient) directQUICPlaneIfReadyAndUsable(now time.Time) *DirectQUICDataPlane {
	if c.directMode == DirectModeRelayOnly {
		return nil
	}
	if !c.directIsUsable(now) {
		return nil
	}

	c.directQUICMu.Lock()
	mgr := c.directQUICManager
	pl := c.directQUICPlane
	readyCh := c.directQUICReadyCh
	c.directQUICMu.Unlock()

	if mgr == nil || pl == nil {
		return nil
	}

	select {
	case <-readyCh:
		// continue
	default:
		return nil
	}

	if mgr.Active() == nil {
		return nil
	}
	return pl
}

func (c *LinkSocksClient) directMarkDegraded(now time.Time, d time.Duration, reason string) {
	if d <= 0 {
		d = 30 * time.Second
	}
	c.directMu.Lock()
	c.directDegradedUntil = now.Add(d)
	sid := c.directPairSessionID
	c.directMu.Unlock()
	_ = c.sendDirectMessage(DirectStatusMessage{SessionID: sid, Status: "degraded", Metrics: DirectMetrics{Reason: reason}})
}

func (c *LinkSocksClient) directMarkRecovered(now time.Time) {
	c.directMu.Lock()
	if c.directDegradedUntil.IsZero() {
		c.directMu.Unlock()
		return
	}
	// Reduce oscillation by keeping a short cooldown even after recovery.
	if now.Before(c.directDegradedUntil) {
		cooldown := now.Add(2 * time.Second)
		if cooldown.Before(c.directDegradedUntil) {
			c.directDegradedUntil = cooldown
		}
	} else {
		c.directDegradedUntil = time.Time{}
	}
	sid := c.directPairSessionID
	c.directMu.Unlock()
	_ = c.sendDirectMessage(DirectStatusMessage{SessionID: sid, Status: "ready"})
}

// ForceDirectDegraded is intended for tests and controlled fault injection.
// It marks direct transport as degraded for the provided duration.
func (c *LinkSocksClient) ForceDirectDegraded(d time.Duration, reason string) {
	c.directMarkDegraded(time.Now(), d, reason)
}

// ConnectedChan returns the current connection-ready signal channel.
// The returned channel may be replaced during reconnect/disconnect cycles.
func (c *LinkSocksClient) ConnectedChan() <-chan struct{} {
	c.connectionMu.Lock()
	ch := c.Connected
	c.connectionMu.Unlock()
	return ch
}

// DisconnectedChan returns the current disconnection signal channel.
// The returned channel may be replaced during reconnect/disconnect cycles.
func (c *LinkSocksClient) DisconnectedChan() <-chan struct{} {
	c.connectionMu.Lock()
	ch := c.Disconnected
	c.connectionMu.Unlock()
	return ch
}

// ClientOption represents configuration options for LinkSocksClient
type ClientOption struct {
	WSURL             string
	Reverse           bool
	SocksHost         string
	SocksPort         int
	SocksUsername     string
	SocksPassword     string
	SocksWaitServer   bool
	Reconnect         bool
	ReconnectDelay    time.Duration
	Logger            zerolog.Logger
	BufferSize        int
	ChannelTimeout    time.Duration
	ConnectTimeout    time.Duration
	Threads           int
	FastOpen          bool
	UpstreamProxy     string
	UpstreamUsername  string
	UpstreamPassword  string
	UpstreamProxyType ProxyType
	NoEnvProxy        bool // Ignore environment proxy settings

	// Direct connection options (experimental; default relay-only).
	DirectMode       DirectMode
	DirectDiscovery  DirectDiscovery
	StunServers      []string
	DirectOnlyAction DirectOnlyAction
	DirectHostCands  DirectHostCandidatesMode

	DirectUPnP        bool
	DirectUPnPLease   time.Duration
	DirectUPnPKeep    bool
	DirectUPnPExtPort int
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

		DirectMode:       DirectModeRelayOnly,
		DirectDiscovery:  DirectDiscoverySTUN,
		StunServers:      nil,
		DirectOnlyAction: DirectOnlyActionExit,
		DirectHostCands:  DirectHostCandidatesAuto,

		DirectUPnP:        false,
		DirectUPnPLease:   30 * time.Minute,
		DirectUPnPKeep:    false,
		DirectUPnPExtPort: 0,
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

// WithUpstreamProxy sets the upstream proxy address
func (o *ClientOption) WithUpstreamProxy(proxy string) *ClientOption {
	o.UpstreamProxy = proxy
	return o
}

// WithUpstreamProxyType sets the upstream proxy type (socks5 or http)
func (o *ClientOption) WithUpstreamProxyType(proxyType ProxyType) *ClientOption {
	o.UpstreamProxyType = proxyType
	return o
}

// WithUpstreamAuth sets the upstream proxy authentication
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

func (o *ClientOption) WithDirectMode(mode DirectMode) *ClientOption {
	o.DirectMode = mode
	return o
}

func (o *ClientOption) WithDirectDiscovery(discovery DirectDiscovery) *ClientOption {
	o.DirectDiscovery = discovery
	return o
}

func (o *ClientOption) WithStunServers(servers []string) *ClientOption {
	o.StunServers = servers
	return o
}

func (o *ClientOption) WithDirectOnlyAction(action DirectOnlyAction) *ClientOption {
	o.DirectOnlyAction = action
	return o
}

func (o *ClientOption) WithDirectHostCandidatesMode(mode DirectHostCandidatesMode) *ClientOption {
	o.DirectHostCands = mode
	return o
}

func (o *ClientOption) WithDirectUPnP(enable bool) *ClientOption {
	o.DirectUPnP = enable
	return o
}

func (o *ClientOption) WithDirectUPnPLease(lease time.Duration) *ClientOption {
	o.DirectUPnPLease = lease
	return o
}

func (o *ClientOption) WithDirectUPnPKeep(keep bool) *ClientOption {
	o.DirectUPnPKeep = keep
	return o
}

func (o *ClientOption) WithDirectUPnPExtPort(port int) *ClientOption {
	o.DirectUPnPExtPort = port
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

	// Use "anonymous" token if not provided
	if token == "" {
		token = "anonymous"
	}

	disconnected := make(chan struct{})
	close(disconnected)

	relayOpt := NewDefaultRelayOption().
		WithBufferSize(opt.BufferSize).
		WithChannelTimeout(opt.ChannelTimeout).
		WithConnectTimeout(opt.ConnectTimeout).
		WithFastOpen(opt.FastOpen).
		WithUpstreamProxy(opt.UpstreamProxy).
		WithUpstreamAuth(opt.UpstreamUsername, opt.UpstreamPassword).
		WithUpstreamProxyType(opt.UpstreamProxyType)

	client := &LinkSocksClient{
		instanceID:              uuid.New(),
		relay:                   NewRelay(opt.Logger, relayOpt),
		log:                     opt.Logger,
		token:                   token,
		wsURL:                   opt.WSURL,
		reverse:                 opt.Reverse,
		socksHost:               opt.SocksHost,
		socksPort:               opt.SocksPort,
		socksUsername:           opt.SocksUsername,
		socksPassword:           opt.SocksPassword,
		socksWaitServer:         opt.SocksWaitServer,
		reconnect:               opt.Reconnect,
		reconnectDelay:          opt.ReconnectDelay,
		errors:                  make(chan error, 16),
		Connected:               make(chan struct{}),
		Disconnected:            disconnected,
		IsConnected:             false,
		numPartners:             0,
		threads:                 opt.Threads,
		websockets:              make([]*WSConn, 0, opt.Threads),
		noEnvProxy:              opt.NoEnvProxy,
		directMode:              opt.DirectMode,
		directDiscovery:         opt.DirectDiscovery,
		stunServers:             opt.StunServers,
		directOnlyAction:        opt.DirectOnlyAction,
		directHostCands:         opt.DirectHostCands,
		directUPnP:              opt.DirectUPnP,
		directUPnPLease:         opt.DirectUPnPLease,
		directUPnPKeep:          opt.DirectUPnPKeep,
		directUPnPExtPort:       opt.DirectUPnPExtPort,
		directNotifyCh:          make(chan struct{}, 1),
		directLocalSessionID:    uuid.New(),
		directPendingRendezvous: make(map[uuid.UUID]struct{}),
		directQUICReadyCh:       make(chan struct{}),
	}

	return client
}

func minUUID(a, b uuid.UUID) uuid.UUID {
	for i := 0; i < len(a); i++ {
		if a[i] < b[i] {
			return a
		}
		if a[i] > b[i] {
			return b
		}
	}
	return a
}

func (c *LinkSocksClient) directNotify() {
	select {
	case c.directNotifyCh <- struct{}{}:
	default:
	}
}

func (c *LinkSocksClient) startDirectAgent(ctx context.Context) {
	if c.directMode == DirectModeRelayOnly {
		return
	}
	c.directStartOnce.Do(func() {
		go c.directAgent(ctx)
	})
}

func (c *LinkSocksClient) startDirectQUICAgent(ctx context.Context) {
	if c.directMode == DirectModeRelayOnly {
		return
	}
	c.directQUICStartOnce.Do(func() {
		go c.directQUICAgent(ctx)
	})
}

func (c *LinkSocksClient) directQUICAgent(ctx context.Context) {
	// Retry loop with backoff. This is intentionally conservative to avoid
	// hammering the UDP socket and to tolerate peers coming online later.
	backoff := 500 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		c.directMu.Lock()
		ready := c.directReady
		degraded := !c.directDegradedUntil.IsZero() && time.Now().Before(c.directDegradedUntil)
		pairSet := c.directPairSessionSet
		pairSession := c.directPairSessionID
		localSession := c.directLocalSessionID
		pairKeyReady := c.directPairKeyReady
		pairKey := append([]byte(nil), c.directPairSessionKey...)
		rcands := append([]DirectCandidate(nil), c.directRemoteCandidates...)
		probePeer := c.directProbePeer
		peerReady := c.directPeerReady && c.directPeerStatusSession == pairSession
		c.directMu.Unlock()

		dialer := localSession == pairSession
		if degraded || !pairSet || !pairKeyReady || pairSession == uuid.Nil || len(pairKey) == 0 {
			time.Sleep(backoff)
			continue
		}
		// Start QUIC once either side indicates direct is possible.
		if !ready && !peerReady {
			time.Sleep(backoff)
			continue
		}
		// The dialer side needs at least one dial target; the accept side can
		// proceed without remote candidates.
		if dialer {
			if !peerReady {
				time.Sleep(backoff)
				continue
			}
			if probePeer == nil && len(rcands) == 0 {
				time.Sleep(backoff)
				continue
			}
		}

		c.directQUICMu.Lock()
		plane := c.directQUICPlane
		mgr := c.directQUICManager
		c.directQUICMu.Unlock()
		// If we have a stale QUIC transport registered on the shared UDP socket,
		// quic-go will panic with "connection already exists" when a new Transport
		// attempts to Listen on the same local address.
		//
		// Close and clear inactive state before reconnecting.
		if mgr != nil && mgr.Active() == nil {
			c.ForceCloseDirectQUIC()
			plane = nil
			mgr = nil
		}
		if plane != nil && mgr != nil && mgr.Active() != nil {
			// Already ready.
			c.directMarkRecovered(time.Now())
			time.Sleep(2 * time.Second)
			continue
		}

		// Reuse the same UDP socket used for probing/candidate advertisement so
		// peers can dial the correct port.
		c.directMu.Lock()
		udpConn := c.directProbeConn
		c.directMu.Unlock()
		if udpConn == nil {
			time.Sleep(backoff)
			continue
		}

		mgr, err := NewDirectQUICManager(udpConn, pairSession, pairKey, c.log)
		if err != nil {
			c.directMarkDegraded(time.Now(), 10*time.Second, fmt.Sprintf("quic manager init: %v", err))
			c.log.Debug().Err(err).Msg("Direct QUIC manager init failed")
			time.Sleep(backoff)
			continue
		}

		// Avoid simultaneous dial creating two valid QUIC connections where each peer
		// picks a different "winner" and then no one serves streams on the other one.
		//
		// Use the smaller per-peer session id as the dialer role (it equals pairSession).
		// The other side only listens and accepts.
		dialCandidates := directSelectQUICDialCandidates(localSession, pairSession, probePeer, rcands)
		connectCtx, cancelConnect := context.WithTimeout(ctx, 12*time.Second)
		conn, err := mgr.Connect(connectCtx, dialCandidates)
		cancelConnect()
		if err != nil {
			_ = mgr.Close()
			c.directMarkDegraded(time.Now(), 15*time.Second, fmt.Sprintf("quic connect: %v", err))
			c.log.Debug().Err(err).Msg("Direct QUIC connect failed")
			time.Sleep(backoff)
			continue
		}

		plane, err = NewDirectQUICDataPlane(conn, c.log)
		if err != nil {
			_ = mgr.Close()
			c.directMarkDegraded(time.Now(), 15*time.Second, fmt.Sprintf("quic dataplane init: %v", err))
			c.log.Debug().Err(err).Msg("Direct QUIC dataplane init failed")
			time.Sleep(backoff)
			continue
		}

		// Start accept loop to handle inbound QUIC streams.
		// This must run on both sides, because either peer can initiate streams.
		_ = plane.Serve(ctx, func(ctx context.Context, ch *directQUICChannel, req ConnectMessage) error {
			w := newDirectQUICBoundWriter(plane, ch, "direct-quic")

			msgChan := make(chan BaseMessage, 1000)
			c.relay.messageQueues.Store(req.ChannelID, msgChan)
			go c.directQUICChannelReadLoop(ctx, ch)
			go func() {
				if err := c.relay.HandleNetworkConnection(ctx, w, req); err != nil && !errors.Is(err, context.Canceled) {
					c.log.Debug().Err(err).Msg("Error handling direct QUIC network connection")
				}
			}()
			return nil
		})

		c.directQUICMu.Lock()
		c.directQUICManager = mgr
		c.directQUICPlane = plane
		readyCh := c.directQUICReadyCh
		c.directQUICMu.Unlock()

		// Auto-cleanup when the QUIC connection is closed, so a future reconnect
		// doesn't reuse a Transport that is still registered in quic-go's global
		// conn multiplexer.
		go func(expected *DirectQUICManager) {
			select {
			case <-ctx.Done():
				return
			case <-conn.Context().Done():
			}

			c.directQUICMu.Lock()
			stillCurrent := c.directQUICManager == expected
			c.directQUICMu.Unlock()
			if stillCurrent {
				c.ForceCloseDirectQUIC()
			}
		}(mgr)

		select {
		case <-readyCh:
		default:
			close(readyCh)
		}

		// Mark direct usable even if UDP probing did not observe a reachable candidate.
		c.directMu.Lock()
		if !c.directReady {
			c.directReady = true
		}
		c.directMu.Unlock()

		peerAddr := ""
		if conn != nil && conn.RemoteAddr() != nil {
			peerAddr = conn.RemoteAddr().String()
		}
		role := "listener"
		if dialer {
			role = "dialer"
		}
		if c.directUserLogShouldEmit(pairSession, "ready") {
			e := c.log.Info().Str("session_id", pairSession.String()).Str("transport", "direct-quic").Str("role", role)
			if peerAddr != "" {
				e = e.Str("peer_addr", peerAddr)
			}
			e.Msg("Direct connectivity ready")
		} else {
			c.log.Debug().Msg("Direct QUIC dataplane ready")
		}
		c.directMarkRecovered(time.Now())
		// Slow down once established.
		backoff = 2 * time.Second
	}
}

func directSelectQUICDialCandidates(localSession uuid.UUID, pairSession uuid.UUID, probePeer *net.UDPAddr, rcands []DirectCandidate) []DirectCandidate {
	if localSession != pairSession {
		return nil
	}
	if probePeer != nil && probePeer.IP != nil && probePeer.Port > 0 {
		return []DirectCandidate{{Addr: probePeer.IP.String(), Port: probePeer.Port, Kind: "probe"}}
	}
	return rcands
}

func (c *LinkSocksClient) sendDirectMessage(msg BaseMessage) error {
	ws := c.getNextWebSocket()
	if ws == nil {
		return errors.New("direct: not connected")
	}
	c.relay.logMessage(msg, "send", ws.Label())
	return ws.WriteMessage(msg)
}

func (c *LinkSocksClient) directAgent(ctx context.Context) {
	var discovery DirectDiscovery
	var stunServers []string
	var localSession uuid.UUID
	var action DirectOnlyAction
	var hostMode DirectHostCandidatesMode

	c.directMu.Lock()
	discovery = c.directDiscovery
	stunServers = append([]string(nil), c.stunServers...)
	localSession = c.directLocalSessionID
	action = c.directOnlyAction
	hostMode = c.directHostCands
	c.directMu.Unlock()

	// Determine whether to advertise host (RFC1918) candidates.
	initialHostEnabled := false
	if hostMode == DirectHostCandidatesAlways {
		initialHostEnabled = true
	} else if hostMode == DirectHostCandidatesNever {
		initialHostEnabled = false
	} else {
		enable, _ := directShouldAdvertiseHostCandidates(c.wsURL)
		initialHostEnabled = enable
	}
	c.directMu.Lock()
	c.directHostCandsEnabled = initialHostEnabled
	c.directMu.Unlock()

	if discovery == DirectDiscoveryAuto {
		// Prefer STUN when configured; otherwise fall back to server discovery for
		// local testing and self-hosted deployments.
		if len(stunServers) > 0 {
			discovery = DirectDiscoverySTUN
		} else {
			discovery = DirectDiscoveryServer
		}
	}
	if discovery != DirectDiscoverySTUN && discovery != DirectDiscoveryServer {
		c.log.Warn().Str("direct_discovery", string(discovery)).Msg("Direct discovery not supported")
		return
	}

	kp, err := newDirectKeyPair()
	if err != nil {
		c.log.Warn().Err(err).Msg("Direct keypair generation failed")
		return
	}

	c.directMu.Lock()
	c.directLocalKeyPair = kp
	c.directLocalPublicKey = append([]byte(nil), kp.pub...)
	c.directMu.Unlock()

	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		c.log.Warn().Err(err).Msg("Direct UDP listen failed")
		return
	}

	upnpExternalPort := 0

	if c.directUPnP {
		localPort := conn.LocalAddr().(*net.UDPAddr).Port
		m, cleanup, err := MapUPnPUDP(ctx, localPort, upnpMappingOption{
			ExternalPort: c.directUPnPExtPort,
			Lease:        c.directUPnPLease,
			Keep:         c.directUPnPKeep,
			AutoRenew:    true,
		}, c.log)
		if err != nil {
			c.log.Warn().Err(err).Msg("Direct UPnP mapping failed")
		} else {
			upnpExternalPort = int(m.externalPort)
			defer cleanup()
		}
	}

	// Keep this socket for QUIC dataplane to ensure the advertised candidate port
	// matches the actual QUIC listener.
	defer func() {
		c.directMu.Lock()
		if c.directProbeConn == conn {
			c.directProbeConn = nil
		}
		c.directMu.Unlock()
		_ = conn.Close()
	}()

	var localCandidates []DirectCandidate
	if discovery == DirectDiscoverySTUN {
		stunRes, err := StunDiscoverFromConn(ctx, conn, &StunDiscoverOption{Servers: stunServers, Timeout: 2 * time.Second, Logger: c.log})
		if err != nil {
			c.log.Warn().Err(err).Msg("Direct STUN discovery failed")
			if c.directMode == DirectModeDirectOnly {
				if action == DirectOnlyActionExit {
					c.reportError(fmt.Errorf("direct-only: STUN discovery failed: %w", err))
					c.Close()
				} else {
					c.directMu.Lock()
					c.directRefuseSocks = true
					c.directMu.Unlock()
				}
			}
			return
		}
		localCandidates = []DirectCandidate{stunRes.Candidate}
		if upnpExternalPort > 0 {
			localCandidates = directDedupCandidates(append(localCandidates, DirectCandidate{Addr: stunRes.Candidate.Addr, Port: upnpExternalPort, Kind: "srflx"}))
		}

		// For local/LAN testing, srflx candidates may be unreachable due to NAT
		// hairpin restrictions. In that case, also advertise host candidates so
		// direct probing (and the QUIC dataplane) can succeed.
		c.directMu.Lock()
		hostEnabled := c.directHostCandsEnabled
		c.directMu.Unlock()
		if hostEnabled {
			includeLoopback := false
			if hostMode == DirectHostCandidatesAuto {
				_, includeLoopback = directShouldAdvertiseHostCandidates(c.wsURL)
			}
			hostCands := directBuildHostCandidates(conn, includeLoopback)
			if len(hostCands) > 0 {
				localCandidates = directDedupCandidates(append(hostCands, localCandidates...))
			}
		}
	} else {
		// Server discovery: ask the server-side UDP rendezvous endpoint for the
		// observed src ip:port (srflx). This avoids leaking private host addresses
		// by default.
		u, err := url.Parse(c.wsURL)
		if err != nil {
			c.log.Warn().Err(err).Msg("Direct server discovery: invalid ws url")
			return
		}
		h := strings.TrimSpace(u.Hostname())
		p := u.Port()
		if h == "" || p == "" {
			c.log.Warn().Str("ws_url", c.wsURL).Msg("Direct server discovery: missing host/port")
			return
		}
		serverAddr := net.JoinHostPort(h, p)
		cand, err := DirectRendezvousDiscoverFromConn(ctx, conn, serverAddr, 2*time.Second)
		if err != nil {
			c.log.Warn().Err(err).Str("server", serverAddr).Msg("Direct server discovery failed")
			if c.directMode == DirectModeDirectOnly {
				if action == DirectOnlyActionExit {
					c.reportError(fmt.Errorf("direct-only: server discovery failed: %w", err))
					c.Close()
				} else {
					c.directMu.Lock()
					c.directRefuseSocks = true
					c.directMu.Unlock()
				}
			}
			return
		}
		localCandidates = []DirectCandidate{*cand}
		if upnpExternalPort > 0 {
			localCandidates = directDedupCandidates(append(localCandidates, DirectCandidate{Addr: cand.Addr, Port: upnpExternalPort, Kind: "srflx"}))
		}

		// For local/LAN testing, also allow explicit host candidates when connecting
		// to a local/private server endpoint.
		c.directMu.Lock()
		hostEnabled := c.directHostCandsEnabled
		c.directMu.Unlock()
		if hostEnabled {
			includeLoopback := false
			if hostMode == DirectHostCandidatesAuto {
				_, includeLoopback = directShouldAdvertiseHostCandidates(c.wsURL)
			}
			hostCands := directBuildHostCandidates(conn, includeLoopback)
			if len(hostCands) > 0 {
				localCandidates = directDedupCandidates(append(hostCands, localCandidates...))
			}
		}
	}

	c.directMu.Lock()
	c.directProbeConn = conn
	c.directMu.Unlock()

	localPub := append([]byte(nil), c.directLocalPublicKey...)
	discLabel := "stun"
	if discovery == DirectDiscoveryServer {
		discLabel = "server"
	}

	broadcastLocalCandidates := func(cands []DirectCandidate, reason string) {
		sig := directCandidatesSignature(cands)
		hostCount := 0
		srflxCount := 0
		for _, cand := range cands {
			switch strings.ToLower(strings.TrimSpace(cand.Kind)) {
			case "host":
				hostCount++
			case "srflx":
				srflxCount++
			}
		}

		c.directMu.Lock()
		changed := c.directLocalCandidatesSig != sig
		c.directLocalCandidatesSig = sig
		c.directLocalCandidates = cands
		c.directLocalReady = true
		pending := make([]uuid.UUID, 0, len(c.directPendingRendezvous))
		for sid := range c.directPendingRendezvous {
			pending = append(pending, sid)
		}
		c.directPendingRendezvous = make(map[uuid.UUID]struct{})
		c.directMu.Unlock()
		c.directNotify()
		if changed {
			c.log.Info().
				Str("reason", reason).
				Int("candidates", len(cands)).
				Int("host_candidates", hostCount).
				Int("srflx_candidates", srflxCount).
				Msg("Direct candidates updated")
			c.log.Debug().Str("reason", reason).Interface("candidates", cands).Msg("Direct candidates detail")
		}

		capMsg := DirectCapabilitiesMessage{SessionID: localSession, Candidates: cands, Discoveries: []string{discLabel}, PublicKey: localPub}
		if err := c.sendDirectMessage(capMsg); err != nil {
			c.log.Debug().Err(err).Msg("Failed to send direct capabilities")
		}
		for _, sid := range pending {
			rendezvous := DirectRendezvousMessage{SessionID: sid, Candidates: cands, PublicKey: localPub}
			_ = c.sendDirectMessage(rendezvous)
		}
		c.directNotify()
	}

	broadcastLocalCandidates(localCandidates, "initial")

	// When using STUN, refresh srflx candidates once the local interface IP set
	// changes (rate-limited to once per minute). This helps when the host network
	// changes without restarting the process.
	if discovery == DirectDiscoverySTUN {
		lastSnap := directHostIPSnapshot()
		go func() {
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			needRefresh := false
			nextAllowed := time.Time{}
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					snap := directHostIPSnapshot()
					if snap != "" && snap != lastSnap {
						lastSnap = snap
						needRefresh = true
					}
					if !needRefresh {
						continue
					}
					now := time.Now()
					if !nextAllowed.IsZero() && now.Before(nextAllowed) {
						continue
					}
					nextAllowed = now.Add(1 * time.Minute)

					rctx, cancel := context.WithTimeout(ctx, 3*time.Second)
					stunRes, err := StunDiscoverFromConn(rctx, conn, &StunDiscoverOption{Servers: stunServers, Timeout: 2 * time.Second, Logger: c.log})
					cancel()
					if err != nil {
						c.log.Debug().Err(err).Msg("Direct STUN rediscovery failed")
						continue
					}

					newCandidates := []DirectCandidate{stunRes.Candidate}
					c.directMu.Lock()
					includeHost := c.directHostCandsEnabled
					c.directMu.Unlock()
					if includeHost {
						includeLoopback := false
						if hostMode == DirectHostCandidatesAuto {
							_, includeLoopback = directShouldAdvertiseHostCandidates(c.wsURL)
						}
						hostCands := directBuildHostCandidates(conn, includeLoopback)
						if len(hostCands) > 0 {
							newCandidates = directDedupCandidates(append(hostCands, newCandidates...))
						}
					}
					broadcastLocalCandidates(newCandidates, "stun-refresh")
					needRefresh = false
				}
			}
		}()
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-c.directNotifyCh:
			var remoteSession uuid.UUID
			var remoteCandidates []DirectCandidate
			var remotePub []byte
			var pairSession uuid.UUID
			var pairSet bool
			var pairKey []byte
			var pairKeyReady bool
			var alreadyProbing bool
			var alreadyReady bool
			var hostEnabled bool
			var localCandidatesSnap []DirectCandidate

			c.directMu.Lock()
			remoteSession = c.directRemoteSessionID
			remoteCandidates = append([]DirectCandidate(nil), c.directRemoteCandidates...)
			remotePub = append([]byte(nil), c.directRemotePublicKey...)
			pairSession = c.directPairSessionID
			pairSet = c.directPairSessionSet
			pairKey = append([]byte(nil), c.directPairSessionKey...)
			pairKeyReady = c.directPairKeyReady
			alreadyProbing = c.directProbing
			alreadyReady = c.directReady
			hostEnabled = c.directHostCandsEnabled
			localCandidatesSnap = append([]DirectCandidate(nil), c.directLocalCandidates...)
			c.directMu.Unlock()

			// auto: if both peers share the same srflx public address, also advertise
			// RFC1918 host candidates for LAN connectivity.
			if discovery == DirectDiscoverySTUN && hostMode == DirectHostCandidatesAuto && !hostEnabled {
				localSrflx := directCandidateAddrByKind(localCandidatesSnap, "srflx")
				remoteSrflx := directCandidateAddrByKind(remoteCandidates, "srflx")
				if localSrflx != "" && remoteSrflx != "" && localSrflx == remoteSrflx {
					awaitSess := pairSession
					if awaitSess == uuid.Nil && remoteSession != uuid.Nil {
						awaitSess = minUUID(localSession, remoteSession)
					}
					c.directMu.Lock()
					armAwait := false
					if !c.directHostCandsEnabled {
						c.directHostCandsEnabled = true
						hostEnabled = true
						if awaitSess != uuid.Nil {
							c.directAwaitHostSess = awaitSess
							c.directAwaitHostUntil = time.Now().Add(5 * time.Second)
							armAwait = !c.directAwaitHostArmed
							if armAwait {
								c.directAwaitHostArmed = true
							}
						}
						c.directMu.Unlock()
						c.log.Info().Str("public_addr", localSrflx).Msg("Direct host candidates enabled")
					} else {
						// Keep awaiting when host candidates were enabled earlier.
						if awaitSess != uuid.Nil && c.directAwaitHostSess == uuid.Nil {
							c.directAwaitHostSess = awaitSess
							c.directAwaitHostUntil = time.Now().Add(5 * time.Second)
							armAwait = !c.directAwaitHostArmed
							if armAwait {
								c.directAwaitHostArmed = true
							}
						}
						c.directMu.Unlock()
					}
					if armAwait && awaitSess != uuid.Nil {
						c.log.Debug().Str("session_id", awaitSess.String()).Msg("Direct awaiting peer host candidates")
						go func(sid uuid.UUID, until time.Time) {
							t := time.NewTimer(time.Until(until))
							defer t.Stop()
							select {
							case <-ctx.Done():
								c.directMu.Lock()
								c.directAwaitHostArmed = false
								c.directMu.Unlock()
								return
							case <-t.C:
								c.directMu.Lock()
								if c.directAwaitHostSess == sid && !c.directAwaitHostUntil.IsZero() && time.Now().After(c.directAwaitHostUntil) {
									c.directAwaitHostUntil = time.Time{}
								}
								c.directAwaitHostArmed = false
								c.directMu.Unlock()
								c.directNotify()
							}
						}(awaitSess, time.Now().Add(5*time.Second))
					}
					if hostEnabled {
						hostCands := directBuildHostCandidates(conn, false)
						if len(hostCands) > 0 {
							broadcastLocalCandidates(directDedupCandidates(append(hostCands, localCandidatesSnap...)), "same-srflx")
						}
					}
				}
			}

			if alreadyReady || alreadyProbing {
				continue
			}
			if len(remoteCandidates) == 0 {
				continue
			}
			// If we missed the peer's DirectCapabilities message (which carries the peer session id),
			// we can still proceed if the peer already advertised the pair session id via DirectStatus.
			if remoteSession == uuid.Nil && !pairSet {
				c.log.Debug().Int("candidates", len(remoteCandidates)).Msg("Direct probe waiting for peer session id")
				continue
			}

			if !pairSet {
				pairSession = minUUID(localSession, remoteSession)
				c.directMu.Lock()
				c.directPairSessionID = pairSession
				c.directPairSessionSet = true
				c.directMu.Unlock()
			}

			// If we just enabled host candidates due to same srflx, give the peer a
			// short window to advertise their host candidates before probing/logging a failure.
			if discovery == DirectDiscoverySTUN && hostMode == DirectHostCandidatesAuto {
				localSrflx := directCandidateAddrByKind(localCandidatesSnap, "srflx")
				remoteSrflx := directCandidateAddrByKind(remoteCandidates, "srflx")
				remoteHasHost := directHasCandidatesKind(remoteCandidates, "host")
				if localSrflx != "" && remoteSrflx != "" && localSrflx == remoteSrflx && !remoteHasHost {
					c.directMu.Lock()
					awaitSess := c.directAwaitHostSess
					awaitUntil := c.directAwaitHostUntil
					c.directMu.Unlock()
					if awaitSess == pairSession && !awaitUntil.IsZero() && time.Now().Before(awaitUntil) {
						c.log.Debug().Str("session_id", pairSession.String()).Msg("Direct probe waiting for peer host candidates")
						continue
					}
				}
			}

			now := time.Now()
			c.directMu.Lock()
			if c.directProbeBackoffSess != pairSession {
				c.directProbeBackoffSess = pairSession
				c.directProbeFailCount = 0
				c.directProbeNext = time.Time{}
				c.directProbeRetryArmed = false
			}
			nextProbe := c.directProbeNext
			c.directMu.Unlock()
			if !nextProbe.IsZero() && now.Before(nextProbe) {
				continue
			}

			if !pairKeyReady {
				if len(remotePub) != 32 {
					// Peer does not support LSHP-060 yet; keep relay path.
					continue
				}
				k, err := deriveDirectSessionKey(pairSession, kp.priv, remotePub)
				if err != nil {
					c.log.Debug().Err(err).Msg("Direct session key derivation failed")
					continue
				}
				pairKey = k
				c.directMu.Lock()
				c.directPairSessionKey = append([]byte(nil), k...)
				c.directPairKeyReady = true
				c.directMu.Unlock()
			}

			e := c.log.Debug().
				Str("pair_session_id", pairSession.String()).
				Str("local_session_id", localSession.String()).
				Int("remote_candidates", len(remoteCandidates))
			if remoteSession != uuid.Nil {
				e = e.Str("remote_session_id", remoteSession.String())
			}
			e.Msg("Starting direct probe")

			prober := NewDirectProber(conn, pairSession, pairKey, c.log)
			c.directMu.Lock()
			c.directProbing = true
			c.directMu.Unlock()

			_ = c.sendDirectMessage(DirectStatusMessage{SessionID: pairSession, Status: "probing"})
			proberCtx, cancelProber := context.WithCancel(ctx)
			proberDone := make(chan struct{})
			go func() {
				defer close(proberDone)
				prober.Start(proberCtx)
			}()

			probeCtx, cancel := context.WithTimeout(ctx, 6*time.Second)
			res, err := prober.Probe(probeCtx, remoteCandidates, 800*time.Millisecond)
			cancel()
			cancelProber()
			// Wait for prober to stop reading from the UDP socket before QUIC reuses it.
			<-proberDone

			if err == nil && res != nil {
				c.directMu.Lock()
				c.directReady = true
				c.directProbing = false
				c.directProbeFailCount = 0
				c.directProbeNext = time.Time{}
				c.directProbeRetryArmed = false
				if res.From != nil {
					peer := *res.From
					if peer.IP != nil {
						peer.IP = append([]byte(nil), peer.IP...)
					}
					c.directProbePeer = &peer
				}
				c.directMu.Unlock()

				_ = c.sendDirectMessage(DirectStatusMessage{
					SessionID: pairSession,
					Status:    "ready",
					Metrics:   DirectMetrics{RTTMs: res.RTT.Milliseconds()},
				})

				if c.directUserLogShouldEmit(pairSession, "ready") {
					e := c.log.Info().
						Str("session_id", pairSession.String()).
						Int64("rtt_ms", res.RTT.Milliseconds()).
						Str("transport", "direct-probe/udp")
					if res.From != nil {
						e = e.Str("peer_addr", res.From.String())
					}
					e.Msg("Direct connectivity ready")
				}

				c.startDirectQUICAgent(ctx)
				continue
			}

			var backoff time.Duration
			c.directMu.Lock()
			c.directProbing = false
			c.directProbeBackoffSess = pairSession
			c.directProbeFailCount++
			failCount := c.directProbeFailCount
			backoff = directProbeBackoff(failCount)
			c.directProbeNext = time.Now().Add(backoff)
			armRetry := !c.directProbeRetryArmed
			if armRetry {
				c.directProbeRetryArmed = true
			}
			c.directMu.Unlock()

			deferFail := false
			if discovery == DirectDiscoverySTUN && hostMode == DirectHostCandidatesAuto {
				localSrflx := directCandidateAddrByKind(localCandidatesSnap, "srflx")
				remoteSrflx := directCandidateAddrByKind(remoteCandidates, "srflx")
				remoteHasHost := directHasCandidatesKind(remoteCandidates, "host")
				if localSrflx != "" && remoteSrflx != "" && localSrflx == remoteSrflx && !remoteHasHost {
					c.directMu.Lock()
					awaitSess := c.directAwaitHostSess
					awaitUntil := c.directAwaitHostUntil
					c.directMu.Unlock()
					if awaitSess == pairSession && !awaitUntil.IsZero() && time.Now().Before(awaitUntil) {
						deferFail = true
					}
				}
			}

			if !deferFail {
				if err != nil {
					_ = c.sendDirectMessage(DirectStatusMessage{SessionID: pairSession, Status: "failed", Metrics: DirectMetrics{Reason: err.Error()}})
				} else {
					_ = c.sendDirectMessage(DirectStatusMessage{SessionID: pairSession, Status: "failed", Metrics: DirectMetrics{Reason: "unknown"}})
				}
			}
			reason := "unknown"
			if err != nil {
				reason = err.Error()
			}
			c.directMu.Lock()
			c.directProbeLastFailSess = pairSession
			c.directProbeLastFailAt = time.Now()
			c.directProbeLastFailReason = reason
			c.directMu.Unlock()
			c.log.Debug().Err(err).Str("session_id", pairSession.String()).Dur("retry_in", backoff).Int("fail_count", failCount).Msg("Direct probe failed")
			if !deferFail && c.directUserLogShouldEmit(pairSession, "failed") {
				lf := c.log.Info().Str("session_id", pairSession.String())
				if reason != "" {
					lf = lf.Str("reason", reason)
				}
				lf.Msg("Direct connectivity failed")
			}

			if armRetry && backoff > 0 {
				go func(d time.Duration) {
					t := time.NewTimer(d)
					defer t.Stop()
					select {
					case <-ctx.Done():
						c.directMu.Lock()
						c.directProbeRetryArmed = false
						c.directMu.Unlock()
						return
					case <-t.C:
						c.directMu.Lock()
						c.directProbeRetryArmed = false
						c.directMu.Unlock()
						c.directNotify()
					}
				}(backoff)
			}

			if c.directMode == DirectModeDirectOnly && !deferFail {
				if action == DirectOnlyActionExit {
					c.reportError(fmt.Errorf("direct-only: probe failed: %w", err))
					c.Close()
					return
				}
				c.directMu.Lock()
				c.directRefuseSocks = true
				c.directMu.Unlock()
			}
		}
	}
}

func directProbeBackoff(fails int) time.Duration {
	if fails <= 0 {
		return 0
	}
	backoff := 2 * time.Second
	shift := fails - 1
	if shift < 0 {
		shift = 0
	}
	if shift > 4 {
		shift = 4
	}
	backoff = backoff << shift
	max := 1 * time.Minute
	if backoff > max {
		return max
	}
	return backoff
}

func directCandidateAddrByKind(cands []DirectCandidate, kind string) string {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		return ""
	}
	for _, c := range cands {
		if strings.ToLower(strings.TrimSpace(c.Kind)) != k {
			continue
		}
		addr := strings.TrimSpace(c.Addr)
		if addr == "" {
			continue
		}
		return addr
	}
	return ""
}

func directHasCandidatesKind(cands []DirectCandidate, kind string) bool {
	k := strings.ToLower(strings.TrimSpace(kind))
	if k == "" {
		return false
	}
	for _, c := range cands {
		if strings.ToLower(strings.TrimSpace(c.Kind)) == k {
			addr := strings.TrimSpace(c.Addr)
			if addr != "" && c.Port > 0 {
				return true
			}
		}
	}
	return false
}

func directCandidatesSignature(cands []DirectCandidate) string {
	if len(cands) == 0 {
		return ""
	}
	parts := make([]string, 0, len(cands))
	for _, c := range cands {
		addr := strings.TrimSpace(c.Addr)
		if addr == "" || c.Port <= 0 {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(c.Kind))
		parts = append(parts, fmt.Sprintf("%s|%s|%d", kind, addr, c.Port))
	}
	sort.Strings(parts)
	return strings.Join(parts, ",")
}

func directDedupCandidates(cands []DirectCandidate) []DirectCandidate {
	if len(cands) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(cands))
	out := make([]DirectCandidate, 0, len(cands))
	for _, c := range cands {
		addr := strings.TrimSpace(c.Addr)
		if addr == "" || c.Port <= 0 {
			continue
		}
		kind := strings.ToLower(strings.TrimSpace(c.Kind))
		key := fmt.Sprintf("%s|%s|%d", kind, addr, c.Port)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, c)
	}
	return out
}

func directHostIPSnapshot() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	set := make(map[string]struct{})
	for _, addr := range addrs {
		var ip net.IP
		switch v := addr.(type) {
		case *net.IPNet:
			ip = v.IP
		case *net.IPAddr:
			ip = v.IP
		default:
			continue
		}
		if ip == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
			continue
		}
		if !ip.IsGlobalUnicast() {
			continue
		}
		set[ip.String()] = struct{}{}
	}
	ips := make([]string, 0, len(set))
	for s := range set {
		ips = append(ips, s)
	}
	sort.Strings(ips)
	return strings.Join(ips, ",")
}

func (c *LinkSocksClient) directUserLogShouldEmit(session uuid.UUID, state string) bool {
	if session == uuid.Nil || strings.TrimSpace(state) == "" {
		return false
	}

	c.directMu.Lock()
	defer c.directMu.Unlock()
	if c.directUserLogSess != session {
		c.directUserLogSess = session
		c.directUserLogState = ""
		c.directUserLogReason = ""
	}
	if c.directUserLogState == state {
		return false
	}
	c.directUserLogState = state
	return true
}

func (c *LinkSocksClient) reportError(err error) {
	if err == nil {
		return
	}
	select {
	case c.errors <- err:
	default:
	}
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

func directShouldAdvertiseHostCandidates(wsURL string) (enable bool, includeLoopback bool) {
	u, err := url.Parse(wsURL)
	if err != nil {
		return false, false
	}
	host := strings.TrimSpace(u.Hostname())
	if host == "" {
		return false, false
	}
	if strings.EqualFold(host, "localhost") {
		return true, true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		// Hostnames could point to public endpoints; avoid leaking private IPs.
		return false, false
	}
	if ip.IsLoopback() {
		return true, true
	}
	if ip.IsPrivate() {
		return true, false
	}
	return false, false
}

func directBuildHostCandidates(conn *net.UDPConn, includeLoopback bool) []DirectCandidate {
	if conn == nil {
		return nil
	}
	la, ok := conn.LocalAddr().(*net.UDPAddr)
	if !ok || la == nil || la.Port <= 0 {
		return nil
	}
	port := la.Port

	seen := make(map[string]struct{})
	out := make([]DirectCandidate, 0, 8)

	if includeLoopback {
		ip := "127.0.0.1"
		seen[ip] = struct{}{}
		out = append(out, DirectCandidate{Addr: ip, Port: port, Kind: "host"})
	}

	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return out
	}
	for _, a := range addrs {
		ipNet, ok := a.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		ip := ipNet.IP
		if ip4 := ip.To4(); ip4 != nil {
			ip = ip4
		}
		// Only advertise IPv4 private addresses.
		if ip == nil || ip.To4() == nil {
			continue
		}
		if ip.IsLoopback() || ip.IsLinkLocalUnicast() {
			continue
		}
		if !ip.IsPrivate() {
			continue
		}
		key := ip.String()
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, DirectCandidate{Addr: key, Port: port, Kind: "host"})
	}
	return out
}

// WaitReady start the client and waits for the client to be ready with optional timeout
func (c *LinkSocksClient) WaitReady(ctx context.Context, timeout time.Duration) error {
	c.mu.RLock()
	if c.closed {
		c.mu.RUnlock()
		return errors.New("client is closed")
	}
	c.mu.RUnlock()

	// Start Connect only once; repeated WaitReady calls just wait.
	c.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(ctx)
		c.mu.Lock()
		c.cancelFunc = cancel
		c.mu.Unlock()
		go func() {
			defer func() {
				if r := recover(); r != nil {
					c.reportError(fmt.Errorf("panic in client Connect: %v", r))
				}
			}()
			if err := c.Connect(ctx); err != nil {
				c.reportError(err)
			}
		}()
	})

	connectedCh := c.ConnectedChan()

	if timeout > 0 {
		select {
		case <-connectedCh:
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
	case <-connectedCh:
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
		connectedCh := c.ConnectedChan()
		select {
		case <-connectedCh:
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
	redirectsLeft := MaxRedirects

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
		// Provide helpful hint when using anonymous token
		if c.token == "anonymous" {
			return &nonRetriableError{msg: "authentication failed: please provide a token with -t or set LINKSOCKS_TOKEN"}
		}
		return &nonRetriableError{msg: "authentication failed: invalid token"}
	}

	// Advertise direct signaling support in a backward-compatible way.
	if c.directMode != DirectModeRelayOnly {
		hello := LogMessage{Level: LogLevelDebug, Msg: DirectClientHelloPrefix + "direct_signaling=1"}
		c.relay.logMessage(hello, "send", wsConn.Label())
		if err := wsConn.WriteMessage(hello); err != nil {
			wsConn.Close()
			return err
		}
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
		c.startDirectAgent(ctx)
	}

	// Measure latency using ping/pong asynchronously
	go func() {
		latency, err := wsConn.MeasureLatency(10 * time.Second)
		if err != nil {
			c.log.Debug().Err(err).Msg("Latency measurement failed")
			return
		}
		c.log.Info().Msgf("Server latency: %s", latency.Round(time.Millisecond))
	}()

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
			case DirectCapabilitiesMessage:
				c.directMu.Lock()
				c.directRemoteSessionID = m.SessionID
				c.directRemoteCandidates = append([]DirectCandidate(nil), m.Candidates...)
				if len(m.PublicKey) > 0 {
					c.directRemotePublicKey = append([]byte(nil), m.PublicKey...)
				}
				localReady := c.directLocalReady
				localCandidates := append([]DirectCandidate(nil), c.directLocalCandidates...)
				localPub := append([]byte(nil), c.directLocalPublicKey...)
				if !localReady {
					c.directPendingRendezvous[m.SessionID] = struct{}{}
				}
				c.directMu.Unlock()

				c.log.Debug().Str("session_id", m.SessionID.String()).Int("candidates", len(m.Candidates)).Msg("Received direct capabilities")
				if localReady {
					rendezvous := DirectRendezvousMessage{SessionID: m.SessionID, Candidates: localCandidates, PublicKey: localPub}
					c.relay.logMessage(rendezvous, "send", ws.Label())
					_ = ws.WriteMessage(rendezvous)
				}
				c.directNotify()

			case DirectRendezvousMessage:
				c.directMu.Lock()
				if m.SessionID == c.directLocalSessionID {
					c.directRemoteCandidates = append([]DirectCandidate(nil), m.Candidates...)
					if len(m.PublicKey) > 0 {
						c.directRemotePublicKey = append([]byte(nil), m.PublicKey...)
					}
				}
				c.directMu.Unlock()
				c.log.Debug().Str("session_id", m.SessionID.String()).Int("candidates", len(m.Candidates)).Msg("Received direct rendezvous")
				c.directNotify()

			case DirectStatusMessage:
				status := strings.ToLower(strings.TrimSpace(m.Status))
				reason := strings.TrimSpace(m.Metrics.Reason)
				e := c.log.Debug().
					Str("session_id", m.SessionID.String()).
					Str("status", status)
				if reason != "" {
					e = e.Str("reason", reason)
				}
				e.Msg("Received direct status")

				// DirectStatus session_id is the pair session id. Use it to seed the local
				// pairing state in case we missed the peer's DirectCapabilities message.
				if m.SessionID != uuid.Nil {
					c.directMu.Lock()
					if !c.directPairSessionSet {
						c.directPairSessionID = m.SessionID
						c.directPairSessionSet = true
					} else if c.directPairSessionID != uuid.Nil && c.directPairSessionID != m.SessionID {
						c.log.Debug().
							Str("prev_session_id", c.directPairSessionID.String()).
							Str("new_session_id", m.SessionID.String()).
							Msg("Direct pair session id changed")
						c.directPairSessionID = m.SessionID
					}
					c.directMu.Unlock()
				}

				now := time.Now()
				switch status {
				case "probing":
					// Peer status is debug-only; local probe outcome drives user-visible logs.

				case "degraded":
					c.directMu.Lock()
					until := now.Add(30 * time.Second)
					if until.After(c.directDegradedUntil) {
						c.directDegradedUntil = until
					}
					c.directMu.Unlock()

				case "failed":
					c.directMu.Lock()
					c.directPeerReady = false
					until := now.Add(30 * time.Second)
					if until.After(c.directDegradedUntil) {
						c.directDegradedUntil = until
					}
					c.directMu.Unlock()

					if c.directMode == DirectModeDirectOnly {
						c.directMu.Lock()
						c.directRefuseSocks = true
						action := c.directOnlyAction
						c.directMu.Unlock()
						if action == DirectOnlyActionExit {
							c.log.Error().Msg("Direct-only mode: exiting due to direct failure")
							c.reportError(fmt.Errorf("direct-only: peer reported failed"))
							c.Close()
						}
					}

				case "ready":
					c.directMu.Lock()
					c.directPeerReady = true
					c.directPeerStatusSession = m.SessionID
					c.directMu.Unlock()
					c.directMarkRecovered(now)
					c.startDirectQUICAgent(ctx)

				default:
					// Ignore unknown status.
				}
				c.directNotify()

			case UnknownMessage:
				c.log.Trace().Int("binary_type", int(m.BinaryType)).Msg("Dropped unknown binary message")

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

	if c.directMode == DirectModeDirectOnly {
		c.directMu.Lock()
		refuse := c.directRefuseSocks
		action := c.directOnlyAction
		c.directMu.Unlock()
		if refuse {
			c.log.Warn().Msg("Direct-only failed, refusing socks request")
			_ = c.relay.RefuseSocksRequest(socksConn, 0x03)
			return
		}

		// Direct-only mode must not silently fall back to relay.
		// Wait briefly for the direct QUIC dataplane to become ready.
		c.startDirectQUICAgent(ctx)
		c.directQUICMu.Lock()
		readyCh := c.directQUICReadyCh
		plane := c.directQUICPlane
		c.directQUICMu.Unlock()

		waitCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		select {
		case <-readyCh:
			now := time.Now()
			if plane == nil || !c.directIsUsable(now) || !c.directQUICIsActive() {
				c.directMu.Lock()
				c.directRefuseSocks = true
				c.directMu.Unlock()
				if action == DirectOnlyActionExit {
					c.reportError(fmt.Errorf("direct-only: direct dataplane not usable"))
					c.Close()
					return
				}
				_ = c.relay.RefuseSocksRequest(socksConn, 0x03)
				return
			}
			w := newDirectQUICDialWriter(ctx, c, plane, "direct-quic")
			if err := c.relay.HandleSocksRequest(ctx, w, socksConn, c.socksUsername, c.socksPassword); err != nil && !errors.Is(err, context.Canceled) {
				if errors.Is(err, io.EOF) {
					c.log.Debug().Err(err).Msg("Error handling SOCKS request")
				} else {
					c.log.Warn().Err(err).Msg("Error handling SOCKS request")
				}
			}
			return
		case <-waitCtx.Done():
			c.directMu.Lock()
			c.directRefuseSocks = true
			c.directMu.Unlock()
			if action == DirectOnlyActionExit {
				c.reportError(fmt.Errorf("direct-only: timeout waiting for direct dataplane"))
				c.Close()
				return
			}
			_ = c.relay.RefuseSocksRequest(socksConn, 0x03)
			return
		}
	}

	// Prefer direct QUIC dataplane when enabled and ready.
	if c.directMode != DirectModeRelayOnly {
		c.startDirectQUICAgent(ctx)
		c.directQUICMu.Lock()
		readyCh := c.directQUICReadyCh
		plane := c.directQUICPlane
		c.directQUICMu.Unlock()
		now := time.Now()
		usable := c.directIsUsable(now)

		select {
		case <-readyCh:
			if usable && plane != nil && c.directQUICIsActive() {
				w := newDirectQUICDialWriter(ctx, c, plane, "direct-quic")
				if err := c.relay.HandleSocksRequest(ctx, w, socksConn, c.socksUsername, c.socksPassword); err != nil && !errors.Is(err, context.Canceled) {
					if errors.Is(err, io.EOF) {
						c.log.Debug().Err(err).Msg("Error handling SOCKS request")
					} else {
						c.log.Warn().Err(err).Msg("Error handling SOCKS request")
					}
				}
				return
			}
		default:
			// Not ready; fall back to relay.
		}
	}

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

	// Close direct QUIC components if they exist
	c.directQUICMu.Lock()
	plane := c.directQUICPlane
	mgr := c.directQUICManager
	c.directQUICPlane = nil
	c.directQUICManager = nil
	c.directQUICMu.Unlock()
	if plane != nil {
		_ = plane.Close()
	}
	if mgr != nil {
		_ = mgr.Close()
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
