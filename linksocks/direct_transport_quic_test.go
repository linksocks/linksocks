package linksocks

import "testing"

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
