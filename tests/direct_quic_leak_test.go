package tests

import (
	"context"
	"net"
	"runtime"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
)

// selectConnection parks a waiter on the losing direction's done channel. If
// that direction's dial is still in flight when Connect returns, the deferred
// cancel aborts the dial without ever closing the done channel, so the waiter
// goroutine is leaked forever.
func TestDirectQUICSelectConnectionGoroutineLeak(t *testing.T) {
	pcA, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket A: %v", err)
	}
	defer pcA.Close()

	// Bound UDP socket that never answers: dials to it hang until timeout.
	pcBlack, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket blackhole: %v", err)
	}
	defer pcBlack.Close()
	black := pcBlack.LocalAddr().(*net.UDPAddr)

	pcB, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket B: %v", err)
	}
	defer pcB.Close()

	sid := uuid.New()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	mgrA, err := linksocks.NewDirectQUICManager(pcA, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager A: %v", err)
	}
	defer mgrA.Close()
	mgrB, err := linksocks.NewDirectQUICManager(pcB, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager B: %v", err)
	}
	defer mgrB.Close()

	addrA := pcA.LocalAddr().(*net.UDPAddr)

	measure := func() int {
		n := runtime.NumGoroutine()
		time.Sleep(50 * time.Millisecond)
		if m := runtime.NumGoroutine(); m < n {
			n = m
		}
		return n
	}

	base := measure()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	// A prefers accept and its own dial goes to the blackhole; B dials A. A
	// wins as soon as B's dial arrives, while A's blackhole dial is still in
	// flight — exactly the leak window.
	errCh := make(chan error, 2)
	go func() {
		_, err := mgrA.Connect(ctx, []linksocks.DirectCandidate{{Addr: black.IP.String(), Port: black.Port, Kind: "srflx"}}, true)
		errCh <- err
	}()
	go func() {
		_, err := mgrB.Connect(ctx, []linksocks.DirectCandidate{{Addr: addrA.IP.String(), Port: addrA.Port, Kind: "srflx"}}, false)
		errCh <- err
	}()

	for i := 0; i < 2; i++ {
		if err := <-errCh; err != nil {
			t.Fatalf("connect returned error: %v", err)
		}
	}

	// Tear both managers down first: live QUIC connections carry their own
	// goroutines, so only genuinely leaked waiters must survive the close.
	_ = mgrA.Close()
	_ = mgrB.Close()
	time.Sleep(300 * time.Millisecond)
	after := measure()

	if after > base {
		t.Fatalf("goroutine count grew from %d to %d after Connect: losing-direction waiter leaked",
			base, after)
	}
}
