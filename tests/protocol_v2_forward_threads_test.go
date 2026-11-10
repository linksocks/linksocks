package tests

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
)

// A forward client with several links opens an encrypted channel: the
// handshake must complete and every connection must round-trip its payload.
// The test is a smoke test for the multi-link forward path; the ordering
// guarantee itself is pinned by TestClientRelayWriterPinsOneLink, which is
// deterministic.
func TestProtocolV2ForwardWithThreads(t *testing.T) {
	echoPort := startEchoPort(t)
	wsPort := freeTCPPort(t)
	socksPort := freeTCPPort(t)

	var serverLog, clientLog bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	srv := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
		WithWSHost("127.0.0.1").
		WithWSPort(wsPort).
		WithLogger(zerolog.New(zerolog.SyncWriter(&serverLog)).Level(zerolog.TraceLevel)))
	if err := srv.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Close)

	token, err := srv.AddForwardToken("")
	if err != nil {
		t.Fatalf("add forward token: %v", err)
	}

	client := linksocks.NewLinkSocksClient(token, linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithProtocolVersion(linksocks.ProtocolVersionV2).
		WithSocksHost("127.0.0.1").
		WithSocksPort(socksPort).
		WithSocksWaitServer(true).
		WithThreads(2).
		WithReconnectDelay(time.Millisecond).
		WithLogger(zerolog.New(zerolog.SyncWriter(&clientLog)).Level(zerolog.TraceLevel)))
	if err := client.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start client: %v", err)
	}
	t.Cleanup(client.Close)

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", socksPort)
	payload := []byte("probe-bytes")

	for i := 0; i < 40; i++ {
		conn, err := net.DialTimeout("tcp", proxyAddr, 2*time.Second)
		if err != nil {
			t.Fatalf("request %d: dial socks proxy: %v", i, err)
		}
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
			_ = conn.Close()
			t.Fatalf("request %d: write greeting: %v", i, err)
		}
		reply := make([]byte, 2)
		if _, err := conn.Read(reply); err != nil || reply[0] != 0x05 || reply[1] != 0x00 {
			_ = conn.Close()
			t.Fatalf("request %d: greeting reply %v: %v\n--- server ---\n%s\n--- client ---\n%s",
				i, reply, err, serverLog.String(), clientLog.String())
		}
		request := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, byte(echoPort >> 8), byte(echoPort)}
		if _, err := conn.Write(request); err != nil {
			_ = conn.Close()
			t.Fatalf("request %d: write connect: %v", i, err)
		}
		head := make([]byte, 10)
		if _, err := conn.Read(head); err != nil || head[1] != 0x00 {
			_ = conn.Close()
			t.Fatalf("request %d: connect reply %v: %v\n--- server ---\n%s\n--- client ---\n%s",
				i, head, err, serverLog.String(), clientLog.String())
		}
		if _, err := conn.Write(payload); err != nil {
			_ = conn.Close()
			t.Fatalf("request %d: write payload: %v", i, err)
		}
		echo := make([]byte, len(payload))
		if _, err := conn.Read(echo); err != nil || !bytes.Equal(echo, payload) {
			_ = conn.Close()
			t.Fatalf("request %d: echo %q: %v\n--- server ---\n%s\n--- client ---\n%s",
				i, echo, err, serverLog.String(), clientLog.String())
		}
		_ = conn.Close()
	}
}
