package linksocks

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// TestCloudWebEndToEnd exercises a deployed linksocks.js relay. It is opt-in so
// the default suite stays offline:
//
//	LINKSOCKS_CLOUD_URL=wss://l.zetx.tech \
//	LINKSOCKS_CLOUD_TOKEN=<dashboard token, optional> \
//	go test ./linksocks -run TestCloudWebEndToEnd -v -count=1
//
// The relay must already run the version that supports the web proxy
// (protocol 0x03+) and the WebSocket tunnel (0x04+).
func TestCloudWebEndToEnd(t *testing.T) {
	relayURL := strings.TrimSpace(os.Getenv("LINKSOCKS_CLOUD_URL"))
	if relayURL == "" {
		t.Skip("set LINKSOCKS_CLOUD_URL to test a deployed relay")
	}
	token := strings.TrimSpace(os.Getenv("LINKSOCKS_CLOUD_TOKEN"))

	origin := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/base/ws" {
			conn, err := (&websocket.Upgrader{
				Subprotocols: []string{"chat"},
				CheckOrigin:  func(*http.Request) bool { return true },
			}).Upgrade(response, request, nil)
			if err != nil {
				http.Error(response, err.Error(), http.StatusBadRequest)
				return
			}
			defer conn.Close()
			for {
				opcode, data, err := conn.ReadMessage()
				if err != nil {
					return
				}
				if opcode == websocket.TextMessage && string(data) == "meta" {
					payload, _ := json.Marshal(map[string]string{
						"path":     request.URL.RequestURI(),
						"protocol": conn.Subprotocol(),
						"host":     request.Header.Get("X-Forwarded-Host"),
					})
					if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
						return
					}
					continue
				}
				if err := conn.WriteMessage(opcode, data); err != nil {
					return
				}
			}
		}

		body, _ := io.ReadAll(request.Body)
		response.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(response).Encode(map[string]string{
			"method":        request.Method,
			"path":          request.URL.RequestURI(),
			"forwardedHost": request.Header.Get("X-Forwarded-Host"),
			"body":          fmt.Sprintf("%x", body),
		})
	}))
	defer origin.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	hosts := make(chan string, 8)
	runResult := make(chan error, 1)
	go func() {
		runResult <- RunWeb(ctx, WebOption{
			URL:          relayURL,
			Token:        token,
			Backends:     []string{origin.URL + "/base"},
			NoReconnect:  true,
			Logger:       zerolog.New(io.Discard),
			OnRegistered: func(host string) { hosts <- host },
		})
	}()

	var host string
	select {
	case host = <-hosts:
	case err := <-runResult:
		t.Fatalf("relay did not accept the registration: %v", err)
	case <-time.After(45 * time.Second):
		t.Fatalf("timed out waiting for %s to register the site", relayURL)
	}
	t.Logf("relay %s assigned %s", relayURL, host)

	// The relay terminates TLS for the published host, so requests go to the
	// public hostname rather than to the local origin.
	client := &http.Client{
		Timeout: 30 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
		},
	}
	siteURL := "https://" + host + "/base"

	t.Run("http get keeps path query and host", func(t *testing.T) {
		response, err := client.Get(siteURL + "/probe?value=cloud")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.StatusCode)
		}
		var payload map[string]string
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload["path"] != "/base/probe?value=cloud" {
			t.Fatalf("origin path = %q", payload["path"])
		}
		if payload["forwardedHost"] != host {
			t.Fatalf("X-Forwarded-Host = %q, want %q", payload["forwardedHost"], host)
		}
	})

	t.Run("http post round trips binary body", func(t *testing.T) {
		body := make([]byte, 64*1024)
		for index := range body {
			body[index] = byte(index)
		}
		response, err := client.Post(siteURL+"/upload", "application/octet-stream", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("status = %d, want 200", response.StatusCode)
		}
		var payload map[string]string
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if payload["method"] != "POST" {
			t.Fatalf("origin method = %q", payload["method"])
		}
		if payload["body"] != fmt.Sprintf("%x", body) {
			t.Fatal("origin body does not match the uploaded payload")
		}
	})

	t.Run("websocket echoes text and binary frames", func(t *testing.T) {
		dialer := websocket.Dialer{
			HandshakeTimeout: 30 * time.Second,
			Subprotocols:     []string{"chat"},
			TLSClientConfig:  &tls.Config{MinVersion: tls.VersionTLS12},
		}
		conn, response, err := dialer.Dial("wss://"+host+"/base/ws?room=cloud", nil)
		if err != nil {
			status := "no response"
			if response != nil {
				status = response.Status
			}
			t.Fatalf("websocket dial: %v (%s)", err, status)
		}
		defer conn.Close()
		if conn.Subprotocol() != "chat" {
			t.Fatalf("negotiated subprotocol = %q, want chat", conn.Subprotocol())
		}
		_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))

		if err := conn.WriteMessage(websocket.TextMessage, []byte("meta")); err != nil {
			t.Fatalf("write meta: %v", err)
		}
		_, payload, err := conn.ReadMessage()
		if err != nil {
			t.Fatalf("read meta: %v", err)
		}
		var meta map[string]string
		if err := json.Unmarshal(payload, &meta); err != nil {
			t.Fatalf("decode meta: %v", err)
		}
		if meta["path"] != "/base/ws?room=cloud" || meta["protocol"] != "chat" || meta["host"] != host {
			t.Fatalf("origin websocket metadata = %#v", meta)
		}

		for _, frame := range []struct {
			opcode int
			data   []byte
		}{
			{websocket.TextMessage, []byte("hello cloud")},
			{websocket.BinaryMessage, []byte{0, 1, 255}},
		} {
			if err := conn.WriteMessage(frame.opcode, frame.data); err != nil {
				t.Fatalf("write frame: %v", err)
			}
			opcode, echoed, err := conn.ReadMessage()
			if err != nil {
				t.Fatalf("read frame: %v", err)
			}
			if opcode != frame.opcode || !bytes.Equal(echoed, frame.data) {
				t.Fatalf("echo mismatch: opcode=%d data=%v", opcode, echoed)
			}
		}
		if err := conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.CloseNormalClosure, "done"), time.Now().Add(5*time.Second)); err != nil {
			t.Fatalf("close: %v", err)
		}
	})

	cancel()
	select {
	case <-runResult:
	case <-time.After(15 * time.Second):
	}
}
