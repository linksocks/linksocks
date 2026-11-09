// Package echtest builds throwaway Encrypted Client Hello keys so tests can
// exercise ECH negotiation without a network lookup.
package echtest

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"fmt"
)

// ConfigAndKey returns a serialized ECHConfigList and the matching server key.
// The encoding follows RFC 9849; the config is not used outside tests.
func ConfigAndKey(publicName string) ([]byte, tls.EncryptedClientHelloKey, error) {
	privateKey, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, tls.EncryptedClientHelloKey{}, err
	}
	publicKey := privateKey.PublicKey().Bytes()
	if len(publicName) == 0 || len(publicName) > 255 {
		return nil, tls.EncryptedClientHelloKey{}, fmt.Errorf("public name must be 1..255 bytes")
	}

	var contents []byte
	contents = append(contents, 0x01)
	contents = binary.BigEndian.AppendUint16(contents, 0x0020)
	contents = binary.BigEndian.AppendUint16(contents, uint16(len(publicKey)))
	contents = append(contents, publicKey...)
	contents = binary.BigEndian.AppendUint16(contents, 4)
	contents = binary.BigEndian.AppendUint16(contents, 0x0001)
	contents = binary.BigEndian.AppendUint16(contents, 0x0001)
	contents = append(contents, 0) // maximum_name_length
	contents = append(contents, byte(len(publicName)))
	contents = append(contents, publicName...)
	contents = binary.BigEndian.AppendUint16(contents, 0)

	var config []byte
	config = binary.BigEndian.AppendUint16(config, 0xfe0d)
	config = binary.BigEndian.AppendUint16(config, uint16(len(contents)))
	config = append(config, contents...)

	list := binary.BigEndian.AppendUint16(nil, uint16(len(config)))
	list = append(list, config...)
	return list, tls.EncryptedClientHelloKey{Config: config, PrivateKey: privateKey.Bytes()}, nil
}
