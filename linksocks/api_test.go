package linksocks

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newTestAPIServer builds a LinkSocksServer with the given primary key and
// additional authentication-only keys, wires the API handler into an httptest
// server, and returns the URL and the server instance.
func newTestAPIServer(t *testing.T, apiKey string, apiKeys []string) (string, *LinkSocksServer) {
	t.Helper()
	srv := NewLinkSocksServer(&ServerOption{
		APIKey:  apiKey,
		APIKeys: apiKeys,
	})
	mux := http.NewServeMux()
	NewAPIHandler(srv, apiKey).RegisterHandlers(mux)
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	return ts.URL, srv
}

func apiRequest(t *testing.T, method, url, apiKey string, body []byte) (int, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("build %s request: %v", method, err)
	}
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("do %s: %v", method, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp.StatusCode, buf.Bytes()
}

// TestAPIAccessConfig verifies GET and PUT semantics of /api/config/access.
func TestAPIAccessConfig(t *testing.T) {
	url, _ := newTestAPIServer(t, "primary", nil)

	// Auth required.
	code, _ := apiRequest(t, http.MethodGet, url+"/api/config/access", "wrong", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for bad key, got %d", code)
	}

	// Initial state: no rules (nil serializes to null).
	code, body := apiRequest(t, http.MethodGet, url+"/api/config/access", "primary", nil)
	if code != http.StatusOK {
		t.Fatalf("GET access: status %d body %s", code, body)
	}
	var resp AccessConfigResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal GET access: %v", err)
	}
	if len(resp.Entry) != 0 || len(resp.Dial) != 0 {
		t.Fatalf("expected empty rules, got entry=%v dial=%v", resp.Entry, resp.Dial)
	}

	// Set entry+dial.
	put := AccessConfigRequest{
		Entry: &[]AccessRule{{Addrs: []string{"192.168.1.0/24"}, Ports: []PortSpec{SinglePort(22)}}},
		Dial:  &[]AccessRule{{Addrs: []string{"10.0.0.0/8"}, Ports: []PortSpec{PortRange(8000, 9000)}}},
	}
	raw, _ := json.Marshal(put)
	code, body = apiRequest(t, http.MethodPut, url+"/api/config/access", "primary", raw)
	if code != http.StatusOK {
		t.Fatalf("PUT access: status %d body %s", code, body)
	}

	code, body = apiRequest(t, http.MethodGet, url+"/api/config/access", "primary", nil)
	if code != http.StatusOK {
		t.Fatalf("GET access after PUT: status %d", code)
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal GET access after PUT: %v", err)
	}
	if len(resp.Entry) != 1 || resp.Entry[0].Addrs[0] != "192.168.1.0/24" {
		t.Fatalf("unexpected entry rules: %+v", resp.Entry)
	}
	if len(resp.Dial) != 1 || resp.Dial[0].Ports[0] != (PortSpec{Start: 8000, End: 9000}) {
		t.Fatalf("unexpected dial rules: %+v", resp.Dial)
	}

	// Updating only one side keeps the other.
	putDialOnly := AccessConfigRequest{Dial: &[]AccessRule{{Addrs: []string{"10.1.0.0/16"}}}}
	raw, _ = json.Marshal(putDialOnly)
	code, body = apiRequest(t, http.MethodPut, url+"/api/config/access", "primary", raw)
	if code != http.StatusOK {
		t.Fatalf("PUT access dial-only: status %d", code)
	}
	code, body = apiRequest(t, http.MethodGet, url+"/api/config/access", "primary", nil)
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(resp.Entry) != 1 {
		t.Fatalf("entry should be preserved, got %+v", resp.Entry)
	}
	if len(resp.Dial) != 1 || resp.Dial[0].Addrs[0] != "10.1.0.0/16" {
		t.Fatalf("dial should be replaced, got %+v", resp.Dial)
	}

	// Invalid rule rejected.
	bad := AccessConfigRequest{Entry: &[]AccessRule{{Addrs: []string{"not-an-addr"}}}}
	raw, _ = json.Marshal(bad)
	code, _ = apiRequest(t, http.MethodPut, url+"/api/config/access", "primary", raw)
	if code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid rule, got %d", code)
	}
}

// TestAPIGlobalRulesEnforced verifies that rules set via /api/config/access
// take effect on the server relay, and that per-token rules still win.
func TestAPIGlobalRulesEnforced(t *testing.T) {
	url, srv := newTestAPIServer(t, "primary", nil)

	rules := AccessConfigRequest{
		Entry: &[]AccessRule{{Addrs: []string{"10.0.0.0/8"}}},
		Dial:  &[]AccessRule{{Addrs: []string{"10.1.0.0/16"}}},
	}
	raw, _ := json.Marshal(rules)
	if code, body := apiRequest(t, http.MethodPut, url+"/api/config/access", "primary", raw); code != http.StatusOK {
		t.Fatalf("PUT access: %d %s", code, body)
	}

	ac := srv.relay.EntryAccessControl()
	if ac == nil || ac.Empty() {
		t.Fatal("entry access control should be set")
	}
	if !ac.Allow("10.0.1.5", 80) {
		t.Fatal("10.0.1.5 should be allowed by global entry rule")
	}
	if ac.Allow("192.168.1.5", 80) {
		t.Fatal("192.168.1.5 should be blocked by global entry rule")
	}

	dialAC := srv.relay.DialAccessControl()
	if dialAC == nil || dialAC.Empty() {
		t.Fatal("dial access control should be set")
	}
	if !dialAC.Allow("10.1.2.3", 443) {
		t.Fatal("10.1.2.3 should be allowed by global dial rule")
	}
	if dialAC.Allow("10.2.3.4", 443) {
		t.Fatal("10.2.3.4 should be blocked by global dial rule")
	}
}

// TestAPIKeyNoKeyRules verifies that API keys only authenticate: tokens
// created with any valid key carry no inherited rules, and rules must be
// supplied explicitly per token.
func TestAPIKeyNoKeyRules(t *testing.T) {
	url, srv := newTestAPIServer(t, "primary", []string{"restricted"})

	// Request without explicit rules -> no restrictions at all.
	create := `{
	  "type": "forward",
	  "token": "my_token"
	}`
	code, body := apiRequest(t, http.MethodPost, url+"/api/token", "restricted", []byte(create))
	if code != http.StatusOK {
		t.Fatalf("create forward token: %d %s", code, body)
	}

	srv.mu.Lock()
	ac := srv.forwardTokenAC["my_token"]
	srv.mu.Unlock()
	if ac != nil && !ac.Empty() {
		t.Fatal("tokens created without explicit rules must have no restrictions")
	}

	// Explicit per-token rules apply.
	create = `{
	  "type": "forward",
	  "token": "my_token_2",
	  "rules": [
	    {"addrs": ["192.168.0.0/16"], "ports": [22]}
	  ]
	}`
	code, body = apiRequest(t, http.MethodPost, url+"/api/token", "restricted", []byte(create))
	if code != http.StatusOK {
		t.Fatalf("create forward token 2: %d %s", code, body)
	}
	srv.mu.Lock()
	ac = srv.forwardTokenAC["my_token_2"]
	srv.mu.Unlock()
	if ac == nil || ac.Empty() {
		t.Fatal("explicit rules should apply")
	}
	if !ac.Allow("192.168.1.5", 22) {
		t.Fatal("192.168.1.5:22 should be allowed")
	}
	if ac.Allow("10.0.1.5", 443) {
		t.Fatal("unrelated destination should be blocked")
	}
}

// TestAPIKeyAuth verifies that additional keys authenticate successfully and
// unknown keys are rejected.
func TestAPIKeyAuth(t *testing.T) {
	url, _ := newTestAPIServer(t, "primary", []string{"second"})

	for _, key := range []string{"primary", "second"} {
		code, _ := apiRequest(t, http.MethodGet, url+"/api/status", key, nil)
		if code != http.StatusOK {
			t.Fatalf("key %q should authenticate, got %d", key, code)
		}
	}
	code, _ := apiRequest(t, http.MethodGet, url+"/api/status", "unknown", nil)
	if code != http.StatusUnauthorized {
		t.Fatalf("unknown key should be rejected, got %d", code)
	}
}
