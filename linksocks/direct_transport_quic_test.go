package linksocks

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// One writer carries one channel's frames. It must stay on one link while that
// link lives: choosing a link per write spreads a channel over several links,
// and the peer merges frames by channel id in arrival order, which breaks the
// sealed stream's ordering (a handshake frame that overtakes another makes the
// peer read the wrong message).
func TestClientRelayWriterPinsOneLink(t *testing.T) {
	client := &LinkSocksClient{
		websockets: []*WSConn{{label: "0"}, {label: "1"}},
	}
	writer, ok := newClientRelayWriter(client).(*clientRelayWriter)
	if !ok {
		t.Fatal("newClientRelayWriter returned an unexpected type")
	}

	first := writer.pinnedLink()
	if first == nil {
		t.Fatal("no link was pinned with two live links")
	}
	// The pool's picker rotates per call; the pin must ignore that.
	for i := 0; i < 4; i++ {
		if got := writer.pinnedLink(); got != first {
			t.Fatalf("pin moved on its own: %s -> %s", first.Label(), got.Label())
		}
	}
	// Reporting the label must not change routing either.
	if label := writer.Label(); label == "" {
		t.Fatal("label is empty")
	}
	if got := writer.pinnedLink(); got != first {
		t.Fatalf("reading the label moved the pin: %s -> %s", first.Label(), got.Label())
	}

	// A failed write releases the pin so the next frame can use a live link.
	writer.releaseLink(first)
	second := writer.pinnedLink()
	if second == nil {
		t.Fatal("no link was pinned after the first was released")
	}
	if second == first {
		t.Fatalf("released link %s was pinned again", first.Label())
	}

	// A link that left the pool must not stay pinned: the writer moves to the
	// surviving link instead of writing into a dead one.
	client.mu.Lock()
	for i, ws := range client.websockets {
		if ws == second {
			client.websockets[i] = nil
		}
	}
	client.mu.Unlock()
	survivor := writer.pinnedLink()
	if survivor == nil {
		t.Fatal("no link was pinned while one was still alive")
	}
	if survivor == second {
		t.Fatalf("dead link %s stayed pinned", second.Label())
	}

	// With no live link at all nothing can be pinned.
	client.mu.Lock()
	for i := range client.websockets {
		client.websockets[i] = nil
	}
	client.mu.Unlock()
	if got := writer.pinnedLink(); got != nil {
		t.Fatalf("pinned a link that is not in the pool: %s", got.Label())
	}
}

// recordingWriter captures every message written through it so tests can
// assert what the channel actually put on the wire.
type recordingWriter struct {
	sent []BaseMessage
}

func (w *recordingWriter) WriteMessage(msg BaseMessage) error {
	w.sent = append(w.sent, msg)
	return nil
}

func (w *recordingWriter) Label() string { return "recording" }

// deliveryTestRig builds a relay with one registered channel and its receive
// queue, the way the direct read loop sees it.
func deliveryTestRig(t *testing.T, queueSize int) (*LinkSocksClient, *Relay, *logicalChannel, chan BaseMessage) {
	t.Helper()
	relay := NewRelay(zerolog.Nop(), NewDefaultRelayOption())
	t.Cleanup(relay.Close)

	client := &LinkSocksClient{relay: relay, log: zerolog.Nop()}
	chID := uuid.New()
	writer := &recordingWriter{}
	channel := relay.registerLogicalChannel(chID, "tcp", writer, writer, channelPathDirect)
	queue := make(chan BaseMessage, queueSize)
	relay.messageQueues.Store(chID, queue)

	return client, relay, channel, queue
}

// A full receive queue must not drop sequenced data: the frame waits for room
// instead, so a slow consumer can never punch a hole into the stream (the
// peer would discard everything after a gap).
func TestDirectDeliveryWaitsForQueueRoom(t *testing.T) {
	oldWait := directQueueFullWait
	directQueueFullWait = 5 * time.Second
	defer func() { directQueueFullWait = oldWait }()

	client, _, channel, queue := deliveryTestRig(t, 1)
	queue <- DataMessage{ChannelID: channel.id, Sequence: 99} // occupy the only slot

	done := make(chan struct{})
	go func() {
		client.deliverDataMessage(DataMessage{ChannelID: channel.id, Sequence: 1, Data: []byte("payload")})
		close(done)
	}()

	select {
	case <-done:
		t.Fatal("sequenced data was dropped instead of waiting for queue room")
	case <-time.After(150 * time.Millisecond):
		// Still waiting for room, as it must.
	}

	<-queue // make room; the waiting frame must land

	select {
	case msg := <-queue:
		dm, ok := msg.(DataMessage)
		if !ok {
			t.Fatalf("delivered message has type %T, want DataMessage", msg)
		}
		if dm.Sequence != 1 || string(dm.Data) != "payload" {
			t.Fatalf("delivered message = seq %d %q, want seq 1 %q", dm.Sequence, dm.Data, "payload")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the frame did not land after the queue had room")
	}

	<-done
	channel.mu.Lock()
	received := channel.received
	channel.mu.Unlock()
	if received != 1 {
		t.Fatalf("channel.received = %d, want 1 (the delivery must advance the sequencer)", received)
	}
}

// A queue that makes no progress at all within the budget must not eat the
// frame silently: the sequence rolls back (so a replay can still deliver it)
// and the channel is torn down so the application sees a failure.
func TestDirectDeliveryClosesChannelWhenQueueStaysFull(t *testing.T) {
	oldWait := directQueueFullWait
	directQueueFullWait = 100 * time.Millisecond
	defer func() { directQueueFullWait = oldWait }()

	client, relay, channel, queue := deliveryTestRig(t, 1)
	queue <- DataMessage{ChannelID: channel.id, Sequence: 99}

	done := make(chan struct{})
	go func() {
		client.deliverDataMessage(DataMessage{ChannelID: channel.id, Sequence: 1, Data: []byte("stuck")})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("delivery never gave up on a permanently full queue")
	}

	channel.mu.Lock()
	received := channel.received
	channel.mu.Unlock()
	if received != 0 {
		t.Fatalf("channel.received = %d, want 0 after the rollback", received)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, ok := relay.messageQueues.Load(channel.id); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the channel was not torn down after its queue stayed full")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Duplicate sequenced frames are consumed without waiting for queue room:
// re-acknowledging a frame that was already delivered must never stall behind
// a full queue.
func TestDirectDeliveryDropsDuplicatesWithoutBlocking(t *testing.T) {
	oldWait := directQueueFullWait
	directQueueFullWait = 5 * time.Second
	defer func() { directQueueFullWait = oldWait }()

	client, _, channel, queue := deliveryTestRig(t, 1)
	// Mark sequence 1 as already consumed, then fill the queue.
	if !channel.acceptData(DataMessage{ChannelID: channel.id, Sequence: 1}) {
		t.Fatal("the sequencer refused a fresh frame")
	}
	queue <- DataMessage{ChannelID: channel.id, Sequence: 99}

	done := make(chan struct{})
	go func() {
		client.deliverDataMessage(DataMessage{ChannelID: channel.id, Sequence: 1, Data: []byte("again")})
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a duplicate frame blocked on a full queue instead of being dropped")
	}
}
