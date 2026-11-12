package linksocks

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func directSignalingTestPair(t *testing.T) (alice, bob *directKeyPair, aliceSession, bobSession uuid.UUID) {
	t.Helper()
	alice, err := newDirectKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	bob, err = newDirectKeyPair()
	if err != nil {
		t.Fatal(err)
	}
	aliceSession = uuid.New()
	bobSession = uuid.New()
	return alice, bob, aliceSession, bobSession
}

func TestDirectSignalingRoundTrip(t *testing.T) {
	alice, bob, aliceSession, bobSession := directSignalingTestPair(t)
	pairSession := minUUID(aliceSession, bobSession)

	aliceKey, err := deriveDirectSignalingKey(pairSession, alice.priv, bob.pub)
	if err != nil {
		t.Fatal(err)
	}
	bobKey, err := deriveDirectSignalingKey(pairSession, bob.priv, alice.pub)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(aliceKey, bobKey) {
		t.Fatalf("peers derived different signaling keys")
	}

	original := DirectCapabilitiesMessage{
		SessionID:   aliceSession,
		Candidates:  []DirectCandidate{{Addr: "203.0.113.7", Port: 45123, Kind: "srflx"}, {Addr: "192.168.1.20", Port: 45123, Kind: "host"}},
		Discoveries: []string{"stun"},
	}
	envelope, err := sealDirectSignaling(aliceKey, pairSession, aliceSession, bobSession, original)
	if err != nil {
		t.Fatal(err)
	}

	// The relay-visible encoding must not carry any peer address.
	wire, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"203.0.113.7", "192.168.1.20", "srflx"} {
		if bytes.Contains(wire, []byte(secret)) {
			t.Fatalf("sealed envelope leaked %q: %s", secret, wire)
		}
	}
	if envelope.RecipientSessionID != bobSession || envelope.SenderSessionID != aliceSession {
		t.Fatalf("envelope routing ids were not preserved")
	}

	decrypted, err := openDirectSignaling(bobKey, pairSession, bobSession, envelope)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := decrypted.(DirectCapabilitiesMessage)
	if !ok {
		t.Fatalf("decrypted message type = %T, want DirectCapabilitiesMessage", decrypted)
	}
	if got.SessionID != aliceSession || len(got.Candidates) != 2 || got.Candidates[0].Addr != "203.0.113.7" {
		t.Fatalf("decrypted capabilities did not round trip: %+v", got)
	}
}

func TestDirectSignalingRejectsForeignRecipient(t *testing.T) {
	alice, bob, aliceSession, bobSession := directSignalingTestPair(t)
	pairSession := minUUID(aliceSession, bobSession)
	key, err := deriveDirectSignalingKey(pairSession, alice.priv, bob.pub)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sealDirectSignaling(key, pairSession, aliceSession, bobSession, DirectStatusMessage{SessionID: pairSession, Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDirectSignaling(key, pairSession, uuid.New(), envelope); err != errDirectSignalingRecipient {
		t.Fatalf("open with foreign recipient error = %v, want %v", err, errDirectSignalingRecipient)
	}
}

func TestDirectSignalingRejectsTampering(t *testing.T) {
	alice, bob, aliceSession, bobSession := directSignalingTestPair(t)
	pairSession := minUUID(aliceSession, bobSession)
	key, err := deriveDirectSignalingKey(pairSession, alice.priv, bob.pub)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sealDirectSignaling(key, pairSession, aliceSession, bobSession, DirectStatusMessage{SessionID: pairSession, Status: "failed"})
	if err != nil {
		t.Fatal(err)
	}
	envelope.Payload[0] ^= 0xff
	if _, err := openDirectSignaling(key, pairSession, bobSession, envelope); err == nil {
		t.Fatalf("tampered envelope was accepted")
	}
}

func TestDirectSignalingRejectsOtherSession(t *testing.T) {
	alice, bob, aliceSession, bobSession := directSignalingTestPair(t)
	pairSession := minUUID(aliceSession, bobSession)
	key, err := deriveDirectSignalingKey(pairSession, alice.priv, bob.pub)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sealDirectSignaling(key, pairSession, aliceSession, bobSession, DirectStatusMessage{SessionID: pairSession, Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	otherSession := uuid.New()
	otherKey, err := deriveDirectSignalingKey(otherSession, bob.priv, alice.pub)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := openDirectSignaling(otherKey, otherSession, bobSession, envelope); err == nil {
		t.Fatalf("envelope from another session was accepted")
	}
}

func TestDirectSignalingKeyIsBoundToSession(t *testing.T) {
	alice, bob, aliceSession, bobSession := directSignalingTestPair(t)
	first, err := deriveDirectSignalingKey(minUUID(aliceSession, bobSession), alice.priv, bob.pub)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deriveDirectSignalingKey(uuid.New(), alice.priv, bob.pub)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatalf("signaling key does not depend on the pair session")
	}
	if len(first) != directSignalingKeyLen {
		t.Fatalf("key length = %d, want %d", len(first), directSignalingKeyLen)
	}
}

func TestDirectSignalingRejectsMalformedEnvelope(t *testing.T) {
	alice, bob, aliceSession, bobSession := directSignalingTestPair(t)
	pairSession := minUUID(aliceSession, bobSession)
	key, err := deriveDirectSignalingKey(pairSession, alice.priv, bob.pub)
	if err != nil {
		t.Fatal(err)
	}
	envelope, err := sealDirectSignaling(key, pairSession, aliceSession, bobSession, DirectStatusMessage{SessionID: pairSession, Status: "ready"})
	if err != nil {
		t.Fatal(err)
	}
	truncated := envelope
	truncated.Nonce = truncated.Nonce[:4]
	if _, err := openDirectSignaling(key, pairSession, bobSession, truncated); err == nil || !strings.Contains(err.Error(), "nonce") {
		t.Fatalf("truncated nonce error = %v, want a nonce error", err)
	}
}
