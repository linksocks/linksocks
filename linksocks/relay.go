package linksocks

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	// DefaultBufferSize is the size of reusable buffers
	// Larger buffers improve throughput but consume more memory
	DefaultBufferSize       = 1024 * 1024 // 1MB buffer size
	DefaultChannelTimeout   = 12 * time.Hour
	DefaultConnectTimeout   = 10 * time.Second
	DefaultMinBatchWaitTime = 20 * time.Millisecond
	DefaultMaxBatchWaitTime = 500 * time.Millisecond
	// DefaultTransportGrace is how long an established channel is kept alive
	// while its WebSocket link is down, so it can resume after a reconnect.
	// Zero disables channel survival (channels die with their link).
	DefaultTransportGrace = 30 * time.Second
	// Default threshold in bytes/sec for increasing batch delay
	DefaultHighSpeedThreshold = 256 * 1024
	// Default threshold in bytes/sec for reverting to immediate send
	DefaultLowSpeedThreshold = 128 * 1024
	// Default threshold in bytes for enabling compression
	DefaultCompressionThreshold = 512 * 1024
)

// ProxyType represents the type of upstream proxy
type ProxyType string

const (
	ProxyTypeNone   ProxyType = ""
	ProxyTypeSocks5 ProxyType = "socks5"
	ProxyTypeHTTP   ProxyType = "http"
)

// RelayOption contains configuration options for Relay
type RelayOption struct {
	// BufferSize controls the size of reusable buffers
	// Larger values may improve performance but increase memory usage
	BufferSize     int
	ChannelTimeout time.Duration
	ConnectTimeout time.Duration
	// TransportGrace is how long an established channel survives a broken
	// WebSocket link before being torn down. Zero disables survival.
	TransportGrace time.Duration
	// FastOpen controls whether to wait for connect success response
	// When false, assumes connection success immediately
	FastOpen bool

	// Upstream proxy configuration
	UpstreamProxy     string // Format: host:port
	UpstreamUsername  string
	UpstreamPassword  string
	UpstreamProxyType ProxyType // socks5 or http

	// Adaptive batching configuration
	EnableDynamicBatching bool
	MaxBatchWaitTime      time.Duration
	MinBatchWaitTime      time.Duration
	// HighSpeedThreshold defines the bytes/sec rate above which batch delay increases
	HighSpeedThreshold float64
	// LowSpeedThreshold defines the bytes/sec rate below which batch delay resets to zero
	LowSpeedThreshold float64
	// CompressionThreshold defines the data size in bytes above which compression is applied
	CompressionThreshold int

	// EntryAccessControl restricts which destinations the local proxy entry
	// may request. Nil means no restriction. Token-level rules passed via
	// context take precedence.
	EntryAccessControl *AccessControl

	// DialAccessControl restricts which destinations this relay may actually
	// connect to. Nil means no restriction.
	DialAccessControl *AccessControl
}

// NewDefaultRelayOption creates a RelayOption with default values
func NewDefaultRelayOption() *RelayOption {
	return &RelayOption{
		BufferSize:            DefaultBufferSize,
		ChannelTimeout:        DefaultChannelTimeout,
		ConnectTimeout:        DefaultConnectTimeout,
		TransportGrace:        DefaultTransportGrace,
		FastOpen:              false,
		EnableDynamicBatching: true,
		MinBatchWaitTime:      DefaultMinBatchWaitTime,
		MaxBatchWaitTime:      DefaultMaxBatchWaitTime,
		HighSpeedThreshold:    float64(DefaultHighSpeedThreshold),
		LowSpeedThreshold:     float64(DefaultLowSpeedThreshold),
		CompressionThreshold:  DefaultCompressionThreshold,
	}
}

// WithBufferSize sets the buffer size for the relay
func (o *RelayOption) WithBufferSize(size int) *RelayOption {
	o.BufferSize = size
	return o
}

// WithChannelTimeout sets the channel timeout for the relay
func (o *RelayOption) WithChannelTimeout(timeout time.Duration) *RelayOption {
	o.ChannelTimeout = timeout
	return o
}

// WithTransportGrace sets how long an established channel survives a broken
// WebSocket link before being torn down. Zero disables survival.
func (o *RelayOption) WithTransportGrace(grace time.Duration) *RelayOption {
	o.TransportGrace = grace
	return o
}

// WithConnectTimeout sets the connect timeout for the relay
func (o *RelayOption) WithConnectTimeout(timeout time.Duration) *RelayOption {
	o.ConnectTimeout = timeout
	return o
}

// WithFastOpen sets the fast open mode for the relay
func (o *RelayOption) WithFastOpen(fastOpen bool) *RelayOption {
	o.FastOpen = fastOpen
	return o
}

// WithUpstreamProxy sets the upstream proxy address
func (o *RelayOption) WithUpstreamProxy(proxy string) *RelayOption {
	o.UpstreamProxy = proxy
	return o
}

// WithUpstreamProxyType sets the upstream proxy type (socks5 or http)
func (o *RelayOption) WithUpstreamProxyType(proxyType ProxyType) *RelayOption {
	o.UpstreamProxyType = proxyType
	return o
}

// WithUpstreamAuth sets the upstream proxy authentication
func (o *RelayOption) WithUpstreamAuth(username, password string) *RelayOption {
	o.UpstreamUsername = username
	o.UpstreamPassword = password
	return o
}

// WithDynamicBatching enables or disables adaptive batching for SOCKS TCP
func (o *RelayOption) WithDynamicBatching(enabled bool) *RelayOption {
	o.EnableDynamicBatching = enabled
	return o
}

// WithBatchingTimeLimits sets the min/max wait times for SOCKS TCP batching
func (o *RelayOption) WithBatchingTimeLimits(min, max time.Duration) *RelayOption {
	if min < 0 {
		min = 0
	}
	if max < min {
		max = min
	}
	o.MinBatchWaitTime = min
	o.MaxBatchWaitTime = max
	return o
}

// WithHighSpeedThreshold sets the threshold for increasing batch delay
func (o *RelayOption) WithHighSpeedThreshold(threshold float64) *RelayOption {
	if threshold < 0 {
		threshold = float64(DefaultHighSpeedThreshold) // Use default if negative
	}
	o.HighSpeedThreshold = threshold
	return o
}

// WithLowSpeedThreshold sets the threshold for reverting to immediate send
func (o *RelayOption) WithLowSpeedThreshold(threshold float64) *RelayOption {
	if threshold < 0 {
		threshold = float64(DefaultLowSpeedThreshold) // Use default if negative
	}
	if threshold > o.HighSpeedThreshold {
		threshold = o.HighSpeedThreshold // Cannot be higher than high speed threshold
	}
	o.LowSpeedThreshold = threshold
	return o
}

// WithCompressionThreshold sets the threshold for enabling data compression
func (o *RelayOption) WithCompressionThreshold(threshold int) *RelayOption {
	if threshold < 0 {
		threshold = DefaultCompressionThreshold // Use default if negative
	}
	o.CompressionThreshold = threshold
	return o
}

// WithEntryAccessControl sets the destination restrictions applied at the
// local proxy entry (SOCKS5/HTTP request parsing).
func (o *RelayOption) WithEntryAccessControl(ac *AccessControl) *RelayOption {
	o.EntryAccessControl = ac
	return o
}

// WithDialAccessControl sets the destination restrictions applied when this
// relay performs the actual connection to the target.
func (o *RelayOption) WithDialAccessControl(ac *AccessControl) *RelayOption {
	o.DialAccessControl = ac
	return o
}

// checkEntryAccess verifies a local proxy request target against the
// token-level rule attached to ctx, falling back to the relay-wide entry rule.
func (r *Relay) checkEntryAccess(ctx context.Context, host string, port int) error {
	ac := accessControlFromContext(ctx)
	if ac == nil {
		ac = r.entryAC.Load()
	}
	if ac == nil {
		ac = r.option.EntryAccessControl
	}
	return r.checkRule(ac, host, port)
}

// checkDialAccess verifies a destination before dialing against the
// relay-wide dial rule.
func (r *Relay) checkDialAccess(ctx context.Context, host string, port int) error {
	ac := r.dialAC.Load()
	if ac == nil {
		ac = r.option.DialAccessControl
	}
	return r.checkRule(ac, host, port)
}

// SetEntryAccessControl replaces the server-wide entry rule at runtime. Nil
// restores the option value.
func (r *Relay) SetEntryAccessControl(ac *AccessControl) {
	r.entryAC.Store(ac)
}

// SetDialAccessControl replaces the server-wide dial rule at runtime. Nil
// restores the option value.
func (r *Relay) SetDialAccessControl(ac *AccessControl) {
	r.dialAC.Store(ac)
}

// EntryAccessControl returns the effective server-wide entry rule: the runtime
// value if set, otherwise the option value.
func (r *Relay) EntryAccessControl() *AccessControl {
	if ac := r.entryAC.Load(); ac != nil {
		return ac
	}
	return r.option.EntryAccessControl
}

// DialAccessControl returns the effective server-wide dial rule: the runtime
// value if set, otherwise the option value.
func (r *Relay) DialAccessControl() *AccessControl {
	if ac := r.dialAC.Load(); ac != nil {
		return ac
	}
	return r.option.DialAccessControl
}

func (r *Relay) checkRule(ac *AccessControl, host string, port int) error {
	if ac.Empty() {
		return nil
	}
	if ac.Allow(host, port) {
		return nil
	}
	return fmt.Errorf("destination %s:%d blocked by access control", host, port)
}

// orphanDataQueueSize bounds the per-channel buffer for data that arrives
// before the channel is bound.
const orphanDataQueueSize = 1000

// orphanDataTTL bounds how long data for an unbound channel is retained
// before it is dropped.
const orphanDataTTL = 30 * time.Second

// orphanData is data that arrived for a channel that is not bound yet.
// Binding is asynchronous, so data may legitimately arrive first; once the
// channel's message queue exists the buffer is drained in order. Data for a
// channel that never binds expires after orphanDataTTL.
type orphanData struct {
	queue chan BaseMessage
	timer *time.Timer
}

// Relay handles stream transport between SOCKS5 and WebSocket
type Relay struct {
	log                  zerolog.Logger
	messageQueues        sync.Map // map[uuid.UUID]chan Message
	logicalChannels      sync.Map // map[uuid.UUID]*logicalChannel
	tcpChannels          sync.Map // map[uuid.UUID]context.CancelFunc
	udpChannels          sync.Map // map[uuid.UUID]context.CancelFunc
	udpClientAddrs       sync.Map // map[uuid.UUID]*net.UDPAddr
	lastActivity         sync.Map // map[uuid.UUID]time.Time
	option               *RelayOption
	done                 chan struct{}
	connectionSuccessMap sync.Map
	bufferPool           sync.Pool      // Buffer pool for reusing byte slices
	cleanupQueue         chan uuid.UUID // Channel for delayed cleanup tasks

	// orphanMu guards orphanData; buffered sends hold it, and expiry closes
	// queues under it, so a queue is never closed under a send.
	orphanMu   sync.Mutex
	orphanData map[uuid.UUID]*orphanData

	// Runtime-updatable server-wide rules. When set via SetEntryAccessControl /
	// SetDialAccessControl they override the option values; nil falls back to
	// the option. The server-side access-control API updates these at runtime.
	entryAC atomic.Pointer[AccessControl]
	dialAC  atomic.Pointer[AccessControl]
}

// transportDownError marks a write failure caused by the transport link
// (WebSocket) itself being down. Such failures are recoverable within the
// channel's grace window, unlike failures caused by the channel being closed.
type transportDownError struct {
	cause error
}

func (e *transportDownError) Error() string { return e.cause.Error() }
func (e *transportDownError) Unwrap() error { return e.cause }

func isTransportDown(err error) bool {
	var e *transportDownError
	return errors.As(err, &e)
}

// writeWithTransportGrace writes to the channel, retrying while the link is
// down until the channel's transport grace expires or ctx is cancelled. It
// returns the last write error once the channel must be torn down.
func (r *Relay) writeWithTransportGrace(ctx context.Context, ch *logicalChannel, msg BaseMessage) error {
	for {
		err := ch.WriteMessage(msg)
		if err == nil {
			ch.resume()
			return nil
		}
		if !isTransportDown(err) || r.option.TransportGrace <= 0 {
			return err
		}
		if !ch.suspend(r.option.TransportGrace) {
			r.log.Warn().Str("channel_id", ch.id.String()).Msg("Transport grace expired, closing channel")
			return err
		}
		select {
		case <-ctx.Done():
			ch.resume()
			return ctx.Err()
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (r *Relay) registerLogicalChannel(channelID uuid.UUID, protocol string, writer MessageWriter, fallback MessageWriter, path channelPath) *logicalChannel {
	if provider, ok := writer.(fallbackWriterProvider); ok {
		fallback = provider.Fallback()
	}
	if fallback == nil {
		fallback = writer
	}
	channel := newLogicalChannel(channelID, protocol, writer, fallback, path)
	r.logicalChannels.Store(channelID, channel)
	return channel
}

func (r *Relay) logicalChannel(channelID uuid.UUID) (*logicalChannel, bool) {
	value, ok := r.logicalChannels.Load(channelID)
	if !ok {
		return nil, false
	}
	channel, ok := value.(*logicalChannel)
	return channel, ok
}

// bufferOrphanData retains data for a channel that is not bound yet. There
// are no preconditions: data for any unbound channel is held until its
// ConnectMessage binds the channel (then drained in order via
// flushOrphanData) or until orphanDataTTL expires.
func (r *Relay) bufferOrphanData(m DataMessage) {
	r.orphanMu.Lock()
	od, ok := r.orphanData[m.ChannelID]
	if !ok {
		od = &orphanData{queue: make(chan BaseMessage, orphanDataQueueSize)}
		r.orphanData[m.ChannelID] = od
		od.timer = time.AfterFunc(orphanDataTTL, func() { r.expireOrphanData(m.ChannelID) })
	}
	select {
	case od.queue <- m:
		r.orphanMu.Unlock()
		r.log.Trace().Str("channel_id", m.ChannelID.String()).Msg("Buffered data for unbound channel")
	default:
		r.orphanMu.Unlock()
		r.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Orphan data queue full, dropping message")
	}
}

// flushOrphanData moves buffered data into the channel's message queue in
// order. Called once when the channel binds; takes the buffer under the lock
// so expiry can never close it mid-drain.
func (r *Relay) flushOrphanData(channelID uuid.UUID, msgChan chan BaseMessage) {
	r.orphanMu.Lock()
	od, ok := r.orphanData[channelID]
	if !ok {
		r.orphanMu.Unlock()
		return
	}
	if od.timer != nil {
		od.timer.Stop()
	}
	delete(r.orphanData, channelID)
	r.orphanMu.Unlock()
	for {
		select {
		case dm := <-od.queue:
			select {
			case msgChan <- dm:
			default:
				r.log.Warn().Str("channel_id", channelID.String()).Msg("Message queue full, dropping buffered data")
				return
			}
		default:
			return
		}
	}
}

// expireOrphanData drops buffered data whose channel never became bound
// within orphanDataTTL.
func (r *Relay) expireOrphanData(channelID uuid.UUID) {
	r.orphanMu.Lock()
	od, ok := r.orphanData[channelID]
	if !ok {
		r.orphanMu.Unlock()
		return
	}
	delete(r.orphanData, channelID)
	close(od.queue)
	r.orphanMu.Unlock()
	r.log.Warn().Str("channel_id", channelID.String()).Msg("Dropped buffered data for channel that never connected")
}

func (r *Relay) switchLogicalChannel(channelID uuid.UUID, writer MessageWriter, path channelPath) error {
	channel, ok := r.logicalChannel(channelID)
	if !ok {
		return fmt.Errorf("logical channel %s not found", channelID)
	}
	return channel.Switch(writer, path)
}

func (r *Relay) logicalChannelPath(channelID uuid.UUID) string {
	channel, ok := r.logicalChannel(channelID)
	if !ok {
		return ""
	}
	return channel.Path()
}

// NewRelay creates a new Relay instance
func NewRelay(logger zerolog.Logger, option *RelayOption) *Relay {
	if option == nil {
		option = NewDefaultRelayOption()
	}

	r := &Relay{
		log:          logger,
		option:       option,
		done:         make(chan struct{}),
		cleanupQueue: make(chan uuid.UUID, 1000),
		orphanData:   make(map[uuid.UUID]*orphanData),
		bufferPool: sync.Pool{
			New: func() interface{} {
				b := make([]byte, option.BufferSize)
				return &b
			},
		},
	}

	go r.channelCleaner()
	// Start cleanup workers
	for i := 0; i < 4; i++ {
		go r.cleanupWorker()
	}

	return r
}

func (r *Relay) channelCleaner() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			now := time.Now()

			// Check TCP channels
			r.tcpChannels.Range(func(key, value interface{}) bool {
				channelID := key.(uuid.UUID)
				cancel := value.(context.CancelFunc)

				if lastTime, ok := r.lastActivity.Load(channelID); ok {
					if now.Sub(lastTime.(time.Time)) > r.option.ChannelTimeout {
						r.log.Trace().
							Str("channel_id", channelID.String()).
							Str("type", "tcp").
							Dur("timeout", r.option.ChannelTimeout).
							Msg("Channel timed out, closing")
						cancel()
						r.tcpChannels.Delete(channelID)
						r.lastActivity.Delete(channelID)
					}
				}
				return true
			})

			// Check UDP channels
			r.udpChannels.Range(func(key, value interface{}) bool {
				channelID := key.(uuid.UUID)
				cancel := value.(context.CancelFunc)

				if lastTime, ok := r.lastActivity.Load(channelID); ok {
					if now.Sub(lastTime.(time.Time)) > r.option.ChannelTimeout {
						r.log.Trace().
							Str("channel_id", channelID.String()).
							Str("type", "udp").
							Dur("timeout", r.option.ChannelTimeout).
							Msg("Channel timed out, closing")
						cancel()
						r.udpChannels.Delete(channelID)
						r.lastActivity.Delete(channelID)
					}
				}
				return true
			})
		}
	}
}

// Add new method to update activity timestamp
func (r *Relay) updateActivityTime(channelID uuid.UUID) {
	r.lastActivity.Store(channelID, time.Now())
}

// HandleNetworkConnection handles network connection based on protocol type
func (r *Relay) HandleNetworkConnection(ctx context.Context, ws MessageWriter, request ConnectMessage) error {
	if request.Resume {
		channel, ok := r.logicalChannel(request.ChannelID)
		if !ok {
			return fmt.Errorf("resume requested for unknown channel %s", request.ChannelID)
		}
		if err := channel.Switch(ws, channelPathRelay); err != nil {
			return err
		}
		return nil
	}
	channel, ok := r.logicalChannel(request.ChannelID)
	if !ok {
		channel = r.registerLogicalChannel(request.ChannelID, request.Protocol, ws, ws, channelPathRelay)
		channel.setRequest(request)
	}
	ws = channel
	if request.Protocol == "tcp" {
		return r.HandleTCPConnection(ctx, ws, request)
	} else if request.Protocol == "udp" {
		return r.HandleUDPConnection(ctx, ws, request)
	}
	return fmt.Errorf("unsupported protocol: %s", request.Protocol)
}

// HandleTCPConnection handles TCP network connection
func (r *Relay) HandleTCPConnection(ctx context.Context, ws MessageWriter, request ConnectMessage) error {
	if request.Port <= 0 || request.Port > 65535 {
		return fmt.Errorf("invalid port number: %d", request.Port)
	}

	if err := r.checkDialAccess(ctx, request.Address, request.Port); err != nil {
		r.log.Warn().Str("address", request.Address).Int("port", request.Port).Msg("TCP connection blocked by access control")
		response := ConnectResponseMessage{
			Success:   false,
			Error:     err.Error(),
			ChannelID: request.ChannelID,
		}
		r.logMessage(response, "send", ws.Label())
		if err := ws.WriteMessage(response); err != nil {
			return fmt.Errorf("write error response error: %w", err)
		}
		return nil
	}

	// Connect to target
	targetAddr := net.JoinHostPort(request.Address, fmt.Sprintf("%d", request.Port))
	r.log.Debug().Str("address", request.Address).Int("port", request.Port).
		Str("target", targetAddr).Msg("Attempting TCP connection to")

	var conn net.Conn
	var err error

	// Use upstream proxy if configured
	if r.option.UpstreamProxy != "" {
		if r.option.UpstreamProxyType == ProxyTypeHTTP {
			conn, err = r.dialViaHTTP(targetAddr)
		} else {
			conn, err = r.dialViaSocks5(targetAddr)
		}
	} else {
		conn, err = net.DialTimeout("tcp", targetAddr, r.option.ConnectTimeout)
	}

	if err != nil {
		r.log.Debug().
			Err(err).
			Str("address", request.Address).
			Int("port", request.Port).
			Str("target", targetAddr).
			Str("channel_id", request.ChannelID.String()).
			Msg("Failed to connect to target")

		response := ConnectResponseMessage{
			Success:   false,
			Error:     err.Error(),
			ChannelID: request.ChannelID,
		}
		r.logMessage(response, "send", ws.Label())
		if err := ws.WriteMessage(response); err != nil {
			return fmt.Errorf("write error response error: %w", err)
		}
		return nil
	}

	// Create child context
	childCtx, cancel := context.WithCancel(ctx)
	r.tcpChannels.Store(request.ChannelID, cancel)
	defer func() {
		cancel()
		conn.Close()
		r.tcpChannels.Delete(request.ChannelID)
		r.lastActivity.Delete(request.ChannelID)
		r.log.Trace().Str("channel_id", request.ChannelID.String()).Msg("TCP connection handler finished")
	}()

	// Send success response
	response := ConnectResponseMessage{
		Success:   true,
		ChannelID: request.ChannelID,
	}
	r.logMessage(response, "send", ws.Label())
	if err := ws.WriteMessage(response); err != nil {
		return fmt.Errorf("write success response error: %w", err)
	}

	// Start relay with child context
	return r.HandleRemoteTCPForward(childCtx, ws, conn, request.ChannelID)
}

// dialViaSocks5 establishes a connection through an upstream SOCKS5 proxy
func (r *Relay) dialViaSocks5(targetAddr string) (net.Conn, error) {
	// Connect to SOCKS5 proxy
	proxyConn, err := net.DialTimeout("tcp", r.option.UpstreamProxy, r.option.ConnectTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect to proxy error: %w", err)
	}

	// Negotiate with SOCKS5 proxy
	if err := r.socks5Handshake(proxyConn); err != nil {
		proxyConn.Close()
		return nil, fmt.Errorf("socks5 handshake error: %w", err)
	}

	// Send connect request
	if err := r.socks5Connect(proxyConn, targetAddr); err != nil {
		proxyConn.Close()
		return nil, fmt.Errorf("socks5 connect error: %w", err)
	}

	return proxyConn, nil
}

// socks5Handshake performs SOCKS5 protocol handshake with authentication if needed
func (r *Relay) socks5Handshake(conn net.Conn) error {
	// Initial handshake
	if r.option.UpstreamUsername != "" && r.option.UpstreamPassword != "" {
		// Auth required
		if _, err := conn.Write([]byte{0x05, 0x01, 0x02}); err != nil {
			return err
		}
	} else {
		// No auth
		if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			return err
		}
	}

	// Read response
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}

	if response[0] != 0x05 {
		return fmt.Errorf("invalid socks version: %d", response[0])
	}

	// Handle authentication if required
	if response[1] == 0x02 {
		if err := r.socks5Auth(conn); err != nil {
			return err
		}
	} else if response[1] != 0x00 {
		return fmt.Errorf("unsupported auth method: %d", response[1])
	}

	return nil
}

// socks5Auth performs username/password authentication
func (r *Relay) socks5Auth(conn net.Conn) error {
	// Username/Password auth version
	auth := []byte{0x01}

	// Add username
	auth = append(auth, byte(len(r.option.UpstreamUsername)))
	auth = append(auth, []byte(r.option.UpstreamUsername)...)

	// Add password
	auth = append(auth, byte(len(r.option.UpstreamPassword)))
	auth = append(auth, []byte(r.option.UpstreamPassword)...)

	if _, err := conn.Write(auth); err != nil {
		return err
	}

	// Read auth response
	response := make([]byte, 2)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}

	if response[0] != 0x01 || response[1] != 0x00 {
		return fmt.Errorf("authentication failed")
	}

	return nil
}

// socks5Connect sends connect request to SOCKS5 proxy
func (r *Relay) socks5Connect(conn net.Conn, targetAddr string) error {
	host, portStr, err := net.SplitHostPort(targetAddr)
	if err != nil {
		return err
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return err
	}

	// Prepare connect request
	request := []byte{0x05, 0x01, 0x00}

	// Add address
	if ip := net.ParseIP(host); ip != nil {
		if ip4 := ip.To4(); ip4 != nil {
			request = append(request, 0x01)
			request = append(request, ip4...)
		} else {
			request = append(request, 0x04)
			request = append(request, ip...)
		}
	} else {
		request = append(request, 0x03, byte(len(host)))
		request = append(request, []byte(host)...)
	}

	// Add port
	portBytes := make([]byte, 2)
	binary.BigEndian.PutUint16(portBytes, uint16(port))
	request = append(request, portBytes...)

	// Send request
	if _, err := conn.Write(request); err != nil {
		return err
	}

	// Read response
	response := make([]byte, 4)
	if _, err := io.ReadFull(conn, response); err != nil {
		return err
	}

	if response[0] != 0x05 {
		return fmt.Errorf("invalid socks version: %d", response[0])
	}

	if response[1] != 0x00 {
		return fmt.Errorf("connection failed: %d", response[1])
	}

	// Skip the rest of the response (bound address and port)
	switch response[3] {
	case 0x01:
		_, err = io.ReadFull(conn, make([]byte, 4+2)) // IPv4 + Port
	case 0x03:
		domainLen := make([]byte, 1)
		_, err = io.ReadFull(conn, domainLen)
		if err == nil {
			_, err = io.ReadFull(conn, make([]byte, int(domainLen[0])+2)) // Domain + Port
		}
	case 0x04:
		_, err = io.ReadFull(conn, make([]byte, 16+2)) // IPv6 + Port
	}

	return err
}

// dialViaHTTP establishes a connection through an HTTP CONNECT proxy
func (r *Relay) dialViaHTTP(targetAddr string) (net.Conn, error) {
	// Connect to HTTP proxy
	proxyConn, err := net.DialTimeout("tcp", r.option.UpstreamProxy, r.option.ConnectTimeout)
	if err != nil {
		return nil, fmt.Errorf("connect to proxy error: %w", err)
	}

	// Build CONNECT request
	connectReq := fmt.Sprintf("CONNECT %s HTTP/1.1\r\nHost: %s\r\n", targetAddr, targetAddr)

	// Add proxy authentication if provided
	if r.option.UpstreamUsername != "" && r.option.UpstreamPassword != "" {
		auth := r.option.UpstreamUsername + ":" + r.option.UpstreamPassword
		encoded := base64Encode([]byte(auth))
		connectReq += fmt.Sprintf("Proxy-Authorization: Basic %s\r\n", encoded)
	}

	connectReq += "\r\n"

	// Send CONNECT request
	if _, err := proxyConn.Write([]byte(connectReq)); err != nil {
		proxyConn.Close()
		return nil, fmt.Errorf("write CONNECT request error: %w", err)
	}

	// Read response
	response := make([]byte, 1024)
	n, err := proxyConn.Read(response)
	if err != nil {
		proxyConn.Close()
		return nil, fmt.Errorf("read CONNECT response error: %w", err)
	}

	// Parse response status
	responseStr := string(response[:n])
	if !strings.Contains(responseStr, "200") {
		proxyConn.Close()
		// Extract status line for error message
		statusLine := strings.Split(responseStr, "\r\n")[0]
		return nil, fmt.Errorf("HTTP CONNECT failed: %s", statusLine)
	}

	return proxyConn, nil
}

// base64Encode encodes data to base64 string
func base64Encode(data []byte) string {
	const base64Chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"
	result := make([]byte, 0, (len(data)+2)/3*4)

	for i := 0; i < len(data); i += 3 {
		var n uint32
		remaining := len(data) - i

		if remaining >= 3 {
			n = uint32(data[i])<<16 | uint32(data[i+1])<<8 | uint32(data[i+2])
			result = append(result, base64Chars[n>>18&0x3F], base64Chars[n>>12&0x3F], base64Chars[n>>6&0x3F], base64Chars[n&0x3F])
		} else if remaining == 2 {
			n = uint32(data[i])<<16 | uint32(data[i+1])<<8
			result = append(result, base64Chars[n>>18&0x3F], base64Chars[n>>12&0x3F], base64Chars[n>>6&0x3F], '=')
		} else {
			n = uint32(data[i]) << 16
			result = append(result, base64Chars[n>>18&0x3F], base64Chars[n>>12&0x3F], '=', '=')
		}
	}

	return string(result)
}

// HandleUDPConnection handles UDP network connection
func (r *Relay) HandleUDPConnection(ctx context.Context, ws MessageWriter, request ConnectMessage) error {
	if request.Address != "" {
		if err := r.checkDialAccess(ctx, request.Address, request.Port); err != nil {
			r.log.Warn().Str("address", request.Address).Int("port", request.Port).Msg("UDP connection blocked by access control")
			response := ConnectResponseMessage{
				Success:   false,
				Error:     err.Error(),
				ChannelID: request.ChannelID,
			}
			r.logMessage(response, "send", ws.Label())
			if err := ws.WriteMessage(response); err != nil {
				return fmt.Errorf("write error response error: %w", err)
			}
			return nil
		}
	}

	// Try dual-stack first
	localAddr := &net.UDPAddr{
		IP:   net.IPv6zero,
		Port: 0,
	}
	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		// Fallback to IPv4-only if dual-stack fails
		localAddr.IP = net.IPv4zero
		conn, err = net.ListenUDP("udp", localAddr)
		if err != nil {
			response := ConnectResponseMessage{
				Success:   false,
				Error:     err.Error(),
				ChannelID: request.ChannelID,
			}
			r.logMessage(response, "send", ws.Label())
			if err := ws.WriteMessage(response); err != nil {
				return fmt.Errorf("write error response error: %w", err)
			}
			return fmt.Errorf("udp listen error: %w", err)
		}
	}

	// Create child context
	childCtx, cancel := context.WithCancel(ctx)
	r.udpChannels.Store(request.ChannelID, cancel)
	defer func() {
		cancel()
		conn.Close()
		r.udpChannels.Delete(request.ChannelID)
		r.lastActivity.Delete(request.ChannelID)
	}()

	// Send success response
	response := ConnectResponseMessage{
		Success:   true,
		ChannelID: request.ChannelID,
	}
	r.logMessage(response, "send", ws.Label())
	if err := ws.WriteMessage(response); err != nil {
		return fmt.Errorf("write success response error: %w", err)
	}

	// Start relay with child context
	return r.HandleRemoteUDPForward(childCtx, ws, conn, request.ChannelID)
}

// HandleSocksRequest handles incoming SOCKS5 client request
func (r *Relay) HandleSocksRequest(ctx context.Context, ws MessageWriter, socksConn net.Conn, socksUsername string, socksPassword string) error {
	buffer := make([]byte, 1024)

	// Helper to send SOCKS5 failure response and close connection
	sendFailureAndClose := func(replyCode byte) {
		resp := []byte{0x05, replyCode, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
		socksConn.Write(resp)
		socksConn.Close()
	}

	// Read version and number of auth methods (exactly 2 bytes)
	if _, err := io.ReadFull(socksConn, buffer[:2]); err != nil {
		return fmt.Errorf("read version error: %w", err)
	}
	if buffer[0] != 0x05 {
		sendFailureAndClose(0x01) // general SOCKS server failure
		return fmt.Errorf("invalid socks version")
	}

	nmethods := int(buffer[1])
	if nmethods == 0 {
		sendFailureAndClose(0x01)
		return fmt.Errorf("no auth methods provided")
	}

	// Read auth methods (exactly nmethods bytes)
	if _, err := io.ReadFull(socksConn, buffer[:nmethods]); err != nil {
		return fmt.Errorf("read auth methods error: %w", err)
	}
	methods := buffer[:nmethods]

	if socksUsername != "" && socksPassword != "" {
		// Require username/password authentication
		var hasUserPass bool
		for _, method := range methods {
			if method == 0x02 {
				hasUserPass = true
				break
			}
		}
		if !hasUserPass {
			if _, err := socksConn.Write([]byte{0x05, 0xFF}); err != nil {
				return fmt.Errorf("write auth method error: %w", err)
			}
			socksConn.Close()
			return fmt.Errorf("no username/password auth method")
		}

		// Send auth method response (username/password)
		if _, err := socksConn.Write([]byte{0x05, 0x02}); err != nil {
			return fmt.Errorf("write auth response error: %w", err)
		}

		// Read auth version (exactly 1 byte)
		if _, err := io.ReadFull(socksConn, buffer[:1]); err != nil {
			return fmt.Errorf("read auth version error: %w", err)
		}
		if buffer[0] != 0x01 {
			socksConn.Write([]byte{0x01, 0x01})
			socksConn.Close()
			return fmt.Errorf("invalid auth version")
		}

		// Read username length (exactly 1 byte)
		if _, err := io.ReadFull(socksConn, buffer[:1]); err != nil {
			return fmt.Errorf("read username length error: %w", err)
		}
		ulen := int(buffer[0])

		// Read username (exactly ulen bytes)
		if _, err := io.ReadFull(socksConn, buffer[:ulen]); err != nil {
			return fmt.Errorf("read username error: %w", err)
		}
		username := string(buffer[:ulen])

		// Read password length (exactly 1 byte)
		if _, err := io.ReadFull(socksConn, buffer[:1]); err != nil {
			return fmt.Errorf("read password length error: %w", err)
		}
		plen := int(buffer[0])

		// Read password (exactly plen bytes)
		if _, err := io.ReadFull(socksConn, buffer[:plen]); err != nil {
			return fmt.Errorf("read password error: %w", err)
		}
		password := string(buffer[:plen])

		if username != socksUsername || password != socksPassword {
			if _, err := socksConn.Write([]byte{0x01, 0x01}); err != nil {
				return fmt.Errorf("write auth failure response error: %w", err)
			}
			socksConn.Close()
			return fmt.Errorf("authentication failed")
		}

		// Send auth success response
		if _, err := socksConn.Write([]byte{0x01, 0x00}); err != nil {
			return fmt.Errorf("write auth success response error: %w", err)
		}
	} else {
		// No authentication required
		if _, err := socksConn.Write([]byte{0x05, 0x00}); err != nil {
			return fmt.Errorf("write auth response error: %w", err)
		}
	}

	// Read request header (VER, CMD, RSV, ATYP = 4 bytes)
	if _, err := io.ReadFull(socksConn, buffer[:4]); err != nil {
		if err == io.EOF {
			r.log.Debug().Msg("Client closed SOCKS connection")
			return nil
		}
		return fmt.Errorf("read request header error: %w", err)
	}
	if buffer[0] != 0x05 {
		sendFailureAndClose(0x01)
		return fmt.Errorf("invalid socks version in request")
	}

	cmd := buffer[1]
	atyp := buffer[3]
	var targetAddr string
	var targetPort uint16

	// Parse address based on type
	switch atyp {
	case 0x01: // IPv4 (4 bytes addr + 2 bytes port)
		if _, err := io.ReadFull(socksConn, buffer[:6]); err != nil {
			sendFailureAndClose(0x01)
			return fmt.Errorf("read IPv4 address error: %w", err)
		}
		targetAddr = net.IP(buffer[:4]).String()
		targetPort = binary.BigEndian.Uint16(buffer[4:6])
	case 0x03: // Domain (1 byte len + domain + 2 bytes port)
		if _, err := io.ReadFull(socksConn, buffer[:1]); err != nil {
			sendFailureAndClose(0x01)
			return fmt.Errorf("read domain length error: %w", err)
		}
		domainLen := int(buffer[0])
		if _, err := io.ReadFull(socksConn, buffer[:domainLen+2]); err != nil {
			sendFailureAndClose(0x01)
			return fmt.Errorf("read domain error: %w", err)
		}
		targetAddr = string(buffer[:domainLen])
		targetPort = binary.BigEndian.Uint16(buffer[domainLen : domainLen+2])
	case 0x04: // IPv6 (16 bytes addr + 2 bytes port)
		if _, err := io.ReadFull(socksConn, buffer[:18]); err != nil {
			sendFailureAndClose(0x01)
			return fmt.Errorf("read IPv6 address error: %w", err)
		}
		targetAddr = net.IP(buffer[:16]).String()
		targetPort = binary.BigEndian.Uint16(buffer[16:18])
	default:
		sendFailureAndClose(0x08) // Address type not supported
		return fmt.Errorf("unsupported address type: %d", atyp)
	}

	// Generate unique client ID and connect ID
	channelID := uuid.New()
	r.log.Trace().Str("channel_id", channelID.String()).Msg("Starting SOCKS request handling")

	// Handle different commands
	switch cmd {
	case 0x01: // CONNECT
		channelQueue := make(chan BaseMessage, 1000)
		r.messageQueues.Store(channelID, channelQueue)
		r.flushOrphanData(channelID, channelQueue)
		defer r.disconnectChannel(channelID)

		if err := r.checkEntryAccess(ctx, targetAddr, int(targetPort)); err != nil {
			r.log.Warn().Str("address", targetAddr).Int("port", int(targetPort)).Msg("SOCKS CONNECT blocked by access control")
			sendFailureAndClose(0x02) // connection not allowed by ruleset
			return err
		}

		// Send connection request to server
		requestData := ConnectMessage{
			Protocol:  "tcp",
			Address:   targetAddr,
			Port:      int(targetPort),
			ChannelID: channelID,
		}
		channel := r.registerLogicalChannel(channelID, "tcp", ws, ws, channelPathRelay)
		channel.setRequest(requestData)
		channel.markResumeOwner()
		ws = channel
		r.log.Debug().Str("address", targetAddr).Int("port", int(targetPort)).Msg("Requesting TCP connecting to")
		r.logMessage(requestData, "send", ws.Label())
		if err := ws.WriteMessage(requestData); err != nil {
			// Return connection failure response to SOCKS client (0x04 = Host unreachable)
			resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
			socksConn.Write(resp)
			// Ensure the SOCKS connection is closed on failure
			_ = socksConn.Close()
			return fmt.Errorf("write connect request error: %w", err)
		}

		var response ConnectResponseMessage
		if !r.option.FastOpen {
			// Wait for response with timeout in normal mode
			select {
			case msg := <-channelQueue:
				var ok bool
				response, ok = msg.(ConnectResponseMessage)
				if !ok {
					r.log.Debug().Str("channel_id", channelID.String()).Msg("Unexpected message type in connect response queue")
					resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
					if _, err := socksConn.Write(resp); err != nil {
						return fmt.Errorf("write failure response error: %w", err)
					}
					// Close SOCKS connection because connect failed/invalid
					_ = socksConn.Close()
					return fmt.Errorf("unexpected message type for connect response")
				}
			case <-time.After(r.option.ConnectTimeout + 5*time.Second):
				r.log.Debug().Str("channel_id", channelID.String()).Str("addr", targetAddr).Int("port", int(targetPort)).Msg("Connect response timeout waiting on queue")
				// Return connection failure response to SOCKS client (0x04 = Host unreachable)
				resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
				if _, err := socksConn.Write(resp); err != nil {
					return fmt.Errorf("write failure response error: %w", err)
				}
				r.log.Debug().Str("addr", targetAddr).Int("port", int(targetPort)).Msg("Remote connection response timeout")
				// Close SOCKS connection after timeout
				_ = socksConn.Close()
				return nil
			}
			if !response.Success {
				r.log.Debug().Str("channel_id", channelID.String()).Str("error", response.Error).Msg("Connect response indicates failure")
				// Return connection failure response to SOCKS client (0x04 = Host unreachable)
				resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
				if _, err := socksConn.Write(resp); err != nil {
					return fmt.Errorf("write failure response error: %w", err)
				}
				r.log.Debug().Str("error", response.Error).Msg("Remote connection failed")
				// Close SOCKS connection when connect fails
				_ = socksConn.Close()
				return nil
			}
			r.log.Trace().Str("addr", targetAddr).Int("port", int(targetPort)).Msg("Remote successfully connected")
		} else {
			r.log.Trace().Str("addr", targetAddr).Int("port", int(targetPort)).Msg("Assume successful connection in fast-open mode")

			go func() {
				timer := time.NewTimer(r.option.ConnectTimeout + 5*time.Second)
				defer timer.Stop()

				select {
				case <-timer.C:
					if _, ok := r.connectionSuccessMap.LoadAndDelete(channelID); !ok {
						r.log.Debug().
							Str("addr", targetAddr).
							Int("port", int(targetPort)).
							Msg("Connection timeout without success confirmation")
						r.disconnectChannel(channelID)
					}
				case <-ctx.Done():
					return
				}
			}()
		}

		// Send success response to client
		resp := []byte{
			0x05,                   // version
			0x00,                   // success
			0x00,                   // reserved
			0x01,                   // IPv4
			0x00, 0x00, 0x00, 0x00, // IP address
			0x00, 0x00, // port
		}
		if _, err := socksConn.Write(resp); err != nil {
			return fmt.Errorf("write success response error: %w", err)
		}

		// Start TCP relay
		return r.HandleSocksTCPForward(ctx, ws, socksConn, channelID)

	case 0x03: // UDP ASSOCIATE
		// Create UDP socket
		udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("resolve UDP addr error: %w", err)
		}

		udpConn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			return fmt.Errorf("listen UDP error: %w", err)
		}

		localAddr := udpConn.LocalAddr().(*net.UDPAddr)

		// Create temporary queue for connection response
		connectQueue := make(chan BaseMessage, 1000)
		r.messageQueues.Store(channelID, connectQueue)
		r.flushOrphanData(channelID, connectQueue)
		defer r.disconnectChannel(channelID)

		// Send UDP associate request to server
		requestData := ConnectMessage{
			Protocol:  "udp",
			ChannelID: channelID,
		}
		channel := r.registerLogicalChannel(channelID, "udp", ws, ws, channelPathRelay)
		channel.setRequest(requestData)
		channel.markResumeOwner()
		ws = channel
		r.log.Debug().Msg("Requesting UDP Associate")
		r.logMessage(requestData, "send", ws.Label())
		if err := ws.WriteMessage(requestData); err != nil {
			udpConn.Close()
			return fmt.Errorf("write UDP request error: %w", err)
		}

		if !r.option.FastOpen {
			// Wait for response with timeout
			var response ConnectResponseMessage
			select {
			case msg := <-connectQueue:
				var ok bool
				response, ok = msg.(ConnectResponseMessage)
				if !ok {
					resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
					if _, err := socksConn.Write(resp); err != nil {
						return fmt.Errorf("write failure response error: %w", err)
					}
					return fmt.Errorf("unexpected message type for connect response")
				}
			case <-time.After(r.option.ConnectTimeout + 5*time.Second):
				udpConn.Close()
				return fmt.Errorf("UDP association response timeout")
			}

			if !response.Success {
				udpConn.Close()
				return fmt.Errorf("UDP association failed: %s", response.Error)
			}
		}

		// Send UDP associate response
		resp := []byte{
			0x05, // version
			0x00, // success
			0x00, // reserved
			0x01, // IPv4
		}
		resp = append(resp, localAddr.IP.To4()...)
		portBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(portBytes, uint16(localAddr.Port))
		resp = append(resp, portBytes...)

		if _, err := socksConn.Write(resp); err != nil {
			udpConn.Close()
			return fmt.Errorf("write UDP associate response error: %w", err)
		}

		r.log.Trace().Int("port", localAddr.Port).Msg("UDP association established")

		// HandleSocksUDPForward monitors the TCP connection for closure itself.
		return r.HandleSocksUDPForward(ctx, ws, udpConn, socksConn, channelID)

	default:
		return fmt.Errorf("unsupported command: %d", cmd)
	}
}

// HandleRemoteTCPForward handles remote TCP forwarding
func (r *Relay) HandleRemoteTCPForward(ctx context.Context, ws MessageWriter, remoteConn net.Conn, channelID uuid.UUID) error {
	channel, ok := r.logicalChannel(channelID)
	if !ok {
		channel = r.registerLogicalChannel(channelID, "tcp", ws, ws, channelPathRelay)
	}
	ws = channel
	// Initialize activity time
	r.updateActivityTime(channelID)

	// Create message queue for this channel if it doesn't exist
	var msgChan chan BaseMessage
	if queue, exists := r.messageQueues.Load(channelID); exists {
		msgChan = queue.(chan BaseMessage)
	} else {
		msgChan = make(chan BaseMessage, 1000)
		r.messageQueues.Store(channelID, msgChan)
	}
	defer r.disconnectChannel(channelID)

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// TCP to WebSocket
	go func() {
		defer wg.Done()
		sendManager := NewSendManager(ctx, r.log, channelID, ws, r, "tcp", errChan)
		sendManager.ProcessReads(remoteConn)
	}()

	// WebSocket to TCP
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				// Drain any remaining queued messages before exiting to avoid
				// losing final response bytes when context is cancelled
				for {
					select {
					case msg, ok := <-msgChan:
						if !ok {
							return
						}
						dataMsg, ok := msg.(DataMessage)
						if !ok {
							if msg != nil {
								r.log.Debug().Str("type", msg.GetType()).Msg("Unexpected message type for data")
							} else {
								r.log.Debug().Msg("Nil message received, skipping")
							}
							continue
						}
						// Update activity time
						r.updateActivityTime(channelID)
						if _, err := remoteConn.Write(dataMsg.Data); err != nil {
							errChan <- fmt.Errorf("remote write error: %w", err)
							return
						}
						if dataMsg.Sequence != 0 {
							if channel, ok := r.logicalChannel(channelID); ok {
								_ = channel.acknowledgeMessage(dataMsg.Sequence)
							}
						}
						r.log.Trace().Int("size", len(dataMsg.Data)).Msg("Sent TCP data to target (drain)")
					default:
						return
					}
				}
			case msg, ok := <-msgChan:
				if !ok {
					// Channel closed; exit goroutine cleanly
					return
				}
				dataMsg, ok := msg.(DataMessage)
				if !ok {
					// msg may be non-nil but not DataMessage; avoid nil interface deref
					if msg != nil {
						r.log.Debug().Str("type", msg.GetType()).Msg("Unexpected message type for data")
					} else {
						r.log.Debug().Msg("Nil message received, skipping")
					}
					continue
				}
				// Update activity time
				r.updateActivityTime(channelID)

				_, err := remoteConn.Write(dataMsg.Data)
				if err != nil {
					errChan <- fmt.Errorf("remote write error: %w", err)
					return
				}
				if dataMsg.Sequence != 0 {
					if channel, ok := r.logicalChannel(channelID); ok {
						_ = channel.acknowledgeMessage(dataMsg.Sequence)
					}
				}
				r.log.Trace().Int("size", len(dataMsg.Data)).Msg("Sent TCP data to target")
			}
		}
	}()

	// Wait for completion or error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		// On TCP side error/EOF, send a DisconnectMessage with reason
		disconnectMsg := DisconnectMessage{ChannelID: channelID}
		if err != nil {
			disconnectMsg.Error = err.Error()
		}
		r.logMessage(disconnectMsg, "send", ws.Label())
		_ = ws.WriteMessage(disconnectMsg)
		return err
	case <-done:
		return nil
	}
}

// HandleRemoteUDPForward handles remote UDP forwarding
func (r *Relay) HandleRemoteUDPForward(ctx context.Context, ws MessageWriter, udpConn *net.UDPConn, channelID uuid.UUID) error {
	channel, ok := r.logicalChannel(channelID)
	if !ok {
		channel = r.registerLogicalChannel(channelID, "udp", ws, ws, channelPathRelay)
	}
	ws = channel
	// Initialize activity time
	r.updateActivityTime(channelID)

	// Create message queue for this channel if it doesn't exist
	var msgChan chan BaseMessage
	if queue, exists := r.messageQueues.Load(channelID); exists {
		msgChan = queue.(chan BaseMessage)
	} else {
		msgChan = make(chan BaseMessage, 1000)
		r.messageQueues.Store(channelID, msgChan)
	}
	defer r.disconnectChannel(channelID)

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// UDP to WebSocket
	go func() {
		defer wg.Done()
		sendManager := NewSendManager(ctx, r.log, channelID, ws, r, "udp", errChan)
		sendManager.ProcessUDPReads(udpConn)
	}()

	// WebSocket to UDP
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgChan:
				if !ok {
					// Channel closed; exit goroutine cleanly
					return
				}
				// Type assert to DataMessage
				dataMsg, ok := msg.(DataMessage)
				if !ok {
					if msg != nil {
						r.log.Debug().Str("type", msg.GetType()).Msg("Unexpected message type for data")
					} else {
						r.log.Debug().Msg("Nil message received, skipping")
					}
					continue
				}
				// Update activity time
				r.updateActivityTime(channelID)

				// Resolve domain name if necessary
				var targetIP net.IP
				if ip := net.ParseIP(dataMsg.TargetAddr); ip != nil {
					targetIP = ip
				} else {
					// Attempt to resolve domain name
					addrs, err := net.LookupHost(dataMsg.TargetAddr)
					if err != nil {
						r.log.Debug().
							Err(err).
							Str("domain", dataMsg.TargetAddr).
							Msg("Failed to resolve domain name")
						continue
					}
					// Parse the first resolved address
					targetIP = net.ParseIP(addrs[0])
					if targetIP == nil {
						r.log.Debug().
							Str("addr", addrs[0]).
							Str("domain", dataMsg.TargetAddr).
							Msg("Failed to parse resolved IP address")
						continue
					}
				}

				targetAddr := &net.UDPAddr{
					IP:   targetIP,
					Port: dataMsg.TargetPort,
				}

				_, err := udpConn.WriteToUDP(dataMsg.Data, targetAddr)
				if err != nil {
					errChan <- fmt.Errorf("udp write error: %w", err)
					return
				}
				if dataMsg.Sequence != 0 {
					if channel, ok := r.logicalChannel(channelID); ok {
						_ = channel.acknowledgeMessage(dataMsg.Sequence)
					}
				}
				r.log.Trace().
					Int("size", len(dataMsg.Data)).
					Str("addr", targetAddr.String()).
					Str("original_addr", dataMsg.TargetAddr).
					Str("original_port", fmt.Sprintf("%d", dataMsg.TargetPort)).
					Msg("Sent UDP data to target")
			}
		}
	}()

	// Wait for completion or error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		return err
	case <-done:
		return nil
	}
}

// HandleSocksTCPForward handles TCP forwarding between SOCKS client and WebSocket
func (r *Relay) HandleSocksTCPForward(ctx context.Context, ws MessageWriter, socksConn net.Conn, channelID uuid.UUID) error {
	channel, ok := r.logicalChannel(channelID)
	if !ok {
		channel = r.registerLogicalChannel(channelID, "tcp", ws, ws, channelPathRelay)
	}
	ws = channel
	// Create a child context that can be cancelled
	ctx, cancel := context.WithCancel(ctx)
	r.tcpChannels.Store(channelID, cancel)
	defer func() {
		cancel()
		r.tcpChannels.Delete(channelID)
		r.lastActivity.Delete(channelID)
	}()

	// Always close the SOCKS TCP connection when the forwarding lifecycle ends
	defer func() { _ = socksConn.Close() }()

	// Send disconnect message
	var disconnectErr string
	defer func() {
		disconnectMsg := DisconnectMessage{
			ChannelID: channelID,
			Error:     disconnectErr,
		}
		r.logMessage(disconnectMsg, "send", ws.Label())
		_ = ws.WriteMessage(disconnectMsg)
	}()

	// Create message queue for this channel if it doesn't exist
	var msgChan chan BaseMessage
	if queue, exists := r.messageQueues.Load(channelID); exists {
		msgChan = queue.(chan BaseMessage)
	} else {
		msgChan = make(chan BaseMessage, 1000)
		r.messageQueues.Store(channelID, msgChan)
	}
	defer r.disconnectChannel(channelID)

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// SOCKS to WebSocket
	go func() {
		defer wg.Done()
		defer cancel() // Ensure context is cancelled on exit

		sendManager := NewSendManager(ctx, r.log, channelID, ws, r, "tcp", errChan)
		sendManager.ProcessReads(socksConn)
	}()

	// WebSocket to SOCKS
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				// Drain any remaining queued messages before exiting to avoid
				// losing final response bytes when context is cancelled
				for {
					select {
					case msg, ok := <-msgChan:
						if !ok {
							return
						}
						// Type assert to DataMessage
						dataMsg, ok := msg.(DataMessage)
						if !ok {
							if msg != nil {
								r.log.Debug().Str("type", msg.GetType()).Msg("Unexpected message type for data")
							} else {
								r.log.Debug().Msg("Nil message received, skipping")
							}
							continue
						}
						// Update activity time
						r.updateActivityTime(channelID)

						if _, err := socksConn.Write(dataMsg.Data); err != nil {
							errChan <- fmt.Errorf("socks write error: %w", err)
							return
						}
						if dataMsg.Sequence != 0 {
							if channel, ok := r.logicalChannel(channelID); ok {
								_ = channel.acknowledgeMessage(dataMsg.Sequence)
							}
						}
						r.log.Trace().Int("size", len(dataMsg.Data)).Msg("Sent TCP data to SOCKS (drain)")
					default:
						return
					}
				}
			case msg, ok := <-msgChan:
				if !ok {
					// Channel closed; exit goroutine cleanly
					return
				}
				// Type assert to DataMessage
				dataMsg, ok := msg.(DataMessage)
				if !ok {
					if msg != nil {
						r.log.Debug().Str("type", msg.GetType()).Msg("Unexpected message type for data")
					} else {
						r.log.Debug().Msg("Nil message received, skipping")
					}
					continue
				}
				// Update activity time
				r.updateActivityTime(channelID)

				_, err := socksConn.Write(dataMsg.Data)
				if err != nil {
					errChan <- fmt.Errorf("socks write error: %w", err)
					return
				}
				if dataMsg.Sequence != 0 {
					if channel, ok := r.logicalChannel(channelID); ok {
						_ = channel.acknowledgeMessage(dataMsg.Sequence)
					}
				}
				r.log.Trace().Int("size", len(dataMsg.Data)).Msg("Sent TCP data to SOCKS")
			}
		}
	}()

	// Wait for completion or error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		if err != nil {
			disconnectErr = err.Error()
		}
		return err
	case <-done:
		return nil
	}
}

// HandleSocksUDPForward handles SOCKS5 UDP forwarding
func (r *Relay) HandleSocksUDPForward(ctx context.Context, ws MessageWriter, udpConn *net.UDPConn, socksConn net.Conn, channelID uuid.UUID) error {
	channel, ok := r.logicalChannel(channelID)
	if !ok {
		channel = r.registerLogicalChannel(channelID, "udp", ws, ws, channelPathRelay)
	}
	ws = channel
	// Create a child context that can be cancelled
	ctx, cancel := context.WithCancel(ctx)
	r.udpChannels.Store(channelID, cancel)
	defer func() {
		cancel()
		r.udpChannels.Delete(channelID)
		r.lastActivity.Delete(channelID)
	}()

	// Send disconnect message on exit
	defer func() {
		disconnectMsg := DisconnectMessage{
			ChannelID: channelID,
		}
		r.logMessage(disconnectMsg, "send", ws.Label())
		ws.WriteMessage(disconnectMsg)
	}()

	// Create message queue for this channel if it doesn't exist
	var msgChan chan BaseMessage
	if queue, exists := r.messageQueues.Load(channelID); exists {
		msgChan = queue.(chan BaseMessage)
	} else {
		msgChan = make(chan BaseMessage, 1000)
		r.messageQueues.Store(channelID, msgChan)
	}
	defer r.disconnectChannel(channelID)

	var wg sync.WaitGroup
	wg.Add(3)
	errChan := make(chan error, 3)

	// Monitor TCP connection for closure
	go func() {
		defer wg.Done()
		defer cancel() // Cancel context when TCP connection closes
		buffer := make([]byte, 1)
		socksConn.Read(buffer)
		udpConn.Close()
		r.log.Trace().Msg("SOCKS TCP connection closed")
	}()

	// UDP to WebSocket with SOCKS5 header handling
	go func() {
		defer wg.Done()
		defer cancel() // Cancel context when this goroutine exits
		buffer := make([]byte, r.option.BufferSize)
		for {
			n, remoteAddr, err := udpConn.ReadFromUDP(buffer)
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					errChan <- fmt.Errorf("udp read error: %w", err)
				}
				return
			}

			r.udpClientAddrs.Store(channelID, remoteAddr)

			// Parse SOCKS UDP header
			if n > 3 { // Minimal UDP header
				atyp := buffer[3]
				var targetAddr string
				var targetPort int
				var payload []byte

				switch atyp {
				case 0x01: // IPv4
					addrBytes := buffer[4:8]
					targetAddr = net.IP(addrBytes).String()
					portBytes := buffer[8:10]
					targetPort = int(binary.BigEndian.Uint16(portBytes))
					payload = buffer[10:n]
				case 0x03: // Domain
					addrLen := int(buffer[4])
					addrBytes := buffer[5 : 5+addrLen]
					targetAddr = string(addrBytes)
					portBytes := buffer[5+addrLen : 7+addrLen]
					targetPort = int(binary.BigEndian.Uint16(portBytes))
					payload = buffer[7+addrLen : n]
				case 0x04: // IPv6
					addrBytes := buffer[4:20]
					targetAddr = net.IP(addrBytes).String()
					portBytes := buffer[20:22]
					targetPort = int(binary.BigEndian.Uint16(portBytes))
					payload = buffer[22:n]
				default:
					r.log.Trace().Msg("Cannot parse UDP packet from associated port")
					continue
				}

				if err := r.checkEntryAccess(ctx, targetAddr, targetPort); err != nil {
					r.log.Warn().Str("address", targetAddr).Int("port", targetPort).Msg("UDP packet blocked by access control")
					continue
				}

				// Update activity time
				r.updateActivityTime(channelID)

				msg := DataMessage{
					Protocol:    "udp",
					ChannelID:   channelID,
					Data:        payload,
					TargetAddr:  targetAddr,
					TargetPort:  targetPort,
					Compression: r.determineCompression(len(payload)),
				}
				r.logMessage(msg, "send", ws.Label())
				if err := ws.WriteMessage(msg); err != nil {
					// Log the error but don't immediately exit - client may have disconnected
					// but we should still handle any remaining UDP packets gracefully
					r.log.Warn().Err(err).Msg("Failed to send UDP response to client")
					errChan <- fmt.Errorf("websocket write error: %w", err)
					return
				}
				r.log.Trace().Int("size", len(payload)).Msg("Sent UDP data to WebSocket")
			}
		}
	}()

	// WebSocket to UDP with SOCKS5 header handling
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg, ok := <-msgChan:
				if !ok {
					// Channel closed; exit goroutine cleanly
					return
				}
				// Type assert to DataMessage
				dataMsg, ok := msg.(DataMessage)
				if !ok {
					if msg != nil {
						r.log.Debug().Str("type", msg.GetType()).Msg("Unexpected message type for data")
					} else {
						r.log.Debug().Msg("Nil message received, skipping")
					}
					continue
				}
				// Update activity time
				r.updateActivityTime(channelID)

				// Construct SOCKS UDP header
				udpHeader := []byte{0, 0, 0} // RSV + FRAG

				// Try parsing as IPv4
				if ip := net.ParseIP(dataMsg.Address); ip != nil {
					if ip4 := ip.To4(); ip4 != nil {
						udpHeader = append(udpHeader, 0x01) // IPv4
						udpHeader = append(udpHeader, ip4...)
					} else {
						udpHeader = append(udpHeader, 0x04) // IPv6
						udpHeader = append(udpHeader, ip...)
					}
				} else {
					// Treat as domain name
					domainBytes := []byte(dataMsg.Address)
					udpHeader = append(udpHeader, 0x03) // Domain
					udpHeader = append(udpHeader, byte(len(domainBytes)))
					udpHeader = append(udpHeader, domainBytes...)
				}

				portBytes := make([]byte, 2)
				binary.BigEndian.PutUint16(portBytes, uint16(dataMsg.Port))
				udpHeader = append(udpHeader, portBytes...)
				udpHeader = append(udpHeader, dataMsg.Data...)

				addr, ok := r.udpClientAddrs.Load(dataMsg.ChannelID)
				if !ok {
					r.log.Warn().Msg("Dropping UDP packet: no socks client address available")
					continue
				}

				clientAddr := addr.(*net.UDPAddr)
				if _, err := udpConn.WriteToUDP(udpHeader, clientAddr); err != nil {
					errChan <- fmt.Errorf("udp write error: %w", err)
					return
				}
				if dataMsg.Sequence != 0 {
					if channel, ok := r.logicalChannel(channelID); ok {
						_ = channel.acknowledgeMessage(dataMsg.Sequence)
					}
				}
				r.log.Trace().Int("size", len(dataMsg.Data)).Str("addr", clientAddr.String()).Msg("Sent UDP data to SOCKS")
			}
		}
	}()

	// Wait for completion or error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		return err
	case <-done:
		return nil
	}
}

// Add this helper method to Relay struct
func (r *Relay) logMessage(msg BaseMessage, direction string, label string) {
	// Only process if debug level is enabled
	if !r.log.Trace().Enabled() {
		return
	}

	logEvent := r.log.Trace().Str("label", label)

	// Create a copy for logging
	data, _ := json.Marshal(msg)
	var msgMap map[string]interface{}
	json.Unmarshal(data, &msgMap)

	// Remove sensitive fields and add data length
	if data, ok := msgMap["data"].(string); ok {
		msgMap["data_length"] = len(data)
		delete(msgMap, "data")
	}
	if _, ok := msgMap["token"]; ok {
		msgMap["token"] = "..."
	}

	logEvent = logEvent.Interface("msg", msgMap)
	logEvent.Msgf("WebSocket message TYPE=%s DIRECTION=%s", msg.GetType(), direction)
}

// Close gracefully shuts down the Relay
func (r *Relay) Close() {
	r.log.Trace().Msg("Closing relay")
	close(r.done)

	// Cancel all active TCP channels
	r.tcpChannels.Range(func(key, value interface{}) bool {
		if cancel, ok := value.(context.CancelFunc); ok {
			cancel()
		}
		r.tcpChannels.Delete(key)
		return true
	})

	// Cancel all active UDP channels
	r.udpChannels.Range(func(key, value interface{}) bool {
		if cancel, ok := value.(context.CancelFunc); ok {
			cancel()
		}
		r.udpChannels.Delete(key)
		return true
	})

	// Clear all maps
	r.messageQueues.Range(func(key, value interface{}) bool {
		r.messageQueues.Delete(key)
		return true
	})
	r.udpClientAddrs.Range(func(key, value interface{}) bool {
		r.udpClientAddrs.Delete(key)
		return true
	})
	r.lastActivity.Range(func(key, value interface{}) bool {
		r.lastActivity.Delete(key)
		return true
	})
}

// determineCompression decides compression method based on data size
func (r *Relay) determineCompression(dataSize int) byte {
	if dataSize >= r.option.CompressionThreshold {
		return DataCompressionGzip
	}
	return DataCompressionNone
}

// disconnectChannel handles cleanup of channel resources
func (r *Relay) disconnectChannel(channelID uuid.UUID) {
	// Queue the channel for delayed cleanup
	select {
	case r.cleanupQueue <- channelID:
		r.log.Trace().Str("channel_id", channelID.String()).Msg("Queued channel for cleanup")
	default:
		// Queue full, cleanup synchronously in a goroutine as fallback
		go r.doCleanup(channelID)
	}
}

// cleanupWorker processes cleanup tasks from the queue
func (r *Relay) cleanupWorker() {
	for {
		select {
		case <-r.done:
			return
		case channelID := <-r.cleanupQueue:
			r.doCleanup(channelID)
		}
	}
}

// doCleanup performs the actual cleanup with delay
func (r *Relay) doCleanup(channelID uuid.UUID) {
	r.log.Trace().Str("channel_id", channelID.String()).Msg("Disconnecting channel")
	time.Sleep(5 * time.Second)
	if cancelVal, ok := r.tcpChannels.LoadAndDelete(channelID); ok {
		if cancel, ok := cancelVal.(context.CancelFunc); ok {
			cancel()
		}
	}
	if cancelVal, ok := r.udpChannels.LoadAndDelete(channelID); ok {
		if cancel, ok := cancelVal.(context.CancelFunc); ok {
			cancel()
		}
	}
	r.messageQueues.Delete(channelID)
	if channel, ok := r.logicalChannels.LoadAndDelete(channelID); ok {
		channel.(*logicalChannel).Close()
	}
	r.udpClientAddrs.Delete(channelID)
	r.connectionSuccessMap.Delete(channelID)
	r.lastActivity.Delete(channelID)
	r.log.Trace().Str("channel_id", channelID.String()).Msg("Disconnected channel")
}

// scheduleChannelExpiry tears down channels still bound to a departed link
// once the transport grace window expires, unless they were switched onto a
// live link in the meantime. This covers idle channels that have no traffic
// to trigger the write-retry teardown path (writeWithTransportGrace).
func (r *Relay) scheduleChannelExpiry(ws MessageWriter, grace time.Duration) {
	if ws == nil || grace <= 0 {
		return
	}
	var ids []uuid.UUID
	r.logicalChannels.Range(func(key, value any) bool {
		if value.(*logicalChannel).currentWriter() == ws {
			ids = append(ids, key.(uuid.UUID))
		}
		return true
	})
	for _, id := range ids {
		id := id
		time.AfterFunc(grace, func() {
			ch, ok := r.logicalChannel(id)
			if !ok {
				return
			}
			if ch.currentWriter() != ws {
				// Rebound onto a live link; no longer pending expiry.
				return
			}
			// Try to notify the peer before closing so it tears the channel
			// down promptly; this fails silently while the link is down.
			disc := DisconnectMessage{ChannelID: id, Error: "transport link not restored"}
			r.logMessage(disc, "send", ch.Label())
			_ = ch.WriteMessage(disc)
			r.disconnectChannel(id)
			r.log.Warn().Str("channel_id", id.String()).Msg("Channel expired after transport grace")
		})
	}
}

// SetConnectionSuccess sets the connection success status for a channel
func (r *Relay) SetConnectionSuccess(channelID uuid.UUID) {
	r.log.Trace().Str("channel_id", channelID.String()).Msg("Setting connection success")
	r.connectionSuccessMap.Store(channelID, true)
}
