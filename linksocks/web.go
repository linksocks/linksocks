package linksocks

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog"
	"github.com/spf13/cobra"
)

// WebOption configures the `linksocks web` provider.
type WebOption struct {
	URL        string
	Token      string
	HostPrefix string
	Backends   []string
	// ECH controls Encrypted Client Hello for the relay connection: ECHAuto
	// (default) or ECHOff.
	ECH            string
	Insecure       bool
	NoReconnect    bool
	ReconnectDelay time.Duration
	Logger         zerolog.Logger
	// OnRegistered is called after a successful registration with the host the
	// relay assigned, and again whenever the hostname rotates. It must not block.
	OnRegistered func(host string)
}

type webState struct {
	mu              sync.Mutex
	requestedPrefix string
	currentHost     string
	expiresAt       time.Time
	rotateSeconds   int
}

type webConn struct {
	opt          WebOption
	ws           *websocket.Conn
	log          zerolog.Logger
	token        string
	state        *webState
	client       *http.Client
	writeMu      sync.Mutex
	pendingMu    sync.Mutex
	pending      map[uuid.UUID]*webPendingRequest
	webSocketsMu sync.Mutex
	webSockets   map[uuid.UUID]*webSocketChannel
	backendIndex atomic.Uint64
	sem          chan struct{}
	peerVersion  byte
	versionSeen  bool
}

type webPendingRequest struct {
	req    WebRequestMessage
	body   bytes.Buffer
	ctx    context.Context
	cancel context.CancelFunc
}

type webSocketChannel struct {
	mu     sync.Mutex
	conn   *websocket.Conn
	cancel context.CancelFunc
}

const (
	webBodyLimit         = 16 * 1024 * 1024
	webDataChunk         = 32 * 1024
	webConcurrentLimit   = 256
	webPingInterval      = 30 * time.Second
	webReadTimeout       = 90 * time.Second
	webRegisterTimeout   = 30 * time.Second
	webDefaultReconnects = 3 * time.Second
)

var errRetryWithoutDomainPrefix = errors.New("retry without domain prefix")

// validDomainPrefix reports whether value is a single DNS label that can be
// composed into "<prefix>.<WEB_ZONE>". Full domains are rejected: the relay
// owns the zone, and the CLI only lets a provider label its own site.
func validDomainPrefix(value string) bool {
	if value == "" || len(value) > 63 {
		return false
	}
	for i := 0; i < len(value); i++ {
		c := value[i]
		switch {
		case c >= 'a' && c <= 'z':
		case c >= '0' && c <= '9':
		case c == '-' && i > 0 && i < len(value)-1:
		default:
			return false
		}
	}
	return true
}

// RunWeb runs a web provider until the context is cancelled.
func RunWeb(ctx context.Context, opt WebOption) error {
	if len(opt.Backends) == 0 {
		return fmt.Errorf("at least one local backend is required")
	}
	if opt.ReconnectDelay <= 0 {
		opt.ReconnectDelay = webDefaultReconnects
	}
	if opt.Token == "" {
		opt.Token = "anonymous"
	}
	prefix := strings.ToLower(strings.TrimSpace(opt.HostPrefix))
	if prefix != "" && !validDomainPrefix(prefix) {
		return fmt.Errorf("invalid domain prefix %q: use a single DNS label such as \"blog\", not a full domain", opt.HostPrefix)
	}
	opt.HostPrefix = prefix

	state := &webState{requestedPrefix: opt.HostPrefix}
	token := opt.Token
	delay := opt.ReconnectDelay

	for {
		startedAt := time.Now()
		err := runWebOnce(ctx, opt, state, &token)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			opt.Logger.Warn().Err(err).Msg("Web provider connection ended")
		}
		var nonRetriable *nonRetriableError
		if opt.NoReconnect || errors.As(err, &nonRetriable) {
			return err
		}
		if time.Since(startedAt) > 30*time.Second {
			delay = opt.ReconnectDelay
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
		if delay < 30*time.Second {
			delay = time.Duration(float64(delay) * 1.5)
		} else {
			delay = 30 * time.Second
		}
	}
}

func runWebOnce(ctx context.Context, opt WebOption, state *webState, token *string) error {
	wsURL, err := buildWebSocketURL(opt.URL, *token)
	if err != nil {
		return err
	}

	ws, err := dialRelayWebSocket(ctx, wsURL, relayDialOptions{
		Insecure: opt.Insecure,
		ECHMode:  opt.ECH,
		Logger:   opt.Logger,
	})
	if err != nil {
		return fmt.Errorf("websocket handshake failed: %w", err)
	}
	defer ws.Close()

	c := &webConn{
		opt:        opt,
		ws:         ws,
		log:        opt.Logger,
		token:      *token,
		state:      state,
		client:     newWebHTTPClient(opt.Insecure),
		pending:    make(map[uuid.UUID]*webPendingRequest),
		webSockets: make(map[uuid.UUID]*webSocketChannel),
		sem:        make(chan struct{}, webConcurrentLimit),
	}
	defer c.closeWebSockets()

	ws.SetReadDeadline(time.Now().Add(webRegisterTimeout))
	actualToken, err := c.readAuthResponse()
	if err != nil {
		return err
	}
	if actualToken != "" {
		c.token = actualToken
		*token = actualToken
	}

	state.mu.Lock()
	prefix := state.requestedPrefix
	state.mu.Unlock()

	// The registration carries the domain prefix, not a hostname: the relay
	// composes it under its own WEB_ZONE.
	if err := c.send(WebRegisterMessage{
		Host:     prefix,
		Backends: c.backends(),
		Insecure: opt.Insecure,
		Label:    "linksocks web",
	}); err != nil {
		return err
	}
	if err := c.readRegistrationResponse(webRegisterTimeout); err != nil {
		return err
	}

	ws.SetReadDeadline(time.Now().Add(webReadTimeout))
	ws.SetPongHandler(func(string) error {
		_ = ws.SetReadDeadline(time.Now().Add(webReadTimeout))
		return nil
	})

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	go c.pingLoop(runCtx)

	for {
		_, data, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		_ = ws.SetReadDeadline(time.Now().Add(webReadTimeout))
		msg, _, err := c.parseWebMessage(data)
		if err != nil {
			var nonRetriable *nonRetriableError
			if errors.As(err, &nonRetriable) {
				return err
			}
			c.log.Warn().Err(err).Msg("Dropping malformed message")
			continue
		}
		if err := c.handleMessage(msg); err != nil {
			return err
		}
	}
}

func (c *webConn) readAuthResponse() (string, error) {
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			return "", err
		}
		msg, version, err := c.parseWebMessage(data)
		if err != nil {
			return "", err
		}
		switch m := msg.(type) {
		case AuthResponseMessage:
			if !m.Success {
				if m.Error != "" {
					return "", fmt.Errorf("authentication failed: %s", m.Error)
				}
				return "", fmt.Errorf("authentication failed")
			}
			if err := validateWebPeerVersion(version); err != nil {
				return "", err
			}
			return m.Token, nil
		case LogMessage:
			c.logServerMessage(m)
		}
	}
}

func (c *webConn) parseWebMessage(data []byte) (BaseMessage, byte, error) {
	msg, version, err := ParseMessageWithVersion(data)
	if err != nil {
		if errors.Is(err, errUnsupportedProtocolMajor) {
			return nil, version, &nonRetriableError{msg: err.Error()}
		}
		return nil, version, err
	}
	if (!c.versionSeen || c.peerVersion != version) && !protocolMinorIsCurrent(version) {
		knownMinor, _ := protocolKnownMinorForMajor(protocolMajor(version))
		baseline := protocolMajor(version)<<4 | knownMinor
		c.log.Warn().Str("peer_version", protocolVersionDebugString(version)).Str("local_version", protocolVersionDebugString(baseline)).Msg("Protocol minor version mismatch; features may differ")
	}
	c.peerVersion = version
	c.versionSeen = true
	switch message := msg.(type) {
	case WebRegisterResponseMessage, WebRequestMessage, WebDataMessage, WebCancelMessage, WebRotateMessage:
		if err := validateWebPeerVersion(version); err != nil {
			return nil, version, err
		}
		if request, ok := message.(WebRequestMessage); ok && request.WebSocket && !protocolSupportsFeature(version, MinVersionWebSocket) {
			return nil, version, &nonRetriableError{msg: "relay protocol version does not support WebSocket proxying"}
		}
	case WebSocketFrameMessage:
		if !protocolSupportsFeature(version, MinVersionWebSocket) {
			return nil, version, &nonRetriableError{msg: "relay protocol version does not support WebSocket frames"}
		}
	}
	return msg, version, nil
}

func validateWebPeerVersion(version byte) error {
	if !protocolSupportsFeature(version, MinVersionWeb) {
		return &nonRetriableError{msg: fmt.Sprintf("web relay protocol version %s does not meet the required minimum %s", protocolVersionDebugString(version), protocolVersionDebugString(MinVersionWeb))}
	}
	return nil
}

func (c *webConn) readRegistrationResponse(timeout time.Duration) error {
	if err := c.ws.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		return err
	}
	for {
		_, data, err := c.ws.ReadMessage()
		if err != nil {
			var networkError net.Error
			if errors.As(err, &networkError) && networkError.Timeout() {
				return fmt.Errorf("web registration response timed out; relay may not support Web: %w", err)
			}
			return fmt.Errorf("web registration response: %w", err)
		}
		msg, _, err := c.parseWebMessage(data)
		if err != nil {
			return err
		}
		if err := c.handleMessage(msg); err != nil {
			return err
		}
		if _, ok := msg.(WebRegisterResponseMessage); ok {
			return nil
		}
	}
}

func (c *webConn) handleMessage(msg BaseMessage) error {
	switch m := msg.(type) {
	case WebRegisterResponseMessage:
		if !m.Success {
			if strings.Contains(m.Error, "Custom hostnames") && c.opt.HostPrefix != "" {
				c.state.mu.Lock()
				c.state.requestedPrefix = ""
				c.state.mu.Unlock()
				return errRetryWithoutDomainPrefix
			}
			if strings.Contains(strings.ToLower(m.Error), "expired") {
				c.state.mu.Lock()
				c.state.requestedPrefix = ""
				c.state.mu.Unlock()
			}
			return fmt.Errorf("web registration failed: %s", m.Error)
		}

		expires := time.Time{}
		if m.ExpiresAt > 0 {
			expires = time.UnixMilli(m.ExpiresAt)
		}
		c.state.mu.Lock()
		c.state.currentHost = m.Host
		c.state.expiresAt = expires
		c.state.rotateSeconds = m.RotateSeconds
		c.state.mu.Unlock()

		event := c.log.Info().
			Str("url", m.URL).
			Str("host", m.Host).
			Bool("temporary", m.Temporary).
			Int("backends", m.BackendCount)
		if m.Owner != "" {
			event = event.Str("owner", m.Owner)
		}
		if !expires.IsZero() {
			event = event.Str("expires", expires.Format(time.RFC3339))
		}
		event.Msg("Web site published")
		if m.Temporary {
			c.log.Info().Msgf("Temporary URL %s expires at %s; it will rotate automatically", m.URL, expires.Format(time.RFC3339))
		}
		if c.opt.OnRegistered != nil {
			c.opt.OnRegistered(m.Host)
		}

	case WebRequestMessage:
		if m.WebSocket {
			c.startWebSocket(m)
		} else {
			c.handleWebRequest(m)
		}

	case WebDataMessage:
		c.handleWebRequestBody(m)

	case WebCancelMessage:
		c.cancelPending(m.ChannelID)
		c.closeWebSocket(m.ChannelID, m.Code, m.Reason, false)

	case WebSocketFrameMessage:
		if err := c.writeWebSocketFrame(m); err != nil {
			c.closeWebSocket(m.ChannelID, 1011, "origin write failed", true)
		}

	case WebRotateMessage:
		expires := time.Time{}
		if m.ExpiresAt > 0 {
			expires = time.UnixMilli(m.ExpiresAt)
		}
		c.state.mu.Lock()
		oldHost := c.state.currentHost
		c.state.currentHost = m.Host
		c.state.expiresAt = expires
		c.state.rotateSeconds = m.RotateSeconds
		c.state.mu.Unlock()
		event := c.log.Info().Str("url", m.URL).Str("host", m.Host)
		if oldHost != "" && oldHost != m.Host {
			event = event.Str("old_host", oldHost)
		}
		if !expires.IsZero() {
			event = event.Str("expires", expires.Format(time.RFC3339))
		}
		event.Msg("Web URL rotated")
		if !expires.IsZero() {
			c.log.Info().Msgf("Temporary URL %s expires at %s; it will rotate automatically", m.URL, expires.Format(time.RFC3339))
		}

	case LogMessage:
		c.logServerMessage(m)

	case PartnersMessage:
		c.log.Debug().Int("count", m.Count).Msg("Partners updated")

	case AuthResponseMessage:
		// Already handled during the handshake.
	}
	return nil
}

func (c *webConn) logServerMessage(msg LogMessage) {
	event := c.log.Info()
	switch strings.ToLower(msg.Level) {
	case "error":
		event = c.log.Error()
	case "warn", "warning":
		event = c.log.Warn()
	case "debug", "trace":
		event = c.log.Debug()
	}
	event.Msg(msg.Msg)
}

func (c *webConn) backends() []WebBackend {
	result := make([]WebBackend, 0, len(c.opt.Backends))
	for _, backend := range c.opt.Backends {
		result = append(result, WebBackend{URL: backend})
	}
	return result
}

func (c *webConn) send(msg BaseMessage) error {
	// Web publishing is plaintext by design: the relay terminates the HTTP
	// traffic, so it cannot be end-to-end encrypted. The web provider keeps
	// speaking the legacy generation regardless of the build's default.
	data, err := PackMessageWithVersion(msg, ProtocolVersionV1)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return c.ws.WriteMessage(websocket.BinaryMessage, data)
}

func (c *webConn) pingLoop(ctx context.Context) {
	ticker := time.NewTicker(webPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			c.writeMu.Lock()
			_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
			err := c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
			c.writeMu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

func (c *webConn) handleWebRequest(msg WebRequestMessage) {
	if !msg.HasBody {
		go c.serveWebRequest(context.Background(), msg, nil)
		return
	}
	c.pendingMu.Lock()
	if len(c.pending) >= webConcurrentLimit {
		c.pendingMu.Unlock()
		c.writeError(msg.ChannelID, http.StatusServiceUnavailable, "too many concurrent uploads")
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	c.pending[msg.ChannelID] = &webPendingRequest{req: msg, ctx: ctx, cancel: cancel}
	c.pendingMu.Unlock()
}

func (c *webConn) startWebSocket(request WebRequestMessage) {
	ctx, cancel := context.WithCancel(context.Background())
	channel := &webSocketChannel{cancel: cancel}
	c.webSocketsMu.Lock()
	if _, exists := c.webSockets[request.ChannelID]; exists {
		c.webSocketsMu.Unlock()
		cancel()
		c.writeError(request.ChannelID, http.StatusConflict, "duplicate WebSocket channel")
		return
	}
	c.webSockets[request.ChannelID] = channel
	c.webSocketsMu.Unlock()
	go c.serveWebSocket(ctx, request, channel)
}

func (c *webConn) serveWebSocket(ctx context.Context, request WebRequestMessage, channel *webSocketChannel) {
	defer c.closeWebSocket(request.ChannelID, 1001, "", false)
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	default:
		c.writeError(request.ChannelID, http.StatusServiceUnavailable, "too many concurrent requests")
		return
	}
	target, err := buildBackendURL(c.pickBackend(), request.Path)
	if err != nil {
		c.writeError(request.ChannelID, http.StatusBadGateway, "invalid backend URL")
		return
	}
	parsed, err := url.Parse(target)
	if err != nil {
		c.writeError(request.ChannelID, http.StatusBadGateway, "invalid backend URL")
		return
	}
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	var protocols []string
	headers := make(http.Header)
	for _, header := range request.Headers {
		name := strings.ToLower(header[0])
		switch name {
		case "sec-websocket-protocol":
			for _, protocol := range strings.Split(header[1], ",") {
				if value := strings.TrimSpace(protocol); value != "" {
					protocols = append(protocols, value)
				}
			}
		case "sec-websocket-key", "sec-websocket-version", "sec-websocket-extensions":
		default:
			if !skipWebRequestHeader(name) {
				headers.Add(header[0], header[1])
			}
		}
	}
	if request.Host != "" {
		headers.Set("X-Forwarded-Host", request.Host)
	}
	dialer := websocket.Dialer{
		HandshakeTimeout: 30 * time.Second,
		TLSClientConfig:  &tls.Config{InsecureSkipVerify: c.opt.Insecure},
		Subprotocols:     protocols,
	}
	conn, response, err := dialer.DialContext(ctx, parsed.String(), headers)
	if err != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		if ctx.Err() == nil {
			c.writeError(request.ChannelID, http.StatusBadGateway, "WebSocket backend request failed")
		}
		return
	}
	conn.SetReadLimit(MaxWebSocketMessageSize)
	channel.mu.Lock()
	if ctx.Err() != nil {
		channel.mu.Unlock()
		_ = conn.Close()
		return
	}
	channel.conn = conn
	channel.mu.Unlock()
	responseHeaders := make([][2]string, 0, 1)
	if protocol := conn.Subprotocol(); protocol != "" {
		responseHeaders = append(responseHeaders, [2]string{"Sec-WebSocket-Protocol", protocol})
	}
	if err := c.send(WebResponseMessage{ChannelID: request.ChannelID, Status: http.StatusSwitchingProtocols, Headers: responseHeaders}); err != nil {
		return
	}
	for {
		opcode, data, err := conn.ReadMessage()
		if err != nil {
			code, reason := 1011, "origin disconnected"
			var closeError *websocket.CloseError
			if errors.As(err, &closeError) {
				code, reason = closeError.Code, closeError.Text
			}
			c.closeWebSocket(request.ChannelID, code, reason, true)
			return
		}
		if opcode != websocket.TextMessage && opcode != websocket.BinaryMessage {
			continue
		}
		if err := c.send(WebSocketFrameMessage{ChannelID: request.ChannelID, Opcode: byte(opcode), Data: data}); err != nil {
			return
		}
	}
}

func (c *webConn) writeWebSocketFrame(frame WebSocketFrameMessage) error {
	c.webSocketsMu.Lock()
	channel := c.webSockets[frame.ChannelID]
	c.webSocketsMu.Unlock()
	if channel == nil {
		return fmt.Errorf("unknown WebSocket channel")
	}
	channel.mu.Lock()
	defer channel.mu.Unlock()
	if channel.conn == nil {
		return fmt.Errorf("WebSocket origin is not connected")
	}
	_ = channel.conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
	return channel.conn.WriteMessage(int(frame.Opcode), frame.Data)
}

func (c *webConn) closeWebSocket(channelID uuid.UUID, code int, reason string, notify bool) {
	c.webSocketsMu.Lock()
	channel := c.webSockets[channelID]
	delete(c.webSockets, channelID)
	c.webSocketsMu.Unlock()
	if channel == nil {
		return
	}
	channel.cancel()
	channel.mu.Lock()
	if channel.conn != nil {
		if code == 0 {
			code = 1000
		}
		_ = channel.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(code, reason), time.Now().Add(time.Second))
		_ = channel.conn.Close()
	}
	channel.mu.Unlock()
	if notify {
		_ = c.send(WebCancelMessage{ChannelID: channelID, Code: code, Reason: reason})
	}
}

func (c *webConn) closeWebSockets() {
	c.webSocketsMu.Lock()
	channels := make([]uuid.UUID, 0, len(c.webSockets))
	for channelID := range c.webSockets {
		channels = append(channels, channelID)
	}
	c.webSocketsMu.Unlock()
	for _, channelID := range channels {
		c.closeWebSocket(channelID, 1001, "relay disconnected", false)
	}
}

func (c *webConn) handleWebRequestBody(msg WebDataMessage) {
	c.pendingMu.Lock()
	pending := c.pending[msg.ChannelID]
	if pending == nil {
		c.pendingMu.Unlock()
		return
	}
	if pending.body.Len()+len(msg.Data) > webBodyLimit {
		delete(c.pending, msg.ChannelID)
		c.pendingMu.Unlock()
		pending.cancel()
		c.writeError(msg.ChannelID, http.StatusRequestEntityTooLarge, "request body too large")
		return
	}
	if len(msg.Data) > 0 {
		_, _ = pending.body.Write(msg.Data)
	}
	ended := msg.End
	if ended {
		delete(c.pending, msg.ChannelID)
	}
	c.pendingMu.Unlock()

	if ended {
		body := append([]byte(nil), pending.body.Bytes()...)
		go c.serveWebRequest(pending.ctx, pending.req, body)
	}
}

func (c *webConn) cancelPending(channelID uuid.UUID) {
	c.pendingMu.Lock()
	pending := c.pending[channelID]
	if pending != nil {
		delete(c.pending, channelID)
	}
	c.pendingMu.Unlock()
	if pending != nil {
		pending.cancel()
	}
}

func (c *webConn) serveWebRequest(ctx context.Context, req WebRequestMessage, body []byte) {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	default:
		c.writeError(req.ChannelID, http.StatusServiceUnavailable, "too many concurrent requests")
		return
	}

	backend := c.pickBackend()
	target, err := buildBackendURL(backend, req.Path)
	if err != nil {
		c.writeError(req.ChannelID, http.StatusBadGateway, err.Error())
		return
	}

	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	httpReq, err := http.NewRequestWithContext(ctx, req.Method, target, reader)
	if err != nil {
		c.writeError(req.ChannelID, http.StatusBadGateway, err.Error())
		return
	}
	for _, header := range req.Headers {
		if len(header) != 2 {
			continue
		}
		if skipWebRequestHeader(header[0]) {
			continue
		}
		httpReq.Header.Add(header[0], header[1])
	}
	if req.ClientIP != "" && httpReq.Header.Get("X-Forwarded-For") == "" {
		httpReq.Header.Set("X-Forwarded-For", req.ClientIP)
	}
	if req.Host != "" {
		httpReq.Header.Set("X-Forwarded-Host", req.Host)
	}

	resp, err := c.client.Do(httpReq)
	if err != nil {
		if ctx.Err() == nil {
			c.log.Warn().Err(err).Str("channel_id", req.ChannelID.String()).Str("url", target).Msg("Backend request failed")
			c.writeError(req.ChannelID, http.StatusBadGateway, "backend request failed")
		}
		return
	}
	defer resp.Body.Close()

	headers := make([][2]string, 0, len(resp.Header)+1)
	for name, values := range resp.Header {
		if skipWebResponseHeader(name) {
			continue
		}
		for _, value := range values {
			headers = append(headers, [2]string{name, value})
		}
	}
	if err := c.send(WebResponseMessage{
		ChannelID:  req.ChannelID,
		Status:     resp.StatusCode,
		Headers:    headers,
		BodyLength: resp.ContentLength,
	}); err != nil {
		return
	}

	if req.Method == http.MethodHead {
		_ = c.send(WebDataMessage{ChannelID: req.ChannelID, End: true})
		return
	}

	buffer := make([]byte, webDataChunk)
	for {
		n, readErr := resp.Body.Read(buffer)
		if n > 0 {
			chunk := append([]byte(nil), buffer[:n]...)
			if err := c.send(WebDataMessage{ChannelID: req.ChannelID, Data: chunk}); err != nil {
				return
			}
		}
		if readErr == io.EOF {
			_ = c.send(WebDataMessage{ChannelID: req.ChannelID, End: true})
			return
		}
		if readErr != nil {
			if ctx.Err() == nil {
				c.log.Debug().Err(readErr).Str("channel_id", req.ChannelID.String()).Msg("Backend response body ended with error")
			}
			_ = c.send(WebDataMessage{ChannelID: req.ChannelID, End: true})
			return
		}
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}

func (c *webConn) writeError(channelID uuid.UUID, status int, message string) {
	body := []byte(message)
	headers := [][2]string{
		{"Content-Type", "text/plain; charset=utf-8"},
		{"Content-Length", fmt.Sprintf("%d", len(body))},
	}
	_ = c.send(WebResponseMessage{
		ChannelID:  channelID,
		Status:     status,
		Headers:    headers,
		BodyLength: int64(len(body)),
	})
	_ = c.send(WebDataMessage{ChannelID: channelID, Data: body})
	_ = c.send(WebDataMessage{ChannelID: channelID, End: true})
}

func (c *webConn) pickBackend() string {
	index := c.backendIndex.Add(1) - 1
	return c.opt.Backends[index%uint64(len(c.opt.Backends))]
}

func newWebHTTPClient(insecure bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: insecure}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func buildWebSocketURL(serverURL, token string) (string, error) {
	value := strings.TrimSpace(serverURL)
	if value == "" {
		value = "ws://localhost:8765"
	}
	if !strings.Contains(value, "://") {
		value = "ws://" + value
	}
	u, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported server scheme %q", u.Scheme)
	}
	if u.Path == "" || u.Path == "/" {
		u.Path = "/socket"
	}
	hash := sha256.Sum256([]byte(token))
	q := u.Query()
	q.Set("token", hex.EncodeToString(hash[:]))
	q.Set("reverse", "true")
	q.Set("web", "1")
	q.Set("instance", uuid.NewString())
	u.RawQuery = q.Encode()
	return u.String(), nil
}

func buildBackendURL(baseURL, requestPath string) (string, error) {
	base, err := url.Parse(baseURL)
	if err != nil {
		return "", err
	}
	rel, err := url.Parse(requestPath)
	if err != nil {
		return "", err
	}
	target := *base
	target.Path = joinWebPath(base.Path, rel.Path)
	target.RawQuery = rel.RawQuery
	target.Fragment = ""
	return target.String(), nil
}

func joinWebPath(base, path string) string {
	if base == "" {
		if path == "" {
			return "/"
		}
		return path
	}
	if path == "" {
		return base
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

func skipWebRequestHeader(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "host", "content-length", "connection", "keep-alive", "proxy-authenticate",
		"proxy-authorization", "proxy-connection", "te", "trailer", "transfer-encoding",
		"upgrade", "expect":
		return true
	}
	return strings.HasPrefix(lower, "x-ls-")
}

func skipWebResponseHeader(name string) bool {
	lower := strings.ToLower(name)
	switch lower {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"proxy-connection", "te", "trailer", "transfer-encoding", "upgrade":
		return true
	}
	return false
}

// runWeb implements the `linksocks web` CLI command.
func (cli *CLI) runWeb(cmd *cobra.Command, args []string) error {
	token, _ := cmd.Flags().GetString("token")
	token = resolveStringFlagEnv(cmd, "token", "LINKSOCKS_TOKEN", token)
	serverURL, _ := cmd.Flags().GetString("url")
	serverURL = resolveStringFlagEnv(cmd, "url", "LINKSOCKS_URL", serverURL)
	hostPrefix, _ := cmd.Flags().GetString("domain-prefix")
	echRaw, _ := cmd.Flags().GetString("ech")
	echRaw = resolveStringFlagEnv(cmd, "ech", "LINKSOCKS_ECH", echRaw)
	echMode, err := ParseECHMode(echRaw)
	if err != nil {
		return err
	}
	locals, _ := cmd.Flags().GetStringArray("local")
	insecure, _ := cmd.Flags().GetBool("insecure")
	noReconnect, _ := cmd.Flags().GetBool("no-reconnect")
	debug, _ := cmd.Flags().GetCount("debug")

	if len(locals) == 0 {
		return fmt.Errorf("at least one --local backend is required (e.g. --local http://127.0.0.1:8080)")
	}
	backends := make([]string, 0, len(locals))
	for _, local := range locals {
		value := strings.TrimSpace(local)
		if value == "" {
			continue
		}
		if !strings.Contains(value, "://") {
			value = "http://" + value
		}
		parsed, err := url.Parse(value)
		if err != nil {
			return fmt.Errorf("invalid --local %q: %w", local, err)
		}
		if parsed.Scheme != "http" && parsed.Scheme != "https" {
			return fmt.Errorf("invalid --local %q: only http and https are supported", local)
		}
		backends = append(backends, strings.TrimRight(parsed.String(), "/"))
	}
	if len(backends) == 0 {
		return fmt.Errorf("at least one valid --local backend is required")
	}

	logger := cli.initLogging(debug)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info().
		Str("server", serverURL).
		Strs("backends", backends).
		Str("domain_prefix", hostPrefix).
		Bool("insecure", insecure).
		Msg("Starting LinkSocks web provider")

	return RunWeb(ctx, WebOption{
		URL:         serverURL,
		Token:       token,
		HostPrefix:  hostPrefix,
		ECH:         echMode,
		Backends:    backends,
		Insecure:    insecure,
		NoReconnect: noReconnect,
		Logger:      logger,
	})
}
