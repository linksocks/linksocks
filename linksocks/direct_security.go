package linksocks

import (
	"crypto/ecdh"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"

	"github.com/google/uuid"
)

const (
	directSessionKeyLen = 32
	directProbeMACLen   = 16
)

type directKeyPair struct {
	priv *ecdh.PrivateKey
	pub  []byte
}

func newDirectKeyPair() (*directKeyPair, error) {
	curve := ecdh.X25519()
	priv, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	pub := priv.PublicKey().Bytes()
	if len(pub) != 32 {
		return nil, errors.New("direct: unexpected public key length")
	}
	return &directKeyPair{priv: priv, pub: pub}, nil
}

func deriveDirectSessionKey(pairSessionID uuid.UUID, localPriv *ecdh.PrivateKey, remotePub []byte) ([]byte, error) {
	if localPriv == nil {
		return nil, errors.New("direct: nil private key")
	}
	if len(remotePub) != 32 {
		return nil, errors.New("direct: invalid remote public key")
	}
	curve := ecdh.X25519()
	remote, err := curve.NewPublicKey(remotePub)
	if err != nil {
		return nil, err
	}
	shared, err := localPriv.ECDH(remote)
	if err != nil {
		return nil, err
	}

	// Derive a fixed-length session key bound to the pair session ID.
	// Avoid including long-lived tokens.
	mac := hmac.New(sha256.New, pairSessionID[:])
	_, _ = mac.Write(shared)
	full := mac.Sum(nil)
	key := make([]byte, directSessionKeyLen)
	copy(key, full[:directSessionKeyLen])
	return key, nil
}

func computeDirectProbeMAC(sessionKey []byte, version byte, typ byte, sessionID uuid.UUID, nonce [8]byte) [directProbeMACLen]byte {
	var out [directProbeMACLen]byte
	if len(sessionKey) == 0 {
		return out
	}

	// MAC input is a stable, minimal tuple to prevent replay/cross-session mix.
	b := make([]byte, 0, 1+1+16+8)
	b = append(b, version)
	b = append(b, typ)
	b = append(b, sessionID[:]...)
	b = append(b, nonce[:]...)

	m := hmac.New(sha256.New, sessionKey)
	_, _ = m.Write(b)
	sum := m.Sum(nil)
	copy(out[:], sum[:directProbeMACLen])
	return out
}

func verifyDirectProbeMAC(sessionKey []byte, version byte, typ byte, sessionID uuid.UUID, nonce [8]byte, mac [directProbeMACLen]byte) bool {
	if len(sessionKey) == 0 {
		return false
	}
	expected := computeDirectProbeMAC(sessionKey, version, typ, sessionID, nonce)
	return subtle.ConstantTimeCompare(expected[:], mac[:]) == 1
}

func putUint16BE(b []byte, v uint16) {
	binary.BigEndian.PutUint16(b, v)
}
