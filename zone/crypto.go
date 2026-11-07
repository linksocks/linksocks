package zone

import (
	"bytes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/netip"
	"sync"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

const macProtocol = "linksocks-zone-mac-v1"

type peerCipher struct {
	peer
	mac     [6]byte
	session [16]byte
	send    cipher.AEAD
	receive cipher.AEAD
	last    uint64
}

type packetCipher struct {
	mu      sync.Mutex
	zoneID  string
	self    netip.Addr
	prefix  netip.Prefix
	mtu     int
	key     string
	mac     [6]byte
	private *ecdh.PrivateKey
	session [16]byte
	next    uint64
	peers   map[netip.Addr]*peerCipher
	macs    map[[6]byte]*peerCipher
}

func deriveMAC(zoneID string, identity []byte) [6]byte {
	sum := sha256.Sum256(append(append([]byte(macProtocol), zoneID...), identity...))
	var mac [6]byte
	copy(mac[:], sum[:6])
	mac[0] = mac[0]&^0x01 | 0x02
	return mac
}

func newPacketCipher(id string, initial control, private *ecdh.PrivateKey, session string, identity []byte) (*packetCipher, error) {
	prefix, self, err := validateNetwork(initial.Zone, id, initial.Self)
	if err != nil {
		return nil, err
	}
	nonce, err := decodeHex(session, 16)
	if err != nil {
		return nil, err
	}
	c := &packetCipher{zoneID: id, self: self, prefix: prefix, mtu: initial.Zone.MTU, private: private, key: hex.EncodeToString(private.PublicKey().Bytes())}
	c.mac = deriveMAC(id, identity)
	copy(c.session[:], nonce)
	return c, c.update(initial)
}

func (c *packetCipher) update(roster control) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	prefix, self, err := validateNetwork(roster.Zone, c.zoneID, roster.Self)
	if err != nil || self != c.self || prefix != c.prefix || roster.Zone.MTU != c.mtu || len(roster.Peers) > 255 {
		return errors.New("invalid zone roster")
	}
	peers := make(map[netip.Addr]*peerCipher, len(roster.Peers))
	macs := make(map[[6]byte]*peerCipher, len(roster.Peers))
	identities := make(map[string]bool, len(roster.Peers))
	keys := make(map[string]bool, len(roster.Peers))
	for _, p := range roster.Peers {
		ip, err := netip.ParseAddr(p.IP)
		if err != nil || !ip.Is4() || !prefix.Contains(ip) || ip == self || ip == prefix.Addr() || ip == broadcast(prefix) || peers[ip] != nil || identities[p.Identity] || keys[p.Key] {
			return errors.New("invalid peer address in zone roster")
		}
		identityBytes, err := decodeHex(p.Identity, 32)
		if err != nil {
			return err
		}
		keyBytes, err := decodeHex(p.Key, 32)
		if err != nil {
			return err
		}
		session, err := decodeHex(p.Session, 16)
		if err != nil {
			return err
		}
		mac := deriveMAC(c.zoneID, identityBytes)
		if macs[mac] != nil || mac == c.mac {
			return errors.New("duplicate peer MAC address in zone roster")
		}
		identities[p.Identity] = true
		keys[p.Key] = true
		if old := c.peers[ip]; old != nil && old.Identity == p.Identity && old.Key == p.Key && old.Session == p.Session {
			peers[ip] = old
			macs[mac] = old
			continue
		}
		public, err := ecdh.X25519().NewPublicKey(keyBytes)
		if err != nil {
			return err
		}
		shared, err := c.private.ECDH(public)
		if err != nil {
			return fmt.Errorf("invalid peer exchange key: %w", err)
		}
		send, err := derivePacketKey(shared, c.zoneID, c.key, p.Key)
		if err != nil {
			return err
		}
		receive, err := derivePacketKey(shared, c.zoneID, p.Key, c.key)
		if err != nil {
			return err
		}
		state := &peerCipher{peer: p, mac: mac, send: send, receive: receive}
		copy(state.session[:], session)
		peers[ip] = state
		macs[mac] = state
	}
	c.peers = peers
	c.macs = macs
	return nil
}

func derivePacketKey(shared []byte, zoneID, sender, receiver string) (cipher.AEAD, error) {
	key := make([]byte, chacha20poly1305.KeySize)
	info := protocol + "\n" + sender + "\n" + receiver
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, []byte(zoneID), []byte(info)), key); err != nil {
		return nil, err
	}
	return chacha20poly1305.NewX(key)
}

func (c *packetCipher) peerAddr(addr netip.Addr) *peerCipher {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peers[addr]
}

func (c *packetCipher) peerAlive(p *peerCipher) bool {
	if p == nil {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.peers[p.addr()] == p
}

func (p *peerCipher) addr() netip.Addr {
	addr, _ := netip.ParseAddr(p.IP)
	return addr
}

// seal allocates the next per-direction sequence number, writes it into the
// message header and seals the payload with the header as additional data.
// Callers serialize seal and send per connection so sequence numbers reach
// the wire in order. A non-zero addr re-verifies that peer is still current.
func (c *packetCipher) seal(peer *peerCipher, addr netip.Addr, header []byte, seqOffset int, payload []byte) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if addr.IsValid() && c.peers[addr] != peer {
		return nil, errors.New("zone peer state changed")
	}
	return c.sealLocked(peer, header, seqOffset, payload)
}

// sealLocked seals with the caller holding c.mu.
func (c *packetCipher) sealLocked(peer *peerCipher, header []byte, seqOffset int, payload []byte) ([]byte, error) {
	if c.next == math.MaxUint64 {
		return nil, errors.New("zone message sequence exhausted")
	}
	c.next++
	binary.BigEndian.PutUint64(header[seqOffset:], c.next)
	var nonce [24]byte
	copy(nonce[:16], c.session[:])
	copy(nonce[16:], header[seqOffset:])
	out := make([]byte, len(header), len(header)+len(payload)+chacha20poly1305.Overhead)
	copy(out, header)
	return peer.send.Seal(out, nonce[:], payload, header), nil
}

// open authenticates one message from a peer, updates the shared replay
// window and returns the plaintext. Failed messages, including ones that
// decrypt but fail validate, do not advance the sequence number. The check
// that peer is still current happens under the same lock as the sequence
// update.
func (c *packetCipher) open(peer *peerCipher, addr netip.Addr, header []byte, seqOffset int, payload []byte, validate func([]byte) error) ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peers[addr] != peer {
		return nil, errors.New("zone peer state changed")
	}
	seq := binary.BigEndian.Uint64(header[seqOffset:])
	if seq <= peer.last {
		return nil, errors.New("replayed or out-of-order zone message")
	}
	var nonce [24]byte
	copy(nonce[:16], peer.session[:])
	copy(nonce[16:], header[seqOffset:])
	plain, err := peer.receive.Open(nil, nonce[:], payload, header)
	if err != nil {
		return nil, err
	}
	if validate != nil {
		if err := validate(plain); err != nil {
			return nil, err
		}
	}
	peer.last = seq
	return plain, nil
}

// sealFrame seals one Ethernet frame once for every destination member:
// unicast for the learned owner of the destination MAC and one copy per peer
// for broadcast and multicast frames, which the relay floods.
func (c *packetCipher) sealFrame(frame []byte) ([][]byte, error) {
	if len(frame) < minFrameSize || len(frame) > maxFrameSize {
		return nil, errors.New("invalid Ethernet frame length")
	}
	var src, dst [6]byte
	copy(dst[:], frame[0:6])
	copy(src[:], frame[6:12])
	if src != c.mac {
		return nil, errors.New("Ethernet frame source MAC is not this member")
	}
	header := make([]byte, frameHeaderSize)
	header[0], header[1] = kindFrame, frameVersion
	copy(header[2:8], src[:])
	copy(header[8:14], dst[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	var targets []*peerCipher
	if dst == c.mac {
		return nil, errors.New("Ethernet frame is addressed to this member")
	} else if isGroupMAC(dst) {
		targets = make([]*peerCipher, 0, len(c.peers))
		for _, peer := range c.peers {
			targets = append(targets, peer)
		}
	} else if peer := c.macs[dst]; peer != nil {
		targets = append(targets, peer)
	} else {
		return nil, errors.New("Ethernet frame has no destination member")
	}
	messages := make([][]byte, 0, len(targets))
	for _, peer := range targets {
		message, err := c.sealLocked(peer, header, 14, frame)
		if err != nil {
			return nil, err
		}
		messages = append(messages, message)
	}
	return messages, nil
}

func (c *packetCipher) openFrame(raw []byte) (*peerCipher, []byte, error) {
	if len(raw) < frameHeaderSize+minFrameSize+chacha20poly1305.Overhead || len(raw) > frameHeaderSize+maxFrameSize+chacha20poly1305.Overhead || raw[0] != kindFrame || raw[1] != frameVersion {
		return nil, nil, errors.New("invalid zone frame message")
	}
	var src, dst [6]byte
	copy(src[:], raw[2:8])
	copy(dst[:], raw[8:14])
	c.mu.Lock()
	peer := c.macs[src]
	c.mu.Unlock()
	if peer == nil {
		return nil, nil, errors.New("zone frame from an unknown source MAC")
	}
	if dst != c.mac && !isGroupMAC(dst) {
		return nil, nil, errors.New("zone frame is not addressed to this member")
	}
	frame, err := c.open(peer, peer.addr(), raw[:frameHeaderSize], 14, raw[frameHeaderSize:], func(frame []byte) error {
		if len(frame) < minFrameSize || len(frame) > maxFrameSize || !bytes.Equal(frame[0:6], dst[:]) || !bytes.Equal(frame[6:12], src[:]) {
			return errors.New("zone frame inner MAC mismatch")
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return peer, frame, nil
}

func isGroupMAC(mac [6]byte) bool {
	return mac[0]&1 == 1
}

func openMessage(id uint32, src, dst netip.Addr, port uint16) []byte {
	message := make([]byte, 16)
	message[0], message[1] = kindStream, opOpen
	binary.BigEndian.PutUint32(message[2:6], id)
	copy(message[6:10], src.AsSlice())
	copy(message[10:14], dst.AsSlice())
	binary.BigEndian.PutUint16(message[14:16], port)
	return message
}

func acceptMessage(id uint32) []byte {
	message := make([]byte, 6)
	message[0], message[1] = kindStream, opAccept
	binary.BigEndian.PutUint32(message[2:6], id)
	return message
}

func refuseMessage(id uint32, code uint16, reason string) []byte {
	if len(reason) > maxStreamReason {
		reason = reason[:maxStreamReason]
	}
	message := make([]byte, 8+len(reason))
	message[0], message[1] = kindStream, opRefuse
	binary.BigEndian.PutUint32(message[2:6], id)
	binary.BigEndian.PutUint16(message[6:8], code)
	copy(message[8:], reason)
	return message
}

func closeMessage(id uint32) []byte {
	message := make([]byte, 6)
	message[0], message[1] = kindStream, opClose
	binary.BigEndian.PutUint32(message[2:6], id)
	return message
}

func resetMessage(id uint32, code uint16) []byte {
	message := make([]byte, 8)
	message[0], message[1] = kindStream, opReset
	binary.BigEndian.PutUint32(message[2:6], id)
	binary.BigEndian.PutUint16(message[6:8], code)
	return message
}

// streamOp splits a kind 2 message into its opcode, stream ID and remaining
// plaintext fields.
func streamOp(raw []byte) (byte, uint32, []byte, error) {
	if len(raw) < 6 || raw[0] != kindStream {
		return 0, 0, nil, errors.New("invalid zone stream message")
	}
	op := raw[1]
	if op < opOpen || op > opReset {
		return 0, 0, nil, errors.New("invalid zone stream opcode")
	}
	return op, binary.BigEndian.Uint32(raw[2:6]), raw[6:], nil
}

func (c *packetCipher) sealStreamData(peer *peerCipher, id uint32, payload []byte) ([]byte, error) {
	if len(payload) == 0 || len(payload) > maxStreamData {
		return nil, errors.New("invalid zone stream payload size")
	}
	header := make([]byte, streamDataHeader)
	header[0], header[1] = kindStream, opData
	binary.BigEndian.PutUint32(header[2:6], id)
	return c.seal(peer, peer.addr(), header, 6, payload)
}

func (c *packetCipher) openStreamData(peer *peerCipher, raw []byte) (uint32, []byte, error) {
	if len(raw) < streamDataHeader+chacha20poly1305.Overhead || len(raw) > streamDataHeader+maxStreamData+chacha20poly1305.Overhead || raw[0] != kindStream || raw[1] != opData {
		return 0, nil, errors.New("invalid zone stream data message")
	}
	payload, err := c.open(peer, peer.addr(), raw[:streamDataHeader], 6, raw[streamDataHeader:], func(payload []byte) error {
		if len(payload) == 0 || len(payload) > maxStreamData {
			return errors.New("invalid zone stream payload size")
		}
		return nil
	})
	if err != nil {
		return 0, nil, err
	}
	return binary.BigEndian.Uint32(raw[2:6]), payload, nil
}

func (c *packetCipher) sealDatagram(dst netip.Addr, srcPort, dstPort uint16, payload []byte) ([]byte, error) {
	c.mu.Lock()
	peer := c.peers[dst]
	self := c.self
	c.mu.Unlock()
	if peer == nil {
		return nil, errors.New("datagram has no matching zone peer")
	}
	header := make([]byte, datagramHeader)
	header[0] = kindDatagram
	copy(header[1:5], self.AsSlice())
	copy(header[5:9], dst.AsSlice())
	binary.BigEndian.PutUint16(header[9:11], srcPort)
	binary.BigEndian.PutUint16(header[11:13], dstPort)
	return c.seal(peer, dst, header, 13, payload)
}

type datagram struct {
	peer    *peerCipher
	src     netip.Addr
	dst     netip.Addr
	srcPort uint16
	dstPort uint16
	payload []byte
}

func (c *packetCipher) openDatagram(raw []byte) (datagram, error) {
	if len(raw) < datagramHeader+chacha20poly1305.Overhead || raw[0] != kindDatagram {
		return datagram{}, errors.New("invalid zone datagram message")
	}
	src := netip.AddrFrom4([4]byte(raw[1:5]))
	dst := netip.AddrFrom4([4]byte(raw[5:9]))
	c.mu.Lock()
	peer := c.peers[src]
	self := c.self
	c.mu.Unlock()
	if peer == nil || dst != self {
		return datagram{}, errors.New("zone datagram from an unknown address")
	}
	payload, err := c.open(peer, src, raw[:datagramHeader], 13, raw[datagramHeader:], nil)
	if err != nil {
		return datagram{}, err
	}
	return datagram{
		peer: peer, src: src, dst: dst,
		srcPort: binary.BigEndian.Uint16(raw[9:11]),
		dstPort: binary.BigEndian.Uint16(raw[11:13]),
		payload: payload,
	}, nil
}
