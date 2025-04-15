package wssocks

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
)

// Binary message format:
/*
All messages start with:
    Version(1) + Type(1)

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
    Version(1) + Type(1) + ChannelID(16)

ConnectorMessage:
    Version(1) + Type(1) + ChannelID(16) + TokenLen(1) + Token(N) + Operation(1)

ConnectorResponseMessage:
    Version(1) + Type(1) + ChannelID(16) + Success(1) +
    [if !Success: ErrorLen(1) + Error(N)] +
    [if Success && HasToken: TokenLen(1) + Token(N)]

ServerMessage:
    Version(1) + Type(1) + DataLen(4) + Data(N)
*/

const (
	// Protocol version
	ProtocolVersion = byte(0x01)

	// Binary message types
	BinaryTypeAuth              = byte(0x01)
	BinaryTypeAuthResponse      = byte(0x02)
	BinaryTypeConnect           = byte(0x03)
	BinaryTypeData              = byte(0x04)
	BinaryTypeConnectResponse   = byte(0x05)
	BinaryTypeDisconnect        = byte(0x06)
	BinaryTypeConnector         = byte(0x07)
	BinaryTypeConnectorResponse = byte(0x08)
	BinaryTypeLog               = byte(0x09)
	BinaryTypePartners          = byte(0x0A)

	// Protocol types
	BinaryProtocolTCP = byte(0x01)
	BinaryProtocolUDP = byte(0x02)

	// Binary connector operations
	BinaryConnectorOperationAdd    = byte(0x01)
	BinaryConnectorOperationRemove = byte(0x02)

	// Type strings
	TypeAuth              = "auth"
	TypeAuthResponse      = "auth_response"
	TypeConnect           = "connect"
	TypeData              = "data"
	TypeConnectResponse   = "connect_response"
	TypeDisconnect        = "disconnect"
	TypeConnector         = "connector"
	TypeConnectorResponse = "connector_response"
	TypeLog               = "log"
	TypePartners          = "partners"

	// Compression flags
	DataCompressionNone = byte(0x00)
	DataCompressionGzip = byte(0x01)
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
}

func (m DataMessage) GetType() string {
	return TypeData
}

// DisconnectMessage represents a connection termination message
type DisconnectMessage struct {
	ChannelID uuid.UUID `json:"channel_id"`
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

// PackMessage converts a message to binary format
func PackMessage(msg BaseMessage) ([]byte, error) {
	// Start with version
	buf := []byte{ProtocolVersion}

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
		return buf, nil

	case DisconnectMessage:
		buf = append(buf, BinaryTypeDisconnect)
		channelID, err := uuidToBytes(m.ChannelID.String())
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		buf = append(buf, channelID...)
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

	default:
		return nil, fmt.Errorf("unsupported message type for binary serialization")
	}
}

// ParseMessage parses a binary message
func ParseMessage(data []byte) (BaseMessage, error) {
	if len(data) < 2 { // Version + Type
		return nil, fmt.Errorf("message too short")
	}

	version := data[0]
	if version != ProtocolVersion {
		return nil, fmt.Errorf("unsupported protocol version: %d, data: %x", version, data)
	}

	msgType := data[1]
	payload := data[2:]

	switch msgType {
	case BinaryTypeAuth:
		if len(payload) < 1 {
			return nil, fmt.Errorf("invalid auth message")
		}
		tokenLen := int(payload[0])
		if len(payload) < 1+tokenLen+1+16 { // +16 for Instance UUID
			return nil, fmt.Errorf("invalid auth message length")
		}
		token := string(payload[1 : 1+tokenLen])
		reverse := byteToBool(payload[1+tokenLen])
		instance, err := uuid.Parse(bytesToUUID(payload[1+tokenLen+1 : 1+tokenLen+1+16]))
		if err != nil {
			return nil, fmt.Errorf("invalid Instance: %w", err)
		}
		return AuthMessage{
			Token:    token,
			Reverse:  reverse,
			Instance: instance,
		}, nil

	case BinaryTypeAuthResponse:
		if len(payload) < 1 {
			return nil, fmt.Errorf("invalid auth response message")
		}
		success := byteToBool(payload[0])
		msg := AuthResponseMessage{
			Success: success,
		}
		if !success && len(payload) > 1 {
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
		msg := ConnectMessage{
			Protocol:  protocol,
			ChannelID: channelID,
		}
		if protocol == "tcp" {
			payload = payload[17:]
			if len(payload) < 1 {
				return nil, fmt.Errorf("invalid tcp connect message")
			}
			addrLen := int(payload[0])
			if len(payload) < 1+addrLen+2 {
				return nil, fmt.Errorf("invalid tcp connect message length")
			}
			msg.Address = string(payload[1 : 1+addrLen])
			msg.Port = int(uint16(payload[1+addrLen])<<8 | uint16(payload[1+addrLen+1]))
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
		msg := ConnectResponseMessage{
			Success:   success,
			ChannelID: channelID,
		}
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

		// Handle decompression
		rawData := payload[22 : 22+dataLen]
		var decompressedData []byte
		if compression == DataCompressionGzip {
			r, err := gzip.NewReader(bytes.NewReader(rawData))
			if err != nil {
				return nil, fmt.Errorf("gzip reader creation failed: %w", err)
			}
			decompressedData, err = io.ReadAll(r)
			if err != nil {
				return nil, fmt.Errorf("gzip decompression failed: %w", err)
			}
			if err := r.Close(); err != nil {
				return nil, fmt.Errorf("gzip close failed: %w", err)
			}
		} else {
			decompressedData = rawData
		}

		msg := DataMessage{
			Protocol:    protocol,
			ChannelID:   channelID,
			Compression: compression,
			Data:        decompressedData,
		}
		if protocol == "udp" {
			payload = payload[22+int(dataLen):]
			if len(payload) < 1 {
				return nil, fmt.Errorf("invalid udp data message")
			}
			addrLen := int(payload[0])
			if len(payload) < 1+addrLen+2+1 {
				return nil, fmt.Errorf("invalid udp data message length")
			}
			msg.Address = string(payload[1 : 1+addrLen])
			msg.Port = int(uint16(payload[1+addrLen])<<8 | uint16(payload[1+addrLen+1]))
			payload = payload[1+addrLen+2:]
			targetAddrLen := int(payload[0])
			if len(payload) < 1+targetAddrLen+2 {
				return nil, fmt.Errorf("invalid udp data message target address")
			}
			msg.TargetAddr = string(payload[1 : 1+targetAddrLen])
			msg.TargetPort = int(uint16(payload[1+targetAddrLen])<<8 | uint16(payload[1+targetAddrLen+1]))
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
		return DisconnectMessage{
			ChannelID: channelID,
		}, nil

	case BinaryTypeConnector:
		if len(payload) < 16 { // ChannelID(16)
			return nil, fmt.Errorf("invalid connector message")
		}
		channelID, err := uuid.Parse(bytesToUUID(payload[:16]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		payload = payload[16:]
		if len(payload) < 1 {
			return nil, fmt.Errorf("invalid connector message length")
		}
		tokenLen := int(payload[0])
		if len(payload) < 1+tokenLen+1 { // +1 for operation
			return nil, fmt.Errorf("invalid connector message length")
		}
		token := string(payload[1 : 1+tokenLen])
		operation := bytesToOperation(payload[1+tokenLen])
		if operation == "" {
			return nil, fmt.Errorf("invalid operation type")
		}
		return ConnectorMessage{
			ChannelID:      channelID,
			ConnectorToken: token,
			Operation:      operation,
		}, nil

	case BinaryTypeConnectorResponse:
		if len(payload) < 17 { // ChannelID(16) + Success(1)
			return nil, fmt.Errorf("invalid connector response message")
		}
		channelID, err := uuid.Parse(bytesToUUID(payload[:16]))
		if err != nil {
			return nil, fmt.Errorf("invalid ChannelID: %w", err)
		}
		success := byteToBool(payload[16])
		msg := ConnectorResponseMessage{
			Success:   success,
			ChannelID: channelID,
		}
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

	default:
		return nil, fmt.Errorf("unknown binary message type: %d", msgType)
	}
}
