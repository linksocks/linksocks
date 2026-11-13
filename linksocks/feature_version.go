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
// a peer of the given version: the V1 flow that moves an established channel
// back to the plaintext relay writer.
func protocolSupportsMigration(version byte) bool {
	if protocolMajor(version) == protocolMajor(ProtocolVersionV2) {
		return false
	}
	return protocolSupportsFeature(version, MinVersionMigration)
}

// protocolUsesPlaintextMigration reports whether a channel may move to the
// plaintext relay writer. Only V1 can: V2 keeps the payload sealed and resumes
// through a new sealed session instead.
func protocolUsesPlaintextMigration(version byte) bool {
	return protocolSupportsMigration(version)
}

// protocolUsesSealedResume reports whether a channel may replay its
// unacknowledged data through a fresh sealed session after its transport died.
// Only protocol V2 does this, and only for TCP: a datagram stream is reordered
// and lossy by design, so holding it to a contiguous sequence would drop live
// datagrams after any gap.
func protocolUsesSealedResume(version byte, protocol string) bool {
	return protocolMajor(version) == protocolMajor(ProtocolVersionV2) && protocol == "tcp"
}
