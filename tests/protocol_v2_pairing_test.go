package tests

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
)

// TestProtocolV2NeverPairsWithLegacyPeer pins the generation boundary of the
// relay: a protocol V2 connector must never be served by a legacy provider,
// because that would send sealed channel frames to a peer that cannot parse
// them (and, worse, invite a plaintext downgrade). Once a V2 provider joins,
// the same request must succeed.
func TestProtocolV2NeverPairsWithLegacyPeer(t *testing.T) {
	echoPort := startEchoPort(t)
	wsPort := freeTCPPort(t)
	connectorSocks := freeTCPPort(t)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// A short connector wait keeps the negative probe quick: with no
	// same-generation provider the request must fail instead of hanging.
	srv := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
		WithWSHost("127.0.0.1").
		WithWSPort(wsPort).
		WithConnectorWait(300 * time.Millisecond).
		WithLogger(zerolog.Nop()))
	if err := srv.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(srv.Close)

	const reverseToken = "v2-pairing-reverse-token"
	if _, err := srv.AddReverseToken(&linksocks.ReverseTokenOptions{Token: reverseToken, Port: 0}); err != nil {
		t.Fatalf("add reverse token: %v", err)
	}
	const connectorToken = "v2-pairing-connector-token"
	if _, err := srv.AddConnectorToken(connectorToken, reverseToken); err != nil {
		t.Fatalf("add connector token: %v", err)
	}

	// Legacy provider: same token, generation 1.
	legacyProvider := linksocks.NewLinkSocksClient(reverseToken, linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithReverse(true).
		WithProtocolVersion(linksocks.ProtocolVersionV1).
		WithSocksHost("127.0.0.1").
		WithSocksPort(0).
		WithSocksWaitServer(true).
		WithLogger(zerolog.Nop()))
	if err := legacyProvider.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start legacy provider: %v", err)
	}
	t.Cleanup(legacyProvider.Close)

	connector := linksocks.NewLinkSocksClient(connectorToken, linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithProtocolVersion(linksocks.ProtocolVersionV2).
		WithSocksHost("127.0.0.1").
		WithSocksPort(connectorSocks).
		WithSocksWaitServer(true).
		WithLogger(zerolog.Nop()))
	if err := connector.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start connector: %v", err)
	}
	t.Cleanup(connector.Close)

	proxyAddr := fmt.Sprintf("127.0.0.1:%d", connectorSocks)

	// Give the relay a moment to register both links, then probe: the V2
	// connector must not be paired with the legacy provider.
	time.Sleep(300 * time.Millisecond)
	if code := socks5ConnectEcho(t, proxyAddr, "127.0.0.1", echoPort, []byte("must not arrive")); code == 0x00 {
		t.Fatal("V2 connector was served by a legacy provider")
	}

	// A V2 provider joins; now the tunnel must work end to end.
	v2Provider := linksocks.NewLinkSocksClient(reverseToken, linksocks.DefaultClientOption().
		WithWSURL(fmt.Sprintf("ws://127.0.0.1:%d", wsPort)).
		WithReverse(true).
		WithProtocolVersion(linksocks.ProtocolVersionV2).
		WithSocksHost("127.0.0.1").
		WithSocksPort(0).
		WithSocksWaitServer(true).
		WithLogger(zerolog.Nop()))
	if err := v2Provider.WaitReady(ctx, 10*time.Second); err != nil {
		t.Fatalf("start V2 provider: %v", err)
	}
	t.Cleanup(v2Provider.Close)

	deadline := time.Now().Add(10 * time.Second)
	for {
		code := socks5ConnectEcho(t, proxyAddr, "127.0.0.1", echoPort, []byte("hello v2"))
		if code == 0x00 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("V2 connector never reached the V2 provider (last reply 0x%02x)", code)
		}
		time.Sleep(100 * time.Millisecond)
	}
}
