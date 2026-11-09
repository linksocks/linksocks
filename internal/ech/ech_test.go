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
	"strings"
	"testing"
	"time"

	"github.com/linksocks/linksocks/internal/echtest"
	"github.com/miekg/dns"
	"github.com/rs/zerolog"
)

// cloudflareECHConfig is a real ech parameter published by the public relay's
// zone (cdn.team1.cc.cd), captured from its HTTPS record.
const cloudflareECHConfig = "AEX+DQBBhgAgACApicgSg55B7bwTgKQhGqzHNCmwYJY3zl7OGJ6OeRmHIAAEAAEAAQASY2xvdWRmbGFyZS1lY2guY29tAAA="

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

func TestECHFromRecordsFollowsCNAMEStyleAnswers(t *testing.T) {
	zone := dns.Fqdn("cdn.example.com")
	answer := []dns.RR{
		&dns.CNAME{Hdr: dns.RR_Header{Name: zone, Rrtype: dns.TypeCNAME, Ttl: 300}, Target: "edge.example.net."},
	}
	raw, err := base64.StdEncoding.DecodeString(cloudflareECHConfig)
	if err != nil {
		t.Fatal(err)
	}
	https := &dns.HTTPS{SVCB: dns.SVCB{
		Hdr:   dns.RR_Header{Name: "edge.example.net.", Rrtype: dns.TypeHTTPS, Ttl: 120},
		Value: []dns.SVCBKeyValue{&dns.SVCBECHConfig{ECH: raw}},
	}}
	answer = append(answer, https)

	list, ttl := echFromRecords(answer)
	if string(list) != string(raw) {
		t.Fatalf("echFromRecords returned %d bytes, want %d", len(list), len(raw))
	}
	if ttl != 120*time.Second {
		t.Fatalf("ttl = %v, want 2m", ttl)
	}
}

func TestECHHandshakeAgainstLocalServer(t *testing.T) {
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

	serverName := strings.TrimPrefix(server.URL, "https://")
	serverName = strings.Split(serverName, ":")[0]

	cfg := &tls.Config{
		ServerName:                     serverName,
		InsecureSkipVerify:             true, // the httptest certificate is self-signed
		MinVersion:                     tls.VersionTLS13,
		EncryptedClientHelloConfigList: configList,
		EncryptedClientHelloRejectionVerify: func(tls.ConnectionState) error {
			t.Error("server rejected ECH")
			return nil
		},
	}
	conn, err := tls.Dial("tcp", server.Listener.Addr().String(), cfg)
	if err != nil {
		t.Fatalf("ECH handshake failed: %v", err)
	}
	defer conn.Close()
	if !conn.ConnectionState().ECHAccepted {
		t.Fatal("ECH was offered but not accepted")
	}
}

func TestConfigDiscoversAndAppliesECH(t *testing.T) {
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

	host := strings.Split(strings.TrimPrefix(server.URL, "https://"), ":")[0]

	// Serve the config from a stub DNS-over-HTTPS resolver.
	doH := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("type") != "HTTPS" {
			http.Error(w, "unexpected query", http.StatusBadRequest)
			return
		}
		payload := map[string]any{
			"Status": 0,
			"Answer": []map[string]any{{
				"name": r.URL.Query().Get("name"),
				"type": 65,
				"TTL":  300,
				"data": `1 . alpn="h2" ech="` + base64.StdEncoding.EncodeToString(configList) + `"`,
			}},
		}
		_ = json.NewEncoder(w).Encode(payload)
	}))
	defer doH.Close()

	originalResolver, originalSystem := echDoHResolver, echSystemLookup
	defer func() {
		echDoHResolver, echSystemLookup = originalResolver, originalSystem
	}()
	echDoHResolver = doH.URL
	echSystemLookup = func(context.Context, string) ([]byte, time.Duration) { return nil, 0 }
	echCacheMu.Lock()
	echCache = map[string]echCacheEntry{}
	echCacheMu.Unlock()

	cfg := Config(context.Background(), host, true, Auto, nil, zerolog.Nop(), nil)
	if len(cfg.EncryptedClientHelloConfigList) == 0 {
		t.Fatal("Config did not attach an ECH config list discovered over DNS-over-HTTPS")
	}
	if cfg.MinVersion != tls.VersionTLS13 {
		t.Fatalf("MinVersion = %x, want TLS 1.3", cfg.MinVersion)
	}

	conn, err := tls.Dial("tcp", server.Listener.Addr().String(), cfg)
	if err != nil {
		t.Fatalf("handshake with the discovered config failed: %v", err)
	}
	defer conn.Close()
	if !conn.ConnectionState().ECHAccepted {
		t.Fatal("ECH was not accepted with a discovered config")
	}
}

func TestConfigOffSkipsLookup(t *testing.T) {
	called := false
	originalSystem := echSystemLookup
	defer func() { echSystemLookup = originalSystem }()
	echSystemLookup = func(context.Context, string) ([]byte, time.Duration) {
		called = true
		return nil, 0
	}
	cfg := Config(context.Background(), "example.com", false, Off, nil, zerolog.Nop(), nil)
	if called {
		t.Fatal("Config looked up an ECH config with ECH off")
	}
	if cfg.EncryptedClientHelloConfigList != nil || cfg.MinVersion != 0 {
		t.Fatal("Config changed TLS settings with ECH off")
	}
}

// A server that does not support ECH rejects the offer; Go reports that as a
// *tls.ECHRejectionError so the caller can retry without ECH.
func TestECHRejectionIsReported(t *testing.T) {
	configList, _, err := echtest.ConfigAndKey("ech.test")
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer server.Close()

	cfg := &tls.Config{
		ServerName:                     "127.0.0.1",
		InsecureSkipVerify:             true,
		MinVersion:                     tls.VersionTLS13,
		EncryptedClientHelloConfigList: configList,
		EncryptedClientHelloRejectionVerify: func(tls.ConnectionState) error {
			return nil
		},
	}
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

type echoErr string

func (e echoErr) Error() string { return string(e) }

func TestSystemDNSLookupAgainstLocalResolver(t *testing.T) {
	// A local resolver serving an HTTPS record with an ECH config, reached
	// through /etc/resolv.conf semantics by direct dependency injection.
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
		record, err := dns.NewRR(r.Question[0].Name + ` 60 IN HTTPS 1 . alpn="h2" ech="` + base64.StdEncoding.EncodeToString(raw) + `"`)
		if err != nil {
			_ = w.WriteMsg(reply)
			return
		}
		reply.Answer = []dns.RR{record}
		_ = w.WriteMsg(reply)
	})}
	go func() { _ = server.ActivateAndServe() }()
	defer server.Shutdown()

	client := &dns.Client{Timeout: 3 * time.Second}
	message := new(dns.Msg)
	message.SetQuestion("relay.example.com.", dns.TypeHTTPS)
	response, _, err := client.Exchange(message, packetConn.LocalAddr().String())
	if err != nil {
		t.Fatal(err)
	}
	list, ttl := echFromRecords(response.Answer)
	if string(list) != string(raw) {
		t.Fatalf("lookup returned %d bytes, want %d", len(list), len(raw))
	}
	if ttl != 60*time.Second {
		t.Fatalf("ttl = %v, want 1m", ttl)
	}
}
