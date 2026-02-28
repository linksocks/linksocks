package linksocks

// MessageWriter is the minimal transport abstraction required by the relay data path.
//
// It is intentionally small so that different transports (e.g. WebSocket, QUIC)
// can reuse the same forwarding logic.
type MessageWriter interface {
	WriteMessage(msg BaseMessage) error
	Label() string
}
