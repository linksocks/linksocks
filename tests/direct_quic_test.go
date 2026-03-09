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
	pc, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket: %v", err)
	}
	defer pc.Close()

	sid := uuid.New()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}

	mgr, err := linksocks.NewDirectQUICManager(pc, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager: %v", err)
	}
	defer mgr.Close()

	udpAddr, ok := pc.LocalAddr().(*net.UDPAddr)
	if !ok {
		t.Fatalf("expected UDPAddr, got %T", pc.LocalAddr())
	}

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	c, err := mgr.Connect(ctx, []linksocks.DirectCandidate{{Addr: udpAddr.IP.String(), Port: udpAddr.Port, Kind: "srflx"}})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.CloseWithError(0, "test")

	if mgr.Active() == nil {
		t.Fatalf("Active connection is nil")
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

	_, err = mgr.Connect(ctx, nil)
	if err == nil {
		t.Fatalf("expected error")
	}
}

var _ quic.Connection
