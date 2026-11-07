package zone

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
)

// Wire contract constants. design/zone.md is the authoritative contract.
//
// Binary messages start with a kind byte:
//
//	kind 1 frame    22-byte header (kind, version, source MAC, destination
//	                MAC, sequence) followed by the sealed Ethernet frame
//	                (14-1514 bytes). The header is AEAD additional data.
//	kind 2 stream   opcode and stream ID; DATA carries a sequence number and
//	                the sealed payload (up to 16384 bytes) with a 14-byte
//	                prefix as AEAD additional data. Other opcodes are
//	                plaintext control messages.
//	kind 3 datagram source and destination IPv4, source and destination port
//	                and the shared-space sequence number, followed by the
//	                sealed UDP payload. The prefix is AEAD additional data.
//	                (The design text says 14 bytes, but its field list spans
//	                13 bytes and omits the sequence number the AEAD nonce
//	                needs; the sequence follows the documented fields so the
//	                relay can parse offsets 1-13 unchanged.)
const (
	// mtu is the zone interface MTU; frames carry complete Ethernet frames.
	mtu      = 1500
	minMTU   = 576
	protocol = "linksocks-zone-v1"

	kindFrame    = 1
	kindStream   = 2
	kindDatagram = 3

	frameVersion    = 2
	frameHeaderSize = 22
	minFrameSize    = 14
	maxFrameSize    = 1514

	streamDataHeader = 14
	maxStreamData    = 16384
	maxStreamReason  = 64

	datagramHeader = 21

	opOpen   = 1
	opAccept = 2
	opRefuse = 3
	opData   = 4
	opClose  = 5
	opReset  = 6

	refuseUnreachable = 1
	refuseNotAllowed  = 2
	refuseFailed      = 3
)

type network struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	CIDR       string `json:"cidr"`
	MTU        int    `json:"mtu"`
	MaxMembers int    `json:"max_members"`
	Visibility string `json:"visibility"`
}

type peer struct {
	Identity string `json:"identity"`
	Key      string `json:"key"`
	Session  string `json:"session"`
	Name     string `json:"name"`
	IP       string `json:"ip"`
}

type settings struct {
	Welcome       string       `json:"welcome"`
	DefaultAction string       `json:"default_action"`
	Rules         []ruleConfig `json:"rules"`
}

type control struct {
	Type      string   `json:"type"`
	Challenge string   `json:"challenge"`
	Error     string   `json:"error"`
	Zone      network  `json:"zone"`
	Self      string   `json:"self"`
	Settings  settings `json:"settings"`
	Peers     []peer   `json:"peers"`
}

type registration struct {
	Type      string `json:"type"`
	Action    string `json:"action"`
	Identity  string `json:"identity"`
	Key       string `json:"key"`
	Session   string `json:"session"`
	Name      string `json:"name"`
	Signature string `json:"signature"`
}

func newRegistration(identity ed25519.PrivateKey, zoneID, challenge, action, name string) (registration, *ecdh.PrivateKey, error) {
	if _, err := decodeHex(challenge, 32); err != nil {
		return registration{}, nil, fmt.Errorf("invalid coordinator challenge: %w", err)
	}
	key, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return registration{}, nil, err
	}
	var session [16]byte
	if _, err := rand.Read(session[:]); err != nil {
		return registration{}, nil, err
	}
	r := registration{
		Type: "join", Action: action, Name: name,
		Identity: hex.EncodeToString(identity.Public().(ed25519.PublicKey)),
		Key:      hex.EncodeToString(key.PublicKey().Bytes()),
		Session:  hex.EncodeToString(session[:]),
	}
	message := strings.Join([]string{protocol, zoneID, challenge, action, r.Key, r.Session, name}, "\n")
	r.Signature = hex.EncodeToString(ed25519.Sign(identity, []byte(message)))
	return r, key, nil
}

func decodeHex(value string, size int) ([]byte, error) {
	data, err := hex.DecodeString(value)
	if err != nil || len(data) != size || value != strings.ToLower(value) {
		return nil, fmt.Errorf("expected %d lowercase hexadecimal bytes", size)
	}
	return data, nil
}

func validateNetwork(n network, id, self string) (netip.Prefix, netip.Addr, error) {
	prefix, err := netip.ParsePrefix(n.CIDR)
	if err != nil || !prefix.Addr().Is4() || prefix.Bits() < 16 || prefix.Bits() > 30 || prefix != prefix.Masked() || !prefix.Addr().IsPrivate() {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("invalid zone IPv4 network: %s", n.CIDR)
	}
	ip, err := netip.ParseAddr(self)
	if n.ID != id || n.MTU < minMTU || n.MTU > mtu || err != nil || !ip.Is4() || !prefix.Contains(ip) || ip == prefix.Addr() || ip == broadcast(prefix) {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("invalid zone assignment: %s", self)
	}
	return prefix, ip, nil
}

func broadcast(prefix netip.Prefix) netip.Addr {
	addr := prefix.Addr().As4()
	for bit := prefix.Bits(); bit < 32; bit++ {
		addr[bit/8] |= 1 << (7 - bit%8)
	}
	return netip.AddrFrom4(addr)
}
