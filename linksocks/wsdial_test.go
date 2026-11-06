package linksocks

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
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
	conn, err := dialWebSocketFollowingRedirects(ctx, websocket.DefaultDialer,
		"ws"+strings.TrimPrefix(entry.URL, "http")+"/socket?token=abc&web=1", zerolog.Nop())
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
	_, err := dialWebSocketFollowingRedirects(ctx, websocket.DefaultDialer,
		"ws"+strings.TrimPrefix(server.URL, "http")+"/socket", zerolog.Nop())
	if err == nil || !strings.Contains(err.Error(), "too many redirects") {
		t.Fatalf("dial error = %v, want too many redirects", err)
	}
}
