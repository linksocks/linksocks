package linksocks

// Feature minimums describe peer capabilities, not the current wire version.
const (
	MinVersionDirect    = byte(0x01)
	MinVersionMigration = byte(0x02)
	MinVersionWeb       = byte(0x03)
	MinVersionWebSocket = byte(0x04)
)

func protocolSupportsFeature(version, minimum byte) bool {
	return protocolMajor(version) == protocolMajor(minimum) && protocolMinor(version) >= protocolMinor(minimum)
}

// Version zero is also how legacy direct peers omit the capability field.
func protocolAllowsDirectPeer(version byte) bool {
	return version == 0 || protocolSupportsFeature(version, MinVersionDirect)
}

func protocolSupportsMigration(version byte) bool {
	return protocolSupportsFeature(version, MinVersionMigration)
}
