package zone

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConnectThroughHTTPRedirect(t *testing.T) {
	const id = "b5bf1fe8-c357-4b77-b70e-3e6487b47e52"
	public, identity, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	verified := make(chan bool, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/zones/"+id+"/info" {
			json.NewEncoder(w).Encode(map[string]any{"zone": network{ID: id}})
			return
		}
		if r.URL.Path != "/zone/"+id+"/socket" {
			http.NotFound(w, r)
			return
		}
		ws, err := (&websocket.Upgrader{}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer ws.Close()
		_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
		challenge := strings.Repeat("ab", 32)
		ws.WriteJSON(map[string]string{"type": "challenge", "challenge": challenge})
		var registration registration
		if err := ws.ReadJSON(&registration); err != nil {
			verified <- false
			return
		}
		message := strings.Join([]string{protocol, id, challenge, "join", registration.Key, registration.Session, registration.Name}, "\n")
		signature, _ := hex.DecodeString(registration.Signature)
		verified <- registration.Identity == hex.EncodeToString(public) && ed25519.Verify(public, []byte(message), signature)
		ws.WriteJSON(control{Type: "members", Self: "10.88.0.1", Zone: network{ID: id, Name: "test", CIDR: "10.88.0.0/24", MTU: mtu, Visibility: "private"}})
		ws.ReadMessage()
	}))
	defer backend.Close()
	entry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, backend.URL+r.URL.Path, http.StatusFound)
	}))
	defer entry.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := connect(ctx, options{url: entry.URL, id: id, name: "node-a", noProxy: true}, identity, "join")
	if err != nil {
		t.Fatal(err)
	}
	defer conn.ws.Close()
	if !<-verified || conn.initial.Self != "10.88.0.1" {
		t.Fatal("registration was not verified or address assignment changed")
	}
}

func TestRelayURLValidation(t *testing.T) {
	for _, raw := range []string{"ftp://example.com", "https://user:password@example.com", "https://example.com?token=secret", "https://example.com#fragment", "://"} {
		if _, err := baseURL(raw); err == nil {
			t.Errorf("accepted invalid relay URL %q", raw)
		}
	}
	for raw, want := range map[string]string{"wss://example.com": "https://example.com", "ws://localhost:8787": "http://localhost:8787", "example.com": "https://example.com"} {
		u, err := baseURL(raw)
		if err != nil || u.String() != want {
			t.Errorf("URL %q: got %v, %v; want %s", raw, u, err, want)
		}
	}
}

func TestCoordinatorHTTPFailureClassification(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound, http.StatusBadGateway, http.StatusServiceUnavailable} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, http.StatusText(status), status)
			}))
			defer server.Close()
			_, err := fetchJSON(context.Background(), options{url: server.URL, noProxy: true}, "/api/zones/public", new(any))
			var terminal terminalError
			if err == nil || errors.As(err, &terminal) != (status < 500) {
				t.Fatalf("unexpected retry classification for HTTP %d: %v", status, err)
			}
		})
	}
}

type syncBuffer struct {
	mu sync.Mutex
	bb bytes.Buffer
}

func (b *syncBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bb.Write(data)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bb.String()
}

// fakeZoneSession is the server side of one membership: it speaks the
// coordinator protocol and then the zone protocol with the client.
type fakeZoneSession struct {
	t      *testing.T
	ws     *websocket.Conn
	cipher *packetCipher
	client netip.Addr
	peer   netip.Addr
	out    *syncBuffer
	update func(settings)
	cancel context.CancelFunc
}

func (s *fakeZoneSession) write(message []byte) {
	s.t.Helper()
	_ = s.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := s.ws.WriteMessage(websocket.BinaryMessage, message); err != nil {
		s.t.Fatalf("write fake zone message: %v", err)
	}
}

func (s *fakeZoneSession) writeControl(message control) {
	s.t.Helper()
	_ = s.ws.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if err := s.ws.WriteJSON(message); err != nil {
		s.t.Fatalf("write fake zone control: %v", err)
	}
}

func (s *fakeZoneSession) expect(description string, match func([]byte) bool) []byte {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		_ = s.ws.SetReadDeadline(deadline)
		kind, data, err := s.ws.ReadMessage()
		if err != nil {
			s.t.Fatalf("waiting for %s: %v", description, err)
		}
		if kind == websocket.BinaryMessage && match(data) {
			return data
		}
		if time.Now().After(deadline) {
			s.t.Fatalf("timed out waiting for %s", description)
		}
	}
}

func (s *fakeZoneSession) peerState() *peerCipher {
	return s.cipher.peers[s.client]
}

// startFakeWorker serves /api/zones/<id>/info and hands the upgraded socket
// to the test, which then plays the coordinator.
func startFakeWorker(t *testing.T) (string, chan *websocket.Conn) {
	t.Helper()
	const id = "b5bf1fe8-c357-4b77-b70e-3e6487b47e52"
	upgrader := &websocket.Upgrader{}
	wsCh := make(chan *websocket.Conn, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/zones/"+id+"/info" {
			json.NewEncoder(w).Encode(map[string]any{"zone": network{ID: id}})
			return
		}
		if r.URL.Path != "/zone/"+id+"/socket" {
			http.NotFound(w, r)
			return
		}
		ws, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		wsCh <- ws
	}))
	t.Cleanup(server.Close)
	return server.URL, wsCh
}

// startFakeZoneClient runs the coordinator handshake against an in-process
// fake Worker, then starts the client's serve loop with the given SOCKS port
// (0 disables the listener).
func startFakeZoneClient(t *testing.T, initial settings, socksPort int) *fakeZoneSession {
	t.Helper()
	const id = "b5bf1fe8-c357-4b77-b70e-3e6487b47e52"
	const selfIP, peerIP = "10.88.0.1", "10.88.0.2"
	_, identity, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	url, wsCh := startFakeWorker(t)
	ctx, cancel := context.WithCancel(context.Background())
	connCh := make(chan *connection, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := connect(ctx, options{url: url, id: id, name: "client", noProxy: true}, identity, "join")
		if err != nil {
			errCh <- err
			return
		}
		connCh <- conn
	}()

	ws := <-wsCh
	challenge := strings.Repeat("ab", 32)
	if err := ws.WriteJSON(map[string]string{"type": "challenge", "challenge": challenge}); err != nil {
		t.Fatal(err)
	}
	var registration registration
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := ws.ReadJSON(&registration); err != nil {
		t.Fatal(err)
	}
	if registration.Identity == "" || registration.Key == "" || registration.Session == "" {
		t.Fatal("client registration is incomplete")
	}
	peerNode := newPacketTestNode(t, "peer", peerIP)
	clientNode := packetTestNode{peer: peer{Identity: registration.Identity, Key: registration.Key, Session: registration.Session, Name: "client", IP: selfIP}}
	cipher := newTestPacketCipher(t, id, peerNode, clientNode)
	roster := control{
		Type: "members",
		Zone: network{ID: id, Name: "fake zone", CIDR: "10.88.0.0/24", MTU: mtu, Visibility: "private"},
		Self: selfIP, Settings: initial, Peers: []peer{peerNode.peer},
	}
	if err := ws.WriteJSON(roster); err != nil {
		t.Fatal(err)
	}
	var conn *connection
	select {
	case conn = <-connCh:
	case err := <-errCh:
		t.Fatal(err)
	case <-time.After(5 * time.Second):
		t.Fatal("client did not join the fake zone")
	}
	_ = ws.SetReadDeadline(time.Time{})
	out := &syncBuffer{}
	go func() { _ = conn.serve(ctx, options{socksPort: socksPort, out: out, noProxy: true}) }()
	session := &fakeZoneSession{
		t: t, ws: ws, cipher: cipher, out: out, cancel: cancel,
		client: netip.MustParseAddr(selfIP), peer: netip.MustParseAddr(peerIP),
	}
	session.update = func(settings settings) {
		roster.Settings = settings
		session.writeControl(roster)
	}
	t.Cleanup(func() {
		cancel()
		ws.Close()
	})
	return session
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	return port
}

// waitFor polls until the session output contains text.
func (s *fakeZoneSession) waitFor(text string) {
	s.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(s.out.String(), text) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	s.t.Fatalf("output never contained %q: %s", text, s.out.String())
}

func startTCPEcho(t *testing.T) (int, func() int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	dials := 0
	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			dials++
			mu.Unlock()
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()
	t.Cleanup(func() { listener.Close() })
	return listener.Addr().(*net.TCPAddr).Port, func() int {
		mu.Lock()
		defer mu.Unlock()
		return dials
	}
}

func startUDPEcho(t *testing.T) int {
	t.Helper()
	socket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		buffer := make([]byte, 65535)
		for {
			n, from, err := socket.ReadFromUDP(buffer)
			if err != nil {
				return
			}
			_, _ = socket.WriteToUDP(buffer[:n], from)
		}
	}()
	t.Cleanup(func() { socket.Close() })
	return socket.LocalAddr().(*net.UDPAddr).Port
}

func TestServeAcceptsInboundStream(t *testing.T) {
	echoPort, _ := startTCPEcho(t)
	session := startFakeZoneClient(t, settings{DefaultAction: "allow", Welcome: "hello zone"}, 0)
	session.waitFor("Joined fake zone")
	session.waitFor("Welcome: hello zone")

	const id = 0x2a2a2a2a
	session.write(openMessage(id, session.peer, session.client, uint16(echoPort)))
	session.expect("ACCEPT", func(message []byte) bool {
		op, got, _, err := streamOp(message)
		return err == nil && op == opAccept && got == id
	})
	data, err := session.cipher.sealStreamData(session.peerState(), id, []byte("ping"))
	if err != nil {
		t.Fatal(err)
	}
	session.write(data)
	reply := session.expect("stream DATA", func(message []byte) bool {
		op, got, _, err := streamOp(message)
		return err == nil && op == opData && got == id
	})
	_, payload, err := session.cipher.openStreamData(session.peerState(), reply)
	if err != nil || string(payload) != "ping" {
		t.Fatalf("echo payload = %q, %v", payload, err)
	}
	session.write(closeMessage(id))
	session.expect("CLOSE", func(message []byte) bool {
		op, got, _, err := streamOp(message)
		return err == nil && op == opClose && got == id
	})
}

func TestServeDeniesInboundStreamByRules(t *testing.T) {
	echoPort, dials := startTCPEcho(t)
	session := startFakeZoneClient(t, settings{DefaultAction: "deny"}, 0)
	session.waitFor("Joined fake zone")
	const blocked = 0x32323232
	session.write(openMessage(blocked, session.peer, session.client, uint16(echoPort)))
	refuse := session.expect("REFUSE", func(message []byte) bool {
		op, got, _, err := streamOp(message)
		return err == nil && op == opRefuse && got == blocked
	})
	op, _, fields, err := streamOp(refuse)
	if err != nil || op != opRefuse || binary.BigEndian.Uint16(fields[:2]) != refuseNotAllowed {
		t.Fatalf("unexpected refusal: %x", refuse)
	}
	time.Sleep(100 * time.Millisecond)
	if dials() != 0 {
		t.Fatal("client dialed a service the rules deny")
	}
}

func TestServeDeliversInboundDatagram(t *testing.T) {
	echoPort := startUDPEcho(t)
	session := startFakeZoneClient(t, settings{DefaultAction: "allow"}, 0)
	session.waitFor("Joined fake zone")
	message, err := session.cipher.sealDatagram(session.client, 40000, uint16(echoPort), []byte("datagram"))
	if err != nil {
		t.Fatal(err)
	}
	session.write(message)
	reply := session.expect("datagram reply", func(message []byte) bool {
		return len(message) > 0 && message[0] == kindDatagram
	})
	datagram, err := session.cipher.openDatagram(reply)
	if err != nil {
		t.Fatal(err)
	}
	if datagram.src != session.client || datagram.dst != session.peer || datagram.srcPort != uint16(echoPort) || datagram.dstPort != 40000 || string(datagram.payload) != "datagram" {
		t.Fatalf("unexpected datagram reply: %+v", datagram)
	}
}

func TestServeDropsInboundDatagramByRules(t *testing.T) {
	echoPort := startUDPEcho(t)
	session := startFakeZoneClient(t, settings{DefaultAction: "deny"}, 0)
	session.waitFor("Joined fake zone")
	message, err := session.cipher.sealDatagram(session.client, 40000, uint16(echoPort), []byte("datagram"))
	if err != nil {
		t.Fatal(err)
	}
	session.write(message)
	_ = session.ws.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	if _, data, err := session.ws.ReadMessage(); err == nil && len(data) > 0 && data[0] == kindDatagram {
		t.Fatal("denied datagram was forwarded")
	}
}

func TestServeSettingsUpdateTakesEffect(t *testing.T) {
	session := startFakeZoneClient(t, settings{DefaultAction: "deny"}, 0)
	session.waitFor("Joined fake zone")
	session.update(settings{DefaultAction: "allow", Welcome: "welcome back"})
	session.waitFor("Welcome: welcome back")
	echoPort, dials := startTCPEcho(t)
	const id = 0x45454545
	session.write(openMessage(id, session.peer, session.client, uint16(echoPort)))
	session.expect("ACCEPT", func(message []byte) bool {
		op, got, _, err := streamOp(message)
		return err == nil && op == opAccept && got == id
	})
	if dials() == 0 {
		t.Fatal("client did not dial after the rules update allowed it")
	}
}

func TestServeSOCKSConnectToMember(t *testing.T) {
	proxyPort := freePort(t)
	session := startFakeZoneClient(t, settings{DefaultAction: "allow"}, proxyPort)
	session.waitFor(fmt.Sprintf("SOCKS5 proxy listening on 127.0.0.1:%d", proxyPort))
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	socksHandshake(t, conn)
	if _, err := conn.Write(socksConnectRequest(socksCommandTCP, session.peer.AsSlice(), socksAddressIPv4, 8080)); err != nil {
		t.Fatal(err)
	}
	open := session.expect("OPEN", func(message []byte) bool {
		op, _, _, err := streamOp(message)
		return err == nil && op == opOpen
	})
	_, id, fields, err := streamOp(open)
	if err != nil || len(fields) != 10 {
		t.Fatalf("unexpected OPEN: %x", open)
	}
	if netip.AddrFrom4([4]byte(fields[0:4])) != session.client || netip.AddrFrom4([4]byte(fields[4:8])) != session.peer || binary.BigEndian.Uint16(fields[8:10]) != 8080 {
		t.Fatalf("OPEN fields = %x", fields)
	}
	session.write(acceptMessage(id))
	if code := readSocksReply(t, conn); code != socksSucceeded {
		t.Fatalf("SOCKS CONNECT reply = %d", code)
	}
	if _, err := conn.Write([]byte("out")); err != nil {
		t.Fatal(err)
	}
	data := session.expect("stream DATA", func(message []byte) bool {
		op, got, _, err := streamOp(message)
		return err == nil && op == opData && got == id
	})
	_, payload, err := session.cipher.openStreamData(session.peerState(), data)
	if err != nil || string(payload) != "out" {
		t.Fatalf("stream payload = %q, %v", payload, err)
	}
	reply, err := session.cipher.sealStreamData(session.peerState(), id, []byte("in"))
	if err != nil {
		t.Fatal(err)
	}
	session.write(reply)
	echo := make([]byte, 2)
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "in" {
		t.Fatalf("SOCKS client read %q, %v", echo, err)
	}
	conn.Close()
	session.expect("CLOSE", func(message []byte) bool {
		op, got, _, err := streamOp(message)
		return err == nil && op == opClose && got == id
	})
}

func TestServeSOCKSDatagramRoundTrip(t *testing.T) {
	proxyPort := freePort(t)
	session := startFakeZoneClient(t, settings{DefaultAction: "allow"}, proxyPort)
	session.waitFor(fmt.Sprintf("SOCKS5 proxy listening on 127.0.0.1:%d", proxyPort))
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	socksHandshake(t, conn)
	if _, err := conn.Write(socksConnectRequest(socksCommandUDP, []byte{0, 0, 0, 0}, socksAddressIPv4, 0)); err != nil {
		t.Fatal(err)
	}
	rawReply := make([]byte, 10)
	if _, err := io.ReadFull(conn, rawReply); err != nil || rawReply[1] != socksSucceeded {
		t.Fatalf("UDP ASSOCIATE reply = %x, %v", rawReply, err)
	}
	relay := net.JoinHostPort(net.IP(rawReply[4:8]).String(), fmt.Sprint(binary.BigEndian.Uint16(rawReply[8:10])))
	socket, err := net.Dial("udp", relay)
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	_ = socket.SetDeadline(time.Now().Add(5 * time.Second))
	request := formatSocksUDPResponse(session.peer, 7777, []byte("question"))
	if _, err := socket.Write(request); err != nil {
		t.Fatal(err)
	}
	message := session.expect("datagram", func(message []byte) bool {
		return len(message) > 0 && message[0] == kindDatagram
	})
	datagram, err := session.cipher.openDatagram(message)
	if err != nil {
		t.Fatal(err)
	}
	if datagram.src != session.client || datagram.dst != session.peer || datagram.dstPort != 7777 || string(datagram.payload) != "question" {
		t.Fatalf("unexpected outbound datagram: %+v", datagram)
	}
	reply, err := session.cipher.sealDatagram(session.client, 7777, datagram.srcPort, []byte("answer"))
	if err != nil {
		t.Fatal(err)
	}
	session.write(reply)
	response := make([]byte, 256)
	n, err := socket.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	parsed, payload, err := parseSocksUDPRequest(response[:n])
	if err != nil || parsed.address != session.peer.String() || parsed.port != 7777 || string(payload) != "answer" {
		t.Fatalf("SOCKS UDP response = %+v %q %v", parsed, payload, err)
	}
}

func TestRunStopsWhenRemovedFromZone(t *testing.T) {
	const id = "b5bf1fe8-c357-4b77-b70e-3e6487b47e52"
	_, identity, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(t.TempDir(), "zone.key")
	if err := os.WriteFile(keyPath, []byte(hex.EncodeToString(identity.Seed())+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	url, wsCh := startFakeWorker(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, options{url: url, id: id, identity: keyPath, name: "client", noProxy: true, out: out})
	}()
	ws := <-wsCh
	if err := ws.WriteJSON(map[string]string{"type": "challenge", "challenge": strings.Repeat("ab", 32)}); err != nil {
		t.Fatal(err)
	}
	var registration registration
	_ = ws.SetReadDeadline(time.Now().Add(5 * time.Second))
	if err := ws.ReadJSON(&registration); err != nil {
		t.Fatal(err)
	}
	roster := control{
		Type: "members",
		Zone: network{ID: id, Name: "fake zone", CIDR: "10.88.0.0/24", MTU: mtu},
		Self: "10.88.0.1",
	}
	if err := ws.WriteJSON(roster); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(out.String(), "Joined fake zone") && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(out.String(), "Joined fake zone") {
		t.Fatalf("client never joined: %s", out.String())
	}
	if err := ws.WriteJSON(map[string]string{"type": "error", "error": "Removed from the zone by its owner"}); err != nil {
		t.Fatal(err)
	}
	_ = ws.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(4003, "Removed from zone"), time.Now().Add(time.Second))
	select {
	case err := <-done:
		var terminal terminalError
		if !errors.As(err, &terminal) {
			t.Fatalf("run returned %v, want a terminal error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run kept reconnecting after the kick")
	}
	if !strings.Contains(out.String(), "Removed from the zone by its owner") || !strings.Contains(out.String(), "Removed from the zone.") {
		t.Fatalf("missing kick output: %s", out.String())
	}
}
