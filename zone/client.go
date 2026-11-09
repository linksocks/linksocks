package zone

import (
	"context"
	"crypto/ed25519"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"github.com/linksocks/linksocks/internal/ech"
	"github.com/rs/zerolog"
)

type options struct {
	url          string
	id           string
	identity     string
	name         string
	iface        string
	ifaceSet     bool
	socksPort    int
	socksPortSet bool
	vlan         bool
	noProxy      bool
	ech          string
	logger       zerolog.Logger
	out          io.Writer
}

type terminalError struct{ error }

type connection struct {
	ws      *websocket.Conn
	writeMu sync.Mutex
	initial control
	cipher  *packetCipher

	settingsMu sync.RWMutex
	rules      ruleset
	welcome    string

	streams     *streamTable
	udpMu       sync.Mutex
	udpClients  map[udpClientKey]*udpClient
	udpServices map[udpServiceKey]*udpService

	ctx  context.Context
	done chan struct{}
}

func (c *connection) self() netip.Addr { return c.cipher.self }

func baseURL(raw string) (*url.URL, error) {
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid zone relay URL")
	}
	switch u.Scheme {
	case "ws":
		u.Scheme = "http"
	case "wss":
		u.Scheme = "https"
	case "http", "https":
	default:
		return nil, errors.New("zone relay URL must use http, https, ws or wss")
	}
	u.Path, u.RawPath = "", ""
	return u, nil
}

func fetchJSON(ctx context.Context, opt options, path string, target any) (*url.URL, error) {
	u, err := baseURL(opt.url)
	if err != nil {
		return nil, err
	}
	u.Path = path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if opt.noProxy {
		transport.Proxy = nil
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 15 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(response.Body, 1024))
		err := fmt.Errorf("zone coordinator: HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
		if response.StatusCode >= http.StatusInternalServerError {
			return nil, err
		}
		return nil, terminalError{err}
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(target); err != nil {
		return nil, fmt.Errorf("invalid coordinator response: %w", err)
	}
	return response.Request.URL, nil
}

func connect(ctx context.Context, opt options, identity ed25519.PrivateKey, action string) (*connection, error) {
	var info struct {
		Zone network `json:"zone"`
	}
	u, err := fetchJSON(ctx, opt, "/api/zones/"+opt.id+"/info", &info)
	if err != nil {
		return nil, err
	}
	if info.Zone.ID != opt.id {
		return nil, terminalError{errors.New("coordinator returned a different zone")}
	}
	u.Path = "/zone/" + opt.id + "/socket"
	u.RawQuery = ""
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	dialer := *websocket.DefaultDialer
	if opt.noProxy {
		dialer.Proxy = nil
	}
	if u.Scheme == "wss" {
		echMode := opt.ech
		if echMode == "" {
			echMode = ech.Auto
		}
		var proxy func(*http.Request) (*url.URL, error)
		if !opt.noProxy {
			proxy = http.ProxyFromEnvironment
		}
		dialer.TLSClientConfig = ech.Config(ctx, u.Hostname(), false, echMode, proxy, opt.logger, nil)
	}
	ws, response, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		if response != nil {
			response.Body.Close()
		}
		return nil, err
	}
	ws.SetReadLimit(128 << 10)
	_ = ws.SetReadDeadline(time.Now().Add(15 * time.Second))
	var message control
	if err := ws.ReadJSON(&message); err != nil {
		ws.Close()
		return nil, err
	}
	if message.Type != "challenge" {
		ws.Close()
		return nil, terminalError{fmt.Errorf("expected zone challenge: %s", message.Error)}
	}
	registration, key, err := newRegistration(identity, opt.id, message.Challenge, action, opt.name)
	if err == nil {
		err = ws.WriteJSON(registration)
	}
	if err == nil {
		err = ws.ReadJSON(&message)
	}
	if err != nil {
		ws.Close()
		return nil, err
	}
	if message.Type == "error" {
		ws.Close()
		return nil, terminalError{errors.New(message.Error)}
	}
	conn := &connection{
		ws: ws, initial: message,
		streams:     newStreamTable(),
		udpClients:  make(map[udpClientKey]*udpClient),
		udpServices: make(map[udpServiceKey]*udpService),
		done:        make(chan struct{}),
	}
	if action == "leave" && message.Type == "left" {
		return conn, nil
	}
	if message.Type != "members" {
		ws.Close()
		return nil, terminalError{errors.New("expected zone membership response")}
	}
	conn.cipher, err = newPacketCipher(opt.id, message, key, registration.Session, identity.Public().(ed25519.PublicKey))
	if err == nil {
		conn.rules, err = compileSettings(message.Settings)
	}
	if err != nil {
		ws.Close()
		return nil, terminalError{err}
	}
	conn.welcome = message.Settings.Welcome
	_ = ws.SetReadDeadline(time.Time{})
	return conn, nil
}

func (c *connection) writeJSON(message any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return c.ws.WriteJSON(message)
}

// send seals messages and writes them under the connection write lock so
// per-direction sequence numbers reach the wire in sealing order.
func (c *connection) send(messages func() ([][]byte, error)) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	built, err := messages()
	if err != nil {
		return err
	}
	for _, message := range built {
		_ = c.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
		if err := c.ws.WriteMessage(websocket.BinaryMessage, message); err != nil {
			c.ws.Close()
			return err
		}
	}
	return nil
}

func (c *connection) sendPlain(message []byte) error {
	return c.send(func() ([][]byte, error) { return [][]byte{message}, nil })
}

func run(ctx context.Context, opt options) error {
	identity, err := loadIdentity(opt.identity)
	if err != nil {
		return err
	}
	delay := time.Second
	for ctx.Err() == nil {
		conn, err := connect(ctx, opt, identity, "join")
		if err == nil {
			err = conn.serve(ctx, opt)
			conn.ws.Close()
			delay = time.Second
		}
		if ctx.Err() != nil {
			return nil
		}
		if err == nil || websocket.IsCloseError(err, 4001) {
			fmt.Fprintf(opt.out, "Left zone %s.\n", opt.id)
			return nil
		}
		if websocket.IsCloseError(err, 4003) {
			fmt.Fprintf(opt.out, "Removed from the zone.\n")
			return terminalError{errors.New("removed from the zone")}
		}
		var terminal terminalError
		if errors.As(err, &terminal) || websocket.IsCloseError(err, 1008) {
			return err
		}
		fmt.Fprintf(opt.out, "Zone connection lost: %v. Reconnecting in %s.\n", err, delay)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
		if delay < 8*time.Second {
			delay *= 2
		}
	}
	return nil
}

func (c *connection) serve(ctx context.Context, opt options) error {
	var tap l2Device
	if opt.vlan {
		device, err := openTAP(ctx, opt.iface, c.cipher.prefix, c.cipher.self, c.cipher.mac)
		if err != nil {
			_ = c.writeJSON(map[string]string{"type": "leave"})
			return terminalError{err}
		}
		tap = device
	} else if opt.ifaceSet {
		fmt.Fprintln(opt.out, "Note: --interface applies to --vlan mode; proxy mode does not create an interface.")
	}
	var server *socksServer
	// --vlan replaces proxy mode; the SOCKS listener only starts there when
	// --socks-port is given explicitly, so several VLAN members can run on
	// one machine without fighting over the default port.
	if opt.socksPort != 0 && (!opt.vlan || opt.socksPortSet) {
		var err error
		server, err = newSocksServer(ctx, c, opt.socksPort)
		if err != nil {
			if tap != nil {
				tap.Close()
			}
			_ = c.writeJSON(map[string]string{"type": "leave"})
			return terminalError{fmt.Errorf("start SOCKS5 proxy: %w", err)}
		}
		go server.serve()
	}

	zoneName := c.initial.Zone.Name + " (" + opt.id
	if visibility := c.initial.Zone.Visibility; visibility != "" {
		zoneName += ", " + visibility
	}
	zoneName += ")"
	where := ""
	if tap != nil {
		where = " on " + tap.Name()
	}
	fmt.Fprintf(opt.out, "Joined %s as %s%s; %d peer(s) online.\n", zoneName, c.initial.Self, where, len(c.initial.Peers))
	if c.welcome != "" {
		fmt.Fprintf(opt.out, "Welcome: %s\n", c.welcome)
	}
	if server != nil {
		fmt.Fprintf(opt.out, "SOCKS5 proxy listening on %s\n", server.addr())
	}

	child, cancel := context.WithCancel(ctx)
	defer cancel()
	c.ctx = child
	readDone := make(chan struct{})
	deviceError := make(chan error, 1)
	if tap != nil {
		go func() {
			defer close(readDone)
			buffer := make([]byte, maxFrameSize)
			for {
				n, err := tap.ReadFrame(buffer)
				if err != nil {
					deviceError <- err
					c.ws.Close()
					return
				}
				if !c.ruleAllowed(ethernetFlow(buffer[:n])) {
					continue
				}
				frame := make([]byte, n)
				copy(frame, buffer[:n])
				if err := c.send(func() ([][]byte, error) { return c.cipher.sealFrame(frame) }); err != nil {
					return
				}
			}
		}()
	} else {
		close(readDone)
	}
	keepaliveDone := make(chan struct{})
	go func() {
		defer close(keepaliveDone)
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				if ctx.Err() != nil {
					_ = c.writeJSON(map[string]string{"type": "leave"})
				}
				c.ws.Close()
				return
			case <-ticker.C:
				if c.writeJSON(map[string]string{"type": "ping"}) != nil {
					c.ws.Close()
					return
				}
			}
		}
	}()
	reapDone := make(chan struct{})
	go func() {
		defer close(reapDone)
		ticker := time.NewTicker(udpReapInterval)
		defer ticker.Stop()
		for {
			select {
			case <-child.Done():
				return
			case <-ticker.C:
				c.reapDatagrams()
			}
		}
	}()
	defer func() {
		cancel()
		if tap != nil {
			tap.Close()
		}
		if ctx.Err() != nil {
			for _, stream := range c.streams.each() {
				_ = c.sendPlain(resetMessage(stream.id, 1))
			}
		}
		c.ws.Close()
		<-readDone
		<-keepaliveDone
		<-reapDone
		if server != nil {
			server.close()
		}
		for _, stream := range c.streams.each() {
			stream.abort()
		}
		c.closeDatagrams()
		close(c.done)
	}()

	peersOnline := len(c.initial.Peers)
	for {
		_ = c.ws.SetReadDeadline(time.Now().Add(75 * time.Second))
		kind, data, err := c.ws.ReadMessage()
		if err != nil {
			select {
			case deviceErr := <-deviceError:
				return terminalError{fmt.Errorf("read zone interface: %w", deviceErr)}
			default:
			}
			return err
		}
		if kind == websocket.BinaryMessage {
			if len(data) == 0 {
				continue
			}
			switch data[0] {
			case kindFrame:
				_, frame, err := c.cipher.openFrame(data)
				if err != nil {
					continue
				}
				if !c.ruleAllowed(ethernetFlow(frame)) {
					continue
				}
				if tap != nil {
					if err := tap.WriteFrame(frame); err != nil {
						return terminalError{fmt.Errorf("write zone interface: %w", err)}
					}
				}
			case kindStream:
				c.handleStream(data)
			case kindDatagram:
				datagram, err := c.cipher.openDatagram(data)
				if err != nil {
					continue
				}
				c.deliverDatagram(datagram)
			}
			continue
		}
		var message control
		if json.Unmarshal(data, &message) != nil {
			return terminalError{errors.New("invalid coordinator control message")}
		}
		switch message.Type {
		case "members":
			if err := c.cipher.update(message); err != nil {
				return terminalError{err}
			}
			if err := c.applySettings(message.Settings, opt); err != nil {
				return terminalError{err}
			}
			c.dropStaleStreams()
			if len(message.Peers) != peersOnline {
				peersOnline = len(message.Peers)
				fmt.Fprintf(opt.out, "Zone peers online: %d.\n", peersOnline)
			}
		case "error":
			if message.Error != "" {
				fmt.Fprintln(opt.out, message.Error)
			}
		case "left":
			return nil
		case "pong":
		default:
			return terminalError{errors.New("unexpected zone control message")}
		}
	}
}

func (c *connection) applySettings(s settings, opt options) error {
	rules, err := compileSettings(s)
	if err != nil {
		return err
	}
	c.settingsMu.Lock()
	changed := s.Welcome != c.welcome
	c.rules, c.welcome = rules, s.Welcome
	c.settingsMu.Unlock()
	if changed && s.Welcome != "" {
		fmt.Fprintf(opt.out, "Welcome: %s\n", s.Welcome)
	}
	return nil
}

func (c *connection) ruleAllowed(f flow) bool {
	c.settingsMu.RLock()
	rules := c.rules
	c.settingsMu.RUnlock()
	return rules.allow(f)
}

func (c *connection) handleStream(message []byte) {
	op, id, fields, err := streamOp(message)
	if err != nil {
		return
	}
	switch op {
	case opOpen:
		c.handleOpen(id, fields)
	case opAccept:
		if s := c.streams.get(id); s != nil && s.ready != nil {
			select {
			case s.ready <- 0:
			default:
			}
		}
	case opRefuse:
		if len(fields) < 2 {
			return
		}
		if s := c.streams.get(id); s != nil && s.ready != nil {
			select {
			case s.ready <- binary.BigEndian.Uint16(fields[:2]):
			default:
			}
		}
	case opData:
		s := c.streams.get(id)
		if s == nil {
			return
		}
		_, payload, err := c.cipher.openStreamData(s.peer, message)
		if err != nil {
			return
		}
		s.enqueue(payload)
	case opClose:
		if s := c.streams.get(id); s != nil {
			s.remoteClose()
		}
	case opReset:
		if s := c.streams.get(id); s != nil {
			s.abort()
		}
	}
}

// handleOpen terminates an inbound stream on 127.0.0.1 after the member's own
// firewall copy allowed it.
func (c *connection) handleOpen(id uint32, fields []byte) {
	if len(fields) != 10 {
		return
	}
	src := netip.AddrFrom4([4]byte(fields[0:4]))
	dst := netip.AddrFrom4([4]byte(fields[4:8]))
	port := binary.BigEndian.Uint16(fields[8:10])
	if dst != c.cipher.self || c.cipher.peerAddr(src) == nil {
		return
	}
	if c.streams.get(id) != nil {
		_ = c.sendPlain(resetMessage(id, 1))
		return
	}
	if !c.ruleAllowed(flow{src: src, dst: dst, proto: "tcp", dstPort: port, dstPortKnown: true}) {
		_ = c.sendPlain(refuseMessage(id, refuseNotAllowed, "not allowed"))
		return
	}
	dialer := net.Dialer{Timeout: streamDialWait}
	target := net.JoinHostPort("127.0.0.1", strconv.Itoa(int(port)))
	conn, err := dialer.DialContext(c.ctx, "tcp", target)
	if err != nil {
		_ = c.sendPlain(refuseMessage(id, refuseFailed, "connection failed"))
		return
	}
	stream, ok := c.streams.insert(id, c, c.cipher.peerAddr(src))
	if !ok {
		conn.Close()
		_ = c.sendPlain(resetMessage(id, 1))
		return
	}
	if err := c.sendPlain(acceptMessage(id)); err != nil {
		stream.abort()
		return
	}
	stream.attach(conn)
}

func (c *connection) openStream(ctx context.Context, dst netip.Addr, port uint16) (*zoneStream, error) {
	peer := c.cipher.peerAddr(dst)
	if peer == nil {
		return nil, errUnreachable
	}
	if !c.ruleAllowed(flow{src: c.cipher.self, dst: dst, proto: "tcp", dstPort: port, dstPortKnown: true}) {
		return nil, errNotAllowed
	}
	stream := c.streams.allocate(c, peer, true)
	if err := c.sendPlain(openMessage(stream.id, c.cipher.self, dst, port)); err != nil {
		c.streams.discard(stream)
		return nil, err
	}
	select {
	case code := <-stream.ready:
		if code == 0 {
			return stream, nil
		}
		c.streams.discard(stream)
		switch code {
		case refuseNotAllowed:
			return nil, errNotAllowed
		case refuseFailed:
			return nil, errRefused
		default:
			return nil, errUnreachable
		}
	case <-time.After(streamOpenTimeout):
		c.streams.discard(stream)
		_ = c.sendPlain(resetMessage(stream.id, 1))
		return nil, errStreamTimeout
	case <-ctx.Done():
		c.streams.discard(stream)
		_ = c.sendPlain(resetMessage(stream.id, 1))
		return nil, ctx.Err()
	case <-c.done:
		return nil, errors.New("zone connection closed")
	}
}

// connect implements socksBackend: the SOCKS connection becomes the local
// side of a zone stream to the member.
func (c *connection) connect(ctx context.Context, dst netip.Addr, port uint16, client net.Conn) (func(), error) {
	stream, err := c.openStream(ctx, dst, port)
	if err != nil {
		return nil, err
	}
	stream.attach(client)
	return stream.wait, nil
}

func (c *connection) resolveMember(name string) (netip.Addr, bool) {
	if name == "" {
		return netip.Addr{}, false
	}
	c.cipher.mu.Lock()
	defer c.cipher.mu.Unlock()
	var folded netip.Addr
	foldedCount := 0
	for addr, p := range c.cipher.peers {
		if p.Name == name {
			return addr, true
		}
		if strings.EqualFold(p.Name, name) {
			folded, foldedCount = addr, foldedCount+1
		}
	}
	if foldedCount == 1 {
		return folded, true
	}
	return netip.Addr{}, false
}

func (c *connection) dropStaleStreams() {
	for _, stream := range c.streams.each() {
		if !c.cipher.peerAlive(stream.peer) {
			stream.abort()
		}
	}
}
