package wssocks

import (
	"encoding/json"
	"fmt"
)

const (
	TypeAuth              = "auth"
	TypeAuthResponse      = "auth_response"
	TypeConnect           = "connect"
	TypeData              = "data"
	TypeConnectResponse   = "connect_response"
	TypeDisconnect        = "disconnect"
	TypeConnector         = "connector"
	TypeConnectorResponse = "connector_response"
)

// BaseMessage defines the common interface for all message types
type BaseMessage interface {
	GetType() string
}

// AuthMessage represents an authentication request
type AuthMessage struct {
	Type    string `json:"type"`
	Token   string `json:"token"`
	Reverse bool   `json:"reverse"`
}

func (m AuthMessage) GetType() string {
	return m.Type
}

// AuthResponseMessage represents an authentication response
type AuthResponseMessage struct {
	Type    string `json:"type"`
	Success bool   `json:"success"`
	Error   string `json:"error,omitempty"`
}

func (m AuthResponseMessage) GetType() string {
	return m.Type
}

// ConnectMessage represents a TCP connection request
type ConnectMessage struct {
	Type      string `json:"type"`
	Protocol  string `json:"protocol"`
	ConnectID string `json:"connect_id"`
	Address   string `json:"address,omitempty"` // tcp only
	Port      int    `json:"port,omitempty"`    // tcp only
}

func (m ConnectMessage) GetType() string {
	return m.Type
}

// ConnectResponseMessage represents a connection response
type ConnectResponseMessage struct {
	Type      string `json:"type"`
	Success   bool   `json:"success"`
	Error     string `json:"error,omitempty"`
	ChannelID string `json:"channel_id"`
	ConnectID string `json:"connect_id"`
	Protocol  string `json:"protocol"`
}

func (m ConnectResponseMessage) GetType() string {
	return m.Type
}

// DataMessage represents a data transfer message
type DataMessage struct {
	Type       string `json:"type"`
	Protocol   string `json:"protocol"`
	ChannelID  string `json:"channel_id"`
	Data       string `json:"data"`                  // hex encoded
	Address    string `json:"address,omitempty"`     // udp only
	Port       int    `json:"port,omitempty"`        // udp only
	TargetAddr string `json:"target_addr,omitempty"` // udp only
	TargetPort int    `json:"target_port,omitempty"` // udp only
}

func (m DataMessage) GetType() string {
	return m.Type
}

// DisconnectMessage represents a connection termination message
type DisconnectMessage struct {
	Type      string `json:"type"`
	ChannelID string `json:"channel_id"`
	Protocol  string `json:"protocol"`
}

func (m DisconnectMessage) GetType() string {
	return m.Type
}

// ConnectorMessage represents a connector management command from reverse client
type ConnectorMessage struct {
	Type           string `json:"type"`
	ConnectID      string `json:"connect_id"`
	ConnectorToken string `json:"connector_token"`
	Operation      string `json:"operation"` // "add" or "remove"
}

func (m ConnectorMessage) GetType() string {
	return m.Type
}

// ConnectorResponseMessage represents a connector management response
type ConnectorResponseMessage struct {
	Type           string `json:"type"`
	Success        bool   `json:"success"`
	Error          string `json:"error,omitempty"`
	ConnectID      string `json:"connect_id"`
	ConnectorToken string `json:"connector_token,omitempty"`
}

func (m ConnectorResponseMessage) GetType() string {
	return m.Type
}

func unmarshalMessage[T any](data []byte) (*T, error) {
	var msg T
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func ParseMessage(data []byte) (BaseMessage, error) {
	var typeOnly struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(data, &typeOnly); err != nil {
		return nil, fmt.Errorf("failed to parse message type: %w", err)
	}

	switch typeOnly.Type {
	case TypeAuth:
		return unmarshalMessage[AuthMessage](data)
	case TypeAuthResponse:
		return unmarshalMessage[AuthResponseMessage](data)
	case TypeConnect:
		return unmarshalMessage[ConnectMessage](data)
	case TypeConnectResponse:
		return unmarshalMessage[ConnectResponseMessage](data)
	case TypeData:
		return unmarshalMessage[DataMessage](data)
	case TypeDisconnect:
		return unmarshalMessage[DisconnectMessage](data)
	case TypeConnector:
		return unmarshalMessage[ConnectorMessage](data)
	case TypeConnectorResponse:
		return unmarshalMessage[ConnectorResponseMessage](data)
	default:
		return nil, fmt.Errorf("unknown message type: %s", typeOnly.Type)
	}
}
