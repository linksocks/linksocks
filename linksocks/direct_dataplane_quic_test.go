package linksocks

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

func TestDirectQUICDataPlane_OpenAndServe_TCPConnectResponse(t *testing.T) {
	pcA, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket A: %v", err)
	}
	defer pcA.Close()

	pcB, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket B: %v", err)
	}
	defer pcB.Close()

	sid := uuid.New()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0xA0 + i)
	}

	mgrA, err := NewDirectQUICManager(pcA, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager A: %v", err)
	}
	defer mgrA.Close()

	mgrB, err := NewDirectQUICManager(pcB, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager B: %v", err)
	}
	defer mgrB.Close()

	addrB := pcB.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	// Start B as the accepting side. Connect() is the only entry point that
	// initializes the listener.
	connBCh := make(chan error, 1)
	go func() {
		_, err := mgrB.Connect(ctx, nil)
		connBCh <- err
	}()

	connA, err := mgrA.Connect(ctx, []DirectCandidate{{Addr: addrB.IP.String(), Port: addrB.Port, Kind: "host"}})
	if err != nil {
		t.Fatalf("Connect A->B: %v", err)
	}
	defer connA.CloseWithError(0, "test")

	select {
	case err := <-connBCh:
		if err != nil {
			t.Fatalf("Connect B accept: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for B Connect")
	}

	connB := mgrB.Active()
	if connB == nil {
		t.Fatalf("expected mgrB.Active() not nil")
	}
	defer connB.CloseWithError(0, "test")

	planeA, err := NewDirectQUICDataPlane(connA, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICDataPlane A: %v", err)
	}
	planeB, err := NewDirectQUICDataPlane(connB, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICDataPlane B: %v", err)
	}

	handled := make(chan struct{}, 1)
	if err := planeB.Serve(ctx, func(ctx context.Context, ch *directQUICChannel, req ConnectMessage) error {
		if req.ChannelID == uuid.Nil {
			return nil
		}
		resp := ConnectResponseMessage{Success: true, ChannelID: req.ChannelID}
		if err := ch.WriteMessage(resp); err != nil {
			return err
		}
		handled <- struct{}{}
		return nil
	}); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	channelID := uuid.New()
	chA, err := planeA.OpenChannel(ctx, ConnectMessage{Protocol: "tcp", Address: "example.com", Port: 80, ChannelID: channelID})
	if err != nil {
		t.Fatalf("OpenChannel: %v", err)
	}
	defer chA.Close()

	m, err := chA.ReadMessage(ctx)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	resp, ok := m.(ConnectResponseMessage)
	if !ok {
		t.Fatalf("expected ConnectResponseMessage, got %T", m)
	}
	if !resp.Success {
		t.Fatalf("expected success")
	}
	if resp.ChannelID != channelID {
		t.Fatalf("channel_id mismatch")
	}

	select {
	case <-handled:
	case <-ctx.Done():
		t.Fatalf("timeout waiting for handler")
	}
}

func TestDirectQUICDataPlane_DataMessage_RoundTrip(t *testing.T) {
	pcA, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket A: %v", err)
	}
	defer pcA.Close()

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

	mgrA, err := NewDirectQUICManager(pcA, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager A: %v", err)
	}
	defer mgrA.Close()

	mgrB, err := NewDirectQUICManager(pcB, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager B: %v", err)
	}
	defer mgrB.Close()

	addrB := pcB.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	connBCh := make(chan error, 1)
	go func() {
		_, err := mgrB.Connect(ctx, nil)
		connBCh <- err
	}()

	connA, err := mgrA.Connect(ctx, []DirectCandidate{{Addr: addrB.IP.String(), Port: addrB.Port, Kind: "host"}})
	if err != nil {
		t.Fatalf("Connect A->B: %v", err)
	}
	defer connA.CloseWithError(0, "test")

	select {
	case err := <-connBCh:
		if err != nil {
			t.Fatalf("Connect B accept: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for B Connect")
	}

	connB := mgrB.Active()
	if connB == nil {
		t.Fatalf("expected mgrB.Active() not nil")
	}
	defer connB.CloseWithError(0, "test")

	planeA, _ := NewDirectQUICDataPlane(connA, zerolog.Nop())
	planeB, _ := NewDirectQUICDataPlane(connB, zerolog.Nop())

	dataDone := make(chan DataMessage, 1)
	if err := planeB.Serve(ctx, func(ctx context.Context, ch *directQUICChannel, req ConnectMessage) error {
		for {
			m, err := ch.ReadMessage(ctx)
			if err != nil {
				return nil
			}
			dm, ok := m.(DataMessage)
			if !ok {
				continue
			}
			dataDone <- dm
			return nil
		}
	}); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	channelID := uuid.New()
	chA, err := planeA.OpenChannel(ctx, ConnectMessage{Protocol: "tcp", Address: "example.com", Port: 80, ChannelID: channelID})
	if err != nil {
		t.Fatalf("OpenChannel: %v", err)
	}
	defer chA.Close()

	payload := []byte("hello over quic")
	if err := chA.WriteMessage(DataMessage{Protocol: "tcp", ChannelID: channelID, Data: payload, Compression: DataCompressionNone}); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}

	select {
	case dm := <-dataDone:
		if dm.ChannelID != channelID {
			t.Fatalf("channel_id mismatch")
		}
		if string(dm.Data) != string(payload) {
			t.Fatalf("payload mismatch")
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for data")
	}
}

func TestDirectQUICDataPlane_OpenChannel_DuplicateChannelID(t *testing.T) {
	pcA, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket A: %v", err)
	}
	defer pcA.Close()

	pcB, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("ListenPacket B: %v", err)
	}
	defer pcB.Close()

	sid := uuid.New()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(0x10 + i)
	}

	mgrA, err := NewDirectQUICManager(pcA, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager A: %v", err)
	}
	defer mgrA.Close()

	mgrB, err := NewDirectQUICManager(pcB, sid, key, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICManager B: %v", err)
	}
	defer mgrB.Close()

	addrB := pcB.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	connBCh := make(chan error, 1)
	go func() {
		_, err := mgrB.Connect(ctx, nil)
		connBCh <- err
	}()

	connA, err := mgrA.Connect(ctx, []DirectCandidate{{Addr: addrB.IP.String(), Port: addrB.Port, Kind: "host"}})
	if err != nil {
		t.Fatalf("Connect A->B: %v", err)
	}
	defer connA.CloseWithError(0, "test")

	select {
	case err := <-connBCh:
		if err != nil {
			t.Fatalf("Connect B accept: %v", err)
		}
	case <-ctx.Done():
		t.Fatalf("timeout waiting for B Connect")
	}

	connB := mgrB.Active()
	if connB == nil {
		t.Fatalf("expected mgrB.Active() not nil")
	}
	defer connB.CloseWithError(0, "test")

	planeA, _ := NewDirectQUICDataPlane(connA, zerolog.Nop())
	planeB, _ := NewDirectQUICDataPlane(connB, zerolog.Nop())

	if err := planeB.Serve(ctx, func(ctx context.Context, ch *directQUICChannel, req ConnectMessage) error {
		return nil
	}); err != nil {
		t.Fatalf("Serve: %v", err)
	}

	channelID := uuid.New()
	ch1, err := planeA.OpenChannel(ctx, ConnectMessage{Protocol: "tcp", Address: "example.com", Port: 80, ChannelID: channelID})
	if err != nil {
		t.Fatalf("OpenChannel #1: %v", err)
	}
	defer ch1.Close()

	if _, err := planeA.OpenChannel(ctx, ConnectMessage{Protocol: "tcp", Address: "example.com", Port: 80, ChannelID: channelID}); err == nil {
		t.Fatalf("expected duplicate channel error")
	}
}
