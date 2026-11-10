package linksocks

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

func TestValidDomainPrefix(t *testing.T) {
	valid := []string{"blog", "a", "a-b", "my-site-2", strings.Repeat("a", 63)}
	for _, prefix := range valid {
		if !validDomainPrefix(prefix) {
			t.Errorf("validDomainPrefix(%q) = false, want true", prefix)
		}
	}
	invalid := []string{"", "-blog", "blog-", "bad_label", "blog.example.com", strings.Repeat("a", 64), "BLOG"}
	for _, prefix := range invalid {
		if validDomainPrefix(prefix) {
			t.Errorf("validDomainPrefix(%q) = true, want false", prefix)
		}
	}
}

func TestRunWebRejectsFullDomainAsPrefix(t *testing.T) {
	err := RunWeb(context.Background(), WebOption{
		HostPrefix: "blog.example.com",
		Backends:   []string{"http://127.0.0.1:8080"},
	})
	if err == nil || !strings.Contains(err.Error(), "single DNS label") {
		t.Fatalf("RunWeb error = %v, want domain prefix validation error", err)
	}
}

func TestRunWebChecksServerVersion(t *testing.T) {
	tests := []struct {
		name         string
		version      byte
		wantRegister bool
		wantError    string
		wantWarning  bool
	}{
		{name: "old worker", version: 0x01, wantError: "required minimum"},
		{name: "wrong major", version: 0x10, wantError: "unsupported protocol major"},
		{name: "current worker", version: ProtocolVersion, wantRegister: true, wantError: "unsupported protocol major"},
		{name: "newer minor", version: 0x05, wantRegister: true, wantError: "unsupported protocol major", wantWarning: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			registered := make(chan bool, 1)
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				ws, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
				if err != nil {
					registered <- false
					return
				}
				defer ws.Close()
				auth, _ := PackMessage(AuthResponseMessage{Success: true})
				auth[0] = test.version
				if err := ws.WriteMessage(websocket.BinaryMessage, auth); err != nil {
					registered <- false
					return
				}
				_ = ws.SetReadDeadline(time.Now().Add(2 * time.Second))
				_, data, err := ws.ReadMessage()
				if err != nil {
					registered <- false
					return
				}
				msg, err := ParseMessage(data)
				_, ok := msg.(WebRegisterMessage)
				registered <- err == nil && ok
				if err != nil || !ok {
					return
				}
				responseFrame, _ := PackMessage(WebRegisterResponseMessage{Success: true, Host: "test.example.com", URL: "https://test.example.com"})
				responseFrame[0] = test.version
				_ = ws.WriteMessage(websocket.BinaryMessage, responseFrame)
				invalidFrame, _ := PackMessage(LogMessage{Level: LogLevelInfo, Msg: "invalid major"})
				invalidFrame[0] = 0x10
				_ = ws.WriteMessage(websocket.BinaryMessage, invalidFrame)
			}))
			defer server.Close()

			var logs bytes.Buffer
			ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
			defer cancel()
			err := RunWeb(ctx, WebOption{
				URL:      server.URL,
				Token:    "test-token",
				Backends: []string{"http://127.0.0.1:8080"},
				Logger:   zerolog.New(&logs),
			})
			var nonRetriable *nonRetriableError
			if !errors.As(err, &nonRetriable) || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("RunWeb error = %v, want non-retriable %q", err, test.wantError)
			}
			if got := <-registered; got != test.wantRegister {
				t.Fatalf("registered = %v, want %v", got, test.wantRegister)
			}
			if got := strings.Contains(logs.String(), "Protocol minor version mismatch"); got != test.wantWarning && test.wantRegister {
				t.Fatalf("minor version warning = %v, want %v; logs: %s", got, test.wantWarning, logs.String())
			}
		})
	}
}

func TestWebRegistrationResponseTimesOut(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ws, err := (&websocket.Upgrader{}).Upgrade(response, request, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_, _, _ = ws.ReadMessage()
	}))
	defer server.Close()

	wsURL := "ws" + strings.TrimPrefix(server.URL, "http")
	ws, _, err := websocket.DefaultDialer.Dial(wsURL, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ws.Close()
	client := &webConn{ws: ws, log: zerolog.Nop()}
	err = client.readRegistrationResponse(50 * time.Millisecond)
	var networkError net.Error
	if !errors.As(err, &networkError) || !networkError.Timeout() || !strings.Contains(err.Error(), "relay may not support Web") {
		t.Fatalf("registration error = %v, want timeout", err)
	}
}

func TestWebMessageVersionMustMeetFeatureMinimum(t *testing.T) {
	client := &webConn{log: zerolog.Nop()}
	legacyLog, err := PackMessage(LogMessage{Level: LogLevelInfo, Msg: "legacy peer"})
	if err != nil {
		t.Fatal(err)
	}
	legacyLog[0] = 0x01
	if _, _, err := client.parseWebMessage(legacyLog); err != nil {
		t.Fatalf("compatible legacy log should be allowed: %v", err)
	}

	for _, message := range []BaseMessage{
		WebRegisterResponseMessage{}, WebRequestMessage{}, WebDataMessage{}, WebCancelMessage{}, WebRotateMessage{},
	} {
		frame, err := PackMessage(message)
		if err != nil {
			t.Fatal(err)
		}
		frame[0] = 0x02
		_, _, err = client.parseWebMessage(frame)
		var nonRetriable *nonRetriableError
		if !errors.As(err, &nonRetriable) || !strings.Contains(err.Error(), "required minimum") {
			t.Errorf("%s: error = %v, want Web minimum error", message.GetType(), err)
		}
	}
}

func TestRenderWebErrorPageFillsPlaceholders(t *testing.T) {
	page := string(renderWebErrorPage(nil, http.StatusBadGateway))
	for _, want := range []string{"502", "Site temporarily unavailable", "did not answer", "<!doctype html>"} {
		if !strings.Contains(page, want) {
			t.Errorf("built-in page does not contain %q", want)
		}
	}
	if strings.Contains(page, "{{") {
		t.Errorf("built-in page kept a placeholder: %s", page)
	}

	custom := renderWebErrorPage([]byte("<p>{{title}}|{{message}}|{{status}}</p>"), http.StatusServiceUnavailable)
	if got, want := string(custom), "<p>Site busy|The site is handling as many requests as it can right now. Wait a moment and try again.|503</p>"; got != want {
		t.Errorf("custom page = %q, want %q", got, want)
	}

	// A page without placeholders is served untouched.
	raw := renderWebErrorPage([]byte("<p>maintenance</p>"), http.StatusBadGateway)
	if string(raw) != "<p>maintenance</p>" {
		t.Errorf("plain page = %q", raw)
	}
}

// A status without wording of its own still gets a page a visitor can read.
func TestWebErrorTextFallsBackToGenericWording(t *testing.T) {
	title, message := webErrorText(http.StatusTeapot)
	if title == "" || message == "" {
		t.Fatalf("generic wording is empty: %q %q", title, message)
	}
	page := string(renderWebErrorPage(nil, http.StatusTeapot))
	if !strings.Contains(page, "418") || !strings.Contains(page, title) || !strings.Contains(page, message) {
		t.Errorf("generic page misses status or wording: %s", page)
	}
}

func TestRunWebRejectsUnreadableErrorPage(t *testing.T) {
	err := RunWeb(context.Background(), WebOption{
		Backends:  []string{"http://127.0.0.1:8080"},
		ErrorPage: filepath.Join(t.TempDir(), "missing.html"),
	})
	if err == nil || !strings.Contains(err.Error(), "error page") {
		t.Fatalf("RunWeb error = %v, want error page read failure", err)
	}
}
