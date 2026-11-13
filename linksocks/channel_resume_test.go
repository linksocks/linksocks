package linksocks

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
)

// sealedFailWriter models a sealed direct transport that died: it refuses the
// plaintext relay fallback and fails every write.
type sealedFailWriter struct {
	err   error
	calls atomic.Int64
}

func (w *sealedFailWriter) WriteMessage(BaseMessage) error {
	w.calls.Add(1)
	return w.err
}

func (w *sealedFailWriter) Label() string { return "sealed-fail" }

func (w *sealedFailWriter) RefuseRelayFallback() bool { return true }

func (w *sealedFailWriter) Fallback() MessageWriter { return &migrationTestWriter{} }

func newSealedResumeChannel(t *testing.T, failed MessageWriter) (*logicalChannel, *migrationTestWriter) {
	t.Helper()
	relayFallback := &migrationTestWriter{}
	channel := newLogicalChannel(uuid.New(), "tcp", failed, relayFallback, channelPathDirect)
	channel.enableMigration(true)
	return channel, relayFallback
}

// A failed sealed write must re-establish the channel through the resume
// function and replay the unacknowledged data exactly once, in sequence order,
// before the caller's next message goes out.
func TestSealedWriteFailureResumesAndFlushesInOrder(t *testing.T) {
	failed := &sealedFailWriter{err: errors.New("transport down")}
	channel, relayFallback := newSealedResumeChannel(t, failed)
	resumed := &migrationTestWriter{}
	var resumeCalls atomic.Int64

	channel.setResumeFn(func() error {
		resumeCalls.Add(1)
		return channel.Switch(resumed, channelPathRelay)
	})

	// First message: the writer fails, the resume installs a new one, and the
	// replay delivers the message on it.
	if err := channel.WriteMessage(DataMessage{Protocol: "tcp", Data: []byte("first")}); err != nil {
		t.Fatalf("write over dead transport: %v", err)
	}
	if got := resumeCalls.Load(); got != 1 {
		t.Fatalf("resume ran %d times, want 1", got)
	}
	if got := failed.calls.Load(); got != 1 {
		t.Fatalf("failed writer saw %d writes, want 1", got)
	}

	msgs := resumed.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("resumed writer got %d messages, want exactly the replayed one", len(msgs))
	}
	first, ok := msgs[0].(DataMessage)
	if !ok {
		t.Fatalf("resumed message type = %T, want DataMessage", msgs[0])
	}
	if string(first.Data) != "first" || first.Sequence != 1 {
		t.Fatalf("replayed message = %q seq %d, want %q seq 1", first.Data, first.Sequence, "first")
	}

	// Second message: no new resume, no re-send of the first frame.
	if err := channel.WriteMessage(DataMessage{Protocol: "tcp", Data: []byte("second")}); err != nil {
		t.Fatalf("write after resume: %v", err)
	}
	if got := resumeCalls.Load(); got != 1 {
		t.Fatalf("resume ran %d times, want 1 (no second handshake)", got)
	}
	msgs = resumed.snapshot()
	if len(msgs) != 2 {
		t.Fatalf("resumed writer got %d messages, want 2", len(msgs))
	}
	second, ok := msgs[1].(DataMessage)
	if !ok || string(second.Data) != "second" || second.Sequence != 2 {
		t.Fatalf("second message = %v, want %q seq 2", msgs[1], "second")
	}

	if len(relayFallback.snapshot()) != 0 {
		t.Fatalf("plaintext relay fallback received %d messages, want none", len(relayFallback.snapshot()))
	}
}

// The responder side cannot start the handshake itself; it must wait for the
// peer's resume and then flush what the dead transport left behind.
func TestSealedWriteFailureWaitsForPeerResume(t *testing.T) {
	failed := &sealedFailWriter{err: errors.New("transport down")}
	channel, _ := newSealedResumeChannel(t, failed)
	channel.resumeGrace = 3 * time.Second
	resumed := &migrationTestWriter{}

	done := make(chan error, 1)
	go func() {
		done <- channel.WriteMessage(DataMessage{Protocol: "tcp", Data: []byte("peer")})
	}()

	// Give the writer time to fail and enter the wait.
	time.Sleep(150 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("write returned %v before any resume", err)
	default:
	}

	// The peer's resume arrives: the channel switches writer and the waiting
	// write finishes by flushing the pending frame.
	if err := channel.Switch(resumed, channelPathRelay); err != nil {
		t.Fatalf("switch: %v", err)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("write after resume: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write did not finish after the writer switched")
	}

	msgs := resumed.snapshot()
	if len(msgs) != 1 {
		t.Fatalf("resumed writer got %d messages, want 1", len(msgs))
	}
	data, ok := msgs[0].(DataMessage)
	if !ok || string(data.Data) != "peer" || data.Sequence != 1 {
		t.Fatalf("replayed message = %v, want %q seq 1", msgs[0], "peer")
	}
}

// Without a resume the failed write must surface the transport failure so the
// connection closes instead of hanging.
func TestSealedWriteFailureWithoutResumeTimesOut(t *testing.T) {
	failed := &sealedFailWriter{err: errors.New("transport down")}
	channel, _ := newSealedResumeChannel(t, failed)
	channel.resumeGrace = 200 * time.Millisecond

	start := time.Now()
	err := channel.WriteMessage(DataMessage{Protocol: "tcp", Data: []byte("lost")})
	if err == nil {
		t.Fatal("write over a dead transport without a resume must fail")
	}
	var down *transportDownError
	if !errors.As(err, &down) {
		t.Fatalf("write error = %v, want a transportDownError", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("write waited %s, want the configured grace", elapsed)
	}
}

// A closed channel must release writers waiting for a resume.
func TestSealedWriteWaitReleasedOnClose(t *testing.T) {
	failed := &sealedFailWriter{err: errors.New("transport down")}
	channel, _ := newSealedResumeChannel(t, failed)
	channel.resumeGrace = 5 * time.Second

	done := make(chan error, 1)
	go func() {
		done <- channel.WriteMessage(DataMessage{Protocol: "tcp", Data: []byte("waiting")})
	}()
	time.Sleep(150 * time.Millisecond)

	channel.Close()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("write on a closed channel must fail")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("closing the channel did not release the waiting write")
	}
}

// Sequence bookkeeping: duplicates are rejected and re-acknowledged, gaps are
// rejected without advancing the window, and a rejected delivery rolls back.
func TestChannelSequenceBookkeeping(t *testing.T) {
	channel, _ := newSealedResumeChannel(t, &migrationTestWriter{})

	if !channel.acceptData(DataMessage{ChannelID: channel.id, Sequence: 1}) {
		t.Fatal("first sequence must be accepted")
	}
	if channel.acceptData(DataMessage{ChannelID: channel.id, Sequence: 1}) {
		t.Fatal("duplicate sequence must be rejected")
	}
	if !channel.isDuplicate(1) {
		t.Fatal("sequence 1 must be reported as duplicate")
	}
	if channel.isDuplicate(2) {
		t.Fatal("sequence 2 must not be reported as duplicate")
	}
	if channel.acceptData(DataMessage{ChannelID: channel.id, Sequence: 3}) {
		t.Fatal("a gap must not be accepted")
	}
	if !channel.acceptData(DataMessage{ChannelID: channel.id, Sequence: 2}) {
		t.Fatal("the missing sequence must still be accepted")
	}
	channel.rejectData(2)
	if !channel.acceptData(DataMessage{ChannelID: channel.id, Sequence: 2}) {
		t.Fatal("a rejected delivery must be acceptable again")
	}
	if channel.acceptData(DataMessage{ChannelID: channel.id, Sequence: 0}) == false {
		t.Fatal("unsequenced data must always be accepted")
	}
}
