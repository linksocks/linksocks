package linksocks

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/google/uuid"
)

// Binary message format:
/*
All messages start with:
    Version(1) + Type(1)

Note on ProtocolVersion:
    The 1-byte ProtocolVersion encodes both major and minor version parts:
    - High nibble (bits 4-7): Major version
    - Low nibble (bits 0-3): Minor version

    Example: 0x01 means Major=0, Minor=1.

    Versioning rules:
    - Minor version update: Adding new semantics that don't break wire compatibility.
      A newer peer talking to an older peer MUST NOT trigger parse errors, and an older
      peer talking to a newer peer MUST remain compatible (though features may differ).
    - Major version update: Disruptive changes that break compatibility.
      Major version mismatches SHOULD NOT be allowed to connect and MUST be explicitly warned/rejected.

AuthMessage:
    Version(1) + Type(1) + TokenLen(1) + Token(N) + Reverse(1) + Instance(16)

AuthResponseMessage:
    Version(1) + Type(1) + Success(1) + [ErrorLen(1) + Error(N) if !Success]

ConnectMessage:
    Version(1) + Type(1) + Protocol(1) + ChannelID(16) + [AddrLen(1) + Addr(N) + Port(2) if TCP]

ConnectResponseMessage:
    Version(1) + Type(1) + Success(1) + ChannelID(16) + [ErrorLen(1) + Error(N) if !Success]

DataMessage:
    Version(1) + Type(1) + Protocol(1) + ChannelID(16) + Compression(1) + DataLen(4) + Data(N) +
    [if UDP: AddrLen(1) + Addr(N) + Port(2) + TargetAddrLen(1) + TargetAddr(N) + TargetPort(2)]

DisconnectMessage:
    Version(1) + Type(1) + ChannelID(16) + [ErrorLen(1) + Error(N) if HasError]

ConnectorMessage:
    Version(1) + Type(1) + ChannelID(16) + TokenLen(1) + Token(N) + Operation(1)

ConnectorResponseMessage:
    Version(1) + Type(1) + ChannelID(16) + Success(1) +
    [if !Success: ErrorLen(1) + Error(N)] +
    [if Success && HasToken: TokenLen(1) + Token(N)]

LogMessage:
    Version(1) + Type(1) + DataLen(4) + JSON{"level": string, "msg": string}

PartnersMessage:
    Version(1) + Type(1) + DataLen(4) + JSON{"count": int}

DirectCapabilitiesMessage:
	Version(1) + Type(1) + DataLen(4) + JSON{...}

DirectRendezvousMessage:
	Version(1) + Type(1) + DataLen(4) + JSON{...}

DirectStatusMessage:
	Version(1) + Type(1) + DataLen(4) + JSON{...}

ChannelBindMessage:
	Version(1) + Type(1) + DataLen(4) + JSON{...}

ChannelMigrateMessage:
	Version(1) + Type(1) + DataLen(4) + JSON{...}

ChannelMigrateAckMessage:
	Version(1) + Type(1) + DataLen(4) + JSON{...}

ChannelDataAckMessage:
	Version(1) + Type(1) + DataLen(4) + JSON{...}

ChannelOpenMessage:
    Version(1) + Type(1) + ChannelID(16) + [Resume(1) if set]

SealedMessage:
    Version(1) + Type(1) + ChannelID(16) + DataLen(4) + Data(N)
*/

const (
	// ProtocolVersion is packed as a single byte: 0xMN.
	//
	// Encoding:
	//   - M (high nibble) is the MAJOR version
	//   - N (low nibble) is the MINOR version
	//
	// Example: 0x04 => major=0, minor=4 (protocol V1).
	//
	// Versioning rules:
	//   - Adding a message type requires a MINOR version bump.
	//   - Wire-compatible changes to existing message semantics require a MINOR bump.
	//   - If a change to an existing message format would make the old parser fail,
	//     ask the project owner before bumping MAJOR.
	//
	// Connection policy:
	//   - Different MAJOR versions MUST NOT connect.
	//   - Different MINOR versions MAY connect, but SHOULD be clearly reported during the
	//     connection/auth phase.
	//
	// Generation V2 (major 2) replaces plaintext relay payloads with end-to-end
	// Noise-encrypted channels: peers open channels with ChannelOpenMessage, run
	// a Noise_XX handshake directly between them through the relay, and exchange
	// every subsequent payload as SealedMessage ciphertext. The relay only routes
	// frames by channel ID. V2 clients MUST NOT fall back to V1 peers or relays.
	ProtocolVersionV1 = byte(0x04)
	ProtocolVersionV2 = byte(0x20)

	// ProtocolVersion is the generation stamped on frames sent by this build.
	ProtocolVersion = ProtocolVersionV2

	// Binary message types
	BinaryTypeAuth                = byte(0x01)
	BinaryTypeAuthResponse        = byte(0x02)
	BinaryTypeConnect             = byte(0x03)
	BinaryTypeData                = byte(0x04)
	BinaryTypeConnectResponse     = byte(0x05)
	BinaryTypeDisconnect          = byte(0x06)
	BinaryTypeConnector           = byte(0x07)
	BinaryTypeConnectorResponse   = byte(0x08)
	BinaryTypeLog                 = byte(0x09)
	BinaryTypePartners            = byte(0x0A)
	BinaryTypeDirectCapabilities  = byte(0x0B)
	BinaryTypeDirectRendezvous    = byte(0x0C)
	BinaryTypeDirectStatus        = byte(0x0D)
	BinaryTypeChannelBind         = byte(0x0E)
	BinaryTypeChannelMigrate      = byte(0x0F)
	BinaryTypeChannelMigrateAck   = byte(0x10)
	BinaryTypeChannelDataAck      = byte(0x11)
	BinaryTypeWebRegister         = byte(0x12)
	BinaryTypeWebRegisterResponse = byte(0x13)
	BinaryTypeWebRequest          = byte(0x14)
	BinaryTypeWebResponse         = byte(0x15)
	BinaryTypeWebData             = byte(0x16)
	BinaryTypeWebCancel           = byte(0x17)
	BinaryTypeWebRotate           = byte(0x18)
	BinaryTypeWebSocketFrame      = byte(0x19)
	BinaryTypeChannelOpen         = byte(0x1A)
	BinaryTypeSealed              = byte(0x1B)
	BinaryProtocolTCP             = byte(0x01)
	BinaryProtocolUDP             = byte(0x02)

	// Binary connector operations
	BinaryConnectorOperationAdd    = byte(0x01)
	BinaryConnectorOperationRemove = byte(0x02)

	// Type strings
	TypeAuth                = "auth"
	TypeAuthResponse        = "auth_response"
	TypeConnect             = "connect"
	TypeData                = "data"
	TypeConnectResponse     = "connect_response"
	TypeDisconnect          = "disconnect"
	TypeConnector           = "connector"
	TypeConnectorResponse   = "connector_response"
	TypeLog                 = "log"
	TypePartners            = "partners"
	TypeDirectCapabilities  = "direct_capabilities"
	TypeDirectRendezvous    = "direct_rendezvous"
	TypeDirectStatus        = "direct_status"
	TypeChannelBind         = "channel_bind"
	TypeChannelMigrate      = "channel_migrate"
	TypeChannelMigrateAck   = "channel_migrate_ack"
	TypeChannelDataAck      = "channel_data_ack"
	TypeWebRegister         = "web_register"
	TypeWebRegisterResponse = "web_register_response"
	TypeWebRequest          = "web_request"
	TypeWebResponse         = "web_response"
	TypeWebData             = "web_data"
	TypeWebCancel           = "web_cancel"
	TypeWebRotate           = "web_rotate"
	TypeWebSocketFrame      = "websocket_frame"
	TypeChannelOpen         = "channel_open"
	TypeSealed              = "sealed"
	TypeUnknown             = "unknown"
	// Compression flags
	DataCompressionNone = byte(0x00)
	DataCompressionGzip = byte(0x01)
)

// protocolKnownMinorForMajor returns the newest minor version this build
// knows for a protocol generation, so peers can be compared against the right
// baseline regardless of which generation they speak.
func protocolKnownMinorForMajor(major byte) (byte, bool) {
	switch major {
	case protocolMajor(ProtocolVersionV2):
		return protocolMinor(ProtocolVersionV2), true
	case protocolMajor(ProtocolVersionV1):
		return protocolMinor(ProtocolVersionV1), true
	}
	return 0, false
}

// protocolMinorIsCurrent reports whether the peer speaks the newest minor
// version this build knows for the peer's generation.
func protocolMinorIsCurrent(v byte) bool {
	minor, ok := protocolKnownMinorForMajor(protocolMajor(v))
	return ok && protocolMinor(v) == minor
}

func protocolMajor(v byte) byte {
	return (v >> 4) & 0x0F
}

// protocolMajorSupported reports whether this build can parse frames of the
// given major version. Builds accept both the current generation and the
// legacy plaintext generation so one updated relay can serve V1 and V2
// clients simultaneously; peers of different majors are never paired, so a
// V2 session cannot silently downgrade to plaintext.
func protocolMajorSupported(v byte) bool {
	major := protocolMajor(v)
	return major == protocolMajor(ProtocolVersion) || major == protocolMajor(ProtocolVersionV1)
}

func protocolMinor(v byte) byte {
	return v & 0x0F
}

// ParseProtocolGeneration parses a protocol generation selector for CLI and
// API use. Accepted values are "v2"/"2" (end-to-end encrypted, the current
// generation) and "v1"/"1" (legacy plaintext), plus raw 0xMN encodings of
// either generation. An empty string means the current generation.
func ParseProtocolGeneration(s string) (byte, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "v2", "2":
		return ProtocolVersionV2, nil
	case "v1", "1":
		return ProtocolVersionV1, nil
	}
	parsed, err := ParseProtocolVersionParam(s)
	if err != nil {
		return 0, fmt.Errorf("invalid protocol version %q: want v2 or v1", s)
	}
	if !protocolMajorSupported(parsed) {
		return 0, fmt.Errorf("invalid protocol version %q: want v2 or v1", s)
	}
	return parsed, nil
}

var errUnsupportedProtocolMajor = errors.New("unsupported protocol major version")

func protocolVersionDebugString(v byte) string {
	return fmt.Sprintf("0x%02X (major=%d minor=%d)", v, protocolMajor(v), protocolMinor(v))
}

const (
	DirectServerHelloPrefix = "linksocks_server_hello:"
	DirectClientHelloPrefix = "linksocks_client_hello:"
	ProviderLivenessProbe   = "probe:"
	ProviderLivenessPong    = "pong:"
)

// BaseMessage defines the common interface for all message types
type BaseMessage interface {
	GetType() string
}

// AuthMessage represents an authentication request
type AuthMessage struct {
	Token    string    `json:"token"`
	Reverse  bool      `json:"reverse"`
	Instance uuid.UUID `json:"instance"`
}

func (m AuthMessage) GetType() string {
	return TypeAuth
}

// AuthResponseMessage represents an authentication response
type AuthResponseMessage struct {
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
	// Token is the actual token assigned by the server, returned
	// when the client authenticated with the anonymous token.
	Token string `json:"token,omitempty"`
}

func (m AuthResponseMessage) GetType() string {
	return TypeAuthResponse
}

// ConnectMessage represents a TCP connection request
type ConnectMessage struct {
	Protocol  string    `json:"protocol"`
	Address   string    `json:"address,omitempty"`
	Port      int       `json:"port,omitempty"`
	ChannelID uuid.UUID `json:"channel_id"`
	Resume    bool      `json:"resume,omitempty"`
}

func (m ConnectMessage) GetType() string {
	return TypeConnect
}

// ConnectResponseMessage represents a connection response
type ConnectResponseMessage struct {
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
	ChannelID uuid.UUID `json:"channel_id"`
}

func (m ConnectResponseMessage) GetType() string {
	return TypeConnectResponse
}

// DataMessage represents a data transfer message
type DataMessage struct {
	Protocol    string    `json:"protocol"`
	ChannelID   uuid.UUID `json:"channel_id"`
	Data        []byte    `json:"data"`
	Compression byte      `json:"compression,omitempty"`
	Address     string    `json:"address,omitempty"`
	Port        int       `json:"port,omitempty"`
	TargetAddr  string    `json:"target_addr,omitempty"`
	TargetPort  int       `json:"target_port,omitempty"`
	Sequence    uint64    `json:"sequence,omitempty"`
}

func (m DataMessage) GetType() string {
	return TypeData
}

// DisconnectMessage represents a connection termination message
type DisconnectMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Error     string    `json:"error,omitempty"`
}

func (m DisconnectMessage) GetType() string {
	return TypeDisconnect
}

// ConnectorMessage represents a connector management command from reverse client
type ConnectorMessage struct {
	ChannelID      uuid.UUID `json:"channel_id"`
	ConnectorToken string    `json:"connector_token"`
	Operation      string    `json:"operation"` // "add" or "remove"
}

func (m ConnectorMessage) GetType() string {
	return TypeConnector
}

// ConnectorResponseMessage represents a connector management response
type ConnectorResponseMessage struct {
	Success        bool      `json:"success"`
	Error          string    `json:"error,omitempty"`
	ChannelID      uuid.UUID `json:"channel_id"`
	ConnectorToken string    `json:"connector_token,omitempty"`
}

func (m ConnectorResponseMessage) GetType() string {
	return TypeConnectorResponse
}

// LogMessage represents a log message from server to client
type LogMessage struct {
	Level string `json:"level"`
	Msg   string `json:"msg"`
}

func (m LogMessage) GetType() string {
	return TypeLog
}

// PartnersMessage represents a partners count update message
type PartnersMessage struct {
	Count int `json:"count"`
}

func (m PartnersMessage) GetType() string {
	return TypePartners
}

type DirectCandidate struct {
	Addr string `json:"addr"`
	Port int    `json:"port"`
	Kind string `json:"kind,omitempty"` // e.g. srflx/relay/host
}

type DirectMetrics struct {
	RTTMs  int64  `json:"rtt_ms,omitempty"`
	Loss   int64  `json:"loss_ppm,omitempty"` // loss in parts-per-million
	Reason string `json:"reason,omitempty"`
}

type DirectCapabilitiesMessage struct {
	SessionID       uuid.UUID         `json:"session_id"`
	Candidates      []DirectCandidate `json:"candidates,omitempty"`
	Discoveries     []string          `json:"discoveries,omitempty"`
	PublicKey       []byte            `json:"public_key,omitempty"`
	ProtocolVersion byte              `json:"protocol_version,omitempty"`
}

func (m DirectCapabilitiesMessage) GetType() string {
	return TypeDirectCapabilities
}

type DirectRendezvousMessage struct {
	SessionID       uuid.UUID         `json:"session_id"`
	RemoteSessionID uuid.UUID         `json:"remote_session_id,omitempty"`
	Candidates      []DirectCandidate `json:"candidates,omitempty"`
	PublicKey       []byte            `json:"public_key,omitempty"`
	ProtocolVersion byte              `json:"protocol_version,omitempty"`
}

func (m DirectRendezvousMessage) GetType() string {
	return TypeDirectRendezvous
}

type DirectStatusMessage struct {
	SessionID uuid.UUID     `json:"session_id"`
	Status    string        `json:"status"` // probing|ready|failed|degraded|disabled
	Metrics   DirectMetrics `json:"metrics,omitempty"`
}

func (m DirectStatusMessage) GetType() string {
	return TypeDirectStatus
}

type ChannelBindMessage struct {
	ChannelID     uuid.UUID `json:"channel_id"`
	Protocol      string    `json:"protocol"`
	PeerSessionID uuid.UUID `json:"peer_session_id,omitempty"`
}

func (m ChannelBindMessage) GetType() string {
	return TypeChannelBind
}

type ChannelMigrateMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Reason    string    `json:"reason,omitempty"`
}

func (m ChannelMigrateMessage) GetType() string {
	return TypeChannelMigrate
}

type ChannelMigrateAckMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Success   bool      `json:"success"`
	Error     string    `json:"error,omitempty"`
}

func (m ChannelMigrateAckMessage) GetType() string {
	return TypeChannelMigrateAck
}

type ChannelDataAckMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Sequence  uint64    `json:"sequence"`
}

func (m ChannelDataAckMessage) GetType() string {
	return TypeChannelDataAck
}

// ChannelOpenMessage (protocol V2) asks the relay to bind a channel to a peer
// and to forward subsequent SealedMessage frames for the channel between the
// pair. It carries no destination information: the connector's real
// ConnectMessage travels sealed inside the channel.
type ChannelOpenMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Resume    bool      `json:"resume,omitempty"`
}

func (m ChannelOpenMessage) GetType() string {
	return TypeChannelOpen
}

// SealedMessage (protocol V2) carries one end-to-end encrypted frame for a
// channel. The relay forwards it by channel ID without parsing the payload.
type SealedMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Data      []byte    `json:"data"`
}

func (m SealedMessage) GetType() string {
	return TypeSealed
}

type WebBackend struct {
	URL    string `json:"url"`
	Weight int    `json:"weight,omitempty"`
}

type WebRegisterMessage struct {
	Host     string       `json:"host,omitempty"`
	Backends []WebBackend `json:"backends"`
	Insecure bool         `json:"insecure,omitempty"`
	Rotate   bool         `json:"rotate,omitempty"`
	Label    string       `json:"label,omitempty"`
}

func (m WebRegisterMessage) GetType() string {
	return TypeWebRegister
}

type WebRegisterResponseMessage struct {
	Success       bool   `json:"success"`
	Error         string `json:"error,omitempty"`
	Host          string `json:"host,omitempty"`
	URL           string `json:"url,omitempty"`
	Temporary     bool   `json:"temporary,omitempty"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	RotateSeconds int    `json:"rotate_seconds,omitempty"`
	BackendCount  int    `json:"backend_count,omitempty"`
	Owner         string `json:"owner,omitempty"`
}

func (m WebRegisterResponseMessage) GetType() string {
	return TypeWebRegisterResponse
}

type WebRequestMessage struct {
	ChannelID  uuid.UUID   `json:"channel_id"`
	Method     string      `json:"method"`
	Path       string      `json:"path"`
	Headers    [][2]string `json:"headers"`
	HasBody    bool        `json:"has_body,omitempty"`
	BodyLength int64       `json:"body_length,omitempty"`
	ClientIP   string      `json:"client_ip,omitempty"`
	Scheme     string      `json:"scheme,omitempty"`
	Host       string      `json:"host,omitempty"`
	WebSocket  bool        `json:"websocket,omitempty"`
}

func (m WebRequestMessage) GetType() string {
	return TypeWebRequest
}

type WebResponseMessage struct {
	ChannelID  uuid.UUID   `json:"channel_id"`
	Status     int         `json:"status"`
	Headers    [][2]string `json:"headers"`
	BodyLength int64       `json:"body_length,omitempty"`
}

func (m WebResponseMessage) GetType() string {
	return TypeWebResponse
}

type WebDataMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Data      []byte    `json:"data"`
	End       bool      `json:"end,omitempty"`
}

func (m WebDataMessage) GetType() string {
	return TypeWebData
}

type WebCancelMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Reason    string    `json:"reason,omitempty"`
	Code      int       `json:"code,omitempty"`
}

func (m WebCancelMessage) GetType() string {
	return TypeWebCancel
}

type WebSocketFrameMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
	Opcode    byte      `json:"opcode"`
	Data      []byte    `json:"data"`
}

func (m WebSocketFrameMessage) GetType() string {
	return TypeWebSocketFrame
}

type WebRotateMessage struct {
	Host          string `json:"host"`
	URL           string `json:"url"`
	ExpiresAt     int64  `json:"expires_at,omitempty"`
	RotateSeconds int    `json:"rotate_seconds,omitempty"`
}

func (m WebRotateMessage) GetType() string {
	return TypeWebRotate
}

type UnknownMessage struct {
	BinaryType byte `json:"binary_type"`
}

func (m UnknownMessage) GetType() string {
	return TypeUnknown
}

// LogLevel constants for ServerMessage
const (
	LogLevelTrace = "trace"
	LogLevelDebug = "debug"
	LogLevelInfo  = "info"
	LogLevelWarn  = "warn"
	LogLevelError = "error"
)

// Helper functions for protocol conversion
func protocolToBytes(protocol string) byte {
	switch protocol {
	case "tcp":
		return BinaryProtocolTCP
	case "udp":
		return BinaryProtocolUDP
	default:
		return 0
	}
}

func bytesToProtocol(b byte) string {
	switch b {
	case BinaryProtocolTCP:
		return "tcp"
	case BinaryProtocolUDP:
		return "udp"
	default:
		return ""
	}
}

func uuidToBytes(uuid string) ([]byte, error) {
	// Remove hyphens from UUID string
	uuid = strings.ReplaceAll(uuid, "-", "")
	if len(uuid) != 32 {
		return nil, fmt.Errorf("invalid UUID length")
	}
	return hex.DecodeString(uuid)
}

func bytesToUUID(b []byte) string {
	if len(b) != 16 {
		return ""
	}
	uuid := hex.EncodeToString(b)
	// Insert hyphens to make standard UUID format
	return fmt.Sprintf("%s-%s-%s-%s-%s",
		uuid[0:8], uuid[8:12], uuid[12:16], uuid[16:20], uuid[20:])
}

func boolToByte(b bool) byte {
	if b {
		return 1
	}
	return 0
}

func byteToBool(b byte) bool {
	return b != 0
}

func binaryUint64(b []byte) uint64 {
	return uint64(b[0])<<56 | uint64(b[1])<<48 | uint64(b[2])<<40 | uint64(b[3])<<32 |
		uint64(b[4])<<24 | uint64(b[5])<<16 | uint64(b[6])<<8 | uint64(b[7])
}

// Helper functions for operation conversion
func operationToBytes(operation string) byte {
	switch operation {
	case "add":
		return BinaryConnectorOperationAdd
	case "remove":
		return BinaryConnectorOperationRemove
	default:
		return 0
	}
}

func bytesToOperation(b byte) string {
	switch b {
	case BinaryConnectorOperationAdd:
		return "add"
	case BinaryConnectorOperationRemove:
		return "remove"
	default:
		return ""
	}
}

// PackMessage converts a message to binary format, stamped with the build's
// current protocol version.
func PackMessage(msg BaseMessage) ([]byte, error) {
	return PackMessageWithVersion(msg, ProtocolVersion)
}

// PackMessageWithVersion converts a message to binary format stamped with an
// explicit protocol version byte. Relays use this to answer each connection
// with the protocol generation that connection negotiated.
func PackMessageWithVersion(msg BaseMessage, version byte) ([]byte, error) {
	// Start with version
	buf := []byte{version}

	switch m := msg.(type) {
	case AuthMessage:
		buf = append(buf, BinaryTypeAuth)
		buf = append(buf, byte(len(m.Token)))
		buf = append(buf, []byte(m.Token)...)
		buf = append(buf, boolToByte(m.Reverse))
		instanceID, err := uuidToBytes(m.Instance.String())
		if err != nil {
			return nil, fmt.Errorf("invalid Instance: %w", err)
		}
		buf = append(buf, instanceID...)
		return buf, nil

	case AuthResponseMessage:
		buf = append(buf, BinaryTypeAuthResponse)
		buf = append(buf, boolToByte(m.Success))
		if !m.Success {
			buf = append(buf, byte(len(m.Error)))
			buf = append(buf, []byte(m.Error)...)
		} else if m.Token != "" {
			buf = append(buf, byte(len(m.Token)))
			buf = append(buf, []byte(m.Token)...)
		}
		return buf, nil

	case ConnectMessage:
		buf = append(buf, BinaryTypeConnect)
		buf = append(buf, protocolToBytes(m.Protocol))
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
		if m.Protocol == "tcp" {
			buf = append(buf, byte(len(m.Address)))
			buf = append(buf, []byte(m.Address)...)
			buf = append(buf, byte(m.Port>>8), byte(m.Port))
		}
		if m.Resume {
			buf = append(buf, 1)
		}
		return buf, nil

	case ConnectResponseMessage:
		buf = append(buf, BinaryTypeConnectResponse)
		buf = append(buf, boolToByte(m.Success))
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
		if !m.Success {
			buf = append(buf, byte(len(m.Error)))
			buf = append(buf, []byte(m.Error)...)
		}
		return buf, nil

	case DataMessage:
		buf = append(buf, BinaryTypeData)
		buf = append(buf, protocolToBytes(m.Protocol))
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)

		// Handle compression
		compressedData := m.Data
		compression := m.Compression
		if compression == DataCompressionGzip {
			var b bytes.Buffer
			w := gzip.NewWriter(&b)
			if _, err := w.Write(m.Data); err != nil {
				return nil, fmt.Errorf("gzip compression failed: %w", err)
			}
			if err := w.Close(); err != nil {
				return nil, fmt.Errorf("gzip close failed: %w", err)
			}
			compressedData = b.Bytes()
		}

		buf = append(buf, compression)
		dataLen := uint32(len(compressedData))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, compressedData...)
		if m.Protocol == "udp" {
			buf = append(buf, byte(len(m.Address)))
			buf = append(buf, []byte(m.Address)...)
			buf = append(buf, byte(m.Port>>8), byte(m.Port))
			buf = append(buf, byte(len(m.TargetAddr)))
			buf = append(buf, []byte(m.TargetAddr)...)
			buf = append(buf, byte(m.TargetPort>>8), byte(m.TargetPort))
		}
		if m.Sequence != 0 {
			buf = append(buf, byte(m.Sequence>>56), byte(m.Sequence>>48), byte(m.Sequence>>40), byte(m.Sequence>>32), byte(m.Sequence>>24), byte(m.Sequence>>16), byte(m.Sequence>>8), byte(m.Sequence))
		}
		return buf, nil

	case DisconnectMessage:
		buf = append(buf, BinaryTypeDisconnect)
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
		if m.Error != "" {
			buf = append(buf, byte(len(m.Error)))
			buf = append(buf, []byte(m.Error)...)
		}
		return buf, nil

	case ConnectorMessage:
		buf = append(buf, BinaryTypeConnector)
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
		buf = append(buf, byte(len(m.ConnectorToken)))
		buf = append(buf, []byte(m.ConnectorToken)...)
		buf = append(buf, operationToBytes(m.Operation))
		return buf, nil

	case ConnectorResponseMessage:
		buf = append(buf, BinaryTypeConnectorResponse)
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
		buf = append(buf, boolToByte(m.Success))
		if !m.Success {
			buf = append(buf, byte(len(m.Error)))
			buf = append(buf, []byte(m.Error)...)
		} else if m.ConnectorToken != "" {
			buf = append(buf, byte(len(m.ConnectorToken)))
			buf = append(buf, []byte(m.ConnectorToken)...)
		}
		return buf, nil

	case LogMessage:
		buf = append(buf, BinaryTypeLog)
		jsonData, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal log message: %w", err)
		}
		dataLen := uint32(len(jsonData))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, jsonData...)
		return buf, nil

	case PartnersMessage:
		buf = append(buf, BinaryTypePartners)
		jsonData, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal partners message: %w", err)
		}
		dataLen := uint32(len(jsonData))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, jsonData...)
		return buf, nil

	case DirectCapabilitiesMessage:
		buf = append(buf, BinaryTypeDirectCapabilities)
		jsonData, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal direct capabilities message: %w", err)
		}
		dataLen := uint32(len(jsonData))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, jsonData...)
		return buf, nil

	case DirectRendezvousMessage:
		buf = append(buf, BinaryTypeDirectRendezvous)
		jsonData, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal direct rendezvous message: %w", err)
		}
		dataLen := uint32(len(jsonData))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, jsonData...)
		return buf, nil

	case DirectStatusMessage:
		buf = append(buf, BinaryTypeDirectStatus)
		jsonData, err := json.Marshal(m)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal direct status message: %w", err)
		}
		dataLen := uint32(len(jsonData))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, jsonData...)
		return buf, nil

	case ChannelBindMessage:
		return packJSONMessage(buf, BinaryTypeChannelBind, m, "channel bind")

	case ChannelMigrateMessage:
		return packJSONMessage(buf, BinaryTypeChannelMigrate, m, "channel migrate")

	case ChannelMigrateAckMessage:
		return packJSONMessage(buf, BinaryTypeChannelMigrateAck, m, "channel migrate ack")

	case ChannelDataAckMessage:
		return packJSONMessage(buf, BinaryTypeChannelDataAck, m, "channel data ack")

	case ChannelOpenMessage:
		buf = append(buf, BinaryTypeChannelOpen)
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
		if m.Resume {
			buf = append(buf, 1)
		}
		return buf, nil

	case SealedMessage:
		buf = append(buf, BinaryTypeSealed)
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
		dataLen := uint32(len(m.Data))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, m.Data...)
		return buf, nil

	case WebRegisterMessage:
		return packJSONMessage(buf, BinaryTypeWebRegister, m, "web register")

	case WebRegisterResponseMessage:
		return packJSONMessage(buf, BinaryTypeWebRegisterResponse, m, "web register response")

	case WebRequestMessage:
		return packJSONMessage(buf, BinaryTypeWebRequest, m, "web request")

	case WebResponseMessage:
		return packJSONMessage(buf, BinaryTypeWebResponse, m, "web response")

	case WebDataMessage:
		buf = append(buf, BinaryTypeWebData)
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
		if m.End {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
		dataLen := uint32(len(m.Data))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, m.Data...)
		return buf, nil

	case WebCancelMessage:
		return packJSONMessage(buf, BinaryTypeWebCancel, m, "web cancel")

	case WebSocketFrameMessage:
		if m.Opcode != 1 && m.Opcode != 2 {
			return nil, fmt.Errorf("invalid WebSocket frame opcode")
		}
		buf = append(buf, BinaryTypeWebSocketFrame)
		buf = append(buf, m.ChannelID[:]...)
		buf = append(buf, m.Opcode)
		dataLen := uint32(len(m.Data))
		buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
		buf = append(buf, m.Data...)
		return buf, nil

	case WebRotateMessage:
		return packJSONMessage(buf, BinaryTypeWebRotate, m, "web rotate")

	default:
		return nil, fmt.Errorf("unsupported message type for binary serialization")
	}
}

func packJSONMessage(buf []byte, msgType byte, msg any, name string) ([]byte, error) {
	buf = append(buf, msgType)
	jsonData, err := json.Marshal(msg)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal %s message: %w", name, err)
	}
	dataLen := uint32(len(jsonData))
	buf = append(buf, byte(dataLen>>24), byte(dataLen>>16), byte(dataLen>>8), byte(dataLen))
	buf = append(buf, jsonData...)
	return buf, nil
}

// ParseProtocolVersionParam parses a peer-reported protocol version string.
//
// Supported formats:
//   - "0x01" / "0X01" (hex)
//   - "01" (hex byte)
//   - "1" (decimal)
func ParseProtocolVersionParam(s string) (byte, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty protocol version")
	}

	// Prefer explicit hex.
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		u, err := strconv.ParseUint(s[2:], 16, 8)
		if err != nil {
			return 0, fmt.Errorf("invalid protocol version hex: %w", err)
		}
		return byte(u), nil
	}

	// If it's exactly 2 hex digits, interpret as a byte.
	if len(s) == 2 {
		if u, err := strconv.ParseUint(s, 16, 8); err == nil {
			return byte(u), nil
		}
	}

	// Otherwise, parse as decimal.
	u, err := strconv.ParseUint(s, 10, 8)
	if err != nil {
		return 0, fmt.Errorf("invalid protocol version: %w", err)
	}
	return byte(u), nil
}

// ParseMessageWithVersion parses a binary message and returns the peer-reported
// protocol version byte.
func ParseMessageWithVersion(data []byte) (BaseMessage, byte, error) {
	if len(data) < 2 { // Version + Type
		return nil, 0, fmt.Errorf("message too short")
	}

	version := data[0]
	if !protocolMajorSupported(version) {
		return nil, version, fmt.Errorf(
			"%w: peer=%s local=%s",
			errUnsupportedProtocolMajor,
			protocolVersionDebugString(version),
			protocolVersionDebugString(ProtocolVersion),
		)
	}

	msgType := data[1]
	payload := data[2:]

	msg, err := parseMessageBody(msgType, payload)
	return msg, version, err
}

func parseMessageBody(msgType byte, payload []byte) (BaseMessage, error) {
	switch msgType {
	case BinaryTypeAuth:
		if len(payload) < 1 {
			return nil, fmt.Errorf("invalid auth message")
		}
		tokenLen := int(payload[0])
		if len(payload) < 1+tokenLen+1+16 { // TokenLen(1) + Token(N) + Reverse(1) + Instance(16)
			return nil, fmt.Errorf("invalid auth message length")
		}
		token := string(payload[1 : 1+tokenLen])
		reverse := byteToBool(payload[1+tokenLen])
		instance, err := uuid.Parse(bytesToUUID(payload[1+tokenLen+1 : 1+tokenLen+1+16]))
		if err != nil {
			return nil, fmt.Errorf("invalid Instance: %w", err)
		}
		return AuthMessage{Token: token, Reverse: reverse, Instance: instance}, nil

	case BinaryTypeAuthResponse:
		if len(payload) < 1 {
			return nil, fmt.Errorf("invalid auth response message")
		}
		success := byteToBool(payload[0])
		msg := AuthResponseMessage{Success: success}
		if success && len(payload) > 1 {
			tokenLen := int(payload[1])
			if len(payload) < 2+tokenLen {
				return nil, fmt.Errorf("invalid auth response token length")
			}
			msg.Token = string(payload[2 : 2+tokenLen])
		} else if !success && len(payload) > 1 {
			errorLen := int(payload[1])
			if len(payload) < 2+errorLen {
				return nil, fmt.Errorf("invalid auth response error length")
			}
			msg.Error = string(payload[2 : 2+errorLen])
		}
		return msg, nil

	case BinaryTypeConnect:
		if len(payload) < 17 { // Protocol(1) + ChannelID(16)
			return nil, fmt.Errorf("invalid connect message")
		}
		protocol := bytesToProtocol(payload[0])
		channelID, err := uuid.Parse(bytesToUUID(payload[1:17]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		msg := ConnectMessage{Protocol: protocol, ChannelID: channelID}
		if protocol == "tcp" {
			rest := payload[17:]
			if len(rest) < 1 {
				return nil, fmt.Errorf("invalid tcp connect message")
			}
			addrLen := int(rest[0])
			if len(rest) < 1+addrLen+2 {
				return nil, fmt.Errorf("invalid tcp connect message length")
			}
			msg.Address = string(rest[1 : 1+addrLen])
			msg.Port = int(uint16(rest[1+addrLen])<<8 | uint16(rest[1+addrLen+1]))
			if len(rest) > 1+addrLen+2 {
				msg.Resume = rest[1+addrLen+2] != 0
			}
		} else if len(payload) > 17 {
			msg.Resume = payload[17] != 0
		}
		return msg, nil

	case BinaryTypeConnectResponse:
		if len(payload) < 17 { // Success(1) + ChannelID(16)
			return nil, fmt.Errorf("invalid connect response message")
		}
		success := byteToBool(payload[0])
		channelID, err := uuid.Parse(bytesToUUID(payload[1:17]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		msg := ConnectResponseMessage{Success: success, ChannelID: channelID}
		if !success {
			if len(payload) < 18 {
				return nil, fmt.Errorf("invalid connect response error length")
			}
			errorLen := int(payload[17])
			if len(payload) < 18+errorLen {
				return nil, fmt.Errorf("invalid connect response message length")
			}
			msg.Error = string(payload[18 : 18+errorLen])
		}
		return msg, nil

	case BinaryTypeData:
		if len(payload) < 22 { // Protocol(1) + ChannelID(16) + Compression(1) + DataLen(4)
			return nil, fmt.Errorf("invalid data message")
		}
		protocol := bytesToProtocol(payload[0])
		channelID, err := uuid.Parse(bytesToUUID(payload[1:17]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		compression := payload[17]
		dataLen := uint32(payload[18])<<24 | uint32(payload[19])<<16 | uint32(payload[20])<<8 | uint32(payload[21])
		if len(payload) < 22+int(dataLen) {
			return nil, fmt.Errorf("invalid data message length")
		}

		rawData := payload[22 : 22+dataLen]
		var decompressedData []byte
		if compression == DataCompressionGzip {
			r, err := gzip.NewReader(bytes.NewReader(rawData))
			if err != nil {
				return nil, fmt.Errorf("gzip reader creation failed: %w", err)
			}
			// Bound decompressed output: a small compressed payload must not
			// balloon past the message read limit.
			decompressedData, err = io.ReadAll(io.LimitReader(r, int64(MaxWebSocketMessageSize)+1))
			if err != nil {
				_ = r.Close()
				return nil, fmt.Errorf("gzip decompression failed: %w", err)
			}
			if err := r.Close(); err != nil {
				return nil, fmt.Errorf("gzip close failed: %w", err)
			}
			if len(decompressedData) > int(MaxWebSocketMessageSize) {
				return nil, fmt.Errorf("gzip decompression exceeded size limit of %d bytes", MaxWebSocketMessageSize)
			}
		} else {
			decompressedData = rawData
		}

		msg := DataMessage{Protocol: protocol, ChannelID: channelID, Compression: compression, Data: decompressedData}
		if protocol == "udp" {
			rest := payload[22+int(dataLen):]
			if len(rest) < 1 {
				return nil, fmt.Errorf("invalid udp data message")
			}
			addrLen := int(rest[0])
			if len(rest) < 1+addrLen+2+1 { // +1 for TargetAddrLen
				return nil, fmt.Errorf("invalid udp data message length")
			}
			msg.Address = string(rest[1 : 1+addrLen])
			msg.Port = int(uint16(rest[1+addrLen])<<8 | uint16(rest[1+addrLen+1]))

			rest = rest[1+addrLen+2:]
			if len(rest) < 1 {
				return nil, fmt.Errorf("invalid udp data message")
			}
			targetAddrLen := int(rest[0])
			if len(rest) < 1+targetAddrLen+2 {
				return nil, fmt.Errorf("invalid udp data message target address")
			}
			msg.TargetAddr = string(rest[1 : 1+targetAddrLen])
			msg.TargetPort = int(uint16(rest[1+targetAddrLen])<<8 | uint16(rest[1+targetAddrLen+1]))
			rest = rest[1+targetAddrLen+2:]
			if len(rest) >= 8 {
				msg.Sequence = binaryUint64(rest[:8])
			}
		} else if len(payload) >= 22+int(dataLen)+8 {
			msg.Sequence = binaryUint64(payload[22+int(dataLen) : 22+int(dataLen)+8])
		}
		return msg, nil

	case BinaryTypeDisconnect:
		if len(payload) < 16 { // ChannelID(16)
			return nil, fmt.Errorf("invalid disconnect message")
		}
		channelID, err := uuid.Parse(bytesToUUID(payload[:16]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		msg := DisconnectMessage{ChannelID: channelID}
		if len(payload) > 16 {
			errLen := int(payload[16])
			if len(payload) < 17+errLen {
				return nil, fmt.Errorf("invalid disconnect message error length")
			}
			if errLen > 0 {
				msg.Error = string(payload[17 : 17+errLen])
			}
		}
		return msg, nil

	case BinaryTypeConnector:
		if len(payload) < 16 { // ChannelID(16)
			return nil, fmt.Errorf("invalid connector message")
		}

		channelID, err := uuid.Parse(bytesToUUID(payload[:16]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}

		rest := payload[16:]
		if len(rest) < 1 {
			return nil, fmt.Errorf("invalid connector message length")
		}

		tokenLen := int(rest[0])
		if len(rest) < 1+tokenLen+1 { // TokenLen(1) + Token(N) + Operation(1)
			return nil, fmt.Errorf("invalid connector message length")
		}

		token := string(rest[1 : 1+tokenLen])
		operation := bytesToOperation(rest[1+tokenLen])
		if len(operation) == 0 {
			return nil, fmt.Errorf("invalid operation type")
		}
		return ConnectorMessage{ChannelID: channelID, ConnectorToken: token, Operation: operation}, nil

	case BinaryTypeConnectorResponse:
		if len(payload) < 17 { // ChannelID(16) + Success(1)
			return nil, fmt.Errorf("invalid connector response message")
		}
		channelID, err := uuid.Parse(bytesToUUID(payload[:16]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		success := byteToBool(payload[16])
		msg := ConnectorResponseMessage{Success: success, ChannelID: channelID}
		if !success {
			if len(payload) < 18 {
				return nil, fmt.Errorf("invalid connector response error length")
			}
			errorLen := int(payload[17])
			if len(payload) < 18+errorLen {
				return nil, fmt.Errorf("invalid connector response message length")
			}
			msg.Error = string(payload[18 : 18+errorLen])
		} else if len(payload) > 17 {
			tokenLen := int(payload[17])
			if len(payload) < 18+tokenLen {
				return nil, fmt.Errorf("invalid connector response token length")
			}
			msg.ConnectorToken = string(payload[18 : 18+tokenLen])
		}
		return msg, nil

	case BinaryTypeLog:
		if len(payload) < 4 { // DataLen(4)
			return nil, fmt.Errorf("invalid log message")
		}
		dataLen := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
		if len(payload) < 4+int(dataLen) {
			return nil, fmt.Errorf("invalid log message length")
		}
		jsonData := payload[4 : 4+dataLen]
		var msg LogMessage
		if err := json.Unmarshal(jsonData, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal log message: %w", err)
		}
		return msg, nil

	case BinaryTypePartners:
		if len(payload) < 4 { // DataLen(4)
			return nil, fmt.Errorf("invalid partners message")
		}
		dataLen := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
		if len(payload) < 4+int(dataLen) {
			return nil, fmt.Errorf("invalid partners message length")
		}
		jsonData := payload[4 : 4+dataLen]
		var msg PartnersMessage
		if err := json.Unmarshal(jsonData, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal partners message: %w", err)
		}
		return msg, nil

	case BinaryTypeDirectCapabilities:
		if len(payload) < 4 { // DataLen(4)
			return nil, fmt.Errorf("invalid direct capabilities message")
		}
		dataLen := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
		if len(payload) < 4+int(dataLen) {
			return nil, fmt.Errorf("invalid direct capabilities message length")
		}
		jsonData := payload[4 : 4+dataLen]
		var msg DirectCapabilitiesMessage
		if err := json.Unmarshal(jsonData, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal direct capabilities message: %w", err)
		}
		return msg, nil

	case BinaryTypeDirectRendezvous:
		if len(payload) < 4 { // DataLen(4)
			return nil, fmt.Errorf("invalid direct rendezvous message")
		}
		dataLen := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
		if len(payload) < 4+int(dataLen) {
			return nil, fmt.Errorf("invalid direct rendezvous message length")
		}
		jsonData := payload[4 : 4+dataLen]
		var msg DirectRendezvousMessage
		if err := json.Unmarshal(jsonData, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal direct rendezvous message: %w", err)
		}
		return msg, nil

	case BinaryTypeDirectStatus:
		if len(payload) < 4 { // DataLen(4)
			return nil, fmt.Errorf("invalid direct status message")
		}
		dataLen := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
		if len(payload) < 4+int(dataLen) {
			return nil, fmt.Errorf("invalid direct status message length")
		}
		jsonData := payload[4 : 4+dataLen]
		var msg DirectStatusMessage
		if err := json.Unmarshal(jsonData, &msg); err != nil {
			return nil, fmt.Errorf("failed to unmarshal direct status message: %w", err)
		}
		return msg, nil

	case BinaryTypeChannelBind:
		var msg ChannelBindMessage
		if err := unmarshalJSONMessage(payload, "channel bind", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeChannelMigrate:
		var msg ChannelMigrateMessage
		if err := unmarshalJSONMessage(payload, "channel migrate", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeChannelMigrateAck:
		var msg ChannelMigrateAckMessage
		if err := unmarshalJSONMessage(payload, "channel migrate ack", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeChannelDataAck:
		var msg ChannelDataAckMessage
		if err := unmarshalJSONMessage(payload, "channel data ack", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeChannelOpen:
		if len(payload) < 16 { // ChannelID(16)
			return nil, fmt.Errorf("invalid channel open message")
		}
		channelID, err := uuid.Parse(bytesToUUID(payload[:16]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		msg := ChannelOpenMessage{ChannelID: channelID}
		if len(payload) > 16 {
			msg.Resume = payload[16] != 0
		}
		return msg, nil

	case BinaryTypeSealed:
		if len(payload) < 20 { // ChannelID(16) + DataLen(4)
			return nil, fmt.Errorf("invalid sealed message")
		}
		channelID, err := uuid.Parse(bytesToUUID(payload[:16]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		dataLen := uint32(payload[16])<<24 | uint32(payload[17])<<16 | uint32(payload[18])<<8 | uint32(payload[19])
		if len(payload) < 20+int(dataLen) {
			return nil, fmt.Errorf("invalid sealed message length")
		}
		return SealedMessage{ChannelID: channelID, Data: payload[20 : 20+dataLen]}, nil

	case BinaryTypeWebRegister:
		var msg WebRegisterMessage
		if err := unmarshalJSONMessage(payload, "web register", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeWebRegisterResponse:
		var msg WebRegisterResponseMessage
		if err := unmarshalJSONMessage(payload, "web register response", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeWebRequest:
		var msg WebRequestMessage
		if err := unmarshalJSONMessage(payload, "web request", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeWebResponse:
		var msg WebResponseMessage
		if err := unmarshalJSONMessage(payload, "web response", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeWebData:
		if len(payload) < 21 {
			return nil, fmt.Errorf("invalid web data message")
		}
		channelID, err := uuid.FromBytes(payload[:16])
		if err != nil {
			return nil, fmt.Errorf("invalid web data channel id: %w", err)
		}
		end := payload[16]&0x01 != 0
		dataLen := uint32(payload[17])<<24 | uint32(payload[18])<<16 | uint32(payload[19])<<8 | uint32(payload[20])
		if len(payload) < 21+int(dataLen) {
			return nil, fmt.Errorf("invalid web data message length")
		}
		data := append([]byte(nil), payload[21:21+dataLen]...)
		return WebDataMessage{ChannelID: channelID, Data: data, End: end}, nil

	case BinaryTypeWebCancel:
		var msg WebCancelMessage
		if err := unmarshalJSONMessage(payload, "web cancel", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	case BinaryTypeWebSocketFrame:
		if len(payload) < 21 {
			return nil, fmt.Errorf("invalid WebSocket frame message")
		}
		channelID, err := uuid.FromBytes(payload[:16])
		if err != nil {
			return nil, fmt.Errorf("invalid WebSocket channel id: %w", err)
		}
		opcode := payload[16]
		if opcode != 1 && opcode != 2 {
			return nil, fmt.Errorf("invalid WebSocket frame opcode")
		}
		dataLen := uint32(payload[17])<<24 | uint32(payload[18])<<16 | uint32(payload[19])<<8 | uint32(payload[20])
		if uint64(dataLen) != uint64(len(payload)-21) {
			return nil, fmt.Errorf("invalid WebSocket frame message length")
		}
		return WebSocketFrameMessage{ChannelID: channelID, Opcode: opcode, Data: append([]byte(nil), payload[21:]...)}, nil

	case BinaryTypeWebRotate:
		var msg WebRotateMessage
		if err := unmarshalJSONMessage(payload, "web rotate", &msg); err != nil {
			return nil, err
		}
		return msg, nil

	default:
		// For forward compatibility, return an UnknownMessage so upper layers can safely drop it.
		return UnknownMessage{BinaryType: msgType}, nil
	}
}

func unmarshalJSONMessage(payload []byte, name string, dst any) error {
	if len(payload) < 4 {
		return fmt.Errorf("invalid %s message", name)
	}
	dataLen := uint32(payload[0])<<24 | uint32(payload[1])<<16 | uint32(payload[2])<<8 | uint32(payload[3])
	if len(payload) < 4+int(dataLen) {
		return fmt.Errorf("invalid %s message length", name)
	}
	if err := json.Unmarshal(payload[4:4+dataLen], dst); err != nil {
		return fmt.Errorf("failed to unmarshal %s message: %w", name, err)
	}
	return nil
}

// ParseMessage parses a binary message.
//
// Compatibility policy:
//   - Major mismatch is rejected.
//   - Minor mismatch is accepted by design; see ProtocolVersion docs.
func ParseMessage(data []byte) (BaseMessage, error) {
	msg, _, err := ParseMessageWithVersion(data)
	return msg, err
}
