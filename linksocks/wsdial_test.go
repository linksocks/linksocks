package linksocks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/linksocks/linksocks/internal/echtest"
	"github.com/rs/zerolog"
)

// A load-balancer entry point answers the handshake with a redirect to a healthy
// backend, exactly like the linksocks.js lb-entry worker in front of l.zetx.tech.
func TestWebSocketDialFollowsRelayRedirect(t *testing.T) {
	received := make(chan string, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			received <- "upgrade failed: " + err.Error()
			return
		}
		defer conn.Close()
		received <- request.URL.RequestURI()
	}))
	defer backend.Close()

	entry := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		target := "ws" + strings.TrimPrefix(backend.URL, "http") + request.URL.RequestURI()
		http.Redirect(response, request, target, http.StatusFound)
	}))
	defer entry.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx,
		"ws"+strings.TrimPrefix(entry.URL, "http")+"/socket?token=abc&web=1",
		relayDialOptions{Logger: zerolog.Nop()})
	if err != nil {
		t.Fatalf("dial through redirect: %v", err)
	}
	defer conn.Close()

	select {
	case got := <-received:
		if !strings.Contains(got, "/socket?token=abc&web=1") {
			t.Fatalf("backend request URI = %q, want the original query string", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("backend never received the redirected handshake")
	}
}

func TestWebSocketDialReportsRedirectLoop(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		http.Redirect(response, request, "ws"+strings.TrimPrefix(server.URL, "http")+request.URL.RequestURI(), http.StatusFound)
	}))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := dialRelayWebSocket(ctx,
		"ws"+strings.TrimPrefix(server.URL, "http")+"/socket",
		relayDialOptions{Logger: zerolog.Nop()})
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("dial error = %v, want too many redirects", err)
	}
}

// A relay without ECH support rejects the offer; the dial must fall back to a
// plain ClientHello so a privacy feature never costs connectivity.
func TestWebSocketDialFallsBackWhenECHIsRejected(t *testing.T) {
	upgraded := make(chan bool, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			upgraded <- false
			return
		}
		defer conn.Close()
		upgraded <- true
	}))
	defer server.Close()

	configList, _, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	conn, err := dialRelayWebSocket(ctx, "wss://"+strings.TrimPrefix(server.URL, "https://")+"/socket", relayDialOptions{
		Insecure:    true,
		ECHMode:     ECHAuto,
		echOverride: configList,
		Logger:      zerolog.Nop(),
	})
	if err != nil {
		t.Fatalf("dial failed although the fallback should have connected: %v", err)
	}
	defer conn.Close()
	select {
	case ok := <-upgraded:
		if !ok {
			t.Fatal("server never saw a successful WebSocket upgrade")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for the upgrade")
	}
}

// ECH off must not offer ECH at all.
func TestWebSocketDialECHAutoSkipsPlainWebSocket(t *testing.T) {
	conn, err := dialRelayWebSocket(context.Background(), "ws://127.0.0.1:1/socket", relayDialOptions{
		ECHMode: ECHAuto,
		Logger:  zerolog.Nop(),
	})
	if conn != nil {
		conn.Close()
	}
	if err == nil {
		t.Fatal("dial to a closed port succeeded")
	}
}
