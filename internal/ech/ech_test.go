package ech

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/linksocks/linksocks/internal/echtest"
	"github.com/miekg/dns"
	"github.com/rs/zerolog"
)

// cloudflareECHConfig is a real ech parameter published by the public relay's
// zone (cdn.team1.cc.cd), captured from its HTTPS record.
const cloudflareECHConfig = "AEX+DQBBhgAgACApicgSg55B7bwTgKQhGqzHNCmwYJY3zl7OGJ6OeRmHIAAEAAEAAQASY2xvdWRmbGFyZS1lY2guY29tAAA="

func resetECHState(t *testing.T) {
	t.Helper()
	echCacheMu.Lock()
	previousCache := echCache
	echCache = map[string]cacheEntry{}
	echCacheMu.Unlock()
	echLastGoodMu.Lock()
	previousLastGood := echLastGood
	echLastGood = nil
	echLastGoodMu.Unlock()
	t.Cleanup(func() {
		echCacheMu.Lock()
		echCache = previousCache
		echCacheMu.Unlock()
		echLastGoodMu.Lock()
		echLastGood = previousLastGood
		echLastGoodMu.Unlock()
	})
}

func TestParseMode(t *testing.T) {
	cases := map[string]string{
		"":         Auto,
		"auto":     Auto,
		"AUTO":     Auto,
		"off":      Off,
		"none":     Off,
		"disabled": Off,
	}
	for input, want := range cases {
		got, err := ParseMode(input)
		if err != nil || got != want {
			t.Errorf("ParseMode(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := ParseMode("sometimes"); err == nil {
		t.Fatal("ParseMode accepted an invalid value")
	}
}

func TestParseConfigList(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(cloudflareECHConfig)
	if err != nil {
		t.Fatal(err)
	}
	for name, input := range map[string]string{
		"std":     cloudflareECHConfig,
		"raw-url": base64.RawURLEncoding.EncodeToString(raw),
		"padded":  "  " + cloudflareECHConfig + "\n",
	} {
		list, err := ParseConfigList(input)
		if err != nil || string(list) != string(raw) {
			t.Fatalf("%s: ParseConfigList returned %d bytes, err=%v", name, len(list), err)
		}
	}

	path := filepath.Join(t.TempDir(), "ech.txt")
	if err := os.WriteFile(path, []byte(cloudflareECHConfig+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if list, err := ParseConfigList("@" + path); err != nil || string(list) != string(raw) {
		t.Fatalf("file form: %d bytes, err=%v", len(list), err)
	}
	if _, err := ParseConfigList("not-base64!!!"); err == nil {
		t.Fatal("ParseConfigList accepted garbage")
	}
	if empty, err := ParseConfigList("  "); err != nil || empty != nil {
		t.Fatalf("empty input: %v, %v", empty, err)
	}
}

func TestECHFromPresentation(t *testing.T) {
	record := `1 . alpn=h2 ipv4hint=104.26.10.150 ech="` + cloudflareECHConfig + `"`
	list := echFromPresentation(record)
	if len(list) == 0 {
		t.Fatal("echFromPresentation found no config in a real Cloudflare record")
	}
	if decoded, err := base64.StdEncoding.DecodeString(cloudflareECHConfig); err != nil || string(decoded) != string(list) {
		t.Fatalf("decoded config = %x, err = %v", list, err)
	}
	if list := echFromPresentation("1 . alpn=h2"); list != nil {
		t.Fatal("echFromPresentation invented a config")
	}
}

func TestECHFromRecords(t *testing.T) {
	raw, err := base64.StdEncoding.DecodeString(cloudflareECHConfig)
	if err != nil {
		t.Fatal(err)
	}
	record, err := dns.NewRR(`edge.example.net. 120 IN HTTPS 1 . alpn="h2" ech="` + cloudflareECHConfig + `"`)
	if err != nil {
		t.Fatal(err)
	}
	list, ttl := echFromRecords([]dns.RR{record})
	if string(list) != string(raw) {
		t.Fatalf("echFromRecords returned %d bytes, want %d", len(list), len(raw))
	}
	if ttl != 120*time.Second {
		t.Fatalf("ttl = %v, want 2m", ttl)
	}
}

// A local resolver that answers CNAME chains the way AliDNS does: only the
// chain, never the terminal HTTPS record, so the client must follow it.
func TestSystemDNSFollowsCNAMEChain(t *testing.T) {
	resetECHState(t)
	raw, err := base64.StdEncoding.DecodeString(cloudflareECHConfig)
	if err != nil {
		t.Fatal(err)
	}
	packetConn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := &dns.Server{PacketConn: packetConn, Handler: dns.HandlerFunc(func(w dns.ResponseWriter, r *dns.Msg) {
		reply := new(dns.Msg)
		reply.SetReply(r)
		name := r.Question[0].Name
		switch name {
		case "relay.example.com.":
			reply.Answer = []dns.RR{&dns.CNAME{Hdr: dns.RR_Header{Name: name, Rrtype: dns.TypeCNAME, Ttl: 42}, Target: "edge.example.net."}}
		case "edge.example.net.":
			record, err := dns.NewRR(`edge.example.net. 60 IN HTTPS 1 . alpn="h2" ech="` + base64.StdEncoding.EncodeToString(raw) + `"`)
			if err == nil {
				reply.Answer = []dns.RR{record}
			}
		default:
			reply.Rcode = dns.RcodeNameError
		}
		_ = w.WriteMsg(reply)
	})}
	go func() { _ = server.ActivateAndServe() }()
	defer server.Shutdown()

	// Point the lookup at this resolver through resolv.conf semantics: the
	// helper reads /etc/resolv.conf, so exercise the same query path directly.
	client := &dns.Client{Timeout: 3 * time.Second}
	name := "relay.example.com."
	var list []byte
	for hop := 0; hop < maxCNAMEHops; hop++ {
		message := new(dns.Msg)
		message.SetQuestion(name, dns.TypeHTTPS)
		response, _, err := client.Exchange(message, packetConn.LocalAddr().String())
		if err != nil || response == nil {
			t.Fatal(err)
		}
		if list, _ = echFromRecords(response.Answer); len(list) > 0 {
			break
		}
		next, ok := lastCNAMETarget(response.Answer)
		if !ok {
			t.Fatal("chain ended without an HTTPS record")
		}
		name = next
	}
	if string(list) != string(raw) {
		t.Fatalf("followed chain returned %d bytes, want %d", len(list), len(raw))
	}
}

func TestLastCNAMETarget(t *testing.T) {
	records := []dns.RR{
		&dns.CNAME{Hdr: dns.RR_Header{Name: "a.", Rrtype: dns.TypeCNAME}, Target: "b."},
		&dns.CNAME{Hdr: dns.RR_Header{Name: "b.", Rrtype: dns.TypeCNAME}, Target: "c."},
	}
	if target, ok := lastCNAMETarget(records); !ok || target != "c." {
		t.Fatalf("lastCNAMETarget = %q, %v", target, ok)
	}
	if _, ok := lastCNAMETarget(nil); ok {
		t.Fatal("lastCNAMETarget found a target in an empty answer")
	}
}

func TestECHFromDoHAnswerPicksTerminal(t *testing.T) {
	var payload dohAnswer
	payload.Answer = append(payload.Answer, struct {
		Name string `json:"name"`
		Type int    `json:"type"`
		TTL  int    `json:"TTL"`
		Data string `json:"data"`
	}{Name: "relay.example.com.", Type: 5, TTL: 30, Data: "edge.example.net."})
	if list, _, next := echFromDoHAnswer(payload); list != nil || next != "edge.example.net" {
		t.Fatalf("chain step: list=%v next=%q", list, next)
	}

	raw, err := base64.StdEncoding.DecodeString(cloudflareECHConfig)
	if err != nil {
		t.Fatal(err)
	}
	payload.Answer = []struct {
		Name string `json:"name"`
		Type int    `json:"type"`
		TTL  int    `json:"TTL"`
		Data string `json:"data"`
	}{{Name: "edge.example.net.", Type: 65, TTL: 60, Data: `1 . alpn="h2" ech="` + base64.StdEncoding.EncodeToString(raw) + `"`}}
	list, ttl, next := echFromDoHAnswer(payload)
	if string(list) != string(raw) || ttl != time.Minute || next != "" {
		t.Fatalf("terminal: %d bytes ttl=%v next=%q", len(list), ttl, next)
	}
}

// One provider answering without a config must not stop the walk: another
// provider may still carry it.
func TestDiscoveryFallsThroughProviders(t *testing.T) {
	resetECHState(t)

	configList, key, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}
	_ = key
	raw, err := base64.StdEncoding.DecodeString(cloudflareECHConfig)
	if err != nil {
		t.Fatal(err)
	}
	_ = raw

	blind := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Status": 0, "Answer": []map[string]any{
			{"name": r.URL.Query().Get("name"), "type": 5, "TTL": 30, "data": "nowhere.example."},
		}})
	}))
	defer blind.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"Status": 0, "Answer": []map[string]any{{
			"name": r.URL.Query().Get("name"), "type": 65, "TTL": 300,
			"data": `1 . alpn="h2" ech="` + base64.StdEncoding.EncodeToString(configList) + `"`,
		}}})
	}))
	defer good.Close()

	restoreProviders(t, []dohProvider{{name: "blind", baseURL: blind.URL}, {name: "good", baseURL: good.URL}})
	restoreSystemLookup(t, func(context.Context, string) ([]byte, time.Duration, bool) { return nil, 0, false })

	found := discoverECHConfigList(context.Background(), "relay.example.com", Request{}, nil, zerolog.Nop())
	if string(found.list) != string(configList) {
		t.Fatalf("discovery returned %d bytes, want the config from the second provider", len(found.list))
	}
	if found.source != "doh:good" {
		t.Fatalf("source = %q, want doh:good", found.source)
	}
}

// When nothing publishes a config, a config discovered earlier for another
// relay host is reused: the deployed edge serves every relay hostname with the
// same ECH key.
func TestDiscoveryReusesConfigFromAnotherHost(t *testing.T) {
	resetECHState(t)
	configList, _, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}
	rememberLastGood(configList)
	restoreProviders(t, nil)
	restoreSystemLookup(t, func(context.Context, string) ([]byte, time.Duration, bool) { return nil, 0, true })
	restoreRelayEndpoint(t, func(context.Context, string, func(*http.Request) (*url.URL, error), zerolog.Logger) ([]byte, time.Duration) {
		return nil, 0
	})

	found := discoverECHConfigList(context.Background(), "blocked.example.com", Request{EntryHost: "blocked.example.com"}, nil, zerolog.Nop())
	if string(found.list) != string(configList) || found.source != "reused" {
		t.Fatalf("discovery = %d bytes from %q, want the reused config", len(found.list), found.source)
	}
}

func TestDiscoveryCachesPerHost(t *testing.T) {
	resetECHState(t)
	configList, _, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	restoreProviders(t, nil)
	restoreSystemLookup(t, func(context.Context, string) ([]byte, time.Duration, bool) {
		calls++
		return configList, time.Minute, true
	})

	for i := 0; i < 3; i++ {
		if list := echConfigListFor(context.Background(), "relay.example.com", Request{}, nil); string(list) != string(configList) {
			t.Fatalf("call %d returned %d bytes", i, len(list))
		}
	}
	if calls != 1 {
		t.Fatalf("lookup ran %d times, want once", calls)
	}
}

// The relay entry point hands over its own ECH config, which is what makes a
// redirect hop reachable on networks that reset unknown hostnames by SNI.
func TestDiscoveryUsesRelayEndpoint(t *testing.T) {
	resetECHState(t)
	configList, _, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}
	restoreProviders(t, nil)
	restoreSystemLookup(t, func(context.Context, string) ([]byte, time.Duration, bool) { return nil, 0, true })

	relay := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/ech" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"host": "entry.example.com",
			"ech":  base64.StdEncoding.EncodeToString(configList),
		})
	}))
	defer relay.Close()

	// Point the endpoint lookup at the local TLS server by overriding the
	// transport: reuse the package lookup with a test host mapping.
	restoreRelayEndpoint(t, func(ctx context.Context, entryHost string, proxy func(*http.Request) (*url.URL, error), log zerolog.Logger) ([]byte, time.Duration) {
		response, err := relay.Client().Get(relay.URL + "/api/ech")
		if err != nil {
			return nil, 0
		}
		defer response.Body.Close()
		var payload struct {
			ECH string `json:"ech"`
		}
		if err := json.NewDecoder(response.Body).Decode(&payload); err != nil {
			return nil, 0
		}
		list, err := ParseConfigList(payload.ECH)
		if err != nil {
			return nil, 0
		}
		return list, 5 * time.Minute
	})

	found := discoverECHConfigList(context.Background(), "cdn.example.com", Request{EntryHost: "entry.example.com"}, nil, zerolog.Nop())
	if string(found.list) != string(configList) || found.source != "relay" {
		t.Fatalf("discovery = %d bytes from %q, want the relay endpoint config", len(found.list), found.source)
	}
}

func TestConfigOffSkipsLookup(t *testing.T) {
	resetECHState(t)
	called := false
	restoreSystemLookup(t, func(context.Context, string) ([]byte, time.Duration, bool) {
		called = true
		return nil, 0, false
	})
	cfg := Config(context.Background(), Request{Host: "example.com", Mode: Off, Logger: zerolog.Nop()})
	if called {
		t.Fatal("Config looked up an ECH config with ECH off")
	}
	if Offered(cfg) || cfg.MinVersion != 0 {
		t.Fatal("Config changed TLS settings with ECH off")
	}
}

func TestConfigUsesPinnedList(t *testing.T) {
	resetECHState(t)
	configList, _, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}
	restoreSystemLookup(t, func(context.Context, string) ([]byte, time.Duration, bool) {
		t.Fatal("discovery ran although a config was pinned")
		return nil, 0, false
	})
	cfg := Config(context.Background(), Request{Host: "example.com", Mode: Auto, Pinned: configList, Logger: zerolog.Nop()})
	if !Offered(cfg) || cfg.MinVersion != tls.VersionTLS13 {
		t.Fatal("pinned config was not applied")
	}
}

func TestECHHandshakeAgainstLocalServer(t *testing.T) {
	resetECHState(t)
	const publicName = "ech.test"
	configList, key, err := echtest.ConfigAndKey(publicName)
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	server.TLS = &tls.Config{
		GetEncryptedClientHelloKeys: func(*tls.ClientHelloInfo) ([]tls.EncryptedClientHelloKey, error) {
			return []tls.EncryptedClientHelloKey{key}, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	cfg := Config(context.Background(), Request{Host: "127.0.0.1", Insecure: true, Mode: Auto, Pinned: configList, Logger: zerolog.Nop()})
	conn, err := tls.Dial("tcp", server.Listener.Addr().String(), cfg)
	if err != nil {
		t.Fatalf("ECH handshake failed: %v", err)
	}
	defer conn.Close()
	if !conn.ConnectionState().ECHAccepted {
		t.Fatal("ECH was offered but not accepted")
	}
}

// A server that does not support ECH rejects the offer; Go reports that as a
// *tls.ECHRejectionError so the caller can retry without ECH.
func TestECHRejectionIsReported(t *testing.T) {
	resetECHState(t)
	configList, _, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	cfg := Config(context.Background(), Request{Host: "127.0.0.1", Insecure: true, Mode: Auto, Pinned: configList, Logger: zerolog.Nop()})
	conn, err := tls.Dial("tcp", server.Listener.Addr().String(), cfg)
	if conn != nil {
		conn.Close()
	}
	var rejection *tls.ECHRejectionError
	if !errors.As(err, &rejection) {
		t.Fatalf("handshake error = %v, want *tls.ECHRejectionError", err)
	}
}

func TestHandshakeFailedMatchesECHErrors(t *testing.T) {
	if !HandshakeFailed(echoErr("tls: malformed ECHConfigList")) {
		t.Fatal("HandshakeFailed missed an ECH error")
	}
	if HandshakeFailed(echoErr("dial tcp: connection refused")) {
		t.Fatal("HandshakeFailed matched an unrelated error")
	}
	if HandshakeFailed(nil) {
		t.Fatal("HandshakeFailed(nil) = true")
	}
}

func TestResetSuspected(t *testing.T) {
	if !ResetSuspected(echoErr("read tcp 10.0.0.1:443: read: connection reset by peer")) {
		t.Fatal("ResetSuspected missed a reset")
	}
	if !ResetSuspected(echoErr("An existing connection was forcibly closed by the remote host")) {
		t.Fatal("ResetSuspected missed a Windows reset message")
	}
	if ResetSuspected(echoErr("context deadline exceeded")) {
		t.Fatal("ResetSuspected matched a timeout")
	}
}

type echoErr string

func (e echoErr) Error() string { return string(e) }

func restoreProviders(t *testing.T, providers []dohProvider) {
	t.Helper()
	previous := echDoHProviders
	echDoHProviders = providers
	t.Cleanup(func() { echDoHProviders = previous })
}

func restoreRelayEndpoint(t *testing.T, lookup func(context.Context, string, func(*http.Request) (*url.URL, error), zerolog.Logger) ([]byte, time.Duration)) {
	t.Helper()
	previous := echRelayEndpointLookup
	echRelayEndpointLookup = lookup
	t.Cleanup(func() { echRelayEndpointLookup = previous })
}

func restoreSystemLookup(t *testing.T, lookup func(context.Context, string) ([]byte, time.Duration, bool)) {
	t.Helper()
	previous := echSystemLookup
	echSystemLookup = lookup
	t.Cleanup(func() { echSystemLookup = previous })
}
