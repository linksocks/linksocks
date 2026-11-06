package linksocks

import (
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
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

// A failed write during drainConnectorReturn requeues the message at the tail
// of the FIFO. Any messages still buffered behind it then leapfrog the failed
// one, corrupting the stream order that bufferConnectorReturn's docs promise.
func TestDrainConnectorReturnPreservesOrder(t *testing.T) {
	srv := NewLinkSocksServer(&ServerOption{Logger: zerolog.Nop()})
	defer srv.Close()

	// Real WS pair: the server side is the drain target; closing the client
	// side forces the server-side write to fail once the message is dequeued.
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	var serverWS *websocket.Conn
	upgraded := make(chan struct{})
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		serverWS = c
		close(upgraded)
		for {
			if _, _, err := c.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer ts.Close()

	cli, _, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(ts.URL, "http"), nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cli.Close()
	<-upgraded

	chID := uuid.New()
	od := &orphanData{queue: make(chan BaseMessage, 10)}
	for _, s := range []string{"A", "B", "C"} {
		od.queue <- DataMessage{ChannelID: chID, Data: []byte(s)}
	}
	srv.connCache.mu.Lock()
	srv.connCache.returnQueues[chID] = od
	srv.connCache.mu.Unlock()

	// Kill the transport so the drain's first write fails and gets requeued.
	_ = cli.UnderlyingConn().Close()

	srv.drainConnectorReturn(chID, &WSConn{conn: serverWS})

	var got []string
	srv.connCache.mu.Lock()
	for {
		select {
		case m := <-od.queue:
			got = append(got, string(m.(DataMessage).Data))
		default:
			srv.connCache.mu.Unlock()
			// Whatever the drain delivered successfully, the messages left
			// in the queue must keep their original relative order.
			if !isSubsequenceOf(got, []string{"A", "B", "C"}) {
				t.Fatalf("drain reordered buffered data: remaining %v is not a subsequence of [A B C]: failed write must not rejoin at the tail", got)
			}
			return
		}
	}
}

func isSubsequenceOf(got, seq []string) bool {
	i := 0
	for _, s := range got {
		for i < len(seq) && seq[i] != s {
			i++
		}
		if i >= len(seq) {
			return false
		}
		i++
	}
	return true
}

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
// is dropped after the relay's OrphanDataTTL.
func TestOrphanExpiry(t *testing.T) {
	r := NewRelay(zerolog.Nop(), NewDefaultRelayOption().WithOrphanDataTTL(200*time.Millisecond))
	id := uuid.New()
	r.bufferOrphanData(DataMessage{ChannelID: id, Data: []byte("x")})

	deadline := time.Now().Add(3 * time.Second)
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
