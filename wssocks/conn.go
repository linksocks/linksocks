package wssocks

import (
	"encoding/binary"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// WSConn wraps a websocket.Conn with mutex protection
type WSConn struct {
	conn *websocket.Conn
	mu   sync.Mutex

	// Batch processing fields
	batchMu    sync.Mutex
	batchData  [][]byte
	batchTimer *time.Timer

	// Message queue for received batch messages
	messageQueue []BaseMessage
	queueMu      sync.Mutex

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
		conn:         conn,
		batchData:    make([][]byte, 0, 16),
		messageQueue: make([]BaseMessage, 0),
		label:        label,
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

// packBatchMessages combines multiple messages into a single byte slice
// Format: [total_count(4 bytes)][msg1_len(4 bytes)][msg1_data][msg2_len(4 bytes)][msg2_data]...
func (c *WSConn) packBatchMessages(messages [][]byte) []byte {
	totalLen := 4 // For message count
	for _, msg := range messages {
		totalLen += 4 + len(msg) // 4 bytes for length + message data
	}

	result := make([]byte, totalLen)

	// Write message count
	binary.BigEndian.PutUint32(result[0:4], uint32(len(messages)))

	offset := 4
	for _, msg := range messages {
		// Write message length
		binary.BigEndian.PutUint32(result[offset:offset+4], uint32(len(msg)))
		offset += 4
		// Write message data
		copy(result[offset:], msg)
		offset += len(msg)
	}

	return result
}

// unpackBatchMessages extracts multiple messages from a single byte slice
func (c *WSConn) unpackBatchMessages(data []byte) ([]BaseMessage, error) {
	if len(data) < 4 {
		return nil, fmt.Errorf("invalid batch message: too short")
	}

	count := binary.BigEndian.Uint32(data[0:4])
	messages := make([]BaseMessage, 0, count)

	offset := 4
	for i := uint32(0); i < count; i++ {
		if offset+4 > len(data) {
			return nil, fmt.Errorf("invalid batch message: incomplete message length")
		}

		msgLen := binary.BigEndian.Uint32(data[offset : offset+4])
		offset += 4

		if offset+int(msgLen) > len(data) {
			return nil, fmt.Errorf("invalid batch message: incomplete message data")
		}

		msg, err := ParseMessage(data[offset : offset+int(msgLen)])
		if err != nil {
			return nil, err
		}

		messages = append(messages, msg)
		offset += int(msgLen)
	}

	return messages, nil
}

// SyncWriteBinary performs thread-safe binary writes to the websocket connection
func (c *WSConn) SyncWriteBinary(data []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn.WriteMessage(websocket.BinaryMessage, data)
}

// ReadMessage reads a BaseMessage from the websocket connection
func (c *WSConn) ReadMessage() (BaseMessage, error) {
	// First check if we have queued messages
	c.queueMu.Lock()
	if len(c.messageQueue) > 0 {
		msg := c.messageQueue[0]
		c.messageQueue = c.messageQueue[1:]
		c.queueMu.Unlock()
		return msg, nil
	}
	c.queueMu.Unlock()

	// If no queued messages, read from websocket
	_, data, err := c.conn.ReadMessage()
	if err != nil {
		return nil, err
	}

	// Try to parse as batch message first
	messages, err := c.unpackBatchMessages(data)
	if err != nil {
		// If parsing as batch fails, try parsing as single message
		return ParseMessage(data)
	}

	if len(messages) == 0 {
		return nil, nil
	}

	// Queue additional messages if any
	if len(messages) > 1 {
		c.queueMu.Lock()
		c.messageQueue = append(c.messageQueue, messages[1:]...)
		c.queueMu.Unlock()
	}

	// Return the first message
	return messages[0], nil
}

// WriteMessage writes a BaseMessage to the websocket connection
func (c *WSConn) WriteMessage(msg BaseMessage) error {
	data, err := PackMessage(msg)
	if err != nil {
		return err
	}

	c.batchMu.Lock()
	defer c.batchMu.Unlock()

	c.batchData = append(c.batchData, data)

	// Send immediately if more than 16 packets are queued
	if len(c.batchData) >= 16 {
		return c.flushBatch()
	}

	// Set or reset the timer
	if c.batchTimer != nil {
		c.batchTimer.Stop()
	}
	c.batchTimer = time.AfterFunc(20*time.Millisecond, func() {
		c.batchMu.Lock()
		defer c.batchMu.Unlock()
		c.flushBatch()
	})

	return nil
}

// flushBatch sends all cached data
func (c *WSConn) flushBatch() error {
	if len(c.batchData) == 0 {
		return nil
	}

	// Pack all messages into a single batch
	batchedData := c.packBatchMessages(c.batchData)

	// Clear the cache
	c.batchData = c.batchData[:0]

	return c.SyncWriteBinary(batchedData)
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
