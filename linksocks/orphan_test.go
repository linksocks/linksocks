package linksocks

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// TestOrphanFlush verifies buffered orphan data drains into the channel
// queue in order once the channel binds, and the orphan entry is removed.
func TestOrphanFlush(t *testing.T) {
	r := NewRelay(zerolog.Nop(), NewDefaultRelayOption())
	id := uuid.New()
	r.bufferOrphanData(DataMessage{ChannelID: id, Data: []byte("a")})
	r.bufferOrphanData(DataMessage{ChannelID: id, Data: []byte("b")})

	msgChan := make(chan BaseMessage, 10)
	r.flushOrphanData(id, msgChan)

	if len(msgChan) != 2 {
		t.Fatalf("expected 2 buffered messages, got %d", len(msgChan))
	}
	var got []string
	for i := 0; i < 2; i++ {
		dm := (<-msgChan).(DataMessage)
		got = append(got, string(dm.Data))
	}
	if got[0] != "a" || got[1] != "b" {
		t.Fatalf("expected order [a b], got %v", got)
	}

	r.orphanMu.Lock()
	_, ok := r.orphanData[id]
	r.orphanMu.Unlock()
	if ok {
		t.Fatalf("orphan entry not removed after flush")
	}
}

// TestOrphanExpiry verifies data buffered for a channel that never binds
// is dropped after orphanDataTTL.
func TestOrphanExpiry(t *testing.T) {
	r := NewRelay(zerolog.Nop(), NewDefaultRelayOption())
	id := uuid.New()
	r.bufferOrphanData(DataMessage{ChannelID: id, Data: []byte("x")})

	deadline := time.Now().Add(orphanDataTTL + 5*time.Second)
	for time.Now().Before(deadline) {
		r.orphanMu.Lock()
		_, ok := r.orphanData[id]
		r.orphanMu.Unlock()
		if !ok {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("orphan data not dropped within TTL")
}