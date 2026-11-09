// Package ech offers Encrypted Client Hello for relay connections.
//
// A client that offers ECH hides the relay hostname from the TLS ClientHello,
// which is what keeps the connection alive on networks that block specific
// hostnames by SNI. Because that network behaviour is exactly when the ECH
// config is hardest to fetch, discovery walks several sources: a manual
// override, an in-process cache, the system resolver, several DNS-over-HTTPS
// providers, the relay's own /api/ech endpoint, and finally a config
// discovered earlier for another relay host on the same Cloudflare edge.
// Nothing is written to disk: a pinned config is passed in every run.
package ech

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
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

// ParseConfigList accepts a serialized ECHConfigList as base64 (standard or
// URL alphabet, padding optional) or as "@path" pointing at a file that
// contains it. It is the value of --ech-config / LINKSOCKS_ECH_CONFIG.
func ParseConfigList(value string) ([]byte, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return nil, nil
	}
	if path, ok := strings.CutPrefix(trimmed, "@"); ok {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read ech config file: %w", err)
		}
		trimmed = strings.TrimSpace(string(data))
	}
	for _, encoding := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if decoded, err := encoding.DecodeString(trimmed); err == nil && len(decoded) > 0 {
			return decoded, nil
		}
	}
	return nil, errors.New("ech config must be a base64 ECHConfigList (or @file containing one)")
}

// dohProvider is one DNS-over-HTTPS endpoint that speaks the JSON API.
type dohProvider struct {
	name    string
	baseURL string
}

// dohProviders are tried in order; hosts reached from networks that block
// Cloudflare's resolver still work through the others. Each provider gets its
// own short timeout so a blocked endpoint costs seconds, not minutes.
var dohProviders = []dohProvider{
	{name: "alidns", baseURL: "https://dns.alidns.com/resolve"},
	// Addressed by IP on purpose: a network that resets hostnames by SNI
	// cannot read a name out of this connection, and the address stays
	// reachable where cloudflare-dns.com is blocked.
	{name: "cloudflare-ip", baseURL: "https://1.1.1.1/dns-query"},
	{name: "cloudflare", baseURL: "https://cloudflare-dns.com/dns-query"},
	{name: "google", baseURL: "https://dns.google/resolve"},
}

// echDoHProviders and echSystemLookup are swapped in tests.
var (
	echDoHProviders = dohProviders
	echSystemLookup = lookupECHViaSystemDNS
	// echRelayEndpointLookup is stubbed in tests.
	echRelayEndpointLookup = lookupViaRelayEndpoint
	// systemDNSServers is stubbed in tests.
	systemDNSServers = configuredDNSServers
	// fallbackDNSServers are public plaintext resolvers; AliDNS and DNSPod
	// answer from networks that block foreign resolvers, Cloudflare and
	// Google cover the other direction.
	fallbackDNSServers = []string{"223.5.5.5:53", "119.29.29.29:53", "1.1.1.1:53", "8.8.8.8:53"}
	dohTimeout         = 5 * time.Second
	maxCNAMEHops       = 8
	// systemDNSNames bounds how many names from a CNAME chain the system
	// resolver is asked about, because zones rotate the chain between queries.
	systemDNSNames = 6
	// systemDNSPasses bounds how many times the walk restarts from the
	// original name, catching a rotation that puts the record back in view.
	systemDNSPasses = 2
	// systemDNSBudget bounds one full plaintext-DNS walk, so a black-holed
	// resolver cannot delay the dial for long.
	systemDNSBudget = 6 * time.Second
	// discoveryBudget bounds every source together: a network that silently
	// drops DNS and DNS-over-HTTPS must not add up to a minute of waiting
	// before the dial proceeds without ECH.
	discoveryBudget = 8 * time.Second
	// dnsQueryTimeout bounds a single query.
	dnsQueryTimeout = 2 * time.Second
	// maxDNSServers caps how many resolvers one walk asks.
	maxDNSServers = 6
)

const (
	echCacheMinTTL  = time.Minute
	echCacheMaxTTL  = time.Hour
	echCacheMissTTL = 5 * time.Minute
	// echStaleGrace keeps a discovered config usable long after its record
	// TTL, because a network that blocks the relay hostname also tends to
	// block the lookups that would refresh it. A rotated key only costs one
	// failed handshake, which the dial layer retries without ECH.
	echStaleGrace = 30 * 24 * time.Hour
)

type cacheEntry struct {
	list    []byte
	expires time.Time
}

var (
	echCacheMu sync.Mutex
	echCache   = map[string]cacheEntry{}
	// echLastGood is the most recent config discovered for any host. The
	// deployed edge serves several relay hostnames from the same ECH key, so
	// a config found for the entry point unlocks the redirect target too.
	echLastGood   []byte
	echLastGoodMu sync.Mutex
)

func rememberLastGood(list []byte) {
	echLastGoodMu.Lock()
	echLastGood = list
	echLastGoodMu.Unlock()
}

func lastGood() []byte {
	echLastGoodMu.Lock()
	defer echLastGoodMu.Unlock()
	return echLastGood
}

// discoverySources names the source that produced a config, for logging.
type discovery struct {
	list   []byte
	source string
	ttl    time.Duration
}

// echConfigListFor returns the serialized ECHConfigList published for host, or
// nil when nothing usable is found.
func echConfigListFor(ctx context.Context, host string, req Request, proxy func(*http.Request) (*url.URL, error)) []byte {
	log := req.Logger
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

	discoveryCtx, cancel := context.WithTimeout(ctx, discoveryBudget)
	defer cancel()
	found := discoverECHConfigList(discoveryCtx, host, req, proxy, log)
	ttl := found.ttl
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
	echCache[host] = cacheEntry{list: found.list, expires: now.Add(ttl)}
	echCacheMu.Unlock()

	switch {
	case len(found.list) == 0:
		log.Trace().Str("host", host).Msg("No ECH config found for the relay host")
	case found.source == "":
		log.Debug().Str("host", host).Int("config_bytes", len(found.list)).Msg("Using Encrypted Client Hello for the relay connection")
	default:
		log.Debug().Str("host", host).Str("source", found.source).
			Int("config_bytes", len(found.list)).
			Str("ech_config", base64.StdEncoding.EncodeToString(found.list)).
			Msg("Using Encrypted Client Hello for the relay connection")
		rememberLastGood(found.list)
	}
	return found.list
}

// discoverECHConfigList walks the sources in order and returns the first
// non-empty config. A source that answers "no config" does not stop the walk:
// resolvers disagree about which name in a CNAME chain carries the HTTPS
// record, so the next source may still succeed.
func discoverECHConfigList(ctx context.Context, host string, req Request, proxy func(*http.Request) (*url.URL, error), log zerolog.Logger) discovery {
	var sawAnswer bool

	// 1. In-process memory: cheapest.
	if entry, ok := cachedEntry(host); ok {
		return discovery{list: entry, source: "cache"}
	}

	// 2. System resolver.
	if list, ttl, answered := echSystemLookup(ctx, host); len(list) > 0 {
		return discovery{list: list, source: "system-dns", ttl: ttl}
	} else if answered {
		sawAnswer = true
	}

	// 3. DNS-over-HTTPS providers.
	for _, provider := range echDoHProviders {
		list, ttl, answered := lookupECHViaDoH(ctx, provider, host, proxy, log)
		if len(list) > 0 {
			return discovery{list: list, source: "doh:" + provider.name, ttl: ttl}
		}
		if answered {
			sawAnswer = true
		}
	}

	// 4. The relay entry point itself: it is reachable (this is the hop that
	// already works), so it can hand over the config for its own name, which
	// the deployed edge serves with the same ECH key as the redirect target.
	if req.EntryHost != "" {
		if list, ttl := echRelayEndpointLookup(ctx, req.EntryHost, proxy, log); len(list) > 0 {
			return discovery{list: list, source: "relay", ttl: ttl}
		}
	}

	// 5. A config discovered for another relay host on the same edge. The
	// deployed edge serves every relay hostname with the same ECH key, and a
	// wrong key only costs one failed handshake.
	if list := lastGood(); len(list) > 0 {
		log.Debug().Str("host", host).Msg("Reusing an ECH config discovered for another relay host")
		return discovery{list: list, source: "reused"}
	}

	if sawAnswer {
		log.Trace().Str("host", host).Msg("Relay host publishes no ECH config")
	}
	return discovery{}
}

func cachedEntry(host string) ([]byte, bool) {
	echCacheMu.Lock()
	defer echCacheMu.Unlock()
	entry, ok := echCache[host]
	if !ok || time.Now().After(entry.expires) || len(entry.list) == 0 {
		return nil, false
	}
	return entry.list, true
}

// lookupECHViaSystemDNS queries the resolver from resolv.conf for the host's
// HTTPS record, following CNAME chains itself because many resolvers return
// only the chain and not the terminal HTTPS record. answered reports whether
// the resolver replied at all.
func lookupECHViaSystemDNS(ctx context.Context, host string) (list []byte, ttl time.Duration, answered bool) {
	servers := dnsServers()
	if len(servers) == 0 {
		return nil, 0, false
	}
	ctx, cancel := context.WithTimeout(ctx, systemDNSBudget)
	defer cancel()

	// Providers disagree about which name in a CNAME chain carries the HTTPS
	// record, and zones rotate their chains between queries. Ask about the
	// original name and about every name the chain mentions.
	answeredAny := false
	for pass := 0; pass < systemDNSPasses; pass++ {
		queue := []string{dns.Fqdn(host)}
		seen := map[string]bool{queue[0]: true}
		for len(queue) > 0 && len(seen) <= systemDNSNames {
			name := queue[0]
			queue = queue[1:]
			list, ttl, answered, targets := queryHTTPSName(ctx, servers, name)
			if answered {
				answeredAny = true
			}
			if len(list) > 0 {
				return list, ttl, true
			}
			for _, target := range targets {
				target = dns.Fqdn(target)
				if !seen[target] {
					seen[target] = true
					queue = append(queue, target)
				}
			}
		}
	}
	return nil, 0, answeredAny
}

// dnsServers lists the plaintext resolvers to ask: the system configuration
// when it can be read, then well-known public resolvers.
//
// The public resolvers exist because the system configuration is not always
// available (Windows has no resolv.conf) and because resolvers disagree about
// a rotating CNAME chain: one answers with the HTTPS record, another with only
// the chain. Queries are plaintext on purpose — the ECH config is public data,
// and a network that resets TLS connections by hostname still resolves names.
func dnsServers() []string {
	servers := systemDNSServers()
	servers = append(servers, fallbackDNSServers...)
	if len(servers) > maxDNSServers {
		servers = servers[:maxDNSServers]
	}
	return servers
}

// configuredDNSServers reads the resolver list from the system configuration.
func configuredDNSServers() []string {
	config, err := dns.ClientConfigFromFile("/etc/resolv.conf")
	if err != nil || len(config.Servers) == 0 {
		return nil
	}
	port := config.Port
	if port == "" {
		port = "53"
	}
	servers := make([]string, 0, len(config.Servers))
	for _, server := range config.Servers {
		servers = append(servers, net.JoinHostPort(server, port))
	}
	return servers
}

// queryHTTPSName asks each resolver in turn for one name, following the name's
// own CNAME chain. It returns the ECH config, the TTL, whether any resolver
// answered, and the CNAME targets seen along the way.
func queryHTTPSName(ctx context.Context, servers []string, host string) ([]byte, time.Duration, bool, []string) {
	client := &dns.Client{Timeout: dnsQueryTimeout}
	answered := false
	var targets []string
	name := host
	for hop := 0; hop < maxCNAMEHops; hop++ {
		message := new(dns.Msg)
		message.SetQuestion(name, dns.TypeHTTPS)
		var response *dns.Msg
		for _, server := range servers {
			if ctx.Err() != nil {
				return nil, 0, answered, targets
			}
			reply, _, err := client.ExchangeContext(ctx, message, server)
			if err == nil && reply != nil && reply.Rcode == dns.RcodeSuccess {
				response = reply
				break
			}
			// A truncated answer means the resolver wants TCP.
			if reply != nil && reply.Truncated {
				tcpClient := &dns.Client{Net: "tcp", Timeout: dnsQueryTimeout}
				if reply, _, err := tcpClient.ExchangeContext(ctx, message, server); err == nil && reply != nil {
					response = reply
					break
				}
			}
		}
		if response == nil {
			return nil, 0, answered, targets
		}
		answered = true
		targets = append(targets, cnameTargets(response.Answer)...)
		if list, ttl := echFromRecords(response.Answer); len(list) > 0 {
			return list, ttl, true, targets
		}
		next, ok := lastCNAMETarget(response.Answer)
		if !ok {
			return nil, 0, answered, targets
		}
		name = next
	}
	return nil, 0, answered, targets
}

// cnameTargets lists every CNAME target in an answer.
func cnameTargets(records []dns.RR) []string {
	var targets []string
	for _, record := range records {
		if cname, ok := record.(*dns.CNAME); ok {
			targets = append(targets, cname.Target)
		}
	}
	return targets
}

// lastCNAMETarget returns the terminal name of a CNAME chain in an answer.
func lastCNAMETarget(records []dns.RR) (string, bool) {
	target := ""
	for _, record := range records {
		if cname, ok := record.(*dns.CNAME); ok {
			target = cname.Target
		}
	}
	return target, target != ""
}

// lookupECHViaDoH asks one DNS-over-HTTPS provider and follows CNAME chains in
// its answer. answered reports whether the provider replied at all.
func lookupECHViaDoH(ctx context.Context, provider dohProvider, host string, proxy func(*http.Request) (*url.URL, error), log zerolog.Logger) (list []byte, ttl time.Duration, answered bool) {
	client := &http.Client{Timeout: dohTimeout, Transport: &http.Transport{Proxy: proxy}}
	defer client.CloseIdleConnections()

	name := host
	for hop := 0; hop < maxCNAMEHops; hop++ {
		payload, ok := dohQuery(ctx, client, provider, name, log)
		if !ok {
			return nil, 0, false
		}
		answered = true
		list, ttl, next := echFromDoHAnswer(payload)
		if len(list) > 0 {
			return list, ttl, true
		}
		if next == "" {
			return nil, 0, true
		}
		name = next
	}
	return nil, 0, answered
}

type dohAnswer struct {
	Status int `json:"Status"`
	Answer []struct {
		Name string `json:"name"`
		Type int    `json:"type"`
		TTL  int    `json:"TTL"`
		Data string `json:"data"`
	} `json:"Answer"`
}

func dohQuery(ctx context.Context, client *http.Client, provider dohProvider, name string, log zerolog.Logger) (dohAnswer, bool) {
	requestURL := provider.baseURL + "?name=" + url.QueryEscape(name) + "&type=HTTPS"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return dohAnswer{}, false
	}
	request.Header.Set("accept", "application/dns-json")
	response, err := client.Do(request)
	if err != nil {
		log.Trace().Err(err).Str("provider", provider.name).Str("name", name).Msg("ECH lookup over DNS-over-HTTPS failed")
		return dohAnswer{}, false
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		log.Trace().Int("status", response.StatusCode).Str("provider", provider.name).Msg("ECH lookup over DNS-over-HTTPS was rejected")
		return dohAnswer{}, false
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	if err != nil {
		return dohAnswer{}, false
	}
	var payload dohAnswer
	if err := json.Unmarshal(body, &payload); err != nil {
		// Some providers answer with a different envelope; a missing parse is
		// not an answer we can use.
		log.Trace().Err(err).Str("provider", provider.name).Msg("ECH lookup returned an unexpected payload")
		return dohAnswer{}, false
	}
	return payload, true
}

// echFromDoHAnswer extracts the ECH config, its TTL, and the next CNAME name.
func echFromDoHAnswer(payload dohAnswer) ([]byte, time.Duration, string) {
	next := ""
	for _, answer := range payload.Answer {
		switch answer.Type {
		case 65: // HTTPS
			if list := echFromPresentation(answer.Data); len(list) > 0 {
				return list, time.Duration(answer.TTL) * time.Second, ""
			}
			if answer.TTL > 0 {
				return nil, time.Duration(answer.TTL) * time.Second, ""
			}
		case 5: // CNAME
			next = strings.TrimSuffix(answer.Data, ".")
		}
	}
	return nil, 0, next
}

// echFromRecords extracts the ECH config list from HTTPS records in an answer.
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

// Request describes one relay connection for ECH purposes.
type Request struct {
	// Host is the hostname being dialed.
	Host string
	// EntryHost is the hostname of the relay URL the user configured. When it
	// differs from Host the connection is a redirect hop, and the entry point
	// can supply the config for its own name over the channel that already
	// works.
	EntryHost string
	Insecure  bool
	Mode      string
	Proxy     func(*http.Request) (*url.URL, error)
	// Pinned replaces every discovery source (--ech-config).
	Pinned []byte
	Logger zerolog.Logger
}

// Config builds the TLS configuration for a relay connection to req.Host.
// With ECH enabled and a config available, the ClientHello hides the relay
// hostname behind the ECH provider's public name. A server that rejects ECH
// ends the handshake with a *tls.ECHRejectionError; the dial layer decides
// whether to retry with the server's retry configs or without ECH.
func Config(ctx context.Context, req Request) *tls.Config {
	host := req.Host
	log := req.Logger
	config := &tls.Config{ServerName: host, InsecureSkipVerify: req.Insecure}
	if req.Mode == Off {
		return config
	}
	if net.ParseIP(host) != nil && len(req.Pinned) == 0 {
		// A connection to an IP literal carries no hostname, so there is
		// nothing for ECH to hide and no HTTPS record to look up. A pinned
		// config still applies, which is how tests drive the handshake against
		// a local server.
		return config
	}
	list := req.Pinned
	if list == nil {
		list = echConfigListFor(ctx, host, req, req.Proxy)
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
		if state.ECHAccepted {
			// The key just proved itself against this host, so keep it: a
			// network that blocks the relay hostname also blocks the lookups
			// that would fetch the config again.
			rememberAccepted(host, list)
		}
		return nil
	}
	return config
}

// rememberAccepted stores a config that a relay accepted, once per host and
// key per process, so pinned configs survive as cache entries too.
func rememberAccepted(host string, list []byte) {
	key := host + "|" + base64.StdEncoding.EncodeToString(list)
	acceptedMu.Lock()
	if acceptedSeen[key] {
		acceptedMu.Unlock()
		return
	}
	acceptedSeen[key] = true
	acceptedMu.Unlock()

	rememberLastGood(list)
}

var (
	acceptedMu   sync.Mutex
	acceptedSeen = map[string]bool{}
)

// HandshakeFailed reports whether a dial error came from ECH negotiation or a
// malformed ECH config, in which case the caller should retry without ECH.
func HandshakeFailed(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "ech") || strings.Contains(text, "encrypted client hello")
}

// lookupViaRelayEndpoint asks the relay's own /api/ech endpoint for the ECH
// config of its hostname. Relays that predate the endpoint answer 404 and the
// walk continues.
func lookupViaRelayEndpoint(ctx context.Context, entryHost string, proxy func(*http.Request) (*url.URL, error), log zerolog.Logger) ([]byte, time.Duration) {
	if entryHost == "" {
		return nil, 0
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://"+entryHost+"/api/ech", nil)
	if err != nil {
		return nil, 0
	}
	client := &http.Client{
		Timeout: 8 * time.Second,
		// No fixed ServerName: the entry point answers with a redirect to the
		// backend, and each hop must present its own SNI.
		Transport: &http.Transport{Proxy: proxy},
	}
	defer client.CloseIdleConnections()
	response, err := client.Do(request)
	if err != nil {
		log.Trace().Err(err).Str("host", entryHost).Msg("ECH lookup through the relay endpoint failed")
		return nil, 0
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, 0
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<14))
	if err != nil {
		return nil, 0
	}
	var payload struct {
		ECH string `json:"ech"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.ECH == "" {
		return nil, 0
	}
	list, err := ParseConfigList(payload.ECH)
	if err != nil || len(list) == 0 {
		return nil, 0
	}
	// The relay config is per zone; refresh it on the record's usual cadence.
	return list, 5 * time.Minute
}

// Offered reports whether the TLS configuration carries an ECH config list.
func Offered(config *tls.Config) bool {
	return config != nil && len(config.EncryptedClientHelloConfigList) > 0
}

// ResetSuspected reports whether an error looks like a middlebox terminating
// the connection, which is what an SNI-based block produces.
func ResetSuspected(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	for _, marker := range []string{"connection reset", "forcibly closed", "broken pipe", "unexpected eof", "eof"} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}
