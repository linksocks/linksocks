package linksocks

import (
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

// connector_secret.go turns a connector token into a shared secret between the
// provider and the connector.
//
// A connector token with secret semantics is never sent to the relay: the
// provider registers a derived handle and the connector authenticates with the
// same handle. Both ends also derive an end-to-end key from the secret, and a
// sealed channel only becomes usable after each side has proved knowledge of
// that key over the Noise handshake transcript. A relay that terminates the
// transport therefore cannot impersonate either peer: it never learns the
// secret, so it cannot produce a proof bound to a handshake it did not share.
//
// The derivations are fixed for interoperability: the provider, the connector
// and any other client implementation must map a given secret to the same
// handle and the same key.
const (
	connectorSecretKDFSalt = "linksocks connector secret v1"
	connectorTokenIDInfo   = "relay-token-id"
	connectorSecretPSKInfo = "channel-auth-psk"
	connectorAuthLabel     = "linksocks channel auth v1"

	connectorTokenIDBytes   = 16
	connectorSecretPSKBytes = 32
	connectorSecretBytes    = 16

	// Roles keep the two proofs of one exchange from being replayed in the
	// other direction.
	connectorAuthRoleInitiator = byte(1)
	connectorAuthRoleResponder = byte(2)
)

// deriveConnectorTokenID maps a connector token secret to the relay-visible
// handle. The derivation is one-way, so neither the relay nor its storage
// learns the secret the two clients share.
func deriveConnectorTokenID(secret string) string {
	key, err := hkdf.Key(sha256.New, []byte(secret), []byte(connectorSecretKDFSalt), connectorTokenIDInfo, connectorTokenIDBytes)
	if err != nil {
		return ""
	}
	return hex.EncodeToString(key)
}

// deriveConnectorPSK derives the end-to-end key that authenticates sealed
// channels behind the relay handle. It is different from the handle, so a
// relay that sees (or stores) the handle cannot derive the key.
func deriveConnectorPSK(secret string) []byte {
	key, err := hkdf.Key(sha256.New, []byte(secret), []byte(connectorSecretKDFSalt), connectorSecretPSKInfo, connectorSecretPSKBytes)
	if err != nil {
		return nil
	}
	return key
}

// generateConnectorSecret returns a fresh random connector token for providers
// that did not configure one. Generating it locally keeps the relay from ever
// seeing the secret even when it offers to generate tokens.
func generateConnectorSecret() (string, error) {
	buf := make([]byte, connectorSecretBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("connector secret: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// channelAuthProof authenticates one side of a sealed channel over the Noise
// handshake transcript. The binding is the final handshake hash, which only
// the two holders of the same handshake share: a relay that terminates two
// separate handshakes produces different binding values and cannot turn a
// proof observed on one leg into a proof for the other.
func channelAuthProof(psk []byte, role byte, channelID uuid.UUID, binding []byte) []byte {
	mac := hmac.New(sha256.New, psk)
	_, _ = mac.Write([]byte(connectorAuthLabel))
	_, _ = mac.Write([]byte{role})
	_, _ = mac.Write(channelID[:])
	_, _ = mac.Write(binding)
	return mac.Sum(nil)
}

// verifyChannelAuthProof reports whether proof authenticates the peer's role
// on this channel with this secret.
func verifyChannelAuthProof(psk []byte, role byte, channelID uuid.UUID, binding, proof []byte) bool {
	expected := channelAuthProof(psk, role, channelID, binding)
	return subtle.ConstantTimeCompare(expected, proof) == 1
}
