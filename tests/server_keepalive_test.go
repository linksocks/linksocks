package tests

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/linksocks/linksocks/linksocks"
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
	opt := linksocks.DefaultServerOption().
		WithWSPort(port).
		WithLogger(zerolog.Nop()).
		WithAPI("TOKEN")
	// Fast probe interval so the keepalive behaviour is observable quickly; it
	// must be shorter than the first blocked read window, because gorilla returns
	// the first timeout error from cache afterwards.
	opt.PingInterval = 100 * time.Millisecond
	srv := linksocks.NewLinkSocksServer(opt)
	if err := srv.WaitReady(context.Background(), 5*time.Second); err != nil {
		t.Fatalf("server not ready: %v", err)
	}
	defer srv.Close()

	// Register a forward token via the API, then connect through /socket with
	// the URL token hash (the same path real clients use).
	body, _ := json.Marshal(linksocks.TokenRequest{Type: "forward"})
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
	var tr linksocks.TokenResponse
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

func TestServerURLAuthChecksProtocolVersion(t *testing.T) {
	tests := []struct {
		name       string
		version    byte
		wantClosed bool
	}{
		{name: "older minor warns", version: 0x01},
		{name: "newer minor warns", version: 0x05},
		{name: "different major closes", version: 0x10, wantClosed: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			port := testFreePort(t)
			server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().WithWSPort(port).WithLogger(zerolog.Nop()))
			token, err := server.AddForwardToken("")
			if err != nil {
				t.Fatalf("add token: %v", err)
			}
			if err := server.WaitReady(context.Background(), 5*time.Second); err != nil {
				t.Fatalf("server not ready: %v", err)
			}
			defer server.Close()

			hash := sha256.Sum256([]byte(token))
			wsURL := fmt.Sprintf("ws://127.0.0.1:%d/socket?token=%s&reverse=false", port, hex.EncodeToString(hash[:]))
			ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
			if err != nil {
				t.Fatalf("dial: %v", err)
			}
			defer ws.Close()
			_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := ws.ReadMessage(); err != nil {
				t.Fatalf("read auth response: %v", err)
			}

			frame, err := linksocks.PackMessage(linksocks.LogMessage{Level: linksocks.LogLevelInfo, Msg: "version probe"})
			if err != nil {
				t.Fatal(err)
			}
			frame[0] = test.version
			if err := ws.WriteMessage(websocket.BinaryMessage, frame); err != nil {
				t.Fatalf("write message: %v", err)
			}

			for {
				_, data, err := ws.ReadMessage()
				if err != nil {
					if test.wantClosed {
						if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
							t.Fatalf("server did not close incompatible connection: %v", err)
						}
						return
					}
					t.Fatalf("read version warning: %v", err)
				}
				message, err := linksocks.ParseMessage(data)
				if err != nil {
					t.Fatalf("parse server message: %v", err)
				}
				if warning, ok := message.(linksocks.LogMessage); ok && warning.Level == linksocks.LogLevelWarn && strings.Contains(warning.Msg, "Protocol minor version mismatch") {
					if test.wantClosed {
						t.Fatal("major mismatch was treated as a minor mismatch")
					}
					return
				}
			}
		})
	}
}
