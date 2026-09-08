package linksocks

import (
	"net/http"
	"net/http/httptest"

	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

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
