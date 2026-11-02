package linksocks

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
)

// dialWebSocketFollowingRedirects opens a WebSocket connection, following up to
// MaxRedirects HTTP redirects.
//
// Entry points that front several relay backends (for example the linksocks.js
// load-balancer worker) answer the opening handshake with a redirect to a
// healthy backend. A client that does not follow it can never establish a
// connection, so every WebSocket mode must use this helper.
func dialWebSocketFollowingRedirects(ctx context.Context, dialer *websocket.Dialer, rawURL string, log zerolog.Logger) (*websocket.Conn, error) {
	currentURL := rawURL

	for redirectsLeft := MaxRedirects; redirectsLeft >= 0; redirectsLeft-- {
		conn, resp, err := dialer.DialContext(ctx, currentURL, nil)
		if err == nil {
			return conn, nil
		}
		// gorilla replaces the body with an in-memory reader on a failed
		// handshake, so closing it here is always safe.
		if resp != nil && resp.Body != nil {
			_ = resp.Body.Close()
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

		log.Debug().
			Str("from", currentURL).
			Str("to", next.String()).
			Int("status", resp.StatusCode).
			Int("redirects_left", redirectsLeft).
			Msg("Following WebSocket redirect")
		currentURL = next.String()
	}

	return nil, fmt.Errorf("too many redirects (maximum %d)", MaxRedirects)
}
