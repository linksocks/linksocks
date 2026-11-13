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

// relayFallbackRefuser marks a transport that must not be replaced by the
// plaintext relay writer when it fails. A protocol V2 direct channel reserves
// the relay writer for fresh channels that run the Noise handshake; resuming an
// existing one there would move its payload out of the end-to-end encryption.
type relayFallbackRefuser interface {
	RefuseRelayFallback() bool
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
	// writerGen counts writer switches, flushedGen records the generation the
	// pending buffer was last replayed for. A write that finds the writer
	// changed replays pending data before sending anything new, so data left
	// behind by the switch is never overtaken by data sent after it: the peer
	// drops out-of-order sequences.
	writerGen  uint64
	flushedGen uint64
	// resumeFn re-establishes the channel on the relay after its transport
	// failed. Only the entry side sets it; the peer waits for that resume.
	resumeFn func() error
	// resumeWait is closed when the writer changes or the channel closes, so a
	// failed write can wait for the peer's resume instead of failing the
	// connection.
	resumeWait chan struct{}
	// resumeGrace bounds how long a failed write waits for the writer change.
	resumeGrace time.Duration
	// suspendSince records when the transport link first failed; zero means
	// the link is healthy. Writes retry within the grace window.
	suspendSince time.Time
}

// channelResumeGrace is the fallback wait for a peer-driven resume when the
// relay does not configure a transport grace.
const channelResumeGrace = 10 * time.Second

func newLogicalChannel(id uuid.UUID, protocol string, writer MessageWriter, fallback MessageWriter, path channelPath) *logicalChannel {
	return &logicalChannel{id: id, protocol: protocol, writer: writer, fallback: fallback, path: path, pending: make(map[uint64]DataMessage)}
}

func (c *logicalChannel) WriteMessage(msg BaseMessage) error {
	if msg == nil {
		return errors.New("logical channel: nil message")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	prepared, w, migration, err := c.prepareWrite(msg)
	if err != nil {
		return err
	}
	if w == nil {
		return errors.New("logical channel: no active writer")
	}
	// Data left behind by a writer switch goes first, so the peer's sequencer
	// never sees a gap and never has to guess which stream is authoritative.
	if err := c.flushPending(migration); err != nil {
		return c.recoverWrite(w, prepared, migration, err)
	}
	if err := w.WriteMessage(prepared); err != nil {
		if _, isControl := prepared.(ChannelMigrateMessage); isControl {
			return err
		}
		return c.recoverWrite(w, prepared, migration, err)
	}
	c.resume()
	return nil
}

// prepareWrite sequences data messages when migration is enabled and returns
// the message along with the writer it must go to.
func (c *logicalChannel) prepareWrite(msg BaseMessage) (BaseMessage, MessageWriter, bool, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return nil, nil, false, errors.New("logical channel: closed")
	}
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
	return msg, c.writer, migration, nil
}

// flushPending replays unacknowledged data when the writer changed since the
// last flush. Caller holds writeMu.
func (c *logicalChannel) flushPending(migration bool) error {
	if !migration {
		return nil
	}
	c.mu.Lock()
	stale := c.writerGen != c.flushedGen
	c.mu.Unlock()
	if !stale {
		return nil
	}
	return c.replayPendingLocked()
}

// recoverWrite handles a write that failed. Writers that may hand their payload
// to the plaintext relay keep the V1 fallback; the sealed direct writers
// re-establish the channel through a fresh end-to-end session instead, so the
// payload never leaves the encryption.
func (c *logicalChannel) recoverWrite(failed MessageWriter, msg BaseMessage, migration bool, cause error) error {
	refuser, refuses := failed.(relayFallbackRefuser)
	if !refuses || !refuser.RefuseRelayFallback() {
		if err := c.switchToFallback("write failure"); err != nil {
			return &transportDownError{cause: err}
		}
		if data, ok := msg.(DataMessage); ok && migration && data.Sequence != 0 {
			// switchToFallback replayed the pending buffer, this frame included.
			return nil
		}
		if err := c.currentWriter().WriteMessage(msg); err != nil {
			return &transportDownError{cause: err}
		}
		return nil
	}

	if err := c.ensureSealedWriter(failed, cause); err != nil {
		return &transportDownError{cause: err}
	}
	// The new writer has nothing on the wire yet: flush whatever the failed
	// transport left unacknowledged before sending new data.
	if err := c.replayPendingLocked(); err != nil {
		return &transportDownError{cause: err}
	}
	if data, ok := msg.(DataMessage); ok && migration && data.Sequence != 0 {
		return nil
	}
	if err := c.currentWriter().WriteMessage(msg); err != nil {
		return &transportDownError{cause: err}
	}
	return nil
}

// ensureSealedWriter makes sure the channel has a writer other than the one
// that just failed. The entry side runs the resume handshake itself; the peer
// waits for the entry side's resume to install the new writer.
func (c *logicalChannel) ensureSealedWriter(failed MessageWriter, cause error) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("logical channel: closed")
	}
	if c.writer != failed {
		c.mu.Unlock()
		return nil
	}
	fn := c.resumeFn
	grace := c.resumeGrace
	c.mu.Unlock()

	if fn != nil {
		if err := fn(); err != nil {
			return fmt.Errorf("resume channel on relay: %w", err)
		}
		return nil
	}

	if grace <= 0 {
		grace = channelResumeGrace
	}
	deadline := time.Now().Add(grace)
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return errors.New("logical channel: closed")
		}
		if c.writer != failed {
			c.mu.Unlock()
			return nil
		}
		wait := c.resumeWait
		if wait == nil {
			wait = make(chan struct{})
			c.resumeWait = wait
		}
		c.mu.Unlock()

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return fmt.Errorf("no relay resume within %s: %w", grace, cause)
		}
		timer := time.NewTimer(remaining)
		select {
		case <-wait:
			timer.Stop()
		case <-timer.C:
			return fmt.Errorf("no relay resume within %s: %w", grace, cause)
		}
	}
}

func (c *logicalChannel) setResumeFn(fn func() error) {
	c.mu.Lock()
	c.resumeFn = fn
	c.mu.Unlock()
}

// wakeResumeWaitersLocked releases writers blocked on a failed transport.
// Caller holds mu.
func (c *logicalChannel) wakeResumeWaitersLocked() {
	if c.resumeWait != nil {
		close(c.resumeWait)
		c.resumeWait = nil
	}
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
	c.writerGen++
	c.wakeResumeWaitersLocked()
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
	if refuser, ok := c.writer.(relayFallbackRefuser); ok && refuser.RefuseRelayFallback() {
		c.mu.Unlock()
		return errors.New("logical channel: relay fallback disabled for this transport")
	}
	if c.fallback == nil {
		c.mu.Unlock()
		return errors.New("logical channel: no relay fallback")
	}
	c.writer = c.fallback
	c.path = channelPathRelay
	c.writerGen++
	c.wakeResumeWaitersLocked()
	fallback := c.fallback
	migration := c.migration
	c.mu.Unlock()
	if !migration {
		return nil
	}
	if err := fallback.WriteMessage(ChannelMigrateMessage{ChannelID: c.id, Reason: reason}); err != nil {
		return err
	}
	return c.replayPendingLocked()
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
	if refuser, ok := c.writer.(relayFallbackRefuser); ok && refuser.RefuseRelayFallback() {
		// A sealed transport must never be swapped for the plaintext relay
		// writer, even if a peer asks it to migrate.
		c.mu.Unlock()
		return errors.New("logical channel: plaintext migration disabled for this transport")
	}
	c.mu.Unlock()
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

// replayPending flushes unacknowledged data over the current writer. Safe to
// call from dispatcher goroutines; writers already holding writeMu use
// replayPendingLocked.
func (c *logicalChannel) replayPending() error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.replayPendingLocked()
}

// replayPendingLocked is replayPending for callers that hold writeMu, so the
// replay cannot interleave with a new write.
func (c *logicalChannel) replayPendingLocked() error {
	c.mu.Lock()
	w := c.writer
	gen := c.writerGen
	result := make([]DataMessage, 0, len(c.pending))
	for _, msg := range c.pending {
		result = append(result, msg)
	}
	c.mu.Unlock()
	if w == nil {
		return errors.New("logical channel: no active writer")
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Sequence < result[j].Sequence })
	for _, msg := range result {
		if err := w.WriteMessage(msg); err != nil {
			return err
		}
	}
	c.mu.Lock()
	if c.writerGen == gen {
		c.flushedGen = gen
	}
	c.mu.Unlock()
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
	c.wakeResumeWaitersLocked()
	c.mu.Unlock()
}
