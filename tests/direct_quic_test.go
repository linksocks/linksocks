package tests

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/linksocks"
	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog"
)

func TestDirectQUICManager_Connect_Loopback(t *testing.T) {
	// Use two separate managers on different UDP sockets to avoid quic-go
	// internal routing issues with self-connect on the same socket.
	pc1, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket 1: %v", err)
	}
	defer pc1.Close()

	pc2, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket 2: %v", err)
	}
	defer pc2.Close()

	sid := uuid.New()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	mgr1, err := linksocks.NewDirectQUICManager(pc1, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager 1: %v", err)
	}
	defer mgr1.Close()

	mgr2, err := linksocks.NewDirectQUICManager(pc2, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager 2: %v", err)
	}
	defer mgr2.Close()

	addr2 := pc2.LocalAddr().(*net.UDPAddr)
	addr1 := pc1.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	// Both sides dial each other simultaneously (bidirectional NAT traversal).
	errCh := make(chan error, 2)
	go func() {
		_, err := mgr1.Connect(ctx, []linksocks.DirectCandidate{{Addr: addr2.IP.String(), Port: addr2.Port, Kind: "srflx"}}, false)
		errCh <- err
	}()
	_, err = mgr2.Connect(ctx, []linksocks.DirectCandidate{{Addr: addr1.IP.String(), Port: addr1.Port, Kind: "srflx"}}, true)
	if err != nil {
		t.Fatalf("Connect mgr2: %v", err)
	}
	// Wait for mgr1 to finish as well.
	if err := <-errCh; err != nil {
		t.Fatalf("Connect mgr1: %v", err)
	}

	if mgr1.Active() == nil {
		t.Fatalf("mgr1 Active connection is nil")
	}
	if mgr2.Active() == nil {
		t.Fatalf("mgr2 Active connection is nil")
	}
}

func TestDirectQUICManager_Connect_InvalidKey(t *testing.T) {
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()

	sid := uuid.New()
	mgr, err := linksocks.NewDirectQUICManager(pc, sid, nil, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager: %v", err)
	}
	defer mgr.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	_, err = mgr.Connect(ctx, nil, false)
	if err == nil {
		t.Fatalf("expected error")
	}
}

var _ *quic.Conn
