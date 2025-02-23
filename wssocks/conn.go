package wssocks

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// WSConn wraps a websocket.Conn with mutex protection
type WSConn struct {
	conn *websocket.Conn
	mu   sync.Mutex

	pingTime time.Time  // Track when ping was sent
	pingMu   sync.Mutex // Mutex for ping timing

	label string
}

func (c *WSConn) Label() string {
	return c.label
}

func (c *WSConn) setLabel(label string) {
	c.label = label
}

// NewWSConn creates a new mutex-protected websocket connection
func NewWSConn(conn *websocket.Conn, label string, logger zerolog.Logger) *WSConn {
	wsConn := &WSConn{
		conn:  conn,
		label: label,
	}

	// Set read limit
	conn.SetReadLimit(32 * 1024 * 1024) // 32MB max message size

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

// Close closes the underlying websocket connection
func (c *WSConn) Close() error {
	return c.conn.Close()
}
