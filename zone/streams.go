package zone

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"time"
)

const (
	// streamQueueLimit bounds the bytes queued toward one local connection.
	// When the queue is full the WebSocket read loop blocks, which pushes
	// backpressure through the relay to the sending member.
	streamQueueLimit  = 512 * 1024
	datagramIdleTTL   = 60 * time.Second
	udpReapInterval   = 15 * time.Second
	streamDialWait    = 10 * time.Second
	streamOpenTimeout = 30 * time.Second
)

type frameQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	chunks [][]byte
	bytes  int
	closed bool
	limit  int
}

func newFrameQueue(limit int) *frameQueue {
	q := &frameQueue{limit: limit}
	q.cond = sync.NewCond(&q.mu)
	return q
}

func (q *frameQueue) push(payload []byte) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for q.bytes >= q.limit && !q.closed {
		q.cond.Wait()
	}
	if q.closed {
		return false
	}
	q.chunks = append(q.chunks, payload)
	q.bytes += len(payload)
	q.cond.Broadcast()
	return true
}

func (q *frameQueue) pop() ([]byte, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.chunks) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.chunks) == 0 {
		return nil, false
	}
	payload := q.chunks[0]
	q.chunks = q.chunks[1:]
	q.bytes -= len(payload)
	q.cond.Broadcast()
	return payload, true
}

func (q *frameQueue) close() {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
}

type zoneStream struct {
	conn     *connection
	id       uint32
	peer     *peerCipher
	queue    *frameQueue
	ready    chan uint16
	finished chan struct{}

	mu           sync.Mutex
	local        net.Conn
	localClosed  bool
	remoteClosed bool
	done         bool
}

type streamTable struct {
	mu      sync.Mutex
	streams map[uint32]*zoneStream
	next    uint32
}

func newStreamTable() *streamTable {
	var seed [4]byte
	_, _ = rand.Read(seed[:])
	return &streamTable{streams: make(map[uint32]*zoneStream), next: binary.BigEndian.Uint32(seed[:])}
}

func (t *streamTable) allocate(c *connection, peer *peerCipher, outbound bool) *zoneStream {
	t.mu.Lock()
	for {
		t.next++
		if t.next == 0 || t.streams[t.next] != nil {
			continue
		}
		break
	}
	stream := t.newStream(t.next, c, peer, outbound)
	t.mu.Unlock()
	return stream
}

// insert registers a stream under the peer's own stream ID for inbound OPEN.
func (t *streamTable) insert(id uint32, c *connection, peer *peerCipher) (*zoneStream, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if id == 0 || t.streams[id] != nil {
		return nil, false
	}
	return t.newStream(id, c, peer, false), true
}

// newStream constructs the stream; the caller holds t.mu.
func (t *streamTable) newStream(id uint32, c *connection, peer *peerCipher, outbound bool) *zoneStream {
	s := &zoneStream{conn: c, id: id, peer: peer, queue: newFrameQueue(streamQueueLimit), finished: make(chan struct{})}
	if outbound {
		s.ready = make(chan uint16, 1)
	}
	t.streams[s.id] = s
	return s
}

func (t *streamTable) get(id uint32) *zoneStream {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.streams[id]
}

func (t *streamTable) discard(s *zoneStream) {
	t.mu.Lock()
	if t.streams[s.id] == s {
		delete(t.streams, s.id)
	}
	t.mu.Unlock()
	s.release()
}

func (t *streamTable) each() []*zoneStream {
	t.mu.Lock()
	defer t.mu.Unlock()
	streams := make([]*zoneStream, 0, len(t.streams))
	for _, s := range t.streams {
		streams = append(streams, s)
	}
	return streams
}

// abort tears the stream down, unblocking both piping directions exactly once.
func (s *zoneStream) abort() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	local := s.local
	s.mu.Unlock()
	s.queue.close()
	if local != nil {
		local.Close()
	}
	s.conn.streams.discard(s)
	close(s.finished)
}

// release only closes the stream's data structures without touching the
// stream table, for callers that already removed the entry.
func (s *zoneStream) release() {
	s.mu.Lock()
	if s.done {
		s.mu.Unlock()
		return
	}
	s.done = true
	local := s.local
	s.mu.Unlock()
	s.queue.close()
	if local != nil {
		local.Close()
	}
	close(s.finished)
}

func (s *zoneStream) wait() {
	<-s.finished
}

// attach binds the local TCP side and starts both directions.
func (s *zoneStream) attach(local net.Conn) {
	s.mu.Lock()
	s.local = local
	s.mu.Unlock()
	go s.writeLoop()
	go s.readLoop()
}

// enqueue queues payload for the local connection. It blocks while the
// per-stream write queue is full, which is the intended backpressure path.
func (s *zoneStream) enqueue(payload []byte) {
	s.queue.push(payload)
}

func (s *zoneStream) writeLoop() {
	for {
		chunk, ok := s.queue.pop()
		if !ok {
			return
		}
		if _, err := s.local.Write(chunk); err != nil {
			s.fail()
			return
		}
	}
}

func (s *zoneStream) readLoop() {
	buffer := make([]byte, maxStreamData)
	for {
		n, err := s.local.Read(buffer)
		if n > 0 {
			payload := make([]byte, n)
			copy(payload, buffer[:n])
			sendErr := s.conn.send(func() ([][]byte, error) {
				message, err := s.conn.cipher.sealStreamData(s.peer, s.id, payload)
				if err != nil {
					return nil, err
				}
				return [][]byte{message}, nil
			})
			if sendErr != nil {
				s.abort()
				return
			}
		}
		if err != nil {
			s.mu.Lock()
			s.localClosed = true
			closed := s.remoteClosed
			s.mu.Unlock()
			if err == io.EOF {
				_ = s.conn.sendPlain(closeMessage(s.id))
			} else {
				_ = s.conn.sendPlain(resetMessage(s.id, 1))
				s.abort()
				return
			}
			if closed {
				s.abort()
			}
			return
		}
	}
}

// remoteClose handles a peer CLOSE: the remote write direction ended, so the
// local connection is half-closed and the queued data still drains.
func (s *zoneStream) remoteClose() {
	s.mu.Lock()
	if s.done || s.remoteClosed {
		s.mu.Unlock()
		return
	}
	s.remoteClosed = true
	local := s.local
	closed := s.localClosed
	s.mu.Unlock()
	s.queue.close()
	if local != nil {
		if half, ok := local.(interface{ CloseWrite() error }); ok {
			_ = half.CloseWrite()
		}
	}
	if closed {
		s.abort()
	}
}

// fail aborts the stream and tells the peer about the local failure.
func (s *zoneStream) fail() {
	_ = s.conn.sendPlain(resetMessage(s.id, 1))
	s.abort()
}

type udpSink interface {
	deliver(client *net.UDPAddr, src netip.Addr, srcPort uint16, payload []byte)
}

type udpClientKey struct {
	peer netip.Addr
	port uint16
}

type udpClient struct {
	client   *net.UDPAddr
	sink     udpSink
	lastSeen time.Time
}

type udpServiceKey struct {
	peer netip.Addr
	port uint16
}

type udpService struct {
	conn     *net.UDPConn
	peer     netip.Addr
	peerPort uint16
	lastSeen time.Time
}

// forwardDatagram tunnels one SOCKS UDP payload and records the NAT mapping
// used to route replies back to the client socket.
func (c *connection) forwardDatagram(sink udpSink, client *net.UDPAddr, dst netip.Addr, port uint16, payload []byte) {
	if c.cipher.peerAddr(dst) == nil {
		return
	}
	if !c.ruleAllowed(flow{src: c.self(), dst: dst, proto: "udp", srcPort: uint16(client.Port), dstPort: port, srcPortKnown: true, dstPortKnown: true}) {
		return
	}
	sourcePort := uint16(client.Port)
	err := c.send(func() ([][]byte, error) {
		message, err := c.cipher.sealDatagram(dst, sourcePort, port, payload)
		if err != nil {
			return nil, err
		}
		return [][]byte{message}, nil
	})
	if err != nil {
		return
	}
	c.udpMu.Lock()
	c.udpClients[udpClientKey{peer: dst, port: port}] = &udpClient{client: client, sink: sink, lastSeen: time.Now()}
	c.udpMu.Unlock()
}

// deliverDatagram routes one decrypted datagram either to the SOCKS client
// that owns the NAT mapping or to a local service socket.
func (c *connection) deliverDatagram(d datagram) {
	if !c.ruleAllowed(flow{src: d.src, dst: d.dst, proto: "udp", srcPort: d.srcPort, dstPort: d.dstPort, srcPortKnown: true, dstPortKnown: true}) {
		return
	}
	now := time.Now()
	c.udpMu.Lock()
	if entry := c.udpClients[udpClientKey{peer: d.src, port: d.srcPort}]; entry != nil {
		entry.lastSeen = now
		client, sink := entry.client, entry.sink
		c.udpMu.Unlock()
		sink.deliver(client, d.src, d.srcPort, d.payload)
		return
	}
	service := c.udpServices[udpServiceKey{peer: d.src, port: d.srcPort}]
	if service == nil {
		socket, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		if err != nil {
			c.udpMu.Unlock()
			return
		}
		service = &udpService{conn: socket, peer: d.src, peerPort: d.srcPort, lastSeen: now}
		c.udpServices[udpServiceKey{peer: d.src, port: d.srcPort}] = service
		go c.readServiceDatagrams(service)
	}
	service.lastSeen = now
	socket := service.conn
	c.udpMu.Unlock()
	_, _ = socket.WriteToUDP(d.payload, &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: int(d.dstPort)})
}

func (c *connection) readServiceDatagrams(service *udpService) {
	buffer := make([]byte, 65535)
	for {
		n, from, err := service.conn.ReadFromUDP(buffer)
		if err != nil {
			return
		}
		sourcePort := uint16(from.Port)
		if !c.ruleAllowed(flow{src: c.self(), dst: service.peer, proto: "udp", srcPort: sourcePort, dstPort: service.peerPort, srcPortKnown: true, dstPortKnown: true}) {
			continue
		}
		payload := make([]byte, n)
		copy(payload, buffer[:n])
		err = c.send(func() ([][]byte, error) {
			message, err := c.cipher.sealDatagram(service.peer, sourcePort, service.peerPort, payload)
			if err != nil {
				return nil, err
			}
			return [][]byte{message}, nil
		})
		if err != nil {
			return
		}
	}
}

func (c *connection) releaseUDPClient(sink udpSink) {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	for key, entry := range c.udpClients {
		if entry.sink == sink {
			delete(c.udpClients, key)
		}
	}
}

func (c *connection) reapDatagrams() {
	now := time.Now()
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	for key, entry := range c.udpClients {
		if now.Sub(entry.lastSeen) > datagramIdleTTL {
			delete(c.udpClients, key)
		}
	}
	for key, service := range c.udpServices {
		if now.Sub(service.lastSeen) > datagramIdleTTL {
			delete(c.udpServices, key)
			service.conn.Close()
		}
	}
}

func (c *connection) closeDatagrams() {
	c.udpMu.Lock()
	defer c.udpMu.Unlock()
	for key, service := range c.udpServices {
		delete(c.udpServices, key)
		service.conn.Close()
	}
	c.udpClients = make(map[udpClientKey]*udpClient)
}

var errStreamTimeout = errors.New("zone stream open timed out")
