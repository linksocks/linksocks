package ech

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/miekg/dns"
	"github.com/rs/zerolog"
)

// Modes accepted by --ech and LINKSOCKS_ECH.
const (
	// Auto looks up the ECH config list published in the relay host's HTTPS
	// DNS record and offers Encrypted Client Hello when one is found.
	Auto = "auto"
	// Off connects with a plain TLS ClientHello.
	Off = "off"
)

// echDoHResolver answers HTTPS record queries when the system resolver does
// not. It is a variable so tests can point it at a local server.
var echDoHResolver = "https://cloudflare-dns.com/dns-query"

// echSystemLookup and echDoHLookup are swapped in tests.
var (
	echSystemLookup = lookupECHViaSystemDNS
	echDoHLookup    = lookupECHViaDoH
)

// ParseMode validates an ECH mode value.
func ParseMode(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", Auto:
		return Auto, nil
	case Off, "none", "disable", "disabled":
		return Off, nil
	}
	return "", fmt.Errorf("invalid ech mode %q: use auto or off", value)
}

type echCacheEntry struct {
	list    []byte
	expires time.Time
}

var (
	echCacheMu sync.Mutex
	echCache   = map[string]echCacheEntry{}
)

const (
	echCacheMinTTL  = time.Minute
	echCacheMaxTTL  = time.Hour
	echCacheMissTTL = 5 * time.Minute
)

// echConfigListFor returns the serialized ECHConfigList published for host, or
// nil when the host publishes none or the lookup fails. Results are cached
// with the record's TTL.
func echConfigListFor(ctx context.Context, host string, proxy func(*http.Request) (*url.URL, error), log zerolog.Logger) []byte {
	if host == "" {
		return nil
	}
	now := time.Now()
	echCacheMu.Lock()
	if entry, ok := echCache[host]; ok && now.Before(entry.expires) {
		echCacheMu.Unlock()
		return entry.list
	}
	echCacheMu.Unlock()

	list, ttl := discoverECHConfigList(ctx, host, proxy, log)
	if ttl <= 0 {
		ttl = echCacheMissTTL
	}
	if ttl < echCacheMinTTL {
		ttl = echCacheMinTTL
	}
	if ttl > echCacheMaxTTL {
		ttl = echCacheMaxTTL
	}
	echCacheMu.Lock()
	echCache[host] = echCacheEntry{list: list, expires: now.Add(ttl)}
	echCacheMu.Unlock()

	if len(list) > 0 {
		log.Debug().Str("host", host).Int("config_bytes", len(list)).Msg("Using Encrypted Client Hello for the relay connection")
	} else {
		log.Trace().Str("host", host).Msg("No ECH config published for the relay host")
	}
	return list
}

// discoverECHConfigList tries the system resolver first, then a DNS-over-HTTPS
// resolver, because many resolvers do not answer HTTPS record queries.
func discoverECHConfigList(ctx context.Context, host string, proxy func(*http.Request) (*url.URL, error), log zerolog.Logger) ([]byte, time.Duration) {
	if list, ttl := echSystemLookup(ctx, host); len(list) > 0 {
		return list, ttl
	} else if ttl > 0 {
		// The system resolver answered, it just has no ECH config.
		return nil, ttl
	}
	list, ttl := echDoHLookup(ctx, host, proxy, log)
	return list, ttl
}

// lookupECHViaSystemDNS queries the resolver from resolv.conf for the host's
// HTTPS record and returns its ECH config list.
func lookupECHViaSystemDNS(ctx context.Context, host string) ([]byte, time.Duration) {
	config, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil || len(config.Servers) == 0 {
		return nil, 0
	}
	client := &dns.Client{Timeout: 5 * time.Second}
	message := new(dns.Msg)
	message.SetQuestion(dns.Fqdn(host), dns.TypeHTTPS)
	for _, server := range config.Servers {
		port := config.Port
		if port == "" {
			port = "53"
		}
		response, _, err := client.ExchangeContext(ctx, message, net.JoinHostPort(server, port))
		if err != nil || response == nil || response.Rcode != dns.RcodeSuccess {
			continue
		}
		if list, ttl := echFromRecords(response.Answer); len(list) > 0 {
			return list, ttl
		}
		return nil, echRecordTTL(response.Answer)
	}
	return nil, 0
}

// echFromRecords extracts the ECH config list from HTTPS records in an answer.
// The record may sit behind a CNAME chain, so every answer entry is inspected.
func echFromRecords(records []dns.RR) ([]byte, time.Duration) {
	for _, record := range records {
		https, ok := record.(*dns.HTTPS)
		if !ok {
			continue
		}
		for _, value := range https.Value {
			if ech, ok := value.(*dns.SVCBECHConfig); ok && len(ech.ECH) > 0 {
				return ech.ECH, time.Duration(https.Hdr.Ttl) * time.Second
			}
		}
	}
	return nil, 0
}

func echRecordTTL(records []dns.RR) time.Duration {
	for _, record := range records {
		if https, ok := record.(*dns.HTTPS); ok {
			return time.Duration(https.Hdr.Ttl) * time.Second
		}
	}
	return 0
}

// lookupECHViaDoH asks a DNS-over-HTTPS resolver over the same proxy the relay
// connection uses. It is the fallback when the local resolver does not answer
// HTTPS queries.
func lookupECHViaDoH(ctx context.Context, host string, proxy func(*http.Request) (*url.URL, error), log zerolog.Logger) ([]byte, time.Duration) {
	endpoint := echDoHResolver
	if endpoint == "" {
		return nil, 0
	}
	requestURL := endpoint + "?name=" + url.QueryEscape(host) + "&type=HTTPS"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, 0
	}
	request.Header.Set("accept", "application/dns-json")
	client := &http.Client{
		Timeout:   8 * time.Second,
		Transport: &http.Transport{Proxy: proxy},
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		log.Trace().Err(err).Str("host", host).Msg("ECH lookup over DNS-over-HTTPS failed")
		return nil, 0
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, 0
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if err != nil {
		return nil, 0
	}
	var payload struct {
		Answer []struct {
			Type int    `json:"type"`
			TTL  int    `json:"TTL"`
			Data string `json:"data"`
		} `json:"Answer"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, 0
	}
	for _, answer := range payload.Answer {
		if answer.Type != 65 { // HTTPS
			continue
		}
		list := echFromPresentation(answer.Data)
		if len(list) > 0 {
			return list, time.Duration(answer.TTL) * time.Second
		}
		if answer.TTL > 0 {
			return nil, time.Duration(answer.TTL) * time.Second
		}
	}
	return nil, 0
}

// echFromPresentation extracts the ech parameter from an HTTPS record in
// presentation format, e.g. `1 . alpn=h2 ech="AEX..."`.
func echFromPresentation(text string) []byte {
	for _, field := range strings.Fields(text) {
		value, ok := strings.CutPrefix(field, "ech=")
		if !ok {
			continue
		}
		value = strings.Trim(value, `"`)
		if decoded, err := base64.StdEncoding.DecodeString(value); err == nil {
			return decoded
		}
		if decoded, err := base64.RawURLEncoding.DecodeString(value); err == nil {
			return decoded
		}
	}
	return nil
}

// Config builds the TLS configuration for a relay connection to host.
// With ECH enabled and a published config, the ClientHello hides the relay
// hostname behind the ECH provider's public name. A server that rejects ECH
// ends the handshake with a *tls.ECHRejectionError; the dial layer decides
// whether to retry with the server's retry configs or without ECH.
//
// override, when non-nil, replaces the discovered config list.
func Config(ctx context.Context, host string, insecure bool, mode string, proxy func(*http.Request) (*url.URL, error), log zerolog.Logger, override []byte) *tls.Config {
	config := &tls.Config{ServerName: host, InsecureSkipVerify: insecure}
	if mode == Off {
		return config
	}
	list := override
	if list == nil {
		list = echConfigListFor(ctx, host, proxy, log)
	}
	if len(list) == 0 {
		return config
	}
	config.MinVersion = tls.VersionTLS13
	config.EncryptedClientHelloConfigList = list
	config.EncryptedClientHelloRejectionVerify = func(tls.ConnectionState) error {
		log.Warn().Str("host", host).Msg("Relay rejected Encrypted Client Hello; continuing with a plain ClientHello")
		return nil
	}
	config.VerifyConnection = func(state tls.ConnectionState) error {
		log.Debug().Str("host", host).Bool("ech_accepted", state.ECHAccepted).Msg("Relay TLS handshake finished")
		return nil
	}
	return config
}

// HandshakeFailed reports whether a dial error came from ECH negotiation or
// a malformed ECH config, in which case the caller should retry without ECH.
func HandshakeFailed(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "ech") || strings.Contains(text, "encrypted client hello")
}
