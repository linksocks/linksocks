package wssocks

import (
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSConn wraps a websocket.Conn with mutex protection
type WSConn struct {
	conn *websocket.Conn
	mu   sync.Mutex
}

// NewWSConn creates a new mutex-protected websocket connection
func NewWSConn(conn *websocket.Conn) *WSConn {
	return &WSConn{
		conn: conn,
	}
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

// SyncWriteControl performs thread-safe control message writes to the websocket connection
func (c *WSConn) SyncWriteControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(messageType, data, deadline)
}

// Close closes the underlying websocket connection
func (c *WSConn) Close() error {
	return c.conn.Close()
}
