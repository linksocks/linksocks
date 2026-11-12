package linksocks

import (
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"golang.org/x/crypto/chacha20poly1305"
)

// direct_signaling.go encrypts direct-mode signaling for peers that speak
// protocol V2.
//
// Direct mode needs a rendezvous: both peers exchange the addresses they can be
// reached at (STUN srflx, IPv6, LAN host candidates) plus an X25519 key, and
// then report probing results. That signaling is what lets two machines build a
// direct QUIC path, and the relay only has to route it to the paired peer.
//
// The first message of the exchange is deliberately minimal: a key
// announcement carrying the sender's session id, X25519 public key and protocol
// version. It contains no addresses, so the relay can route it without learning
// how to reach either machine. Everything after that (candidates, rendezvous,
// status) travels inside DirectSealedMessage, encrypted with a key derived from
// the two X25519 public keys. The relay forwards the envelopes by role and can
// see only the session identifiers.
//
// The derived key is bound to the pair session id (the lower of the two peer
// session ids) and to a fixed label, so a message sealed for one session or one
// peer cannot be replayed into another.
const (
	directSignalingKeyLen   = 32
	directSignalingNonceLen = chacha20poly1305.NonceSize
	directSignalingLabel    = "linksocks-direct-signaling-v2"
)

var (
	errDirectSignalingRecipient = errors.New("direct signaling: message is for another session")
	errDirectSignalingAuth      = errors.New("direct signaling: authentication failed")
)

// deriveDirectSignalingKey derives the ChaCha20-Poly1305 key used for signaling
// between the two peers of one direct session.
func deriveDirectSignalingKey(pairSession uuid.UUID, localPriv *ecdh.PrivateKey, remotePub []byte) ([]byte, error) {
	if localPriv == nil {
		return nil, errors.New("direct signaling: nil private key")
	}
	if len(remotePub) != 32 {
		return nil, errors.New("direct signaling: invalid remote public key")
	}
	remote, err := ecdh.X25519().NewPublicKey(remotePub)
	if err != nil {
		return nil, err
	}
	shared, err := localPriv.ECDH(remote)
	if err != nil {
		return nil, err
	}
	key, err := hkdf.Key(sha256.New, shared, pairSession[:], directSignalingLabel, directSignalingKeyLen)
	if err != nil {
		return nil, err
	}
	return key, nil
}

func directSignalingAEAD(key []byte) (cipher.AEAD, error) {
	if len(key) != directSignalingKeyLen {
		return nil, fmt.Errorf("direct signaling: invalid key length %d", len(key))
	}
	return chacha20poly1305.New(key)
}

// directSignalingAAD binds a sealed frame to its sender, recipient and session.
func directSignalingAAD(pairSession, sender, recipient uuid.UUID) []byte {
	aad := make([]byte, 0, len(directSignalingLabel)+3*16)
	aad = append(aad, directSignalingLabel...)
	aad = append(aad, pairSession[:]...)
	aad = append(aad, sender[:]...)
	aad = append(aad, recipient[:]...)
	return aad
}

// sealDirectSignaling wraps one signaling message for the peer.
func sealDirectSignaling(key []byte, pairSession, sender, recipient uuid.UUID, msg BaseMessage) (DirectSealedMessage, error) {
	if msg == nil {
		return DirectSealedMessage{}, errors.New("direct signaling: nil message")
	}
	body, err := PackMessage(msg)
	if err != nil {
		return DirectSealedMessage{}, err
	}
	aead, err := directSignalingAEAD(key)
	if err != nil {
		return DirectSealedMessage{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return DirectSealedMessage{}, err
	}
	payload := aead.Seal(nil, nonce, body, directSignalingAAD(pairSession, sender, recipient))
	return DirectSealedMessage{
		SenderSessionID:    sender,
		RecipientSessionID: recipient,
		Nonce:              nonce,
		Payload:            payload,
	}, nil
}

// openDirectSignaling authenticates and decrypts one signaling envelope.
func openDirectSignaling(key []byte, pairSession, localSession uuid.UUID, m DirectSealedMessage) (BaseMessage, error) {
	if m.RecipientSessionID != localSession {
		return nil, errDirectSignalingRecipient
	}
	aead, err := directSignalingAEAD(key)
	if err != nil {
		return nil, err
	}
	if len(m.Nonce) != aead.NonceSize() {
		return nil, errors.New("direct signaling: invalid nonce")
	}
	body, err := aead.Open(nil, m.Nonce, m.Payload, directSignalingAAD(pairSession, m.SenderSessionID, m.RecipientSessionID))
	if err != nil {
		return nil, errDirectSignalingAuth
	}
	msg, _, err := ParseMessageWithVersion(body)
	if err != nil {
		return nil, fmt.Errorf("direct signaling: %w", err)
	}
	return msg, nil
}
