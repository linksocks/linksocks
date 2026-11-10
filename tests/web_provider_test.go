package tests

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
)

func TestWebProviderWebSocketProxy(t *testing.T) {
	originResult := make(chan error, 1)
	origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.RequestURI() != "/base/chat?room=one" || request.Header.Get("Origin") != "https://app.example.com" || request.Header.Get("Cookie") != "sid=test" {
			originResult <- fmt.Errorf("origin request metadata mismatch")
			return
		}
		upgrader := websocket.Upgrader{Subprotocols: []string{"chat"}, CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(response, request, nil)
		if err != nil {
			originResult <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		for _, expected := range []struct {
			opcode int
			data   []byte
		}{{websocket.TextMessage, []byte("hello")}, {websocket.BinaryMessage, []byte{0, 1, 255}}} {
			opcode, data, err := conn.ReadMessage()
			if err != nil || opcode != expected.opcode || !bytes.Equal(data, expected.data) {
				originResult <- fmt.Errorf("origin received unexpected frame: %v", err)
				return
			}
			if err := conn.WriteMessage(opcode, data); err != nil {
				originResult <- err
				return
			}
		}
		_, _, err = conn.ReadMessage()
		closeError, ok := err.(*websocket.CloseError)
		if !ok || closeError.Code != 1000 {
			originResult <- fmt.Errorf("origin did not receive normal close: %v", err)
			return
		}
		originResult <- nil
	}))
	defer origin.Close()

	relayResult := make(chan error, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			relayResult <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		write := func(message linksocks.BaseMessage) error {
			frame, err := linksocks.PackMessage(message)
			if err != nil {
				return err
			}
			return conn.WriteMessage(websocket.BinaryMessage, frame)
		}
		read := func() (linksocks.BaseMessage, error) {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return nil, err
			}
			return linksocks.ParseMessage(frame)
		}
		result := func() error {
			if err := write(linksocks.AuthResponseMessage{Success: true}); err != nil {
				return err
			}
			registration, err := read()
			if err != nil {
				return err
			}
			if _, ok := registration.(linksocks.WebRegisterMessage); !ok {
				return fmt.Errorf("expected Web registration, got %T", registration)
			}
			if err := write(linksocks.WebRegisterResponseMessage{Success: true, Host: "site.example.com", URL: "https://site.example.com"}); err != nil {
				return err
			}
			channelID := uuid.New()
			if err := write(linksocks.WebRequestMessage{
				ChannelID: channelID, Method: "GET", Path: "/chat?room=one", WebSocket: true,
				Headers: [][2]string{{"Origin", "https://app.example.com"}, {"Cookie", "sid=test"}, {"Sec-WebSocket-Protocol", "chat"}, {"Sec-WebSocket-Key", "browser-key"}, {"Sec-WebSocket-Version", "13"}},
			}); err != nil {
				return err
			}
			message, err := read()
			if err != nil {
				return err
			}
			upgrade, ok := message.(linksocks.WebResponseMessage)
			if !ok || upgrade.Status != 101 || len(upgrade.Headers) != 1 || upgrade.Headers[0] != [2]string{"Sec-WebSocket-Protocol", "chat"} {
				return fmt.Errorf("unexpected WebSocket upgrade response: %#v", message)
			}
			for _, expected := range []struct {
				opcode byte
				data   []byte
			}{{1, []byte("hello")}, {2, []byte{0, 1, 255}}} {
				if err := write(linksocks.WebSocketFrameMessage{ChannelID: channelID, Opcode: expected.opcode, Data: expected.data}); err != nil {
					return err
				}
				message, err := read()
				if err != nil {
					return err
				}
				frame, ok := message.(linksocks.WebSocketFrameMessage)
				if !ok || frame.ChannelID != channelID || frame.Opcode != expected.opcode || !bytes.Equal(frame.Data, expected.data) {
					return fmt.Errorf("unexpected echoed WebSocket frame: %#v", message)
				}
			}
			return write(linksocks.WebCancelMessage{ChannelID: channelID, Code: 1000, Reason: "done"})
		}()
		relayResult <- result
	}))
	defer relay.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	clientResult := make(chan error, 1)
	go func() {
		clientResult <- linksocks.RunWeb(ctx, linksocks.WebOption{URL: relay.URL, Token: "test-token", Backends: []string{origin.URL + "/base"}, NoReconnect: true, Logger: zerolog.Nop()})
	}()
	if err := <-relayResult; err != nil {
		t.Fatalf("relay: %v", err)
	}
	if err := <-originResult; err != nil {
		t.Fatalf("origin: %v", err)
	}
	<-clientResult
}

// runWebProviderOnce drives one request through a fake relay and returns the
// response the provider produced for it.
func runWebProviderOnce(t *testing.T, opt linksocks.WebOption, request linksocks.WebRequestMessage) (linksocks.WebResponseMessage, []byte) {
	t.Helper()
	type result struct {
		head linksocks.WebResponseMessage
		body []byte
	}
	results := make(chan result, 1)
	relay := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{}).Upgrade(response, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
		write := func(message linksocks.BaseMessage) error {
			frame, err := linksocks.PackMessage(message)
			if err != nil {
				return err
			}
			return conn.WriteMessage(websocket.BinaryMessage, frame)
		}
		read := func() (linksocks.BaseMessage, error) {
			_, frame, err := conn.ReadMessage()
			if err != nil {
				return nil, err
			}
			return linksocks.ParseMessage(frame)
		}
		if err := write(linksocks.AuthResponseMessage{Success: true}); err != nil {
			return
		}
		if _, err := read(); err != nil {
			return
		}
		if err := write(linksocks.WebRegisterResponseMessage{Success: true, Host: "site.example.com", URL: "https://site.example.com"}); err != nil {
			return
		}
		if err := write(request); err != nil {
			return
		}
		message, err := read()
		if err != nil {
			return
		}
		head, ok := message.(linksocks.WebResponseMessage)
		if !ok {
			return
		}
		var body []byte
		for {
			message, err := read()
			if err != nil {
				return
			}
			data, ok := message.(linksocks.WebDataMessage)
			if !ok {
				return
			}
			body = append(body, data.Data...)
			if data.End {
				break
			}
		}
		results <- result{head: head, body: body}
	}))
	defer relay.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	opt.URL = relay.URL
	opt.Token = "test-token"
	opt.NoReconnect = true
	go func() {
		_ = linksocks.RunWeb(ctx, opt)
	}()

	select {
	case got := <-results:
		return got.head, got.body
	case <-time.After(8 * time.Second):
		t.Fatal("timed out waiting for the provider response")
		return linksocks.WebResponseMessage{}, nil
	}
}

// closedAddress returns a loopback address nothing listens on.
func closedAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return "http://" + address
}

// A backend that does not answer must reach the visitor as a page written for
// them, not as an internal error string.
func TestWebProviderServesDefaultErrorPage(t *testing.T) {
	head, body := runWebProviderOnce(t,
		linksocks.WebOption{Backends: []string{closedAddress(t)}},
		linksocks.WebRequestMessage{ChannelID: uuid.New(), Method: "GET", Path: "/down"},
	)
	if head.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", head.Status, body)
	}
	contentType := ""
	for _, header := range head.Headers {
		if strings.EqualFold(header[0], "Content-Type") {
			contentType = header[1]
		}
	}
	if !strings.Contains(contentType, "text/html") {
		t.Fatalf("Content-Type = %q, want text/html", contentType)
	}
	page := string(body)
	for _, want := range []string{"502", "Site temporarily unavailable", "did not answer"} {
		if !strings.Contains(page, want) {
			t.Errorf("error page does not mention %q: %s", want, page)
		}
	}
	if strings.Contains(page, "{{") {
		t.Errorf("error page kept a placeholder: %s", page)
	}
	if strings.Contains(page, "connect") || strings.Contains(page, "refused") {
		t.Errorf("error page leaks the internal reason: %s", page)
	}
}

// Operators can serve their own page; placeholders are filled in.
func TestWebProviderServesCustomErrorPage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "error.html")
	custom := "<html><body><h1>{{title}}</h1><p>{{message}}</p><p>code {{status}} · contact ops@example.com</p></body></html>"
	if err := os.WriteFile(path, []byte(custom), 0o644); err != nil {
		t.Fatal(err)
	}

	head, body := runWebProviderOnce(t,
		linksocks.WebOption{Backends: []string{closedAddress(t)}, ErrorPage: path},
		linksocks.WebRequestMessage{ChannelID: uuid.New(), Method: "GET", Path: "/down"},
	)
	if head.Status != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502; body: %s", head.Status, body)
	}
	page := string(body)
	for _, want := range []string{"contact ops@example.com", "Site temporarily unavailable", "code 502"} {
		if !strings.Contains(page, want) {
			t.Errorf("custom page does not contain %q: %s", want, page)
		}
	}
	if strings.Contains(page, "{{") {
		t.Errorf("custom page kept a placeholder: %s", page)
	}
}
