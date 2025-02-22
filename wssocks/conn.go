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

	// Batch processing related fields
	buffer     [][]byte
	bufferMu   sync.Mutex
	batchTimer *time.Timer
}

// NewWSConn creates a new mutex-protected websocket connection
func NewWSConn(conn *websocket.Conn) *WSConn {
	ws := &WSConn{
		conn:   conn,
		buffer: make([][]byte, 0),
	}
	ws.startBatchTimer()
	return ws
}

// SyncWriteBinary performs thread-safe binary writes to the websocket connection
func (c *WSConn) SyncWriteBinary(data []byte) error {
	c.bufferMu.Lock()
	c.buffer = append(c.buffer, data)
	bufferSize := len(c.buffer)
	c.bufferMu.Unlock()

	// Flush immediately if buffer size exceeds threshold
	if bufferSize >= 16 {
		return c.flushBuffer()
	}
	return nil
}

// flushBuffer sends all buffered data in batch
func (c *WSConn) flushBuffer() error {
	c.bufferMu.Lock()
	if len(c.buffer) == 0 {
		c.bufferMu.Unlock()
		return nil
	}

	// Calculate total size and combine data
	totalSize := 0
	for _, b := range c.buffer {
		totalSize += len(b)
	}

	combinedData := make([]byte, 0, totalSize)
	for _, b := range c.buffer {
		combinedData = append(combinedData, b...)
	}

	c.buffer = c.buffer[:0] // Clear the buffer
	c.bufferMu.Unlock()

	// Perform actual write with original mutex protection
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, combinedData)
}

// startBatchTimer starts a timer to periodically flush the buffer
func (c *WSConn) startBatchTimer() {
	c.batchTimer = time.NewTimer(20 * time.Millisecond)
	go func() {
		for range c.batchTimer.C {
			c.flushBuffer()
			c.batchTimer.Reset(20 * time.Millisecond)
		}
	}()
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
	if c.batchTimer != nil {
		c.batchTimer.Stop()
	}
	c.flushBuffer() // Ensure all data is sent before closing
	return c.conn.Close()
}
