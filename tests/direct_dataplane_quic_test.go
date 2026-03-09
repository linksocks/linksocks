package tests

import (
	"context"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
)

func serveDirectQUICDataPlane(
	t *testing.T,
	plane *linksocks.DirectQUICDataPlane,
	ctx context.Context,
	onConnect func(context.Context, reflect.Value, linksocks.ConnectMessage) error,
) {
	t.Helper()

	serve := reflect.ValueOf(plane).MethodByName("Serve")
	if !serve.IsValid() {
		t.Fatalf("Serve method not found")
	}
	serveType := serve.Type()
	handlerType := serveType.In(1)
	handler := reflect.MakeFunc(handlerType, func(args []reflect.Value) []reflect.Value {
		err := onConnect(args[0].Interface().(context.Context), args[1], args[2].Interface().(linksocks.ConnectMessage))
		if err == nil {
			return []reflect.Value{reflect.Zero(handlerType.Out(0))}
		}
		return []reflect.Value{reflect.ValueOf(err)}
	})
	results := serve.Call([]reflect.Value{reflect.ValueOf(ctx), handler})
	if len(results) != 1 || results[0].IsNil() {
		return
	}
	t.Fatalf("Serve: %v", results[0].Interface().(error))
}

func writeDirectQUICChannelMessage(t *testing.T, ch reflect.Value, msg linksocks.BaseMessage) {
	t.Helper()
	results := callChannelMethod(ch, "WriteMessage", reflect.ValueOf(msg))
	if len(results) != 1 || results[0].IsNil() {
		return
	}
	t.Fatalf("WriteMessage: %v", results[0].Interface().(error))
}

func readDirectQUICChannelMessage(t *testing.T, ch reflect.Value, ctx context.Context) linksocks.BaseMessage {
	t.Helper()
	results := callChannelMethod(ch, "ReadMessage", reflect.ValueOf(ctx))
	if len(results) != 2 {
		t.Fatalf("unexpected ReadMessage result count: %d", len(results))
	}
	if !results[1].IsNil() {
		t.Fatalf("ReadMessage: %v", results[1].Interface().(error))
	}
	msg, ok := results[0].Interface().(linksocks.BaseMessage)
	if !ok {
		t.Fatalf("unexpected ReadMessage type: %T", results[0].Interface())
	}
	return msg
}

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

	addrB := pcB.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	connBCh := make(chan error, 1)
	go func() {
		_, err := mgrB.Connect(ctx, nil)
		connBCh <- err
	}()

	connA, err := mgrA.Connect(ctx, []linksocks.DirectCandidate{{Addr: addrB.IP.String(), Port: addrB.Port, Kind: "host"}})
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

	planeA, err := linksocks.NewDirectQUICDataPlane(connA, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICDataPlane A: %v", err)
	}
	planeB, err := linksocks.NewDirectQUICDataPlane(connB, zerolog.Nop())
	if err != nil {
		t.Fatalf("NewDirectQUICDataPlane B: %v", err)
	}

	handled := make(chan struct{}, 1)
	serveDirectQUICDataPlane(t, planeB, ctx, func(ctx context.Context, ch reflect.Value, req linksocks.ConnectMessage) error {
		if req.ChannelID == uuid.Nil {
			return nil
		}
		resp := linksocks.ConnectResponseMessage{Success: true, ChannelID: req.ChannelID}
		writeDirectQUICChannelMessage(t, ch, resp)
		handled <- struct{}{}
		return nil
	})

	channelID := uuid.New()
	chA, err := planeA.OpenChannel(ctx, linksocks.ConnectMessage{Protocol: "tcp", Address: "example.com", Port: 80, ChannelID: channelID})
	if err != nil {
		t.Fatalf("OpenChannel: %v", err)
	}
	defer chA.Close()

	m, err := chA.ReadMessage(ctx)
	if err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	resp, ok := m.(linksocks.ConnectResponseMessage)
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

	addrB := pcB.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	connBCh := make(chan error, 1)
	go func() {
		_, err := mgrB.Connect(ctx, nil)
		connBCh <- err
	}()

	connA, err := mgrA.Connect(ctx, []linksocks.DirectCandidate{{Addr: addrB.IP.String(), Port: addrB.Port, Kind: "host"}})
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

	planeA, _ := linksocks.NewDirectQUICDataPlane(connA, zerolog.Nop())
	planeB, _ := linksocks.NewDirectQUICDataPlane(connB, zerolog.Nop())

	dataDone := make(chan linksocks.DataMessage, 1)
	serveDirectQUICDataPlane(t, planeB, ctx, func(ctx context.Context, ch reflect.Value, req linksocks.ConnectMessage) error {
		for {
			m := readDirectQUICChannelMessage(t, ch, ctx)
			dm, ok := m.(linksocks.DataMessage)
			if !ok {
				continue
			}
			dataDone <- dm
			return nil
		}
	})

	channelID := uuid.New()
	chA, err := planeA.OpenChannel(ctx, linksocks.ConnectMessage{Protocol: "tcp", Address: "example.com", Port: 80, ChannelID: channelID})
	if err != nil {
		t.Fatalf("OpenChannel: %v", err)
	}
	defer chA.Close()

	payload := []byte("hello over quic")
	if err := chA.WriteMessage(linksocks.DataMessage{Protocol: "tcp", ChannelID: channelID, Data: payload, Compression: linksocks.DataCompressionNone}); err != nil {
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

	addrB := pcB.LocalAddr().(*net.UDPAddr)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	connBCh := make(chan error, 1)
	go func() {
		_, err := mgrB.Connect(ctx, nil)
		connBCh <- err
	}()

	connA, err := mgrA.Connect(ctx, []linksocks.DirectCandidate{{Addr: addrB.IP.String(), Port: addrB.Port, Kind: "host"}})
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

	planeA, _ := linksocks.NewDirectQUICDataPlane(connA, zerolog.Nop())
	planeB, _ := linksocks.NewDirectQUICDataPlane(connB, zerolog.Nop())

	serveDirectQUICDataPlane(t, planeB, ctx, func(ctx context.Context, ch reflect.Value, req linksocks.ConnectMessage) error {
		return nil
	})

	channelID := uuid.New()
	ch1, err := planeA.OpenChannel(ctx, linksocks.ConnectMessage{Protocol: "tcp", Address: "example.com", Port: 80, ChannelID: channelID})
	if err != nil {
		t.Fatalf("OpenChannel #1: %v", err)
	}
	defer ch1.Close()

	if _, err := planeA.OpenChannel(ctx, linksocks.ConnectMessage{Protocol: "tcp", Address: "example.com", Port: 80, ChannelID: channelID}); err == nil {
		t.Fatalf("expected duplicate channel error")
	}
}
