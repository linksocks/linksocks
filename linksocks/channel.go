package linksocks

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

type channelPath string

type fallbackWriterProvider interface {
	Fallback() MessageWriter
}

const (
	channelPathRelay  channelPath = "relay"
	channelPathDirect channelPath = "direct"
)

type logicalChannel struct {
	id       uuid.UUID
	protocol string

	mu          sync.Mutex
	writeMu     sync.Mutex
	path        channelPath
	writer      MessageWriter
	fallback    MessageWriter
	request     ConnectMessage
	resumeOwner bool
	migration   bool
	sequence    uint64
	received    uint64
	pending     map[uint64]DataMessage
	closed      bool
	// suspendSince records when the transport link first failed; zero means
	// the link is healthy. Writes retry within the grace window.
	suspendSince time.Time
}

func newLogicalChannel(id uuid.UUID, protocol string, writer MessageWriter, fallback MessageWriter, path channelPath) *logicalChannel {
	return &logicalChannel{id: id, protocol: protocol, writer: writer, fallback: fallback, path: path, pending: make(map[uint64]DataMessage)}
}

func (c *logicalChannel) WriteMessage(msg BaseMessage) error {
	if msg == nil {
		return errors.New("logical channel: nil message")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("logical channel: closed")
	}
	w := c.writer
	migration := c.migration
	if data, ok := msg.(DataMessage); ok && migration {
		if data.ChannelID == uuid.Nil {
			data.ChannelID = c.id
		}
		c.sequence++
		data.Sequence = c.sequence
		c.pending[data.Sequence] = data
		msg = data
	}
	c.mu.Unlock()

	if w == nil {
		return errors.New("logical channel: no active writer")
	}
	if err := w.WriteMessage(msg); err != nil {
		if _, isControl := msg.(ChannelMigrateMessage); isControl {
			return err
		}
		if err := c.switchToFallback("write failure"); err != nil {
			return &transportDownError{cause: err}
		}
		if _, ok := msg.(DataMessage); ok && migration {
			return nil
		}
		if err := c.currentWriter().WriteMessage(msg); err != nil {
			return &transportDownError{cause: err}
		}
		return nil
	}
	return nil
}

func (c *logicalChannel) Label() string {
	c.mu.Lock()
	path := c.path
	c.mu.Unlock()
	return fmt.Sprintf("channel/%s/%s", c.id, path)
}

func (c *logicalChannel) Switch(writer MessageWriter, path channelPath) error {
	if writer == nil {
		return errors.New("logical channel: nil writer")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return errors.New("logical channel: closed")
	}
	c.writer = writer
	c.path = path
	return nil
}

func (c *logicalChannel) currentWriter() MessageWriter {
	c.mu.Lock()
	w := c.writer
	c.mu.Unlock()
	return w
}

func (c *logicalChannel) switchToFallback(reason string) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("logical channel: closed")
	}
	if c.fallback == nil {
		c.mu.Unlock()
		return errors.New("logical channel: no relay fallback")
	}
	c.writer = c.fallback
	c.path = channelPathRelay
	fallback := c.fallback
	migration := c.migration
	c.mu.Unlock()
	if !migration {
		return nil
	}
	if err := fallback.WriteMessage(ChannelMigrateMessage{ChannelID: c.id, Reason: reason}); err != nil {
		return err
	}
	return c.replayPending()
}

func (c *logicalChannel) switchToDirect(writer MessageWriter) error {
	return c.Switch(writer, channelPathDirect)
}

func (c *logicalChannel) setRequest(request ConnectMessage) {
	c.mu.Lock()
	c.request = request
	c.mu.Unlock()
}

func (c *logicalChannel) markResumeOwner() {
	c.mu.Lock()
	c.resumeOwner = true
	c.mu.Unlock()
}

func (c *logicalChannel) enableMigration(enabled bool) {
	c.mu.Lock()
	c.migration = enabled
	c.mu.Unlock()
}

func (c *logicalChannel) migrationEnabled() bool {
	c.mu.Lock()
	enabled := c.migration
	c.mu.Unlock()
	return enabled
}

func (c *logicalChannel) canResume() bool {
	c.mu.Lock()
	canResume := c.resumeOwner && c.request.ChannelID != uuid.Nil && !c.closed
	c.mu.Unlock()
	return canResume
}

func (c *logicalChannel) requestMessage() ConnectMessage {
	c.mu.Lock()
	request := c.request
	c.mu.Unlock()
	return request
}

func (c *logicalChannel) switchToRelay() error {
	c.mu.Lock()
	fallback := c.fallback
	migration := c.migration
	c.mu.Unlock()
	if fallback == nil {
		return errors.New("logical channel: no relay fallback")
	}
	if err := c.Switch(fallback, channelPathRelay); err != nil {
		return err
	}
	if !migration {
		return nil
	}
	return c.replayPending()
}

func (c *logicalChannel) acceptData(msg DataMessage) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if msg.Sequence == 0 {
		return true
	}
	if msg.Sequence <= c.received {
		return false
	}
	if msg.Sequence != c.received+1 {
		return false
	}
	c.received = msg.Sequence
	return true
}

func (c *logicalChannel) rejectData(sequence uint64) {
	if sequence == 0 {
		return
	}
	c.mu.Lock()
	if c.received == sequence {
		c.received = sequence - 1
	}
	c.mu.Unlock()
}

func (c *logicalChannel) isDuplicate(sequence uint64) bool {
	if sequence == 0 {
		return false
	}
	c.mu.Lock()
	duplicate := sequence <= c.received
	c.mu.Unlock()
	return duplicate
}

func (c *logicalChannel) acknowledge(sequence uint64) {
	c.mu.Lock()
	delete(c.pending, sequence)
	c.mu.Unlock()
}

func (c *logicalChannel) pendingMessages() []DataMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	result := make([]DataMessage, 0, len(c.pending))
	for _, msg := range c.pending {
		result = append(result, msg)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	return result
}

func (c *logicalChannel) acknowledgeMessage(sequence uint64) error {
	if !c.migrationEnabled() {
		return nil
	}
	return c.WriteMessage(ChannelDataAckMessage{ChannelID: c.id, Sequence: sequence})
}

func (c *logicalChannel) replayPending() error {
	w := c.currentWriter()
	if w == nil {
		return errors.New("logical channel: no active writer")
	}
	for _, msg := range c.pendingMessages() {
		if err := w.WriteMessage(msg); err != nil {
			return err
		}
	}
	return nil
}

func (c *logicalChannel) Path() string {
	c.mu.Lock()
	path := c.path
	c.mu.Unlock()
	return string(path)
}

// suspend marks the transport link as down and reports whether the channel
// is still inside its grace window. Call on each failed write.
func (c *logicalChannel) suspend(grace time.Duration) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	if c.suspendSince.IsZero() {
		c.suspendSince = now
	}
	return now.Sub(c.suspendSince) <= grace
}

// resume clears the suspended state after a successful write.
func (c *logicalChannel) resume() {
	c.mu.Lock()
	c.suspendSince = time.Time{}
	c.mu.Unlock()
}

// suspended reports whether the transport link is currently marked down.
func (c *logicalChannel) suspended() bool {
	c.mu.Lock()
	suspended := !c.suspendSince.IsZero()
	c.mu.Unlock()
	return suspended
}

func (c *logicalChannel) Close() {
	c.mu.Lock()
	c.closed = true
	c.writer = nil
	c.mu.Unlock()
}
