package linksocks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

func testFreePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	_ = l.Close()
	return port
}

// A half-open TCP link is invisible to the server: it never pings idle
// clients, so keepalive must come from somewhere. Design expectation: the
// server proactively probes idle connections so dead links cannot linger.
func TestServerProactivelyPingsIdleClient(t *testing.T) {
	port := testFreePort(t)
	opt := DefaultServerOption().
		WithWSPort(port).
		WithLogger(zerolog.Nop()).
		WithAPI("TOKEN")
	// Fast probe interval so the keepalive behaviour is observable quickly; it
	// must be shorter than the first blocked read window, because gorilla returns
	// the first timeout error from cache afterwards.
	opt.PingInterval = 100 * time.Millisecond
	srv := NewLinkSocksServer(opt)
	if err := srv.WaitReady(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("server not ready: %v", err)
	}
	defer srv.Close()

	// Register a forward token via the API, then connect through /socket with
	// the URL token hash (the same path real clients use).
	body, _ := json.Marshal(TokenRequest{Type: "forward"})
	req, err := http.NewRequest("POST", fmt.Sprintf("http://127.0.0.1:%d/api/token", port), bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build token request: %v", err)
	}
	req.Header.Set("X-API-Key", "TOKEN")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create token: %v", err)
	}
	defer resp.Body.Close()
	var tr TokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tr); err != nil {
		t.Fatalf("decode token response: %v", err)
	}
	if !tr.Success || tr.Token == "" {
		t.Fatalf("token creation failed: %+v", tr)
	}

	sum := sha256.Sum256([]byte(tr.Token))
	wsURL := fmt.Sprintf("ws://127.0.0.1:%d/socket?token=%s&reverse=false&instance=%s",
		port, hex.EncodeToString(sum[:]), uuid.New().String())

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ws, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ws.Close()

	pingSeen := false
	ws.SetPingHandler(func(appData string) error {
		pingSeen = true
		return ws.WriteControl(websocket.PongMessage, nil, time.Now().Add(time.Second))
	})

	// Bounded read windows: the ping arrives as a control frame processed inside
	// some window. The loop is capped far below gorilla's repeated-read panic
	// threshold (1000), since a stale timeout error is returned for free after
	// the first one.
	for i := 0; i < 8 && !pingSeen; i++ {
		_ = ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
		if _, _, err := ws.ReadMessage(); err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			break
		}
	}

	if !pingSeen {
		t.Fatalf("server never pinged idle connection within 2s: idle half-open links are never detected")
	}
}
