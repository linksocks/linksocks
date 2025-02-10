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

// SyncWriteJSON performs thread-safe JSON writes to the websocket connection
func (c *WSConn) SyncWriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteJSON(v)
}

// SyncWriteControl performs thread-safe control message writes to the websocket connection
func (c *WSConn) SyncWriteControl(messageType int, data []byte, deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteControl(messageType, data, deadline)
}

// ReadJSON reads a JSON message from the websocket connection
func (c *WSConn) ReadJSON(v interface{}) error {
	return c.conn.ReadJSON(v)
}

// Close closes the underlying websocket connection
func (c *WSConn) Close() error {
	return c.conn.Close()
}
