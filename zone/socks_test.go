package zone

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"
)

type forwardedDatagram struct {
	sink    udpSink
	client  *net.UDPAddr
	dst     netip.Addr
	port    uint16
	payload []byte
}

type stubSocksBackend struct {
	members    map[string]netip.Addr
	connectErr error
	connected  chan netip.AddrPort
	forwarded  chan forwardedDatagram
	released   chan udpSink
}

func newStubSocksBackend() *stubSocksBackend {
	return &stubSocksBackend{
		members:   map[string]netip.Addr{"member-b": netip.MustParseAddr("10.88.0.2")},
		connected: make(chan netip.AddrPort, 8),
		forwarded: make(chan forwardedDatagram, 8),
		released:  make(chan udpSink, 8),
	}
}

func (b *stubSocksBackend) resolveMember(name string) (netip.Addr, bool) {
	addr, ok := b.members[name]
	return addr, ok
}

func (b *stubSocksBackend) connect(_ context.Context, dst netip.Addr, port uint16, client net.Conn) (func(), error) {
	if b.connectErr != nil {
		return nil, b.connectErr
	}
	b.connected <- netip.AddrPortFrom(dst, port)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buffer := make([]byte, 1024)
		for {
			n, err := client.Read(buffer)
			if err != nil {
				return
			}
			if _, err := client.Write(buffer[:n]); err != nil {
				return
			}
		}
	}()
	return func() { <-done }, nil
}

func (b *stubSocksBackend) forwardDatagram(sink udpSink, client *net.UDPAddr, dst netip.Addr, port uint16, payload []byte) {
	b.forwarded <- forwardedDatagram{sink: sink, client: client, dst: dst, port: port, payload: append([]byte(nil), payload...)}
}

func (b *stubSocksBackend) releaseUDPClient(sink udpSink) {
	b.released <- sink
}

func startSocksTestServer(t *testing.T, backend socksBackend) net.Conn {
	t.Helper()
	server, err := newSocksServer(context.Background(), backend, 0)
	if err != nil {
		t.Fatal(err)
	}
	go server.serve()
	conn, err := net.Dial("tcp", server.addr())
	if err != nil {
		server.close()
		t.Fatal(err)
	}
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	t.Cleanup(func() {
		conn.Close()
		server.close()
	})
	return conn
}

func socksHandshake(t *testing.T, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte{socksVersion, 1, socksNoAuth}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[0] != socksVersion || reply[1] != socksNoAuth {
		t.Fatalf("handshake reply = %x, %v", reply, err)
	}
}

func socksConnectRequest(command byte, address []byte, atyp byte, port uint16) []byte {
	request := []byte{socksVersion, command, 0, atyp}
	request = append(request, address...)
	request = append(request, byte(port>>8), byte(port))
	return request
}

func readSocksReply(t *testing.T, conn net.Conn) byte {
	t.Helper()
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatal(err)
	}
	if reply[0] != socksVersion || reply[2] != 0 || reply[3] != socksAddressIPv4 {
		t.Fatalf("unexpected SOCKS reply %x", reply)
	}
	return reply[1]
}

func TestSocksConnectEchoesThroughBackend(t *testing.T) {
	backend := newStubSocksBackend()
	conn := startSocksTestServer(t, backend)
	socksHandshake(t, conn)
	if _, err := conn.Write(socksConnectRequest(socksCommandTCP, netip.MustParseAddr("10.88.0.2").AsSlice(), socksAddressIPv4, 8080)); err != nil {
		t.Fatal(err)
	}
	if code := readSocksReply(t, conn); code != socksSucceeded {
		t.Fatalf("CONNECT reply = %d, want success", code)
	}
	select {
	case got := <-backend.connected:
		if got.String() != "10.88.0.2:8080" {
			t.Fatalf("backend connected to %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("backend connect was not called")
	}
	if _, err := conn.Write([]byte("hello tunnel")); err != nil {
		t.Fatal(err)
	}
	echo := make([]byte, len("hello tunnel"))
	if _, err := io.ReadFull(conn, echo); err != nil || string(echo) != "hello tunnel" {
		t.Fatalf("echo = %q, %v", echo, err)
	}
}

func TestSocksConnectResolvesMemberNames(t *testing.T) {
	backend := newStubSocksBackend()
	conn := startSocksTestServer(t, backend)
	socksHandshake(t, conn)
	request := socksConnectRequest(socksCommandTCP, append([]byte{8}, []byte("member-b")...), socksAddressName, 22)
	if _, err := conn.Write(request); err != nil {
		t.Fatal(err)
	}
	if code := readSocksReply(t, conn); code != socksSucceeded {
		t.Fatalf("CONNECT reply = %d, want success", code)
	}
	select {
	case got := <-backend.connected:
		if got != netip.MustParseAddrPort("10.88.0.2:22") {
			t.Fatalf("backend connected to %s", got)
		}
	case <-time.After(time.Second):
		t.Fatal("backend connect was not called")
	}
}

func TestSocksConnectFailureCodes(t *testing.T) {
	for _, tc := range []struct {
		name       string
		connectErr error
		address    []byte
		atyp       byte
		want       byte
	}{
		{"rules", errNotAllowed, netip.MustParseAddr("10.88.0.2").AsSlice(), socksAddressIPv4, socksNotAllowed},
		{"refused", errRefused, netip.MustParseAddr("10.88.0.2").AsSlice(), socksAddressIPv4, socksConnectionRefus},
		{"unreachable", errUnreachable, netip.MustParseAddr("10.88.0.2").AsSlice(), socksAddressIPv4, socksHostUnreach},
		{"timeout", errStreamTimeout, netip.MustParseAddr("10.88.0.2").AsSlice(), socksAddressIPv4, socksHostUnreach},
		{"unknown_member", nil, append([]byte{8}, []byte("member-b")...), socksAddressName, socksHostUnreach},
	} {
		t.Run(tc.name, func(t *testing.T) {
			backend := newStubSocksBackend()
			if tc.name == "unknown_member" {
				backend.members = map[string]netip.Addr{}
			} else {
				backend.connectErr = tc.connectErr
			}
			conn := startSocksTestServer(t, backend)
			socksHandshake(t, conn)
			if _, err := conn.Write(socksConnectRequest(socksCommandTCP, tc.address, tc.atyp, 80)); err != nil {
				t.Fatal(err)
			}
			if code := readSocksReply(t, conn); code != tc.want {
				t.Fatalf("CONNECT reply = %d, want %d", code, tc.want)
			}
		})
	}
}

func TestSocksUnsupportedCommands(t *testing.T) {
	backend := newStubSocksBackend()
	conn := startSocksTestServer(t, backend)
	socksHandshake(t, conn)
	if _, err := conn.Write(socksConnectRequest(socksCommandBind, netip.MustParseAddr("10.88.0.2").AsSlice(), socksAddressIPv4, 80)); err != nil {
		t.Fatal(err)
	}
	if code := readSocksReply(t, conn); code != socksCommandInvalid {
		t.Fatalf("BIND reply = %d, want %d", code, socksCommandInvalid)
	}

	conn = startSocksTestServer(t, backend)
	socksHandshake(t, conn)
	if _, err := conn.Write(socksConnectRequest(socksCommandTCP, make([]byte, 16), socksAddressIPv6, 80)); err != nil {
		t.Fatal(err)
	}
	if code := readSocksReply(t, conn); code != socksAddressInvalid {
		t.Fatalf("IPv6 reply = %d, want %d", code, socksAddressInvalid)
	}
}

func TestSocksRejectsUnknownAuthMethod(t *testing.T) {
	backend := newStubSocksBackend()
	conn := startSocksTestServer(t, backend)
	if _, err := conn.Write([]byte{socksVersion, 1, 0x02}); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != socksNoAcceptable {
		t.Fatalf("method reply = %x, %v", reply, err)
	}
}

func TestSocksUDPAssociateRoundTrip(t *testing.T) {
	backend := newStubSocksBackend()
	conn := startSocksTestServer(t, backend)
	socksHandshake(t, conn)
	if _, err := conn.Write(socksConnectRequest(socksCommandUDP, []byte{0, 0, 0, 0}, socksAddressIPv4, 0)); err != nil {
		t.Fatal(err)
	}
	reply := make([]byte, 10)
	if _, err := io.ReadFull(conn, reply); err != nil || reply[1] != socksSucceeded {
		t.Fatalf("UDP ASSOCIATE reply = %x, %v", reply, err)
	}
	relay := netip.AddrPortFrom(netip.AddrFrom4([4]byte(reply[4:8])), binary.BigEndian.Uint16(reply[8:10]))
	if !relay.Addr().IsLoopback() || relay.Port() == 0 {
		t.Fatalf("unexpected relay address %s", relay)
	}
	socket, err := net.Dial("udp", relay.String())
	if err != nil {
		t.Fatal(err)
	}
	defer socket.Close()
	_ = socket.SetDeadline(time.Now().Add(5 * time.Second))

	request := formatSocksUDPResponse(netip.MustParseAddr("10.88.0.2"), 5353, []byte("question"))
	if _, err := socket.Write(request); err != nil {
		t.Fatal(err)
	}
	var forwarded forwardedDatagram
	select {
	case forwarded = <-backend.forwarded:
	case <-time.After(time.Second):
		t.Fatal("backend did not receive the forwarded datagram")
	}
	if forwarded.dst != netip.MustParseAddr("10.88.0.2") || forwarded.port != 5353 || string(forwarded.payload) != "question" || forwarded.client == nil {
		t.Fatalf("unexpected forwarded datagram: %+v", forwarded)
	}
	forwarded.sink.deliver(forwarded.client, netip.MustParseAddr("10.88.0.2"), 5353, []byte("answer"))
	response := make([]byte, 128)
	n, err := socket.Read(response)
	if err != nil {
		t.Fatal(err)
	}
	parsed, payload, err := parseSocksUDPRequest(response[:n])
	if err != nil || parsed.address != "10.88.0.2" || parsed.port != 5353 || string(payload) != "answer" {
		t.Fatalf("unexpected response: %+v %q %v", parsed, payload, err)
	}

	fragmented := formatSocksUDPResponse(netip.MustParseAddr("10.88.0.2"), 5353, []byte("fragment"))
	fragmented[2] = 1
	if _, err := socket.Write(fragmented); err != nil {
		t.Fatal(err)
	}
	select {
	case datagram := <-backend.forwarded:
		t.Fatalf("forwarded a fragmented datagram: %+v", datagram)
	case <-time.After(200 * time.Millisecond):
	}

	conn.Close()
	select {
	case <-backend.released:
	case <-time.After(2 * time.Second):
		t.Fatal("closing the TCP control connection did not release the UDP association")
	}
}

func TestSocksUDPCodecs(t *testing.T) {
	request, payload, err := parseSocksUDPRequest(formatSocksUDPResponse(netip.MustParseAddr("10.88.0.2"), 8080, []byte("data")))
	if err != nil || request.atyp != socksAddressIPv4 || request.address != "10.88.0.2" || request.port != 8080 || string(payload) != "data" {
		t.Fatalf("IPv4 datagram round trip failed: %+v %q %v", request, payload, err)
	}
	name := append([]byte{0, 0, 0, socksAddressName, 3}, []byte("abc")...)
	name = append(name, 0x1f, 0x90, 'p', 'a', 'y')
	request, payload, err = parseSocksUDPRequest(name)
	if err != nil || request.address != "abc" || request.port != 8080 || string(payload) != "pay" {
		t.Fatalf("name datagram failed: %+v %q %v", request, payload, err)
	}
	for _, invalid := range [][]byte{
		nil,
		{0, 0, 1, 1, 1, 2, 3, 4, 0, 53},
		{0, 1, 0, 1, 1, 2, 3, 4, 0, 53},
		{0, 0, 0, 1, 1, 2, 3},
		{0, 0, 0, socksAddressIPv6, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 53},
		{0, 0, 0, socksAddressName, 5, 'a', 'b'},
	} {
		if _, _, err := parseSocksUDPRequest(invalid); err == nil {
			t.Fatalf("accepted invalid SOCKS UDP datagram %x", invalid)
		}
	}
	if code := socksReplyFor(fmt.Errorf("wrapped: %w", errNotAllowed)); code != socksNotAllowed {
		t.Fatalf("wrapped rule error mapped to %d", code)
	}
	if code := socksReplyFor(context.DeadlineExceeded); code != socksGeneralFailure {
		t.Fatalf("unknown error mapped to %d", code)
	}
}

func TestSocksRequestParsing(t *testing.T) {
	request, err := readSocksRequest(bytes.NewReader(socksConnectRequest(socksCommandTCP, netip.MustParseAddr("10.88.0.2").AsSlice(), socksAddressIPv4, 443)))
	if err != nil || request.command != socksCommandTCP || request.address != "10.88.0.2" || request.port != 443 {
		t.Fatalf("IPv4 request = %+v, %v", request, err)
	}
	request, err = readSocksRequest(bytes.NewReader(socksConnectRequest(socksCommandUDP, append([]byte{9}, []byte("member-bx")...), socksAddressName, 53)))
	if err != nil || request.address != "member-bx" || request.port != 53 {
		t.Fatalf("name request = %+v, %v", request, err)
	}
	for _, invalid := range [][]byte{
		nil,
		{4, 1, 0, 1, 1, 2, 3, 4, 0, 80},
		{5, 1, 0, 1, 1, 2},
		{5, 1, 0, socksAddressIPv6, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 80},
		{5, 1, 0, 9, 0, 80},
	} {
		if _, err := readSocksRequest(bytes.NewReader(invalid)); err == nil {
			t.Fatalf("accepted invalid SOCKS request %x", invalid)
		}
	}
	var reply bytes.Buffer
	if err := writeSocksReply(&reply, socksSucceeded, netip.MustParseAddrPort("127.0.0.1:1080")); err != nil {
		t.Fatal(err)
	}
	want := []byte{5, 0, 0, 1, 127, 0, 0, 1, 4, 56}
	if !bytes.Equal(reply.Bytes(), want) {
		t.Fatalf("reply = %x, want %x", reply.Bytes(), want)
	}
	reply.Reset()
	_ = writeSocksReply(&reply, socksHostUnreach)
	if !bytes.Equal(reply.Bytes(), []byte{5, 4, 0, 1, 0, 0, 0, 0, 0, 0}) {
		t.Fatalf("failure reply = %x", reply.Bytes())
	}
}
