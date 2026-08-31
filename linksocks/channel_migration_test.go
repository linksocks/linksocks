package linksocks

import (
	"errors"
	"sync"
	"testing"

	"github.com/google/uuid"
)

type migrationTestWriter struct {
	mu       sync.Mutex
	messages []BaseMessage
	err      error
}

func (w *migrationTestWriter) WriteMessage(msg BaseMessage) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return w.err
	}
	w.messages = append(w.messages, msg)
	return nil
}

func (w *migrationTestWriter) Label() string {
	return "test"
}

func (w *migrationTestWriter) snapshot() []BaseMessage {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]BaseMessage(nil), w.messages...)
}

func TestLogicalChannelSwitchAndReplay(t *testing.T) {
	id := uuid.New()
	relay := &migrationTestWriter{}
	direct := &migrationTestWriter{}
	channel := newLogicalChannel(id, "tcp", direct, relay, channelPathDirect)
	channel.enableMigration(true)

	data := DataMessage{Protocol: "tcp", ChannelID: id, Data: []byte("payload")}
	if err := channel.WriteMessage(data); err != nil {
		t.Fatalf("write data: %v", err)
	}
	if got := channel.Path(); got != string(channelPathDirect) {
		t.Fatalf("direct path = %q", got)
	}

	if err := channel.switchToFallback("test"); err != nil {
		t.Fatalf("switch to fallback: %v", err)
	}

	messages := relay.snapshot()
	if len(messages) != 2 {
		t.Fatalf("relay messages = %d, want migration plus replay", len(messages))
	}
	if _, ok := messages[0].(ChannelMigrateMessage); !ok {
		t.Fatalf("first relay message = %T, want ChannelMigrateMessage", messages[0])
	}
	replayed, ok := messages[1].(DataMessage)
	if !ok || replayed.Sequence != 1 {
		t.Fatalf("replayed message = %#v, want sequence 1", messages[1])
	}
}

func TestLogicalChannelWriteFailureFallsBackAndReplays(t *testing.T) {
	id := uuid.New()
	relay := &migrationTestWriter{}
	direct := &migrationTestWriter{err: errors.New("direct unavailable")}
	channel := newLogicalChannel(id, "tcp", direct, relay, channelPathDirect)
	channel.enableMigration(true)

	if err := channel.WriteMessage(DataMessage{Protocol: "tcp", ChannelID: id, Data: []byte("payload")}); err != nil {
		t.Fatalf("write after fallback: %v", err)
	}
	if got := channel.Path(); got != string(channelPathRelay) {
		t.Fatalf("path = %q, want relay", got)
	}
	messages := relay.snapshot()
	if len(messages) != 2 {
		t.Fatalf("relay messages = %d, want migration plus replay", len(messages))
	}
}

func TestLogicalChannelWithoutMigrationUsesLegacyDataPath(t *testing.T) {
	id := uuid.New()
	relay := &migrationTestWriter{}
	direct := &migrationTestWriter{err: errors.New("direct unavailable")}
	channel := newLogicalChannel(id, "tcp", direct, relay, channelPathDirect)

	if err := channel.WriteMessage(DataMessage{Protocol: "tcp", ChannelID: id, Data: []byte("payload")}); err != nil {
		t.Fatalf("write after fallback: %v", err)
	}
	if got := channel.Path(); got != string(channelPathRelay) {
		t.Fatalf("path = %q, want relay", got)
	}
	messages := relay.snapshot()
	if len(messages) != 1 {
		t.Fatalf("relay messages = %d, want one legacy data message", len(messages))
	}
	data, ok := messages[0].(DataMessage)
	if !ok {
		t.Fatalf("relay message = %T, want DataMessage", messages[0])
	}
	if data.Sequence != 0 {
		t.Fatalf("legacy data sequence = %d, want zero", data.Sequence)
	}
	if len(channel.pendingMessages()) != 0 {
		t.Fatal("legacy channel retained pending messages")
	}
}

func TestProtocolSupportsMigration(t *testing.T) {
	tests := []struct {
		name string
		v    byte
		want bool
	}{
		{name: "missing version", v: 0x00, want: false},
		{name: "legacy minor", v: 0x01, want: false},
		{name: "migration version", v: MigrationProtocolVersion, want: true},
		{name: "newer minor", v: 0x03, want: true},
		{name: "different major", v: 0x10, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := protocolSupportsMigration(tt.v); got != tt.want {
				t.Fatalf("protocolSupportsMigration(0x%02x) = %v, want %v", tt.v, got, tt.want)
			}
		})
	}
}

func TestLogicalChannelRejectDataDoesNotAdvanceSequence(t *testing.T) {
	channel := newLogicalChannel(uuid.New(), "tcp", &migrationTestWriter{}, &migrationTestWriter{}, channelPathRelay)
	msg := DataMessage{Sequence: 1}
	if !channel.acceptData(msg) {
		t.Fatal("first data message was rejected")
	}
	channel.rejectData(1)
	if !channel.acceptData(msg) {
		t.Fatal("rejected data was not available for retry")
	}
}
