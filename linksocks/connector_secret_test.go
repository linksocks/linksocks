package linksocks

import (
	"bytes"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestDeriveConnectorTokenKeys(t *testing.T) {
	const secret = "my_connector_token"
	id := deriveConnectorTokenID(secret)
	if id == "" || len(id) != 32 {
		t.Fatalf("connector token id = %q, want 32 hex characters", id)
	}
	if id == secret || strings.Contains(id, secret) {
		t.Fatal("the relay handle must not contain the secret")
	}
	if strings.ToLower(id) != id {
		t.Fatal("the relay handle must be lowercase hex")
	}

	psk := deriveConnectorPSK(secret)
	if len(psk) != connectorSecretPSKBytes {
		t.Fatalf("connector PSK length = %d, want %d", len(psk), connectorSecretPSKBytes)
	}
	if bytes.Equal(psk, []byte(id)) {
		t.Fatal("the relay handle and the end-to-end key must be different values")
	}

	// Both ends derive the same values from the same secret; different
	// secrets must not collide.
	if deriveConnectorTokenID(secret) != id || !bytes.Equal(deriveConnectorPSK(secret), psk) {
		t.Fatal("the derivation must be deterministic")
	}
	if deriveConnectorTokenID(secret+"x") == id {
		t.Fatal("different secrets produced the same relay handle")
	}
	if bytes.Equal(deriveConnectorPSK(secret+"x"), psk) {
		t.Fatal("different secrets produced the same end-to-end key")
	}
}

func TestGenerateConnectorSecretIsRandom(t *testing.T) {
	first, err := generateConnectorSecret()
	if err != nil {
		t.Fatal(err)
	}
	second, err := generateConnectorSecret()
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != connectorSecretBytes*2 {
		t.Fatalf("generated secret length = %d, want %d", len(first), connectorSecretBytes*2)
	}
	if first == second {
		t.Fatal("two generated secrets were identical")
	}
}

func TestChannelAuthProof(t *testing.T) {
	psk := deriveConnectorPSK("secret-a")
	otherPSK := deriveConnectorPSK("secret-b")
	channelID := uuid.New()
	otherChannel := uuid.New()
	binding := bytes.Repeat([]byte{0xAB}, 32)
	otherBinding := bytes.Repeat([]byte{0xCD}, 32)

	proof := channelAuthProof(psk, connectorAuthRoleInitiator, channelID, binding)
	if len(proof) != 32 {
		t.Fatalf("proof length = %d, want 32", len(proof))
	}

	if !verifyChannelAuthProof(psk, connectorAuthRoleInitiator, channelID, binding, proof) {
		t.Fatal("the matching secret and transcript must verify")
	}
	if verifyChannelAuthProof(otherPSK, connectorAuthRoleInitiator, channelID, binding, proof) {
		t.Fatal("a proof verified under the wrong secret")
	}
	if verifyChannelAuthProof(psk, connectorAuthRoleResponder, channelID, binding, proof) {
		t.Fatal("a proof verified under the wrong role")
	}
	if verifyChannelAuthProof(psk, connectorAuthRoleInitiator, otherChannel, binding, proof) {
		t.Fatal("a proof verified under the wrong channel")
	}
	if verifyChannelAuthProof(psk, connectorAuthRoleInitiator, channelID, otherBinding, proof) {
		t.Fatal("a proof verified under a different handshake transcript")
	}

	// The relay-terminated case: the attacker shares one handshake with the
	// connector and another with the provider. The proofs observed on one leg
	// must never verify on the other, because the handshake hashes differ.
	relayProofForConnector := channelAuthProof(psk, connectorAuthRoleResponder, channelID, otherBinding)
	if verifyChannelAuthProof(psk, connectorAuthRoleResponder, channelID, binding, relayProofForConnector) {
		t.Fatal("a proof from a different handshake transcript verified")
	}
}
