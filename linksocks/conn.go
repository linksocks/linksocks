package linksocks

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

const (
	MaxWebSocketMessageSize = 32 * 1024 * 1024 // 32MB max message size
)

// WSConn wraps a websocket.Conn with mutex protection
type WSConn struct {
	conn *websocket.Conn
	mu   sync.Mutex

	pingTime time.Time  // Track when ping was sent
	pingMu   sync.Mutex // Mutex for ping timing

	label    string
	clientIP string
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

	// Set pong handler to measure RTT
	conn.SetPongHandler(func(string) error {
		wsConn.pingMu.Lock()
		rtt := time.Since(wsConn.pingTime)
		wsConn.pingMu.Unlock()

		// Log the actual RTT when pong is received
		logger.
			Trace().
			Int64("rtt_ms", rtt.Milliseconds()).
			Msg("Received pong, RTT measured")

		return conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	return wsConn
}

// SyncWriteBinary performs thread-safe binary writes to the websocket connection
func (c *WSConn) SyncWriteBinary(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
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

// WriteMessage writes a BaseMessage to the websocket connection
func (c *WSConn) WriteMessage(msg BaseMessage) error {
	data, err := PackMessage(msg)
	if err != nil {
		return err
	}
	return c.SyncWriteBinary(data)
}

// SyncWriteControl performs thread-safe control message writes and tracks ping time
func (c *WSConn) SyncWriteControl(messageType int, data []byte, deadline time.Time) error {
	if messageType == websocket.PingMessage {
		c.pingMu.Lock()
		c.pingTime = time.Now()
		c.pingMu.Unlock()
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(messageType, data, deadline)
}

// MeasureLatency sends a ping and waits for pong to measure latency
// Returns the one-way latency (RTT/2) or error if timeout
func (c *WSConn) MeasureLatency(timeout time.Duration) (time.Duration, error) {
	pongReceived := make(chan time.Duration, 1)
	originalHandler := c.conn.PongHandler()

	c.conn.SetPongHandler(func(appData string) error {
		c.pingMu.Lock()
		rtt := time.Since(c.pingTime)
		c.pingMu.Unlock()

		select {
		case pongReceived <- rtt / 2:
		default:
		}

		if originalHandler != nil {
			return originalHandler(appData)
		}
		return c.conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	})

	if err := c.SyncWriteControl(websocket.PingMessage, nil, time.Now().Add(timeout)); err != nil {
		c.conn.SetPongHandler(originalHandler)
		return 0, err
	}

	select {
	case latency := <-pongReceived:
		c.conn.SetPongHandler(originalHandler)
		return latency, nil
	case <-time.After(timeout):
		c.conn.SetPongHandler(originalHandler)
		return 0, errors.New("timeout waiting for pong")
	}
}

// Close closes the underlying websocket connection
func (c *WSConn) Close() error {
	return c.conn.Close()
}
