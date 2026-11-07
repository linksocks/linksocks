package zone

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	socksVersion = 5

	socksNoAuth       = 0x00
	socksNoAcceptable = 0xff
	socksCommandTCP   = 0x01
	socksCommandBind  = 0x02
	socksCommandUDP   = 0x03
	socksAddressIPv4  = 0x01
	socksAddressName  = 0x03
	socksAddressIPv6  = 0x04

	socksSucceeded       = 0x00
	socksGeneralFailure  = 0x01
	socksNotAllowed      = 0x02
	socksNetworkUnreach  = 0x03
	socksHostUnreach     = 0x04
	socksConnectionRefus = 0x05
	socksCommandInvalid  = 0x07
	socksAddressInvalid  = 0x08

	socksHandshakeTimeout = 10 * time.Second
)

var (
	errUnreachable = errors.New("zone member is not reachable")
	errNotAllowed  = errors.New("zone firewall denied the connection")
	errRefused     = errors.New("zone member refused the connection")
)

// socksBackend is the zone connection side of the SOCKS5 server.
type socksBackend interface {
	// resolveMember maps a member name to its virtual IPv4 address.
	resolveMember(name string) (netip.Addr, bool)
	// connect opens a zone stream to dst:port and hands it the local client
	// connection. It returns after ACCEPT; wait blocks until the stream ends.
	connect(ctx context.Context, dst netip.Addr, port uint16, client net.Conn) (wait func(), err error)
	// forwardDatagram tunnels one datagram; the sink receives the replies.
	forwardDatagram(sink udpSink, client *net.UDPAddr, dst netip.Addr, port uint16, payload []byte)
	// releaseUDPClient drops VPN NAT state owned by a closed UDP association.
	releaseUDPClient(sink udpSink)
}

type socksServer struct {
	ctx      context.Context
	backend  socksBackend
	listener net.Listener

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
	udp    map[*socksUDP]struct{}
}

func newSocksServer(ctx context.Context, backend socksBackend, port int) (*socksServer, error) {
	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}
	return &socksServer{
		ctx: ctx, backend: backend, listener: listener,
		conns: make(map[net.Conn]struct{}), udp: make(map[*socksUDP]struct{}),
	}, nil
}

func (s *socksServer) addr() string {
	return s.listener.Addr().String()
}

func (s *socksServer) serve() {
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return
		}
		if !s.trackConn(conn) {
			conn.Close()
			return
		}
		go s.handle(conn)
	}
}

func (s *socksServer) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for conn := range s.conns {
		conns = append(conns, conn)
	}
	associations := make([]*socksUDP, 0, len(s.udp))
	for association := range s.udp {
		associations = append(associations, association)
	}
	s.mu.Unlock()
	_ = s.listener.Close()
	for _, conn := range conns {
		conn.Close()
	}
	for _, association := range associations {
		association.close()
	}
}

func (s *socksServer) trackConn(conn net.Conn) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.conns[conn] = struct{}{}
	return true
}

func (s *socksServer) untrackConn(conn net.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.conns, conn)
}

func (s *socksServer) track(association *socksUDP) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return false
	}
	s.udp[association] = struct{}{}
	return true
}

func (s *socksServer) untrack(association *socksUDP) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.udp, association)
}

func (s *socksServer) handle(conn net.Conn) {
	defer s.untrackConn(conn)
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(socksHandshakeTimeout))
	if err := readSocksGreeting(conn); err != nil {
		if errors.Is(err, errSocksNoMethod) {
			_, _ = conn.Write([]byte{socksVersion, socksNoAcceptable})
		}
		return
	}
	if _, err := conn.Write([]byte{socksVersion, socksNoAuth}); err != nil {
		return
	}
	request, err := readSocksRequest(conn)
	if err != nil {
		code := byte(socksGeneralFailure)
		if errors.Is(err, errSocksAddress) {
			code = socksAddressInvalid
		}
		_ = writeSocksReply(conn, code)
		return
	}
	_ = conn.SetDeadline(time.Time{})
	switch request.command {
	case socksCommandTCP:
		s.handleConnect(conn, request)
	case socksCommandUDP:
		s.handleAssociate(conn)
	case socksCommandBind:
		_ = writeSocksReply(conn, socksCommandInvalid)
	default:
		_ = writeSocksReply(conn, socksCommandInvalid)
	}
}

func (s *socksServer) handleConnect(conn net.Conn, request socksRequest) {
	dst, ok := s.resolve(request)
	if !ok {
		_ = writeSocksReply(conn, socksHostUnreach)
		return
	}
	wait, err := s.backend.connect(s.ctx, dst, request.port, conn)
	if err != nil {
		_ = writeSocksReply(conn, socksReplyFor(err))
		return
	}
	if err := writeSocksReply(conn, socksSucceeded); err != nil {
		conn.Close()
		return
	}
	wait()
}

func (s *socksServer) handleAssociate(conn net.Conn) {
	socket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		_ = writeSocksReply(conn, socksGeneralFailure)
		return
	}
	association := &socksUDP{server: s, socket: socket}
	if !s.track(association) {
		socket.Close()
		return
	}
	defer s.untrack(association)
	defer association.close()
	bound := socket.LocalAddr().(*net.UDPAddr)
	if err := writeSocksReply(conn, socksSucceeded, bound.AddrPort()); err != nil {
		return
	}
	go association.readLoop()
	_, _ = io.Copy(io.Discard, conn)
}

func (s *socksServer) resolve(request socksRequest) (netip.Addr, bool) {
	switch request.atyp {
	case socksAddressIPv4:
		addr, err := netip.ParseAddr(request.address)
		return addr, err == nil
	case socksAddressName:
		if addr, err := netip.ParseAddr(request.address); err == nil && addr.Is4() {
			return addr, true
		}
		return s.backend.resolveMember(request.address)
	default:
		return netip.Addr{}, false
	}
}

func socksReplyFor(err error) byte {
	switch {
	case errors.Is(err, errNotAllowed):
		return socksNotAllowed
	case errors.Is(err, errUnreachable), errors.Is(err, errStreamTimeout):
		return socksHostUnreach
	case errors.Is(err, errRefused):
		return socksConnectionRefus
	default:
		return socksGeneralFailure
	}
}

func readSocksGreeting(r io.Reader) error {
	var header [2]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	if header[0] != socksVersion {
		return errors.New("unsupported SOCKS version")
	}
	methods := make([]byte, int(header[1]))
	if _, err := io.ReadFull(r, methods); err != nil {
		return err
	}
	for _, method := range methods {
		if method == socksNoAuth {
			return nil
		}
	}
	return errSocksNoMethod
}

var errSocksNoMethod = errors.New("no acceptable SOCKS authentication method")

type socksRequest struct {
	command byte
	atyp    byte
	address string
	port    uint16
}

var errSocksAddress = errors.New("unsupported SOCKS address type")

func readSocksRequest(r io.Reader) (socksRequest, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return socksRequest{}, err
	}
	if header[0] != socksVersion {
		return socksRequest{}, errors.New("unsupported SOCKS version")
	}
	request := socksRequest{command: header[1], atyp: header[3]}
	switch header[3] {
	case socksAddressIPv4:
		var raw [4]byte
		if _, err := io.ReadFull(r, raw[:]); err != nil {
			return socksRequest{}, err
		}
		request.address = netip.AddrFrom4(raw).String()
	case socksAddressName:
		var length [1]byte
		if _, err := io.ReadFull(r, length[:]); err != nil {
			return socksRequest{}, err
		}
		name := make([]byte, int(length[0]))
		if _, err := io.ReadFull(r, name); err != nil {
			return socksRequest{}, err
		}
		request.address = string(name)
	case socksAddressIPv6:
		return socksRequest{}, errSocksAddress
	default:
		return socksRequest{}, errSocksAddress
	}
	var port [2]byte
	if _, err := io.ReadFull(r, port[:]); err != nil {
		return socksRequest{}, err
	}
	request.port = binary.BigEndian.Uint16(port[:])
	return request, nil
}

func writeSocksReply(w io.Writer, code byte, bound ...netip.AddrPort) error {
	message := []byte{socksVersion, code, 0, socksAddressIPv4, 0, 0, 0, 0, 0, 0}
	if code == socksSucceeded && len(bound) == 1 {
		copy(message[4:8], bound[0].Addr().AsSlice())
		binary.BigEndian.PutUint16(message[8:10], bound[0].Port())
	}
	_, err := w.Write(message)
	return err
}

// parseSocksUDPRequest splits one SOCKS5 UDP request datagram into its
// destination and payload. Fragmented datagrams are rejected.
func parseSocksUDPRequest(packet []byte) (socksRequest, []byte, error) {
	if len(packet) < 4 || packet[0] != 0 || packet[1] != 0 || packet[2] != 0 {
		return socksRequest{}, nil, errors.New("invalid SOCKS UDP datagram")
	}
	request := socksRequest{atyp: packet[3]}
	switch packet[3] {
	case socksAddressIPv4:
		if len(packet) < 10 {
			return socksRequest{}, nil, errors.New("short SOCKS UDP datagram")
		}
		request.address = netip.AddrFrom4([4]byte(packet[4:8])).String()
		request.port = binary.BigEndian.Uint16(packet[8:10])
		return request, packet[10:], nil
	case socksAddressName:
		if len(packet) < 7 {
			return socksRequest{}, nil, errors.New("short SOCKS UDP datagram")
		}
		length := int(packet[4])
		if len(packet) < 7+length {
			return socksRequest{}, nil, errors.New("short SOCKS UDP datagram")
		}
		request.address = string(packet[5 : 5+length])
		request.port = binary.BigEndian.Uint16(packet[5+length : 7+length])
		return request, packet[7+length:], nil
	default:
		return socksRequest{}, nil, errSocksAddress
	}
}

func formatSocksUDPResponse(src netip.Addr, srcPort uint16, payload []byte) []byte {
	packet := make([]byte, 10+len(payload))
	packet[3] = socksAddressIPv4
	copy(packet[4:8], src.AsSlice())
	binary.BigEndian.PutUint16(packet[8:10], srcPort)
	copy(packet[10:], payload)
	return packet
}

type socksUDP struct {
	server *socksServer
	socket *net.UDPConn
}

func (u *socksUDP) readLoop() {
	buffer := make([]byte, 65535)
	for {
		n, client, err := u.socket.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		request, payload, err := parseSocksUDPRequest(buffer[:n])
		if err != nil {
			continue
		}
		dst, ok := u.server.resolve(request)
		if !ok {
			continue
		}
		quoted := make([]byte, len(payload))
		copy(quoted, payload)
		u.server.backend.forwardDatagram(u, client, dst, request.port, quoted)
	}
}

func (u *socksUDP) deliver(client *net.UDPAddr, src netip.Addr, srcPort uint16, payload []byte) {
	_, _ = u.socket.WriteToUDP(formatSocksUDPResponse(src, srcPort, payload), client)
}

func (u *socksUDP) close() {
	_ = u.socket.Close()
	u.server.backend.releaseUDPClient(u)
}
