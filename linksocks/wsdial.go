package linksocks

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"

	"github.com/gorilla/websocket"
	"github.com/linksocks/linksocks/internal/ech"
	"github.com/rs/zerolog"
)

// relayDialOptions configures how a client connects to a relay or coordinator.
type relayDialOptions struct {
	// Insecure skips TLS certificate verification.
	Insecure bool
	// ECHMode is ECHAuto (default) or ECHOff.
	ECHMode string
	// echOverride replaces the discovered ECH config list (used to retry with
	// the configs a server offered, and by tests).
	echOverride []byte
	// NoEnvProxy ignores proxy settings from the environment.
	NoEnvProxy bool
	Logger     zerolog.Logger
}

// newRelayDialer builds a WebSocket dialer for one URL. The TLS configuration
// is rebuilt per URL, so each redirect hop gets the ECH config published for
// its own host.
func newRelayDialer(ctx context.Context, rawURL string, opt relayDialOptions) (*websocket.Dialer, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, err
	}

	dialer := &websocket.Dialer{
		HandshakeTimeout: websocket.DefaultDialer.HandshakeTimeout,
		ReadBufferSize:   websocket.DefaultDialer.ReadBufferSize,
		WriteBufferSize:  websocket.DefaultDialer.WriteBufferSize,
	}
	var proxy func(*http.Request) (*url.URL, error)
	if !opt.NoEnvProxy {
		proxy = http.ProxyFromEnvironment
		dialer.Proxy = proxy
	}

	switch parsed.Scheme {
	case "wss", "https":
		echMode := opt.ECHMode
		if echMode == "" {
			echMode = ECHAuto
		}
		dialer.TLSClientConfig = ech.Config(ctx, parsed.Hostname(), opt.Insecure, echMode, proxy, opt.Logger, opt.echOverride)
	case "ws", "http":
		if opt.Insecure {
			dialer.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
		}
	}
	return dialer, nil
}

// dialRelayWebSocket opens a WebSocket connection, following up to MaxRedirects
// HTTP redirects.
//
// Entry points that front several relay backends (for example the linksocks.js
// load-balancer worker) answer the opening handshake with a redirect to a
// healthy backend. A client that does not follow it can never establish a
// connection, so every WebSocket mode must use this helper.
//
// ECH is offered when the target host publishes an ECH config; a handshake
// that fails because of ECH is retried once with a plain ClientHello so a
// stale or malformed config can never take the connection down.
func dialRelayWebSocket(ctx context.Context, rawURL string, opt relayDialOptions) (*websocket.Conn, error) {
	currentURL := rawURL
	echRetried := false

	for redirectsLeft := MaxRedirects; redirectsLeft >= 0; redirectsLeft-- {
		dialer, err := newRelayDialer(ctx, currentURL, opt)
		if err != nil {
			return nil, err
		}
		conn, resp, err := dialer.DialContext(ctx, currentURL, nil)
		if err == nil {
			return conn, nil
		}
		// gorilla replaces the body with an in-memory reader on a failed
		// handshake, so closing it here is always safe.
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
		}
		if !echRetried && opt.ECHMode != ECHOff {
			var rejection *tls.ECHRejectionError
			switch {
			case errors.As(err, &rejection) && len(rejection.RetryConfigList) > 0:
				// The server rejected ECH but handed out configs to use next.
				echRetried = true
				opt.Logger.Info().Str("host", currentURL).Msg("Relay rejected ECH and offered new configs; retrying with them")
				retry := opt
				retry.echOverride = rejection.RetryConfigList
				return dialRelayWebSocket(ctx, currentURL, retry)
			case errors.As(err, &rejection), ech.HandshakeFailed(err):
				// A rejected or unusable ECH offer must not break the relay
				// connection, so the retry uses a plain ClientHello.
				echRetried = true
				opt.Logger.Warn().Err(err).Msg("Encrypted Client Hello was rejected; retrying without ECH")
				retry := opt
				retry.ECHMode = ECHOff
				return dialRelayWebSocket(ctx, currentURL, retry)
			}
		}
		if !errors.Is(err, websocket.ErrBadHandshake) || resp == nil ||
			resp.StatusCode < 301 || resp.StatusCode > 308 {
			return nil, err
		}

		location := resp.Header.Get("Location")
		if location == "" {
			return nil, errors.New("redirect response missing Location header")
		}
		if redirectsLeft == 0 {
			return nil, fmt.Errorf("too many redirects (maximum %d)", MaxRedirects)
		}

		next, parseErr := url.Parse(location)
		if parseErr != nil {
			return nil, parseErr
		}
		if !next.IsAbs() {
			base, baseErr := url.Parse(currentURL)
			if baseErr != nil {
				return nil, baseErr
			}
			next = base.ResolveReference(next)
		}
		switch next.Scheme {
		case "http":
			next.Scheme = "ws"
		case "https":
			next.Scheme = "wss"
		}

		opt.Logger.Debug().
			Str("from", currentURL).
			Str("to", next.String()).
			Int("status", resp.StatusCode).
			Int("redirects_left", redirectsLeft).
			Msg("Following WebSocket redirect")
		currentURL = next.String()
	}
	return nil, fmt.Errorf("too many redirects (maximum %d)", MaxRedirects)
}
