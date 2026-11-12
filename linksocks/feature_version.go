package linksocks

// Feature minimums describe peer capabilities, not the current wire version.
const (
	MinVersionDirect    = byte(0x01)
	MinVersionMigration = byte(0x02)
	MinVersionWeb       = byte(0x03)
	MinVersionWebSocket = byte(0x04)
)

// protocolSupportsFeature reports whether a peer running the given protocol
// version supports a feature introduced at `minimum`. Protocol V2 (major 2)
// supersedes the whole V1 feature set, so V1-era features are available on V2
// unless a helper below explicitly disables them.
func protocolSupportsFeature(version, minimum byte) bool {
	if protocolMajor(version) == protocolMajor(ProtocolVersionV2) {
		return true
	}
	return protocolMajor(version) == protocolMajor(minimum) && protocolMinor(version) >= protocolMinor(minimum)
}

// protocolAllowsDirectPeer reports whether direct-mode signaling may be used
// with a peer of the given version. Version zero is how legacy direct peers
// omit the capability field.
//
// Both generations support direct mode. V2 peers seal the candidate exchange
// with a key derived from the two X25519 public keys, so the relay forwards the
// envelopes without learning peer addresses; V1 peers exchange it in plaintext.
func protocolAllowsDirectPeer(version byte) bool {
	if version == 0 {
		return true
	}
	return protocolSupportsFeature(version, MinVersionDirect)
}

// protocolSupportsMigration reports whether channel migration may be used with
// a peer of the given version. Migration belongs to the direct data plane and
// is disabled for protocol V2.
func protocolSupportsMigration(version byte) bool {
	if protocolMajor(version) == protocolMajor(ProtocolVersionV2) {
		return false
	}
	return protocolSupportsFeature(version, MinVersionMigration)
}
