package linksocks

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

// Local proxy protocol kinds detected on the shared listen port.
type localProxyProtocol int

const (
	localProxyProtocolUnknown localProxyProtocol = iota
	localProxyProtocolSOCKS5
	localProxyProtocolHTTP
)

// hopByHopHeaders lists hop-by-hop headers that must not be forwarded when
// relaying absolute-form HTTP requests through the local HTTP proxy.
var hopByHopHeaders = map[string]bool{
	"connection":          true,
	"proxy-connection":    true,
	"keep-alive":          true,
	"proxy-authenticate":  true,
	"proxy-authorization": true,
	"te":                  true,
	"trailers":            true,
	"transfer-encoding":   true,
	"upgrade":             true,
}

// bufferedConn wraps a net.Conn with a bufio.Reader so protocol detection can
// peek without consuming bytes that subsequent handlers still need.
type bufferedConn struct {
	net.Conn
	reader *bufio.Reader
}

func newBufferedConn(conn net.Conn) *bufferedConn {
	return &bufferedConn{
		Conn:   conn,
		reader: bufio.NewReader(conn),
	}
}

func (c *bufferedConn) Read(payload []byte) (int, error) {
	return c.reader.Read(payload)
}

func (c *bufferedConn) Reader() *bufio.Reader {
	return c.reader
}

// detectLocalProxyProtocol peeks the first byte to distinguish SOCKS5 from HTTP.
// SOCKS5 always starts with 0x05; HTTP methods start with printable ASCII.
func detectLocalProxyProtocol(reader *bufio.Reader) (localProxyProtocol, error) {
	firstByte, err := reader.Peek(1)
	if err != nil {
		return localProxyProtocolUnknown, err
	}

	switch firstByte[0] {
	case 0x05:
		return localProxyProtocolSOCKS5, nil
	case 'C', 'G', 'P', 'H', 'D', 'O', 'T':
		// CONNECT, GET, POST/PUT/PATCH, HEAD, DELETE, OPTIONS, TRACE
		return localProxyProtocolHTTP, nil
	default:
		if firstByte[0] >= 0x41 && firstByte[0] <= 0x5A {
			// Other uppercase HTTP method letters.
			return localProxyProtocolHTTP, nil
		}
		return localProxyProtocolUnknown, fmt.Errorf("unrecognized local proxy protocol (first byte 0x%02x)", firstByte[0])
	}
}

// HandleLocalProxyRequest accepts either SOCKS5 or HTTP proxy clients on the
// same TCP port. Protocol selection is based on the first byte of the stream.
func (r *Relay) HandleLocalProxyRequest(ctx context.Context, ws MessageWriter, conn net.Conn, username string, password string) error {
	buffered := newBufferedConn(conn)
	protocol, err := detectLocalProxyProtocol(buffered.Reader())
	if err != nil {
		_ = conn.Close()
		return err
	}

	switch protocol {
	case localProxyProtocolSOCKS5:
		return r.HandleSocksRequest(ctx, ws, buffered, username, password)
	case localProxyProtocolHTTP:
		return r.HandleHTTPProxyRequest(ctx, ws, buffered, username, password)
	default:
		_ = conn.Close()
		return fmt.Errorf("unsupported local proxy protocol")
	}
}

// RefuseLocalProxyRequest rejects a client on the hybrid local proxy port when
// no backend tunnel is available. The refusal format matches the detected protocol.
func (r *Relay) RefuseLocalProxyRequest(conn net.Conn, socksReason byte) error {
	buffered := newBufferedConn(conn)
	protocol, err := detectLocalProxyProtocol(buffered.Reader())
	if err != nil {
		_ = conn.Close()
		return err
	}

	switch protocol {
	case localProxyProtocolSOCKS5:
		return r.refuseSocksOnBuffered(buffered, socksReason)
	case localProxyProtocolHTTP:
		return r.refuseHTTPProxy(buffered, http.StatusServiceUnavailable, "No backend available")
	default:
		_ = conn.Close()
		return fmt.Errorf("unsupported local proxy protocol")
	}
}

// RefuseSocksRequest refuses a SOCKS5 (or hybrid) client request.
// Prefer RefuseLocalProxyRequest for hybrid ports; this keeps the historical API.
func (r *Relay) RefuseSocksRequest(conn net.Conn, reason byte) error {
	return r.RefuseLocalProxyRequest(conn, reason)
}

func (r *Relay) refuseSocksOnBuffered(conn *bufferedConn, reason byte) error {
	buffer := make([]byte, 1024)

	n, err := conn.Read(buffer)
	if err != nil {
		return fmt.Errorf("read error: %w", err)
	}
	if n == 0 || buffer[0] != 0x05 {
		return fmt.Errorf("invalid socks version")
	}

	// Consume remaining method bytes if the first read only got the header.
	if n >= 2 {
		methodsDeclared := int(buffer[1])
		alreadyRead := n - 2
		if alreadyRead < methodsDeclared {
			remaining := methodsDeclared - alreadyRead
			if _, err := io.ReadFull(conn, buffer[:remaining]); err != nil {
				return fmt.Errorf("read auth methods error: %w", err)
			}
		}
	}

	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return fmt.Errorf("write auth response error: %w", err)
	}

	n, err = conn.Read(buffer)
	if err != nil {
		if err == io.EOF {
			r.log.Debug().Msg("Client closed SOCKS connection")
			return nil
		}
		return fmt.Errorf("read request error: %w", err)
	}
	if n < 7 {
		return fmt.Errorf("request too short")
	}

	response := []byte{
		0x05,
		reason,
		0x00,
		0x01,
		0x00, 0x00, 0x00, 0x00,
		0x00, 0x00,
	}
	if _, err := conn.Write(response); err != nil {
		return fmt.Errorf("write refusal response error: %w", err)
	}

	_ = conn.Close()
	return nil
}

func (r *Relay) refuseHTTPProxy(conn net.Conn, statusCode int, message string) error {
	// Drain the request headers so polite clients finish sending before close.
	reader := bufio.NewReader(conn)
	if buffered, ok := conn.(*bufferedConn); ok {
		reader = buffered.Reader()
	}
	_, _ = http.ReadRequest(reader)

	response := &http.Response{
		StatusCode: statusCode,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(message)),
		Close:      true,
	}
	response.Header.Set("Content-Type", "text/plain; charset=utf-8")
	response.Header.Set("Connection", "close")
	response.ContentLength = int64(len(message))

	if err := response.Write(conn); err != nil {
		_ = conn.Close()
		return fmt.Errorf("write HTTP refusal response error: %w", err)
	}
	_ = conn.Close()
	return nil
}

// HandleHTTPProxyRequest handles inbound HTTP proxy traffic (CONNECT and
// absolute-form HTTP requests) on the hybrid local proxy port.
func (r *Relay) HandleHTTPProxyRequest(ctx context.Context, ws MessageWriter, conn *bufferedConn, username string, password string) error {
	request, err := http.ReadRequest(conn.Reader())
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("read HTTP proxy request error: %w", err)
	}

	if username != "" && password != "" {
		if err := r.authenticateHTTPProxy(request, username, password); err != nil {
			_ = r.writeHTTPProxyAuthRequired(conn)
			return err
		}
	}

	if request.Method == http.MethodConnect {
		return r.handleHTTPConnect(ctx, ws, conn, request)
	}
	return r.handleHTTPAbsoluteRequest(ctx, ws, conn, request)
}

func (r *Relay) authenticateHTTPProxy(request *http.Request, expectedUsername string, expectedPassword string) error {
	authorizationHeader := request.Header.Get("Proxy-Authorization")
	if authorizationHeader == "" {
		return fmt.Errorf("HTTP proxy authentication required")
	}

	const basicPrefix = "Basic "
	if !strings.HasPrefix(authorizationHeader, basicPrefix) {
		return fmt.Errorf("unsupported HTTP proxy authentication scheme")
	}

	decodedCredentials, err := base64.StdEncoding.DecodeString(strings.TrimSpace(authorizationHeader[len(basicPrefix):]))
	if err != nil {
		return fmt.Errorf("invalid HTTP proxy authentication encoding")
	}

	credentialParts := strings.SplitN(string(decodedCredentials), ":", 2)
	if len(credentialParts) != 2 {
		return fmt.Errorf("invalid HTTP proxy authentication credentials")
	}

	if credentialParts[0] != expectedUsername || credentialParts[1] != expectedPassword {
		return fmt.Errorf("HTTP proxy authentication failed")
	}
	return nil
}

func (r *Relay) writeHTTPProxyAuthRequired(conn net.Conn) error {
	const body = "Proxy authentication required"
	response := &http.Response{
		StatusCode: http.StatusProxyAuthRequired,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Close:      true,
	}
	response.Header.Set("Proxy-Authenticate", `Basic realm="LinkSocks"`)
	response.Header.Set("Content-Type", "text/plain; charset=utf-8")
	response.Header.Set("Connection", "close")
	response.ContentLength = int64(len(body))

	if err := response.Write(conn); err != nil {
		_ = conn.Close()
		return err
	}
	_ = conn.Close()
	return nil
}

func (r *Relay) writeHTTPProxyError(conn net.Conn, statusCode int, message string) error {
	response := &http.Response{
		StatusCode: statusCode,
		ProtoMajor: 1,
		ProtoMinor: 1,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(message)),
		Close:      true,
	}
	response.Header.Set("Content-Type", "text/plain; charset=utf-8")
	response.Header.Set("Connection", "close")
	response.ContentLength = int64(len(message))

	if err := response.Write(conn); err != nil {
		_ = conn.Close()
		return err
	}
	_ = conn.Close()
	return nil
}

// parseHTTPProxyTarget extracts host and port from a CONNECT host or absolute URL.
func parseHTTPProxyTarget(rawHost string, defaultPort int) (string, int, error) {
	host := strings.TrimSpace(rawHost)
	if host == "" {
		return "", 0, fmt.Errorf("empty proxy target host")
	}

	// url.ParseRequestURI-style hosts may include a scheme when taken from Request.URL.
	if strings.Contains(host, "://") {
		parsedURL, err := url.Parse(host)
		if err != nil {
			return "", 0, fmt.Errorf("invalid proxy target URL: %w", err)
		}
		host = parsedURL.Host
		if defaultPort == 0 {
			switch strings.ToLower(parsedURL.Scheme) {
			case "https":
				defaultPort = 443
			case "http":
				defaultPort = 80
			}
		}
	}

	if strings.HasPrefix(host, "[") {
		// Possible bare IPv6 literal without port, e.g. "[::1]".
		if !strings.Contains(host, "]:") {
			host = strings.TrimPrefix(host, "[")
			host = strings.TrimSuffix(host, "]")
			if defaultPort <= 0 {
				return "", 0, fmt.Errorf("missing port for target %q", host)
			}
			return host, defaultPort, nil
		}
	}

	if hostname, portText, err := net.SplitHostPort(host); err == nil {
		port, convErr := strconv.Atoi(portText)
		if convErr != nil || port <= 0 || port > 65535 {
			return "", 0, fmt.Errorf("invalid port in target %q", host)
		}
		return hostname, port, nil
	}

	// Host without port.
	if defaultPort <= 0 {
		return "", 0, fmt.Errorf("missing port for target %q", host)
	}
	// Strip brackets from IPv6 literals without ports.
	host = strings.TrimPrefix(host, "[")
	host = strings.TrimSuffix(host, "]")
	return host, defaultPort, nil
}

func (r *Relay) handleHTTPConnect(ctx context.Context, ws MessageWriter, conn *bufferedConn, request *http.Request) error {
	targetHost, targetPort, err := parseHTTPProxyTarget(request.Host, 443)
	if err != nil {
		_ = r.writeHTTPProxyError(conn, http.StatusBadRequest, "Invalid CONNECT target")
		return err
	}

	if err := r.checkEntryAccess(ctx, targetHost, targetPort); err != nil {
		r.log.Warn().Str("address", targetHost).Int("port", targetPort).Msg("HTTP CONNECT blocked by access control")
		_ = r.writeHTTPProxyError(conn, http.StatusForbidden, "Destination blocked by access control")
		return err
	}

	channelID := uuid.New()
	r.log.Trace().
		Str("channel_id", channelID.String()).
		Str("address", targetHost).
		Int("port", targetPort).
		Msg("Starting HTTP CONNECT handling")

	channelQueue := make(chan BaseMessage, 1000)
	r.messageQueues.Store(channelID, channelQueue)
	r.flushOrphanData(channelID, channelQueue)
	defer r.disconnectChannel(channelID)

	connectRequest := ConnectMessage{
		Protocol:  "tcp",
		Address:   targetHost,
		Port:      targetPort,
		ChannelID: channelID,
	}
	channel := r.registerLogicalChannel(channelID, "tcp", ws, ws, channelPathRelay)
	channel.setRequest(connectRequest)
	channel.markResumeOwner()
	ws = channel
	r.log.Debug().Str("address", targetHost).Int("port", targetPort).Msg("Requesting TCP connection via HTTP CONNECT")
	r.logMessage(connectRequest, "send", ws.Label())
	if err := ws.WriteMessage(connectRequest); err != nil {
		_ = r.writeHTTPProxyError(conn, http.StatusBadGateway, "Failed to reach backend")
		return fmt.Errorf("write connect request error: %w", err)
	}

	if !r.option.FastOpen {
		if err := r.waitForConnectSuccess(ctx, channelQueue, channelID, targetHost, targetPort); err != nil {
			_ = r.writeHTTPProxyError(conn, http.StatusBadGateway, "Connection failed")
			return err
		}
	} else {
		r.armFastOpenTimeout(ctx, channelID, targetHost, targetPort)
	}

	// RFC 7231: any 2xx means the tunnel is established. 200 is universal.
	if _, err := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return fmt.Errorf("write CONNECT success response error: %w", err)
	}

	return r.HandleSocksTCPForward(ctx, ws, conn, channelID)
}

func (r *Relay) handleHTTPAbsoluteRequest(ctx context.Context, ws MessageWriter, conn *bufferedConn, request *http.Request) error {
	if request.URL == nil || request.URL.Host == "" {
		// Origin-form requests are not valid for an HTTP proxy without a Host rewrite path.
		_ = r.writeHTTPProxyError(conn, http.StatusBadRequest, "Absolute-form request URL required")
		return fmt.Errorf("HTTP proxy request missing absolute URL")
	}

	if strings.EqualFold(request.URL.Scheme, "https") {
		_ = r.writeHTTPProxyError(conn, http.StatusBadRequest, "Use CONNECT for HTTPS targets")
		return fmt.Errorf("absolute-form HTTPS is not supported; use CONNECT")
	}

	defaultPort := 80
	targetHost, targetPort, err := parseHTTPProxyTarget(request.URL.Host, defaultPort)
	if err != nil {
		_ = r.writeHTTPProxyError(conn, http.StatusBadRequest, "Invalid request target")
		return err
	}

	if err := r.checkEntryAccess(ctx, targetHost, targetPort); err != nil {
		r.log.Warn().Str("address", targetHost).Int("port", targetPort).Msg("HTTP proxy request blocked by access control")
		_ = r.writeHTTPProxyError(conn, http.StatusForbidden, "Destination blocked by access control")
		return err
	}

	channelID := uuid.New()
	r.log.Trace().
		Str("channel_id", channelID.String()).
		Str("address", targetHost).
		Int("port", targetPort).
		Str("method", request.Method).
		Msg("Starting absolute-form HTTP proxy handling")

	channelQueue := make(chan BaseMessage, 1000)
	r.messageQueues.Store(channelID, channelQueue)
	r.flushOrphanData(channelID, channelQueue)
	defer r.disconnectChannel(channelID)

	connectRequest := ConnectMessage{
		Protocol:  "tcp",
		Address:   targetHost,
		Port:      targetPort,
		ChannelID: channelID,
	}
	channel := r.registerLogicalChannel(channelID, "tcp", ws, ws, channelPathRelay)
	channel.setRequest(connectRequest)
	channel.markResumeOwner()
	ws = channel
	r.log.Debug().Str("address", targetHost).Int("port", targetPort).Msg("Requesting TCP connection via HTTP proxy")
	r.logMessage(connectRequest, "send", ws.Label())
	if err := ws.WriteMessage(connectRequest); err != nil {
		_ = r.writeHTTPProxyError(conn, http.StatusBadGateway, "Failed to reach backend")
		return fmt.Errorf("write connect request error: %w", err)
	}

	if !r.option.FastOpen {
		if err := r.waitForConnectSuccess(ctx, channelQueue, channelID, targetHost, targetPort); err != nil {
			_ = r.writeHTTPProxyError(conn, http.StatusBadGateway, "Connection failed")
			return err
		}
	} else {
		r.armFastOpenTimeout(ctx, channelID, targetHost, targetPort)
	}

	// Rewrite to origin-form for the origin server and strip hop-by-hop headers.
	outboundRequest := request.Clone(ctx)
	outboundRequest.RequestURI = ""
	outboundRequest.URL.Scheme = ""
	outboundRequest.URL.Host = ""
	outboundRequest.Host = net.JoinHostPort(targetHost, strconv.Itoa(targetPort))
	if targetPort == 80 && strings.EqualFold(request.URL.Scheme, "http") {
		outboundRequest.Host = targetHost
	}
	if targetPort == 443 && strings.EqualFold(request.URL.Scheme, "https") {
		outboundRequest.Host = targetHost
	}

	for headerName := range hopByHopHeaders {
		outboundRequest.Header.Del(headerName)
	}
	if connectionHeader := request.Header.Get("Connection"); connectionHeader != "" {
		for _, connectionToken := range strings.Split(connectionHeader, ",") {
			outboundRequest.Header.Del(strings.TrimSpace(connectionToken))
		}
	}
	outboundRequest.Header.Del("Proxy-Connection")
	outboundRequest.Header.Set("Connection", "close")

	// Pipe: write rewritten request into the tunnel as the first payload, then
	// continue with standard bidirectional TCP forwarding for the response.
	requestPipeReader, requestPipeWriter := net.Pipe()
	go func() {
		defer requestPipeWriter.Close()
		if err := outboundRequest.Write(requestPipeWriter); err != nil {
			r.log.Debug().Err(err).Msg("Failed to write rewritten HTTP proxy request")
		}
	}()

	// Combine rewritten request stream with any leftover client bytes (usually none
	// after ReadRequest consumed headers/body) via multi-reader conn facade.
	combined := &httpProxyClientConn{
		Conn:   conn,
		reader: io.MultiReader(requestPipeReader, conn),
	}

	return r.HandleSocksTCPForward(ctx, ws, combined, channelID)
}

// httpProxyClientConn presents a net.Conn whose Read order is: rewritten request,
// then the original client connection (for pipelined follow-ups, typically unused).
type httpProxyClientConn struct {
	net.Conn
	reader io.Reader
}

func (c *httpProxyClientConn) Read(payload []byte) (int, error) {
	return c.reader.Read(payload)
}

func (r *Relay) waitForConnectSuccess(ctx context.Context, channelQueue <-chan BaseMessage, channelID uuid.UUID, targetHost string, targetPort int) error {
	select {
	case message := <-channelQueue:
		response, ok := message.(ConnectResponseMessage)
		if !ok {
			r.log.Debug().Str("channel_id", channelID.String()).Msg("Unexpected message type in connect response queue")
			return fmt.Errorf("unexpected message type for connect response")
		}
		if !response.Success {
			r.log.Debug().
				Str("channel_id", channelID.String()).
				Str("error", response.Error).
				Msg("Connect response indicates failure")
			return fmt.Errorf("remote connection failed: %s", response.Error)
		}
		r.log.Trace().Str("addr", targetHost).Int("port", targetPort).Msg("Remote successfully connected")
		return nil
	case <-time.After(r.option.ConnectTimeout + 5*time.Second):
		r.log.Debug().
			Str("channel_id", channelID.String()).
			Str("addr", targetHost).
			Int("port", targetPort).
			Msg("Connect response timeout waiting on queue")
		return fmt.Errorf("remote connection response timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Relay) armFastOpenTimeout(ctx context.Context, channelID uuid.UUID, targetHost string, targetPort int) {
	r.log.Trace().Str("addr", targetHost).Int("port", targetPort).Msg("Assume successful connection in fast-open mode")

	go func() {
		timer := time.NewTimer(r.option.ConnectTimeout + 5*time.Second)
		defer timer.Stop()

		select {
		case <-timer.C:
			if _, ok := r.connectionSuccessMap.LoadAndDelete(channelID); !ok {
				r.log.Debug().
					Str("addr", targetHost).
					Int("port", targetPort).
					Msg("Connection timeout without success confirmation")
				r.disconnectChannel(channelID)
			}
		case <-ctx.Done():
			return
		}
	}()
}
