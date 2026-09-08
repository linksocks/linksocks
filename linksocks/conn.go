package linksocks

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

const (
	MaxWebSocketMessageSize = 32 * 1024 * 1024 // 32MB max message size
	// defaultWriteTimeout bounds a single WebSocket write so a half-open
	// link cannot block a dispatcher or forwarding loop forever.
	defaultWriteTimeout = 60 * time.Second
	// defaultPingInterval is how often a keepalive pings the peer so a
	// half-open link is detected and cleaned up.
	defaultPingInterval = 30 * time.Second
	// readTimeout is how long a link may be silent (no frame at all) before
	// the dispatcher treats it as dead. Two missed ping rounds are fatal.
	readTimeout = 2 * defaultPingInterval
	// connStaleAfter is how long a link may go without a pong before its
	// channels are considered owned by a dead link.
	connStaleAfter = 3 * defaultPingInterval
)

// WSConn wraps a websocket.Conn with mutex protection
type WSConn struct {
	conn *websocket.Conn
	mu   sync.Mutex

	// All timestamps are unix-millis stored atomically.
	pingSentMillis int64 // when the last ping was sent
	lastPongMillis int64 // when the last pong arrived; zero means never
	rttNS          int64 // last measured RTT in nanoseconds

	label    string
	clientIP string
}

// isStale reports whether the link has been silent long enough to be treated
// as dead, even though it may still be registered as online.
func (c *WSConn) isStale(now time.Time) bool {
	last := time.UnixMilli(atomic.LoadInt64(&c.lastPongMillis))
	// A link that never answered a ping is stale; one that never got a pong
	// chance yet (fresh link) is not.
	return !last.IsZero() && now.Sub(last) > connStaleAfter
}

func (c *WSConn) Label() string {
	return c.label
}

func (c *WSConn) setLabel(label string) {
	c.label = label
}

// GetClientIP returns the client IP address
func (c *WSConn) GetClientIP() string {
	return c.clientIP
}

// SetClientIPFromRequest extracts and sets the client IP from HTTP request
func (c *WSConn) SetClientIPFromRequest(r *http.Request) {
	c.clientIP = getClientIPFromRequest(r)
}

// getClientIPFromRequest extracts client IP from HTTP request
func getClientIPFromRequest(r *http.Request) string {
	// Check CF-Connecting-IP header first
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}

	// Check X-Forwarded-For header
	if ip := r.Header.Get("X-Forwarded-For"); ip != "" {
		// X-Forwarded-For can contain multiple IPs, take the first one
		if idx := strings.Index(ip, ","); idx != -1 {
			return strings.TrimSpace(ip[:idx])
		}
		return strings.TrimSpace(ip)
	}

	// Get the remote address
	ip, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return ip
}

// NewWSConn creates a new mutex-protected websocket connection
func NewWSConn(conn *websocket.Conn, label string, logger zerolog.Logger) *WSConn {
	wsConn := &WSConn{
		conn:  conn,
		label: label,
	}

	// Set read limit
	conn.SetReadLimit(MaxWebSocketMessageSize)
	wsConn.armReadDeadline()

	// Set pong handler to measure RTT
	conn.SetPongHandler(func(string) error {
		now := time.Now()
		atomic.StoreInt64(&wsConn.lastPongMillis, now.UnixMilli())
		if sent := time.UnixMilli(atomic.LoadInt64(&wsConn.pingSentMillis)); !sent.IsZero() {
			atomic.StoreInt64(&wsConn.rttNS, int64(now.Sub(sent)))

			// Log the actual RTT when pong is received
			logger.
				Trace().
				Int64("rtt_ms", now.Sub(sent).Milliseconds()).
				Msg("WebSocket pong received, RTT measured")
		}

		wsConn.armReadDeadline()
		return nil
	})

	return wsConn
}

// armReadDeadline re-arms the read deadline; any incoming frame (data or
// control) keeps the connection alive while a silent one expires.
func (c *WSConn) armReadDeadline() {
	c.conn.SetReadDeadline(time.Now().Add(readTimeout))
}

// TouchReadDeadline re-arms the read deadline after a received frame.
func (c *WSConn) TouchReadDeadline() {
	c.armReadDeadline()
}

// Ping sends a WebSocket ping and records the send time so the pong handler
// can measure the round-trip time.
func (c *WSConn) Ping() error {
	atomic.StoreInt64(&c.pingSentMillis, time.Now().UnixMilli())
	return c.SyncWriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
}

// KeepAlive pings the peer at a fixed interval until ctx is done or a ping
// fails, so a half-open link is detected and cleaned up.
func (c *WSConn) KeepAlive(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		interval = defaultPingInterval
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-t.C:
			if err := c.Ping(); err != nil {
				return err
			}
		}
	}
}

// RTT returns the most recently measured round-trip time, or zero if no pong
// has been received yet.
func (c *WSConn) RTT() time.Duration {
	return time.Duration(atomic.LoadInt64(&c.rttNS))
}

// SyncWriteBinary performs a mutex-protected WebSocket binary write with a
// bounded write deadline, so a dead link cannot block callers indefinitely.
func (c *WSConn) SyncWriteBinary(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.SetWriteDeadline(time.Now().Add(defaultWriteTimeout)); err != nil {
		return err
	}
	defer c.conn.SetWriteDeadline(time.Time{})
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

// ReadMessage reads a BaseMessage from the websocket connection
func (c *WSConn) ReadMessage() (BaseMessage, error) {
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return nil, err
	}
	return ParseMessage(data)
}

// ReadMessageWithVersion reads a BaseMessage and returns the peer's protocol version
func (c *WSConn) ReadMessageWithVersion() (BaseMessage, byte, error) {
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return nil, 0, err
	}
	return ParseMessageWithVersion(data)
}

// WriteMessage writes a BaseMessage to the websocket connection
func (c *WSConn) WriteMessage(msg BaseMessage) error {
	data, err := PackMessage(msg)
	if err != nil {
		return err
	}
	return c.SyncWriteBinary(data)
}

// SyncWriteControl performs thread-safe control message writes.
func (c *WSConn) SyncWriteControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(messageType, data, deadline)
}

// MeasureLatency sends a ping and waits for the pong to measure the
// round-trip time. Returns the one-way latency (RTT/2) or an error on
// timeout or write failure.
func (c *WSConn) MeasureLatency(timeout time.Duration) (time.Duration, error) {
	pongReceived := make(chan struct{}, 1)
	originalHandler := c.conn.PongHandler()

	c.conn.SetPongHandler(func(appData string) error {
		select {
		case pongReceived <- struct{}{}:
		default:
		}
		if originalHandler != nil {
			return originalHandler(appData)
		}
		return nil
	})
	defer c.conn.SetPongHandler(originalHandler)

	if err := c.Ping(); err != nil {
		return 0, err
	}

	select {
	case <-pongReceived:
		return time.Duration(atomic.LoadInt64(&c.rttNS)) / 2, nil
	case <-time.After(timeout):
		return 0, errors.New("timeout waiting for pong")
	}
}

// Close closes the underlying websocket connection
func (c *WSConn) Close() error {
	return c.conn.Close()
}
