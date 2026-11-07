package zone

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"io"
	"math"
	"net/netip"
	"sort"
	"strings"
	"sync"
	"testing"

	"golang.org/x/crypto/chacha20poly1305"
	"golang.org/x/crypto/hkdf"
)

type packetTestNode struct {
	private *ecdh.PrivateKey
	peer    peer
}

func newPacketTestNode(t *testing.T, name, ip string) packetTestNode {
	t.Helper()
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	seed := sha256.Sum256([]byte(name))
	identity := ed25519.NewKeyFromSeed(seed[:])
	return packetTestNode{private: key, peer: peer{
		Identity: hex.EncodeToString(identity.Public().(ed25519.PublicKey)),
		Key:      hex.EncodeToString(key.PublicKey().Bytes()),
		Session:  hex.EncodeToString(seed[:16]),
		Name:     name,
		IP:       ip,
	}}
}

func packetTestRoster(id string, self packetTestNode, peers ...packetTestNode) control {
	roster := control{
		Type: "members",
		Zone: network{ID: id, Name: "Test network", CIDR: "10.88.0.0/24", MTU: mtu, MaxMembers: 256},
		Self: self.peer.IP,
	}
	for _, node := range peers {
		roster.Peers = append(roster.Peers, node.peer)
	}
	return roster
}

func newTestPacketCipher(t *testing.T, id string, self packetTestNode, peers ...packetTestNode) *packetCipher {
	t.Helper()
	c, err := newPacketCipher(id, packetTestRoster(id, self, peers...), self.private, self.peer.Session, self.mustIdentity())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func newPacketTestPair(t *testing.T) (*packetCipher, *packetCipher, packetTestNode, packetTestNode) {
	t.Helper()
	a := newPacketTestNode(t, "alice", "10.88.0.1")
	b := newPacketTestNode(t, "bob", "10.88.0.2")
	return newTestPacketCipher(t, "zone-one", a, b), newTestPacketCipher(t, "zone-one", b, a), a, b
}

func testEthernetFrame(src, dst [6]byte, ethertype uint16, payload []byte) []byte {
	frame := make([]byte, minFrameSize+len(payload))
	copy(frame[0:6], dst[:])
	copy(frame[6:12], src[:])
	binary.BigEndian.PutUint16(frame[12:14], ethertype)
	copy(frame[14:], payload)
	return frame
}

func testIPv4Packet(src, dst string, protocol byte, payload []byte) []byte {
	packet := make([]byte, 20+len(payload))
	packet[0] = 0x45
	binary.BigEndian.PutUint16(packet[2:4], uint16(len(packet)))
	packet[8], packet[9] = 64, protocol
	copy(packet[12:16], netip.MustParseAddr(src).AsSlice())
	copy(packet[16:20], netip.MustParseAddr(dst).AsSlice())
	copy(packet[20:], payload)
	var checksum uint32
	for i := 0; i < 20; i += 2 {
		checksum += uint32(binary.BigEndian.Uint16(packet[i : i+2]))
	}
	for checksum>>16 != 0 {
		checksum = checksum&0xffff + checksum>>16
	}
	binary.BigEndian.PutUint16(packet[10:12], ^uint16(checksum))
	return packet
}

func testTransportPacket(src, dst string, protocol byte, srcPort, dstPort uint16, payload []byte) []byte {
	transport := make([]byte, 4+len(payload))
	binary.BigEndian.PutUint16(transport[0:2], srcPort)
	binary.BigEndian.PutUint16(transport[2:4], dstPort)
	copy(transport[4:], payload)
	return testIPv4Packet(src, dst, protocol, transport)
}

func testZoneFrame(sender, receiver *packetCipher, packet []byte) []byte {
	return testEthernetFrame(sender.mac, receiver.mac, etherTypeIPv4, packet)
}

func sealTestFrame(t *testing.T, c *packetCipher, frame []byte) []byte {
	t.Helper()
	messages, err := c.sealFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 1 {
		t.Fatalf("unicast frame produced %d messages, want 1", len(messages))
	}
	return messages[0]
}

// authenticateRaw models a peer that encrypts arbitrary payloads with the
// connection keys but bypasses the structural checks of seal.
func authenticateRaw(c *packetCipher, dst netip.Addr, header []byte, seqOffset int, seq uint64, payload []byte) []byte {
	binary.BigEndian.PutUint64(header[seqOffset:], seq)
	var nonce [24]byte
	copy(nonce[:16], c.session[:])
	copy(nonce[16:], header[seqOffset:])
	out := make([]byte, len(header), len(header)+len(payload)+chacha20poly1305.Overhead)
	copy(out, header)
	return c.peers[dst].send.Seal(out, nonce[:], payload, header)
}

func TestPacketCipherFrameRoundTrip(t *testing.T) {
	a, b, _, _ := newPacketTestPair(t)
	for _, direction := range []struct {
		name             string
		sender, receiver *packetCipher
		senderName       string
	}{{"alice_to_bob", a, b, "alice"}, {"bob_to_alice", b, a, "bob"}} {
		t.Run(direction.name, func(t *testing.T) {
			for _, size := range []int{14, 64, 1514} {
				payload := bytes.Repeat([]byte("zone frame payload;"), 100)[:size-14]
				frame := testEthernetFrame(direction.sender.mac, direction.receiver.mac, etherTypeIPv4, payload)
				original := bytes.Clone(frame)
				message := sealTestFrame(t, direction.sender, frame)
				if len(message) != frameHeaderSize+len(frame)+chacha20poly1305.Overhead {
					t.Fatalf("message length = %d, want %d", len(message), frameHeaderSize+len(frame)+16)
				}
				if len(payload) >= 8 && bytes.Contains(message, payload) {
					t.Fatal("message contains plaintext frame payload")
				}
				if !bytes.Equal(frame, original) {
					t.Fatal("seal mutated the input frame")
				}
				originalMessage := bytes.Clone(message)
				peer, opened, err := direction.receiver.openFrame(message)
				if err != nil || !bytes.Equal(opened, frame) {
					t.Fatalf("open did not recover the frame: %v", err)
				}
				if peer.Name != direction.senderName {
					t.Fatalf("frame attributed to %q, want %q", peer.Name, direction.senderName)
				}
				if !bytes.Equal(message, originalMessage) {
					t.Fatal("open mutated the input message")
				}
			}
		})
	}
}

func TestPacketCipherFrameWireContract(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	frame := testZoneFrame(a, b, []byte("wire interoperability"))
	message := sealTestFrame(t, a, frame)
	var wantHeader [frameHeaderSize]byte
	wantHeader[0], wantHeader[1] = kindFrame, frameVersion
	aliceMAC, bobMAC := alice.mustMAC("zone-one"), bob.mustMAC("zone-one")
	copy(wantHeader[2:8], aliceMAC[:])
	copy(wantHeader[8:14], bobMAC[:])
	binary.BigEndian.PutUint64(wantHeader[14:], 1)
	if !bytes.Equal(message[:frameHeaderSize], wantHeader[:]) {
		t.Fatalf("header = %x, want %x", message[:frameHeaderSize], wantHeader)
	}
	shared, err := bob.private.ECDH(alice.private.PublicKey())
	if err != nil {
		t.Fatal(err)
	}
	key := make([]byte, 32)
	info := "linksocks-zone-v1\n" + alice.peer.Key + "\n" + bob.peer.Key
	if _, err := io.ReadFull(hkdf.New(sha256.New, shared, []byte("zone-one"), []byte(info)), key); err != nil {
		t.Fatal(err)
	}
	aead, err := chacha20poly1305.NewX(key)
	if err != nil {
		t.Fatal(err)
	}
	session, err := hex.DecodeString(alice.peer.Session)
	if err != nil {
		t.Fatal(err)
	}
	nonce := append(session, wantHeader[14:22]...)
	opened, err := aead.Open(nil, nonce, message[frameHeaderSize:], wantHeader[:])
	if err != nil || !bytes.Equal(opened, frame) {
		t.Fatalf("frame does not follow the documented HKDF, nonce and AAD format: %v", err)
	}
}

func TestPacketCipherRejectsTamperingAndReplay(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"kind", func(f []byte) []byte { f[0] = kindDatagram; return f }},
		{"version", func(f []byte) []byte { f[1] = 1; return f }},
		{"source", func(f []byte) []byte { f[3] ^= 1; return f }},
		{"destination", func(f []byte) []byte { f[9] ^= 1; return f }},
		{"sequence", func(f []byte) []byte { binary.BigEndian.PutUint64(f[14:22], math.MaxUint64); return f }},
		{"ciphertext", func(f []byte) []byte { f[frameHeaderSize] ^= 1; return f }},
		{"tag", func(f []byte) []byte { f[len(f)-1] ^= 1; return f }},
		{"truncated", func(f []byte) []byte { return f[:len(f)-1] }},
		{"extended", func(f []byte) []byte { return append(f, 0) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, alice, bob := newPacketTestPair(t)
			frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
			message := sealTestFrame(t, a, frame)
			if _, _, err := b.openFrame(tc.mutate(bytes.Clone(message))); err == nil {
				t.Fatal("accepted a tampered frame")
			}
			if _, _, err := b.openFrame(message); err != nil {
				t.Fatalf("failed frame consumed the valid sequence: %v", err)
			}
			if _, _, err := b.openFrame(message); err == nil {
				t.Fatal("accepted a replay")
			}
		})
	}
	t.Run("out_of_order", func(t *testing.T) {
		a, b, alice, bob := newPacketTestPair(t)
		frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
		first, second := sealTestFrame(t, a, frame), sealTestFrame(t, a, frame)
		if _, _, err := b.openFrame(second); err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.openFrame(first); err == nil {
			t.Fatal("accepted an older sequence")
		}
	})
}

func (n packetTestNode) mustIdentity() []byte {
	identity, err := hex.DecodeString(n.peer.Identity)
	if err != nil {
		panic(err)
	}
	return identity
}

func (n packetTestNode) mustMAC(zoneID string) [6]byte {
	return deriveMAC(zoneID, n.mustIdentity())
}

func TestPacketCipherRejectsAuthenticatedInvalidFrames(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	validFrame := testEthernetFrame(a.mac, b.mac, etherTypeIPv4, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
	for _, tc := range []struct {
		name  string
		frame func() []byte
	}{
		{"source_mismatch", func() []byte {
			return testEthernetFrame([6]byte{2, 8, 8, 8, 8, 8}, b.mac, etherTypeIPv4, nil)
		}},
		{"destination_mismatch", func() []byte {
			return testEthernetFrame(a.mac, [6]byte{2, 8, 8, 8, 8, 8}, etherTypeIPv4, nil)
		}},
		{"short", func() []byte { return validFrame[:minFrameSize-1] }},
		{"oversized", func() []byte {
			return append(bytes.Clone(validFrame), make([]byte, maxFrameSize-len(validFrame)+1)...)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			frame := tc.frame()
			header := make([]byte, frameHeaderSize)
			header[0], header[1] = kindFrame, frameVersion
			copy(header[2:8], a.mac[:])
			copy(header[8:14], b.mac[:])
			bad := authenticateRaw(a, b.self, header, 14, math.MaxUint64, frame)
			if _, _, err := b.openFrame(bad); err == nil {
				t.Fatal("accepted an authenticated invalid inner frame")
			}
			if _, _, err := b.openFrame(sealTestFrame(t, a, validFrame)); err != nil {
				t.Fatalf("invalid inner frame consumed the receive sequence: %v", err)
			}
		})
	}
	t.Run("unknown_source_mac", func(t *testing.T) {
		a, b, alice, bob := newPacketTestPair(t)
		frame := testEthernetFrame([6]byte{2, 1, 2, 3, 4, 5}, b.mac, etherTypeIPv4, nil)
		_ = a
		_ = alice
		_ = bob
		if _, _, err := b.openFrame(frame); err == nil {
			t.Fatal("accepted a frame with an unknown source MAC")
		}
	})
	t.Run("zero_sequence", func(t *testing.T) {
		a, b, alice, bob := newPacketTestPair(t)
		header := make([]byte, frameHeaderSize)
		header[0], header[1] = kindFrame, frameVersion
		copy(header[2:8], a.mac[:])
		copy(header[8:14], b.mac[:])
		frame := testEthernetFrame(a.mac, b.mac, etherTypeIPv4, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
		if _, _, err := b.openFrame(authenticateRaw(a, b.self, header, 14, 0, frame)); err == nil {
			t.Fatal("accepted sequence zero")
		}
	})
}

func TestPacketCipherRejectsFramesNotAddressedToSelf(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	other := newPacketTestNode(t, "carol", "10.88.0.3")
	unicast := testEthernetFrame(a.mac, other.mustMAC("zone-one"), etherTypeIPv4, nil)
	header := make([]byte, frameHeaderSize)
	header[0], header[1] = kindFrame, frameVersion
	copy(header[2:8], a.mac[:])
	copy(header[8:14], unicast[0:6])
	message := authenticateRaw(a, b.self, header, 14, 1, unicast)
	_ = alice
	_ = bob
	if _, _, err := b.openFrame(message); err == nil {
		t.Fatal("accepted a unicast frame addressed to another member")
	}
	broadcast := testEthernetFrame(a.mac, [6]byte{255, 255, 255, 255, 255, 255}, etherTypeARP, nil)
	copy(header[8:14], broadcast[0:6])
	message = authenticateRaw(a, b.self, header, 14, 1, broadcast)
	if _, _, err := b.openFrame(message); err != nil {
		t.Fatalf("did not decrypt a broadcast frame: %v", err)
	}
}

func TestPacketCipherRejectsInvalidFramesWithoutAdvancingSequence(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"short", func(f []byte) []byte { return f[:13] }},
		{"oversized", func(f []byte) []byte { return append(f, make([]byte, maxFrameSize)...) }},
		{"wrong_source", func(f []byte) []byte { f[6] ^= 1; return f }},
		{"self_addressed", func(f []byte) []byte { copy(f[0:6], f[6:12]); return f }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, alice, bob := newPacketTestPair(t)
			frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
			if _, err := a.sealFrame(tc.mutate(bytes.Clone(frame))); err == nil {
				t.Fatal("accepted an invalid frame")
			}
			message := sealTestFrame(t, a, frame)
			if seq := binary.BigEndian.Uint64(message[14:22]); seq != 1 {
				t.Fatalf("failed seal consumed sequence: got %d, want 1", seq)
			}
			if _, _, err := b.openFrame(message); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("unknown_destination_mac", func(t *testing.T) {
		a, b, alice, bob := newPacketTestPair(t)
		frame := testEthernetFrame(a.mac, [6]byte{2, 9, 9, 9, 9, 9}, etherTypeIPv4, nil)
		if _, err := a.sealFrame(frame); err == nil {
			t.Fatal("accepted a frame without a destination member")
		}
		_ = b
		_ = alice
		_ = bob
	})
}

func TestPacketCipherFrameBroadcast(t *testing.T) {
	alice := newPacketTestNode(t, "alice", "10.88.0.1")
	bob := newPacketTestNode(t, "bob", "10.88.0.2")
	carol := newPacketTestNode(t, "carol", "10.88.0.3")
	a := newTestPacketCipher(t, "zone-one", alice, bob, carol)
	b := newTestPacketCipher(t, "zone-one", bob, alice, carol)
	frame := testEthernetFrame(alice.mustMAC("zone-one"), [6]byte{255, 255, 255, 255, 255, 255}, etherTypeARP, nil)
	messages, err := a.sealFrame(frame)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 2 {
		t.Fatalf("broadcast produced %d messages, want one per peer", len(messages))
	}
	for _, message := range messages {
		seen := 0
		if _, _, err := b.openFrame(message); err == nil {
			seen++
		}
		if _, _, err := newTestPacketCipher(t, "zone-one", carol, alice, bob).openFrame(message); err == nil {
			seen++
		}
		if seen == 0 {
			t.Fatal("no peer could decrypt a broadcast frame copy")
		}
	}
}

func TestPacketCipherStreamData(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	for _, size := range []int{1, 256, maxStreamData} {
		payload := bytes.Repeat([]byte("stream data;"), maxStreamData)[:size]
		message, err := a.sealStreamData(a.peers[netip.MustParseAddr(bob.peer.IP)], 0x01020304, payload)
		if err != nil {
			t.Fatal(err)
		}
		if len(message) != streamDataHeader+len(payload)+chacha20poly1305.Overhead {
			t.Fatalf("message length = %d, want %d", len(message), streamDataHeader+len(payload)+16)
		}
		if message[0] != kindStream || message[1] != opData || binary.BigEndian.Uint32(message[2:6]) != 0x01020304 {
			t.Fatalf("unexpected stream DATA prefix: %x", message[:14])
		}
		peer := b.peers[netip.MustParseAddr(alice.peer.IP)]
		id, opened, err := b.openStreamData(peer, message)
		if err != nil || id != 0x01020304 || !bytes.Equal(opened, payload) {
			t.Fatalf("openStreamData = %d, %d bytes, %v", id, len(opened), err)
		}
	}
	if _, err := a.sealStreamData(a.peers[netip.MustParseAddr(bob.peer.IP)], 1, make([]byte, maxStreamData+1)); err == nil {
		t.Fatal("accepted an oversized stream payload")
	}
	if _, err := a.sealStreamData(a.peers[netip.MustParseAddr(bob.peer.IP)], 1, nil); err == nil {
		t.Fatal("accepted an empty stream payload")
	}
	over := authenticateRaw(a, b.self, []byte{kindStream, opData, 0, 0, 0, 1, 0, 0, 0, 0, 0, 0, 0, 9}, 6, 9, make([]byte, maxStreamData+1))
	if _, _, err := b.openStreamData(b.peers[netip.MustParseAddr(alice.peer.IP)], over); err == nil {
		t.Fatal("accepted an authenticated oversized payload")
	}
	good, _ := a.sealStreamData(a.peers[netip.MustParseAddr(bob.peer.IP)], 7, []byte("payload"))
	if _, _, err := b.openStreamData(b.peers[netip.MustParseAddr(alice.peer.IP)], good); err != nil {
		t.Fatalf("oversized failure consumed the sequence: %v", err)
	}
	if _, _, err := b.openStreamData(b.peers[netip.MustParseAddr(alice.peer.IP)], good); err == nil {
		t.Fatal("accepted a replayed DATA message")
	}
}

func TestPacketCipherStreamDataAADBinding(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	payload := []byte("bound to the prefix")
	message, err := a.sealStreamData(a.peers[netip.MustParseAddr(bob.peer.IP)], 42, payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]byte)
	}{
		{"kind", func(m []byte) { m[0] = kindFrame }},
		{"opcode", func(m []byte) { m[1] = opClose }},
		{"stream_id", func(m []byte) { m[2] ^= 1 }},
		{"sequence", func(m []byte) { binary.BigEndian.PutUint64(m[6:14], 77) }},
		{"payload", func(m []byte) { m[streamDataHeader] ^= 1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mutated := bytes.Clone(message)
			tc.mutate(mutated)
			if _, _, err := b.openStreamData(b.peers[netip.MustParseAddr(alice.peer.IP)], mutated); err == nil {
				t.Fatal("accepted a message with a modified prefix")
			}
		})
	}
	if _, _, err := b.openStreamData(b.peers[netip.MustParseAddr(alice.peer.IP)], message); err != nil {
		t.Fatalf("prefix tampering consumed the sequence: %v", err)
	}
	_ = payload
}

func TestPacketCipherDatagram(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	payload := []byte("datagram payload")
	dst := netip.MustParseAddr(bob.peer.IP)
	message, err := a.sealDatagram(dst, 0x1234, 0x5678, payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(message) != datagramHeader+len(payload)+chacha20poly1305.Overhead {
		t.Fatalf("message length = %d, want %d", len(message), datagramHeader+len(payload)+16)
	}
	if message[0] != kindDatagram || binary.BigEndian.Uint16(message[9:11]) != 0x1234 || binary.BigEndian.Uint16(message[11:13]) != 0x5678 {
		t.Fatalf("unexpected datagram prefix: %x", message[:datagramHeader])
	}
	if binary.BigEndian.Uint64(message[13:21]) != 1 {
		t.Fatal("datagram is not part of the shared sequence space")
	}
	datagram, err := b.openDatagram(message)
	if err != nil {
		t.Fatal(err)
	}
	if datagram.src.String() != alice.peer.IP || datagram.dst.String() != bob.peer.IP || datagram.srcPort != 0x1234 || datagram.dstPort != 0x5678 || !bytes.Equal(datagram.payload, payload) {
		t.Fatalf("unexpected datagram: %+v", datagram)
	}
	if _, err := b.openDatagram(message); err == nil {
		t.Fatal("accepted a replayed datagram")
	}
	if empty, err := a.sealDatagram(dst, 1, 2, nil); err != nil {
		t.Fatal(err)
	} else if datagram, err := b.openDatagram(empty); err != nil || len(datagram.payload) != 0 {
		t.Fatalf("empty datagram failed: %v", err)
	}
	for _, tc := range []struct {
		name   string
		mutate func([]byte) []byte
	}{
		{"source", func(m []byte) []byte { m[2] ^= 1; return m }},
		{"destination", func(m []byte) []byte { m[6] ^= 1; return m }},
		{"source_port", func(m []byte) []byte { m[9] ^= 1; return m }},
		{"destination_port", func(m []byte) []byte { m[11] ^= 1; return m }},
		{"truncated", func(m []byte) []byte { return m[:len(m)-1] }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fresh, err := a.sealDatagram(dst, 1, 2, payload)
			if err != nil {
				t.Fatal(err)
			}
			mutated := tc.mutate(bytes.Clone(fresh))
			if _, err := b.openDatagram(mutated); err == nil {
				t.Fatal("accepted a tampered datagram")
			}
			if _, err := b.openDatagram(fresh); err != nil {
				t.Fatalf("failed datagram consumed the sequence: %v", err)
			}
		})
	}
	if _, err := a.sealDatagram(netip.MustParseAddr("10.88.0.9"), 1, 2, payload); err == nil {
		t.Fatal("sealed a datagram without a zone peer")
	}
}

func TestPacketCipherSharedSequenceAcrossKinds(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
	first := sealTestFrame(t, a, frame)
	data, err := a.sealStreamData(a.peers[netip.MustParseAddr(bob.peer.IP)], 5, []byte("stream"))
	if err != nil {
		t.Fatal(err)
	}
	datagram, err := a.sealDatagram(netip.MustParseAddr(bob.peer.IP), 1, 2, []byte("datagram"))
	if err != nil {
		t.Fatal(err)
	}
	if binary.BigEndian.Uint64(first[14:22]) != 1 || binary.BigEndian.Uint64(data[6:14]) != 2 || binary.BigEndian.Uint64(datagram[13:21]) != 3 {
		t.Fatalf("sealed seqs = %d, %d, %d; want 1, 2, 3",
			binary.BigEndian.Uint64(first[14:22]), binary.BigEndian.Uint64(data[6:14]), binary.BigEndian.Uint64(datagram[13:21]))
	}
	if _, err := b.openDatagram(datagram); err != nil {
		t.Fatalf("newest before oldest should open: %v", err)
	}
	if _, _, err := b.openStreamData(b.peers[netip.MustParseAddr(alice.peer.IP)], data); err == nil {
		t.Fatal("accepted an out-of-order stream message after a newer datagram")
	}
	if _, _, err := b.openFrame(first); err == nil {
		t.Fatal("accepted an out-of-order frame after a newer datagram")
	}
}

func TestPacketCipherSequenceExhaustion(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	a.next = math.MaxUint64 - 1
	frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
	message := sealTestFrame(t, a, frame)
	if seq := binary.BigEndian.Uint64(message[14:22]); seq != math.MaxUint64 {
		t.Fatalf("last sequence = %d, want MaxUint64", seq)
	}
	if _, _, err := b.openFrame(message); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := a.sealFrame(frame); err == nil {
			t.Fatal("sequence exhaustion allowed nonce reuse")
		}
		if _, err := a.sealDatagram(netip.MustParseAddr(bob.peer.IP), 1, 2, []byte("x")); err == nil {
			t.Fatal("sequence exhaustion allowed nonce reuse")
		}
	}
}

func TestPacketCipherRosterPreservesReplayState(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
	message := sealTestFrame(t, a, frame)
	if _, _, err := b.openFrame(message); err != nil {
		t.Fatal(err)
	}
	roster := packetTestRoster("zone-one", bob, alice)
	roster.Peers[0].Name = "Alice renamed"
	for range 3 {
		if err := b.update(roster); err != nil {
			t.Fatal(err)
		}
		if _, _, err := b.openFrame(message); err == nil {
			t.Fatal("unchanged peer keys reset replay state")
		}
	}
	if _, _, err := b.openFrame(sealTestFrame(t, a, frame)); err != nil {
		t.Fatalf("roster refresh broke fresh frames: %v", err)
	}
	if err := b.update(packetTestRoster("zone-one", bob)); err != nil {
		t.Fatal(err)
	}
	fresh := newTestPacketCipher(t, "zone-one", alice, bob)
	if _, _, err := b.openFrame(sealTestFrame(t, fresh, frame)); err == nil {
		t.Fatal("accepted a frame from a removed peer")
	}
}

func TestPacketCipherPeerRotation(t *testing.T) {
	for _, change := range []string{"key", "session", "key_and_session"} {
		t.Run(change, func(t *testing.T) {
			a, b, alice, bob := newPacketTestPair(t)
			frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
			if _, _, err := b.openFrame(sealTestFrame(t, a, frame)); err != nil {
				t.Fatal(err)
			}
			oldFrame := sealTestFrame(t, a, frame)
			rotated := newPacketTestNode(t, "alice reconnected", alice.peer.IP)
			rotated.peer.Identity = alice.peer.Identity
			if change == "key" {
				rotated.peer.Session = alice.peer.Session
			}
			if change == "session" {
				rotated.private, rotated.peer.Key = alice.private, alice.peer.Key
			}
			if err := b.update(packetTestRoster("zone-one", bob, rotated)); err != nil {
				t.Fatal(err)
			}
			if _, _, err := b.openFrame(oldFrame); err == nil {
				t.Fatal("accepted a frame from the previous peer connection")
			}
			fresh := newTestPacketCipher(t, "zone-one", rotated, bob)
			if _, _, err := b.openFrame(sealTestFrame(t, fresh, testZoneFrame(fresh, b, nil))); err != nil {
				t.Fatalf("rotated peer could not restart at sequence one: %v", err)
			}
			old := newTestPacketCipher(t, "zone-one", alice, bob)
			if _, _, err := b.openFrame(sealTestFrame(t, old, frame)); err == nil {
				t.Fatal("accepted an old-key frame after a new connection frame")
			}
			if _, _, err := b.openFrame(sealTestFrame(t, fresh, testZoneFrame(fresh, b, nil))); err != nil {
				t.Fatalf("old-key failure consumed the new connection sequence: %v", err)
			}
		})
	}
}

func TestPacketCipherSeparatesZonesAndDirections(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	otherA := newTestPacketCipher(t, "zone-two", alice, bob)
	otherB := newTestPacketCipher(t, "zone-two", bob, alice)
	frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
	message := sealTestFrame(t, a, frame)
	if _, _, err := otherB.openFrame(message); err == nil {
		t.Fatal("accepted ciphertext from another zone with identical peer keys and sessions")
	}
	otherFrame := testZoneFrame(otherA, otherB, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
	if _, _, err := otherB.openFrame(sealTestFrame(t, otherA, otherFrame)); err != nil {
		t.Fatalf("cross-zone failure consumed the local zone sequence: %v", err)
	}
	reflected := bytes.Clone(message)
	copy(reflected[2:8], b.mac[:])
	copy(reflected[8:14], a.mac[:])
	if _, _, err := a.openFrame(reflected); err == nil {
		t.Fatal("accepted a reflected outgoing frame")
	}
	reverse := testZoneFrame(b, a, testIPv4Packet(bob.peer.IP, alice.peer.IP, 6, nil))
	if _, _, err := a.openFrame(sealTestFrame(t, b, reverse)); err != nil {
		t.Fatalf("reflection failure consumed the reverse direction sequence: %v", err)
	}
}

func TestPacketCipherRejectsInvalidRostersAtomically(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*control)
	}{
		{"zone_changed", func(r *control) { r.Zone.ID = "another-zone" }},
		{"network_changed", func(r *control) { r.Zone.CIDR = "10.88.0.0/16" }},
		{"mtu_changed", func(r *control) { r.Zone.MTU = 1200 }},
		{"self_changed", func(r *control) { r.Self = "10.88.0.3" }},
		{"too_many_peers", func(r *control) { r.Peers = make([]peer, 256) }},
		{"duplicate_address", func(r *control) { r.Peers = append(r.Peers, r.Peers[0]) }},
		{"duplicate_identity", func(r *control) {
			p := r.Peers[0]
			p.IP = "10.88.0.3"
			r.Peers = append(r.Peers, p)
		}},
		{"duplicate_key", func(r *control) {
			p := r.Peers[0]
			p.Identity = strings.Repeat("cd", 32)
			p.IP = "10.88.0.3"
			r.Peers = append(r.Peers, p)
		}},
		{"self_peer", func(r *control) { r.Peers[0].IP = r.Self }},
		{"network_address", func(r *control) { r.Peers[0].IP = "10.88.0.0" }},
		{"broadcast_address", func(r *control) { r.Peers[0].IP = "10.88.0.255" }},
		{"outside_network", func(r *control) { r.Peers[0].IP = "10.89.0.1" }},
		{"ipv6", func(r *control) { r.Peers[0].IP = "::1" }},
		{"malformed_address", func(r *control) { r.Peers[0].IP = "not-an-ip" }},
		{"short_identity", func(r *control) { r.Peers[0].Identity = "ab" }},
		{"invalid_identity", func(r *control) { r.Peers[0].Identity = strings.Repeat("zz", 32) }},
		{"uppercase_identity", func(r *control) { r.Peers[0].Identity = strings.Repeat("AB", 32) }},
		{"short_key", func(r *control) { r.Peers[0].Key = "ab" }},
		{"invalid_key", func(r *control) { r.Peers[0].Key = strings.Repeat("zz", 32) }},
		{"uppercase_key", func(r *control) { r.Peers[0].Key = strings.Repeat("AB", 32) }},
		{"low_order_key", func(r *control) { r.Peers[0].Key = strings.Repeat("00", 32) }},
		{"short_session", func(r *control) { r.Peers[0].Session = "ab" }},
		{"invalid_session", func(r *control) { r.Peers[0].Session = strings.Repeat("zz", 16) }},
		{"uppercase_session", func(r *control) { r.Peers[0].Session = strings.Repeat("AB", 16) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b, alice, bob := newPacketTestPair(t)
			frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
			first := sealTestFrame(t, a, frame)
			if _, _, err := b.openFrame(first); err != nil {
				t.Fatal(err)
			}
			roster := packetTestRoster("zone-one", bob, alice)
			tc.mutate(&roster)
			if err := b.update(roster); err == nil {
				t.Fatal("accepted an invalid roster")
			}
			if _, _, err := b.openFrame(first); err == nil {
				t.Fatal("rejected roster reset replay state")
			}
			if _, _, err := b.openFrame(sealTestFrame(t, a, frame)); err != nil {
				t.Fatalf("rejected roster damaged the existing peer state: %v", err)
			}
		})
	}
}

func TestPacketCipherConcurrentUse(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	frame := testZoneFrame(a, b, testIPv4Packet(alice.peer.IP, bob.peer.IP, 6, nil))
	const count = 32
	messages := make(chan []byte, count)
	failures := make(chan error, count+1)
	var wg sync.WaitGroup
	for range count {
		wg.Go(func() {
			sealed, err := a.sealFrame(frame)
			if err != nil {
				failures <- err
				return
			}
			messages <- sealed[0]
		})
	}
	wg.Go(func() {
		for range count {
			if err := a.update(packetTestRoster("zone-one", alice, bob)); err != nil {
				failures <- err
				return
			}
		}
	})
	wg.Wait()
	close(messages)
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	ordered := make([][]byte, 0, count)
	for message := range messages {
		ordered = append(ordered, message)
	}
	sort.Slice(ordered, func(i, j int) bool {
		return binary.BigEndian.Uint64(ordered[i][14:22]) < binary.BigEndian.Uint64(ordered[j][14:22])
	})
	for i, message := range ordered {
		if seq := binary.BigEndian.Uint64(message[14:22]); seq != uint64(i+1) {
			t.Fatalf("concurrent seals reused or skipped a sequence: got %d, want %d", seq, i+1)
		}
		if _, _, err := b.openFrame(message); err != nil {
			t.Fatal(err)
		}
	}
	message := sealTestFrame(t, a, frame)
	results := make(chan error, count+1)
	for range count {
		wg.Go(func() {
			_, _, err := b.openFrame(message)
			results <- err
		})
	}
	wg.Go(func() {
		for range count {
			if err := b.update(packetTestRoster("zone-one", bob, alice)); err != nil {
				results <- err
				return
			}
		}
	})
	wg.Wait()
	close(results)
	accepted := 0
	for err := range results {
		if err == nil {
			accepted++
		}
	}
	if accepted != 1 {
		t.Fatalf("concurrent opens accepted one sequence %d times, want 1", accepted)
	}
}

func TestPacketCipherPeerNameResolutionAndAlive(t *testing.T) {
	a, b, alice, bob := newPacketTestPair(t)
	_ = b
	current := a.peerAddr(netip.MustParseAddr(bob.peer.IP))
	if current == nil || current.Name != "bob" {
		t.Fatal("peerAddr did not find the peer")
	}
	if !a.peerAlive(current) {
		t.Fatal("peerAlive rejected a current peer")
	}
	rotated := newPacketTestNode(t, "bob reconnected", bob.peer.IP)
	rotated.peer.Identity = bob.peer.Identity
	if err := a.update(packetTestRoster("zone-one", alice, rotated)); err != nil {
		t.Fatal(err)
	}
	if a.peerAlive(current) {
		t.Fatal("peerAlive accepted a rotated peer state")
	}
	if !a.peerAlive(a.peerAddr(netip.MustParseAddr(bob.peer.IP))) {
		t.Fatal("peerAlive rejected the rotated peer")
	}
}

func TestDeriveMAC(t *testing.T) {
	key := bytes.Repeat([]byte{0x5a}, 32)
	first := deriveMAC("zone-one", key)
	if first != deriveMAC("zone-one", key) {
		t.Fatal("MAC derivation is not deterministic")
	}
	if second := deriveMAC("zone-two", key); second == first {
		t.Fatal("MAC derivation does not separate zones")
	}
	if second := deriveMAC("zone-one", append(bytes.Clone(key), 0)); second == first {
		t.Fatal("MAC derivation does not separate keys")
	}
	if first[0]&0x01 != 0 {
		t.Fatal("derived MAC has the multicast bit set")
	}
	if first[0]&0x02 == 0 {
		t.Fatal("derived MAC is not locally administered")
	}
}

func TestStreamControlMessages(t *testing.T) {
	open := openMessage(7, netip.MustParseAddr("10.88.0.1"), netip.MustParseAddr("10.88.0.2"), 443)
	op, id, fields, err := streamOp(open)
	if err != nil || op != opOpen || id != 7 || len(fields) != 10 {
		t.Fatalf("streamOp(open) = %d, %d, %d, %v", op, id, len(fields), err)
	}
	if fields[0] != 10 || fields[4] != 10 || binary.BigEndian.Uint16(fields[8:]) != 443 {
		t.Fatalf("unexpected OPEN fields: %x", fields)
	}
	for _, message := range []struct {
		name string
		raw  []byte
		op   byte
	}{
		{"accept", acceptMessage(9), opAccept},
		{"close", closeMessage(9), opClose},
		{"reset", resetMessage(9, 4), opReset},
		{"refuse", refuseMessage(9, refuseNotAllowed, "not allowed"), opRefuse},
	} {
		op, id, fields, err := streamOp(message.raw)
		if err != nil || op != message.op || id != 9 {
			t.Fatalf("%s: op=%d id=%d err=%v", message.name, op, id, err)
		}
		if message.op == opReset && binary.BigEndian.Uint16(fields) != 4 {
			t.Fatal("RESET code not preserved")
		}
		if message.op == opRefuse && (binary.BigEndian.Uint16(fields) != refuseNotAllowed || string(fields[2:]) != "not allowed") {
			t.Fatal("REFUSE fields not preserved")
		}
	}
	long := refuseMessage(1, 1, strings.Repeat("x", maxStreamReason+10))
	if len(long) != 8+maxStreamReason {
		t.Fatalf("oversized reason was not trimmed: %d bytes", len(long))
	}
	for _, invalid := range [][]byte{nil, {kindFrame}, {kindStream, opOpen}, {kindStream, 99, 0, 0, 0, 1}} {
		if _, _, _, err := streamOp(invalid); err == nil {
			t.Fatalf("accepted invalid stream message %x", invalid)
		}
	}
}
