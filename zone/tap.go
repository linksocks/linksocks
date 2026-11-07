package zone

// l2Device is the Ethernet interface used by --vlan mode. Frames read from
// and written to the device are complete Ethernet frames, the same payload
// kind 1 messages carry.
type l2Device interface {
	Name() string
	ReadFrame(buf []byte) (int, error)
	WriteFrame(frame []byte) error
	Close() error
}
