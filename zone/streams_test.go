package zone

import (
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"
)

func TestFrameQueueBackpressureAndClose(t *testing.T) {
	queue := newFrameQueue(4)
	if !queue.push([]byte("abcd")) {
		t.Fatal("push within the limit blocked")
	}
	done := make(chan bool, 1)
	go func() {
		done <- queue.push([]byte("e"))
	}()
	select {
	case <-done:
		t.Fatal("push beyond the limit did not block")
	case <-time.After(50 * time.Millisecond):
	}
	if chunk, ok := queue.pop(); !ok || string(chunk) != "abcd" {
		t.Fatalf("pop = %q, %v", chunk, ok)
	}
	select {
	case ok := <-done:
		if !ok {
			t.Fatal("push did not succeed after the queue drained")
		}
	case <-time.After(time.Second):
		t.Fatal("push stayed blocked after the queue drained")
	}
	if chunk, ok := queue.pop(); !ok || string(chunk) != "e" {
		t.Fatalf("pop = %q, %v", chunk, ok)
	}

	queue.push([]byte("1234"))
	pusher := make(chan struct{})
	go func() {
		queue.push([]byte("x"))
		close(pusher)
	}()
	select {
	case <-pusher:
		t.Fatal("push beyond the limit did not block")
	case <-time.After(50 * time.Millisecond):
	}
	queue.close()
	select {
	case <-pusher:
	case <-time.After(time.Second):
		t.Fatal("closing the queue did not unblock push")
	}
	if chunk, ok := queue.pop(); !ok || string(chunk) != "1234" {
		t.Fatalf("pop = %q, %v", chunk, ok)
	}
	popped := make(chan bool, 1)
	go func() {
		_, ok := queue.pop()
		popped <- ok
	}()
	select {
	case ok := <-popped:
		if ok {
			t.Fatal("closed queue returned a chunk")
		}
	case <-time.After(time.Second):
		t.Fatal("closed queue blocked pop")
	}
}

func TestStreamTableAllocation(t *testing.T) {
	table := newStreamTable()
	first := table.allocate(nil, nil, true)
	second := table.allocate(nil, nil, false)
	if first.id == second.id || first.id == 0 || second.id == 0 {
		t.Fatalf("allocated ids %d and %d", first.id, second.id)
	}
	if table.get(first.id) != first || first.ready == nil || second.ready != nil {
		t.Fatal("stream registration state is wrong")
	}
	if stream, ok := table.insert(first.id, nil, nil); ok || stream != nil {
		t.Fatal("insert replaced an active stream id")
	}
	inserted, ok := table.insert(0xABCD, nil, nil)
	if !ok || table.get(0xABCD) != inserted {
		t.Fatal("insert did not register the stream")
	}
	table.discard(inserted)
	if table.get(0xABCD) != nil {
		t.Fatal("discard did not remove the stream")
	}
	for _, stream := range table.each() {
		if stream == inserted {
			t.Fatal("discarded stream still listed")
		}
	}
}

type countingSink struct {
	delivered atomic.Int32
}

func (s *countingSink) deliver(*net.UDPAddr, netip.Addr, uint16, []byte) {
	s.delivered.Add(1)
}

func TestDatagramNATReaping(t *testing.T) {
	c := &connection{
		udpClients:  make(map[udpClientKey]*udpClient),
		udpServices: make(map[udpServiceKey]*udpService),
	}
	fresh := netip.MustParseAddr("10.88.0.2")
	stale := netip.MustParseAddr("10.88.0.3")
	sink := &countingSink{}
	c.udpClients[udpClientKey{peer: fresh, port: 1}] = &udpClient{client: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5}, sink: sink, lastSeen: time.Now()}
	c.udpClients[udpClientKey{peer: stale, port: 2}] = &udpClient{client: &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 6}, sink: &countingSink{}, lastSeen: time.Now().Add(-2 * datagramIdleTTL)}
	socket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	c.udpServices[udpServiceKey{peer: stale, port: 3}] = &udpService{conn: socket, peer: stale, peerPort: 3, lastSeen: time.Now().Add(-2 * datagramIdleTTL)}
	c.reapDatagrams()
	if len(c.udpClients) != 1 || c.udpClients[udpClientKey{peer: fresh, port: 1}] == nil {
		t.Fatal("reaper removed a fresh NAT entry or kept a stale one")
	}
	if len(c.udpServices) != 0 {
		t.Fatal("reaper kept a stale service socket")
	}
	if _, err := socket.WriteToUDP([]byte("x"), &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1}); err == nil {
		t.Fatal("reaped service socket is still usable")
	}
	c.releaseUDPClient(sink)
	if len(c.udpClients) != 0 {
		t.Fatal("releaseUDPClient kept NAT entries for the sink")
	}
	c.closeDatagrams()
}
