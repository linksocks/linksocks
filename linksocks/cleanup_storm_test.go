package linksocks

import (
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// A saturated cleanup queue must apply backpressure to the caller instead of
// spawning one cleanup goroutine per overflowed channel.
func TestDisconnectChannelNoGoroutineStorm(t *testing.T) {
	r := NewRelay(zerolog.Nop(), NewDefaultRelayOption())
	defer r.Close()

	// Fill the queue completely (4 slots may already be in workers' hands).
	for i := 0; i < cleanupQueueSize+4; i++ {
		r.disconnectChannel(uuid.New())
	}

	minCount := func() int {
		n := runtime.NumGoroutine()
		time.Sleep(50 * time.Millisecond)
		if m := runtime.NumGoroutine(); m < n {
			n = m
		}
		return n
	}
	base := minCount()

	// The caller must block; the only growth is the caller goroutine itself.
	blocked := make(chan struct{})
	go func() {
		r.disconnectChannel(uuid.New())
		close(blocked)
	}()
	time.Sleep(200 * time.Millisecond)
	select {
	case <-blocked:
		t.Fatalf("disconnectChannel did not backpressure on a saturated queue")
	default:
	}

	after := minCount()
	if after > base+1 {
		t.Fatalf("goroutine count grew from %d to %d with saturated cleanup queue: overflow spawns per-channel goroutines",
			base, after)
	}
}
