package zone

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"net/netip"
	"strings"
	"testing"
)

func TestNewRegistrationSignatureContract(t *testing.T) {
	identity := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	public := identity.Public().(ed25519.PublicKey)
	const zoneID = "zone-contract"
	challenge := strings.Repeat("ab", 32)
	const name = "工作站 Δ"
	for _, action := range []string{"join", "leave"} {
		t.Run(action, func(t *testing.T) {
			r, private, err := newRegistration(identity, zoneID, challenge, action, name)
			if err != nil {
				t.Fatal(err)
			}
			if r.Type != "join" || r.Action != action || r.Name != name {
				t.Fatalf("unexpected registration fields: type=%q action=%q name=%q", r.Type, r.Action, r.Name)
			}
			if r.Identity != hex.EncodeToString(public) {
				t.Fatal("registration identity is not the Ed25519 public key")
			}
			if private == nil || r.Key != hex.EncodeToString(private.PublicKey().Bytes()) {
				t.Fatal("registration key does not match the returned X25519 private key")
			}
			if session, err := hex.DecodeString(r.Session); err != nil || len(session) != 16 || strings.ToLower(r.Session) != r.Session {
				t.Fatalf("invalid session ID %q: %v", r.Session, err)
			}
			signature, err := hex.DecodeString(r.Signature)
			if err != nil || len(signature) != ed25519.SignatureSize || strings.ToLower(r.Signature) != r.Signature {
				t.Fatalf("invalid signature encoding: %v", err)
			}
			message := fmt.Sprintf("linksocks-zone-v1\n%s\n%s\n%s\n%s\n%s\n%s", zoneID, challenge, action, r.Key, r.Session, name)
			if !ed25519.Verify(public, []byte(message), signature) || !bytes.Equal(signature, ed25519.Sign(identity, []byte(message))) {
				t.Fatal("signature does not cover the exact documented UTF-8 string")
			}
			fields := strings.Split(message, "\n")
			for i := range fields {
				changed := append([]string(nil), fields...)
				changed[i] += "x"
				if ed25519.Verify(public, []byte(strings.Join(changed, "\n")), signature) {
					t.Fatalf("signature does not bind field %d", i)
				}
			}
			if ed25519.Verify(public, []byte(message+"\n"), signature) {
				t.Fatal("signature accepted an extra trailing newline")
			}
		})
	}
}

func TestNewRegistrationFreshConnectionSecrets(t *testing.T) {
	identity := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x23}, ed25519.SeedSize))
	challenge := strings.Repeat("01", 32)
	first, _, err := newRegistration(identity, "zone-one", challenge, "join", "workstation")
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := newRegistration(identity, "zone-one", challenge, "join", "workstation")
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity != second.Identity || first.Key == second.Key || first.Session == second.Session {
		t.Fatal("reconnection must retain identity and rotate both exchange key and session ID")
	}
}

func TestNewRegistrationRejectsInvalidChallenge(t *testing.T) {
	identity := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	for _, challenge := range []string{
		"", strings.Repeat("ab", 31), strings.Repeat("ab", 33),
		strings.Repeat("AB", 32), strings.Repeat("zz", 32), strings.Repeat("a", 63),
		" " + strings.Repeat("ab", 32), strings.Repeat("ab", 32) + "\n",
	} {
		r, key, err := newRegistration(identity, "zone-one", challenge, "join", "workstation")
		if err == nil || key != nil || r.Signature != "" {
			t.Fatalf("accepted invalid challenge %q", challenge)
		}
	}
}

func TestDecodeHexRequiresCanonicalEncoding(t *testing.T) {
	for _, size := range []int{16, 32, 64} {
		want := bytes.Repeat([]byte{0xab}, size)
		value := hex.EncodeToString(want)
		got, err := decodeHex(value, size)
		if err != nil || !bytes.Equal(got, want) {
			t.Fatalf("decodeHex(%d bytes) = %x, %v", size, got, err)
		}
		for _, invalid := range []string{
			strings.ToUpper(value), value[:len(value)-1], value[:len(value)-2], value + "00", " " + value, value + "\n", strings.Repeat("gg", size),
		} {
			if _, err := decodeHex(invalid, size); err == nil {
				t.Fatalf("accepted noncanonical %d-byte hex %q", size, invalid)
			}
		}
	}
}

func TestValidateNetwork(t *testing.T) {
	for _, tc := range []struct {
		cidr, self string
	}{
		{"10.88.0.0/16", "10.88.0.1"},
		{"10.88.0.0/24", "10.88.0.254"},
		{"172.16.0.0/16", "172.16.255.254"},
		{"172.31.0.0/16", "172.31.0.1"},
		{"192.168.255.252/30", "192.168.255.253"},
		{"192.168.255.252/30", "192.168.255.254"},
	} {
		t.Run(tc.cidr+"_"+tc.self, func(t *testing.T) {
			n := network{ID: "zone-one", CIDR: tc.cidr, MTU: 1280}
			prefix, self, err := validateNetwork(n, "zone-one", tc.self)
			if err != nil || prefix != netip.MustParsePrefix(tc.cidr) || self != netip.MustParseAddr(tc.self) {
				t.Fatalf("valid assignment rejected: prefix=%s self=%s err=%v", prefix, self, err)
			}
		})
	}
	for _, tc := range []struct {
		name   string
		mutate func(*network, *string)
	}{
		{"zone_mismatch", func(n *network, _ *string) { n.ID = "zone-two" }},
		{"zero_mtu", func(n *network, _ *string) { n.MTU = 0 }},
		{"small_mtu", func(n *network, _ *string) { n.MTU = minMTU - 1 }},
		{"large_mtu", func(n *network, _ *string) { n.MTU = mtu + 1 }},
		{"invalid_cidr", func(n *network, _ *string) { n.CIDR = "not-a-network" }},
		{"ipv6_network", func(n *network, _ *string) { n.CIDR = "fd00::/64" }},
		{"public_network", func(n *network, _ *string) { n.CIDR = "8.8.8.0/24" }},
		{"loopback_network", func(n *network, _ *string) { n.CIDR = "127.0.0.0/16" }},
		{"link_local_network", func(n *network, _ *string) { n.CIDR = "169.254.0.0/16" }},
		{"host_bits_set", func(n *network, _ *string) { n.CIDR = "10.88.0.1/24" }},
		{"too_large", func(n *network, _ *string) { n.CIDR = "10.88.0.0/15" }},
		{"too_small", func(n *network, _ *string) { n.CIDR = "10.88.0.0/31" }},
		{"single_host", func(n *network, _ *string) { n.CIDR = "10.88.0.1/32" }},
		{"invalid_self", func(_ *network, self *string) { *self = "invalid" }},
		{"ipv6_self", func(_ *network, self *string) { *self = "::1" }},
		{"mapped_ipv4_self", func(_ *network, self *string) { *self = "::ffff:10.88.0.1" }},
		{"self_outside_network", func(_ *network, self *string) { *self = "10.89.0.1" }},
		{"network_address", func(_ *network, self *string) { *self = "10.88.0.0" }},
		{"broadcast_address", func(_ *network, self *string) { *self = "10.88.0.255" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := network{ID: "zone-one", CIDR: "10.88.0.0/24", MTU: 1280}
			self := "10.88.0.1"
			tc.mutate(&n, &self)
			if _, _, err := validateNetwork(n, "zone-one", self); err == nil {
				t.Fatal("accepted invalid network assignment")
			}
		})
	}
}

func TestBroadcast(t *testing.T) {
	for _, tc := range []struct{ cidr, want string }{
		{"10.88.0.0/16", "10.88.255.255"},
		{"10.88.0.0/24", "10.88.0.255"},
		{"172.16.0.0/20", "172.16.15.255"},
		{"192.168.255.252/30", "192.168.255.255"},
	} {
		if got := broadcast(netip.MustParsePrefix(tc.cidr)); got.String() != tc.want {
			t.Errorf("broadcast(%s) = %s, want %s", tc.cidr, got, tc.want)
		}
	}
}
