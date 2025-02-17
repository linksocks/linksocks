package wssocks

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

const (
	BufferSize          = 32 * 1024 // 32KB buffer size
	TypeAuth            = "auth"
	TypeAuthResponse    = "auth_response"
	TypeConnect         = "connect"
	TypeData            = "data"
	TypeConnectResponse = "connect_response"
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
	default:
		return nil, fmt.Errorf("unknown message type: %s", typeOnly.Type)
	}
}

// Relay handles stream transport between SOCKS5 and WebSocket
type Relay struct {
	log            zerolog.Logger
	messageQueues  sync.Map // map[string]chan Message
	tcpChannels    sync.Map // map[string]net.Conn
	udpChannels    sync.Map // map[string]*net.UDPConn
	udpClientAddrs sync.Map // map[string]*net.UDPAddr
	bufferSize     int
}

// NewRelay creates a new Relay instance
func NewRelay(logger zerolog.Logger, bufferSize int) *Relay {
	// If buffer size is not positive, use default
	if bufferSize <= 0 {
		bufferSize = BufferSize
	}

	return &Relay{
		log:        logger,
		bufferSize: bufferSize,
	}
}

// RefuseSocksRequest refuses a SOCKS5 client request with the specified reason
func (r *Relay) RefuseSocksRequest(conn net.Conn, reason byte) error {
	buffer := make([]byte, 1024)
	n, err := conn.Read(buffer)
	if err != nil {
		return fmt.Errorf("read error: %w", err)
	}
	if n == 0 || buffer[0] != 0x05 {
		return fmt.Errorf("invalid socks version")
	}

	// Send auth method response
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return fmt.Errorf("write auth response error: %w", err)
	}

	// Read request
	n, err = conn.Read(buffer)
	if err != nil {
		return fmt.Errorf("read request error: %w", err)
	}
	if n < 7 {
		return fmt.Errorf("request too short")
	}

	// Send refusal response
	response := []byte{
		0x05,                   // version
		reason,                 // reply code
		0x00,                   // reserved
		0x01,                   // address type (IPv4)
		0x00, 0x00, 0x00, 0x00, // IP address
		0x00, 0x00, // port
	}
	if _, err := conn.Write(response); err != nil {
		return fmt.Errorf("write refusal response error: %w", err)
	}

	return nil
}

// HandleNetworkConnection handles network connection based on protocol type
func (r *Relay) HandleNetworkConnection(ctx context.Context, ws *WSConn, request ConnectMessage) error {
	if request.Protocol == "tcp" {
		return r.HandleTCPConnection(ctx, ws, request)
	} else if request.Protocol == "udp" {
		return r.HandleUDPConnection(ctx, ws, request)
	}
	return fmt.Errorf("unsupported protocol: %s", request.Protocol)
}

// HandleTCPConnection handles TCP network connection
func (r *Relay) HandleTCPConnection(ctx context.Context, ws *WSConn, request ConnectMessage) error {
	// Generate channel_id
	channelID := uuid.New().String()

	if request.Port <= 0 || request.Port > 65535 {
		return fmt.Errorf("invalid port number: %d", request.Port)
	}

	// Connect to target
	targetAddr := fmt.Sprintf("%s:%d", request.Address, request.Port)
	r.log.Debug().Str("address", request.Address).Int("port", request.Port).
		Str("target", targetAddr).Msg("Attempting TCP connection to")

	conn, err := net.DialTimeout("tcp", targetAddr, 10*time.Second)
	if err != nil {
		r.log.Error().
			Err(err).
			Str("address", request.Address).
			Int("port", request.Port).
			Str("target", targetAddr).
			Msg("Failed to connect to target")

		response := ConnectResponseMessage{
			Type:      TypeConnectResponse,
			Success:   false,
			Error:     err.Error(),
			ConnectID: request.ConnectID,
		}
		r.logMessage(response, "send")
		if err := ws.SyncWriteJSON(response); err != nil {
			return fmt.Errorf("write error response error: %w", err)
		}
		return fmt.Errorf("tcp connect error: %w", err)
	}

	// Store connection
	r.tcpChannels.Store(channelID, conn)
	defer func() {
		conn.Close()
		r.tcpChannels.Delete(channelID)
	}()

	// Send success response
	response := ConnectResponseMessage{
		Type:      TypeConnectResponse,
		Success:   true,
		ChannelID: channelID,
		ConnectID: request.ConnectID,
		Protocol:  "tcp",
	}
	r.logMessage(response, "send")
	if err := ws.SyncWriteJSON(response); err != nil {
		return fmt.Errorf("write success response error: %w", err)
	}

	// Start relay
	return r.HandleRemoteTCPForward(ctx, ws, conn, channelID)
}

// HandleUDPConnection handles UDP network connection
func (r *Relay) HandleUDPConnection(ctx context.Context, ws *WSConn, request ConnectMessage) error {
	// Generate channel_id
	channelID := uuid.New().String()

	// Try dual-stack first
	localAddr := &net.UDPAddr{
		IP:   net.IPv6zero,
		Port: 0,
	}
	conn, err := net.ListenUDP("udp", localAddr)
	if err != nil {
		// Fallback to IPv4-only if dual-stack fails
		localAddr.IP = net.IPv4zero
		conn, err = net.ListenUDP("udp", localAddr)
		if err != nil {
			response := ConnectResponseMessage{
				Type:      TypeConnectResponse,
				Success:   false,
				Error:     err.Error(),
				ConnectID: request.ConnectID,
			}
			r.logMessage(response, "send")
			if err := ws.SyncWriteJSON(response); err != nil {
				return fmt.Errorf("write error response error: %w", err)
			}
			return fmt.Errorf("udp listen error: %w", err)
		}
	}

	// Store connection
	r.udpChannels.Store(channelID, conn)
	defer func() {
		conn.Close()
		r.udpChannels.Delete(channelID)
	}()

	// Send success response
	response := ConnectResponseMessage{
		Type:      TypeConnectResponse,
		Success:   true,
		ChannelID: channelID,
		ConnectID: request.ConnectID,
		Protocol:  "udp",
	}
	r.logMessage(response, "send")
	if err := ws.SyncWriteJSON(response); err != nil {
		return fmt.Errorf("write success response error: %w", err)
	}

	// Start relay
	return r.HandleRemoteUDPForward(ctx, ws, conn, channelID)
}

// HandleSocksRequest handles incoming SOCKS5 client request
func (r *Relay) HandleSocksRequest(ctx context.Context, ws *WSConn, socksConn net.Conn, socksUsername string, socksPassword string) error {
	buffer := make([]byte, 1024)

	// Read version and auth methods
	n, err := socksConn.Read(buffer)
	if err != nil {
		return fmt.Errorf("read version error: %w", err)
	}
	if n < 2 || buffer[0] != 0x05 {
		return fmt.Errorf("invalid socks version")
	}

	nmethods := int(buffer[1])
	methods := buffer[2 : 2+nmethods]

	if socksUsername != "" && socksPassword != "" {
		// Require username/password authentication
		var hasUserPass bool
		for _, method := range methods {
			if method == 0x02 {
				hasUserPass = true
				break
			}
		}
		if !hasUserPass {
			if _, err := socksConn.Write([]byte{0x05, 0xFF}); err != nil {
				return fmt.Errorf("write auth method error: %w", err)
			}
			return fmt.Errorf("no username/password auth method")
		}

		// Send auth method response (username/password)
		if _, err := socksConn.Write([]byte{0x05, 0x02}); err != nil {
			return fmt.Errorf("write auth response error: %w", err)
		}

		// Read auth version
		_, err = socksConn.Read(buffer[:1])
		if err != nil {
			return fmt.Errorf("read auth version error: %w", err)
		}
		if buffer[0] != 0x01 {
			return fmt.Errorf("invalid auth version")
		}

		// Read username length
		_, err = socksConn.Read(buffer[:1])
		if err != nil {
			return fmt.Errorf("read username length error: %w", err)
		}
		ulen := int(buffer[0])

		// Read username
		_, err = socksConn.Read(buffer[:ulen])
		if err != nil {
			return fmt.Errorf("read username error: %w", err)
		}
		username := string(buffer[:ulen])

		// Read password length
		_, err = socksConn.Read(buffer[:1])
		if err != nil {
			return fmt.Errorf("read password length error: %w", err)
		}
		plen := int(buffer[0])

		// Read password
		_, err = socksConn.Read(buffer[:plen])
		if err != nil {
			return fmt.Errorf("read password error: %w", err)
		}
		password := string(buffer[:plen])

		if username != socksUsername || password != socksPassword {
			if _, err := socksConn.Write([]byte{0x01, 0x01}); err != nil {
				return fmt.Errorf("write auth failure response error: %w", err)
			}
			return fmt.Errorf("authentication failed")
		}

		// Send auth success response
		if _, err := socksConn.Write([]byte{0x01, 0x00}); err != nil {
			return fmt.Errorf("write auth success response error: %w", err)
		}
	} else {
		// No authentication required
		if _, err := socksConn.Write([]byte{0x05, 0x00}); err != nil {
			return fmt.Errorf("write auth response error: %w", err)
		}
	}

	// Read request
	n, err = socksConn.Read(buffer)
	if err != nil {
		return fmt.Errorf("read request error: %w", err)
	}
	if n < 7 {
		return fmt.Errorf("request too short")
	}

	cmd := buffer[1]
	atyp := buffer[3]
	var targetAddr string
	var targetPort uint16
	var offset int

	// Parse address
	switch atyp {
	case 0x01: // IPv4
		if n < 10 {
			return fmt.Errorf("request too short for IPv4")
		}
		targetAddr = net.IP(buffer[4:8]).String()
		offset = 8
	case 0x03: // Domain
		domainLen := int(buffer[4])
		if n < 5+domainLen+2 {
			return fmt.Errorf("request too short for domain")
		}
		targetAddr = string(buffer[5 : 5+domainLen])
		offset = 5 + domainLen
	case 0x04: // IPv6
		if n < 22 {
			return fmt.Errorf("request too short for IPv6")
		}
		targetAddr = net.IP(buffer[4:20]).String()
		offset = 20
	default:
		return fmt.Errorf("unsupported address type: %d", atyp)
	}

	targetPort = binary.BigEndian.Uint16(buffer[offset : offset+2])

	// Generate unique client ID and connect ID
	connectID := uuid.New().String()
	r.log.Debug().Str("connect_id", connectID).Msg("Starting SOCKS request handling")

	// Handle different commands
	switch cmd {
	case 0x01: // CONNECT
		// Create temporary queue for connection response
		connectQueue := make(chan ConnectResponseMessage, 1)
		r.messageQueues.Store(connectID, connectQueue)
		defer r.messageQueues.Delete(connectID)

		// Send connection request to server
		requestData := ConnectMessage{
			Type:      TypeConnect,
			Protocol:  "tcp",
			Address:   targetAddr,
			Port:      int(targetPort),
			ConnectID: connectID,
		}
		r.logMessage(requestData, "send")
		if err := ws.SyncWriteJSON(requestData); err != nil {
			// Return connection failure response to SOCKS client (0x04 = Host unreachable)
			resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
			socksConn.Write(resp)
			return fmt.Errorf("write connect request error: %w", err)
		}

		// Wait for response with timeout
		var response ConnectResponseMessage
		select {
		case msg := <-connectQueue:
			response = msg
		case <-time.After(10 * time.Second):
			// Return connection failure response to SOCKS client (0x04 = Host unreachable)
			resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
			socksConn.Write(resp)
			return fmt.Errorf("remote connection response timeout")
		}

		if !response.Success {
			// Return connection failure response to SOCKS client (0x04 = Host unreachable)
			resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
			if _, err := socksConn.Write(resp); err != nil {
				return fmt.Errorf("write failure response error: %w", err)
			}
			r.log.Error().Str("error", response.Error).Msg("Target connection failed")
			return fmt.Errorf("remote connection failed: %s", response.Error)
		}

		r.log.Debug().Str("addr", targetAddr).Int("port", int(targetPort)).Msg("Remote successfully connected")

		// Send success response to client
		resp := []byte{
			0x05,                   // version
			0x00,                   // success
			0x00,                   // reserved
			0x01,                   // IPv4
			0x00, 0x00, 0x00, 0x00, // IP address
			0x00, 0x00, // port
		}
		if _, err := socksConn.Write(resp); err != nil {
			return fmt.Errorf("write success response error: %w", err)
		}

		// Start TCP relay
		return r.HandleSocksTCPForward(ctx, ws, socksConn, response.ChannelID)

	case 0x03: // UDP ASSOCIATE
		// Create UDP socket
		udpAddr, err := net.ResolveUDPAddr("udp", "127.0.0.1:0")
		if err != nil {
			return fmt.Errorf("resolve UDP addr error: %w", err)
		}

		udpConn, err := net.ListenUDP("udp", udpAddr)
		if err != nil {
			return fmt.Errorf("listen UDP error: %w", err)
		}

		localAddr := udpConn.LocalAddr().(*net.UDPAddr)

		// Create temporary queue for connection response
		connectQueue := make(chan ConnectResponseMessage, 1)
		r.messageQueues.Store(connectID, connectQueue)
		defer r.messageQueues.Delete(connectID)

		// Send UDP associate request to server
		requestData := ConnectMessage{
			Type:      TypeConnect,
			Protocol:  "udp",
			ConnectID: connectID,
		}
		r.logMessage(requestData, "send")
		if err := ws.SyncWriteJSON(requestData); err != nil {
			udpConn.Close()
			return fmt.Errorf("write UDP request error: %w", err)
		}

		// Wait for response with timeout
		var response ConnectResponseMessage
		select {
		case msg := <-connectQueue:
			response = msg
		case <-time.After(10 * time.Second):
			udpConn.Close()
			return fmt.Errorf("UDP association response timeout")
		}

		if !response.Success {
			udpConn.Close()
			return fmt.Errorf("UDP association failed: %s", response.Error)
		}

		// Send UDP associate response
		resp := []byte{
			0x05, // version
			0x00, // success
			0x00, // reserved
			0x01, // IPv4
		}
		resp = append(resp, localAddr.IP.To4()...)
		portBytes := make([]byte, 2)
		binary.BigEndian.PutUint16(portBytes, uint16(localAddr.Port))
		resp = append(resp, portBytes...)

		if _, err := socksConn.Write(resp); err != nil {
			udpConn.Close()
			return fmt.Errorf("write UDP associate response error: %w", err)
		}

		r.log.Debug().Int("port", localAddr.Port).Msg("UDP association established")

		// Monitor TCP connection for closure
		go func() {
			buffer := make([]byte, 1)
			socksConn.Read(buffer)
			udpConn.Close()
		}()

		// Start UDP relay
		return r.HandleSocksUDPForward(ctx, ws, udpConn, response.ChannelID)

	default:
		return fmt.Errorf("unsupported command: %d", cmd)
	}
}

// HandleRemoteTCPForward handles remote TCP forwarding
func (r *Relay) HandleRemoteTCPForward(ctx context.Context, ws *WSConn, remoteConn net.Conn, channelID string) error {
	msgChan := make(chan DataMessage, 100)
	r.messageQueues.Store(channelID, msgChan)
	defer r.messageQueues.Delete(channelID)

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// TCP to WebSocket
	go func() {
		defer wg.Done()
		buffer := make([]byte, r.bufferSize)
		for {
			n, err := remoteConn.Read(buffer)
			if err != nil {
				if err != io.EOF {
					errChan <- fmt.Errorf("remote read error: %w", err)
				}
				return
			}
			if n == 0 {
				return
			}

			msg := DataMessage{
				Type:      TypeData,
				Protocol:  "tcp",
				ChannelID: channelID,
				Data:      hex.EncodeToString(buffer[:n]),
			}
			r.logMessage(msg, "send")
			if err := ws.SyncWriteJSON(msg); err != nil {
				errChan <- fmt.Errorf("websocket write error: %w", err)
				return
			}
			r.log.Debug().Int("size", n).Msg("Sent TCP data to WebSocket")
		}
	}()

	// WebSocket to TCP
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-msgChan:
				data, err := hex.DecodeString(msg.Data)
				if err != nil {
					errChan <- fmt.Errorf("hex decode error: %w", err)
					return
				}

				_, err = remoteConn.Write(data)
				if err != nil {
					errChan <- fmt.Errorf("remote write error: %w", err)
					return
				}
				r.log.Debug().Int("size", len(data)).Msg("Sent TCP data to target")
			}
		}
	}()

	// Wait for completion or error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		return err
	case <-done:
		return nil
	}
}

// HandleRemoteUDPForward handles remote UDP forwarding
func (r *Relay) HandleRemoteUDPForward(ctx context.Context, ws *WSConn, udpConn *net.UDPConn, channelID string) error {
	msgChan := make(chan DataMessage, 100)
	r.messageQueues.Store(channelID, msgChan)
	defer r.messageQueues.Delete(channelID)

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// UDP to WebSocket
	go func() {
		defer wg.Done()
		buffer := make([]byte, r.bufferSize)
		for {
			n, remoteAddr, err := udpConn.ReadFromUDP(buffer)
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					errChan <- fmt.Errorf("udp read error: %w", err)
				}
				return
			}

			msg := DataMessage{
				Type:      TypeData,
				Protocol:  "udp",
				ChannelID: channelID,
				Data:      hex.EncodeToString(buffer[:n]),
				Address:   remoteAddr.IP.String(),
				Port:      remoteAddr.Port,
			}
			r.logMessage(msg, "send")
			if err := ws.SyncWriteJSON(msg); err != nil {
				errChan <- fmt.Errorf("websocket write error: %w", err)
				return
			}
			r.log.Debug().Int("size", n).Str("addr", remoteAddr.String()).Msg("Sent UDP data to WebSocket")
		}
	}()

	// WebSocket to UDP
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-msgChan:
				data, err := hex.DecodeString(msg.Data)
				if err != nil {
					errChan <- fmt.Errorf("hex decode error: %w", err)
					return
				}

				// Resolve domain name if necessary
				var targetIP net.IP
				if ip := net.ParseIP(msg.TargetAddr); ip != nil {
					targetIP = ip
				} else {
					// Attempt to resolve domain name
					addrs, err := net.LookupHost(msg.TargetAddr)
					if err != nil {
						r.log.Error().
							Err(err).
							Str("domain", msg.TargetAddr).
							Msg("Failed to resolve domain name")
						continue
					}
					// Parse the first resolved address
					targetIP = net.ParseIP(addrs[0])
					if targetIP == nil {
						r.log.Error().
							Str("addr", addrs[0]).
							Str("domain", msg.TargetAddr).
							Msg("Failed to parse resolved IP address")
						continue
					}
				}

				targetAddr := &net.UDPAddr{
					IP:   targetIP,
					Port: msg.TargetPort,
				}

				_, err = udpConn.WriteToUDP(data, targetAddr)
				if err != nil {
					errChan <- fmt.Errorf("udp write error: %w", err)
					return
				}
				r.log.Debug().
					Int("size", len(data)).
					Str("addr", targetAddr.String()).
					Str("original_addr", msg.TargetAddr).
					Str("original_port", fmt.Sprintf("%d", msg.TargetPort)).
					Msg("Sent UDP data to target")
			}
		}
	}()

	// Wait for completion or error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		return err
	case <-done:
		return nil
	}
}

// HandleSocksTCPForward handles TCP forwarding between SOCKS client and WebSocket
func (r *Relay) HandleSocksTCPForward(ctx context.Context, ws *WSConn, socksConn net.Conn, channelID string) error {
	// Create message queue for this channel
	msgChan := make(chan DataMessage, 100)
	r.messageQueues.Store(channelID, msgChan)
	defer r.messageQueues.Delete(channelID)
	defer socksConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// SOCKS to WebSocket
	go func() {
		defer wg.Done()
		buffer := make([]byte, r.bufferSize)
		for {
			n, err := socksConn.Read(buffer)
			if err != nil {
				if err != io.EOF {
					errChan <- fmt.Errorf("socks read error: %w", err)
				}
				return
			}
			if n == 0 {
				return
			}

			msg := DataMessage{
				Type:      TypeData,
				Protocol:  "tcp",
				ChannelID: channelID,
				Data:      hex.EncodeToString(buffer[:n]),
			}
			r.logMessage(msg, "send")
			if err := ws.SyncWriteJSON(msg); err != nil {
				errChan <- fmt.Errorf("websocket write error: %w", err)
				return
			}
			r.log.Debug().Int("size", n).Msg("Sent TCP data to WebSocket")
		}
	}()

	// WebSocket to SOCKS
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-msgChan:
				data, err := hex.DecodeString(msg.Data)
				if err != nil {
					errChan <- fmt.Errorf("hex decode error: %w", err)
					return
				}

				_, err = socksConn.Write(data)
				if err != nil {
					errChan <- fmt.Errorf("socks write error: %w", err)
					return
				}
				r.log.Debug().Int("size", len(data)).Msg("Sent TCP data to SOCKS")
			}
		}
	}()

	// Wait for completion or error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		return err
	case <-done:
		return nil
	}
}

// HandleSocksUDPForward handles SOCKS5 UDP forwarding
func (r *Relay) HandleSocksUDPForward(ctx context.Context, ws *WSConn, udpConn *net.UDPConn, channelID string) error {
	msgChan := make(chan DataMessage, 100)
	r.messageQueues.Store(channelID, msgChan)
	defer r.messageQueues.Delete(channelID)
	defer udpConn.Close()

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// UDP to WebSocket with SOCKS5 header handling
	go func() {
		defer wg.Done()
		buffer := make([]byte, BufferSize)
		for {
			n, remoteAddr, err := udpConn.ReadFromUDP(buffer)
			if err != nil {
				if !errors.Is(err, net.ErrClosed) {
					errChan <- fmt.Errorf("udp read error: %w", err)
				}
				return
			}

			r.udpClientAddrs.Store(channelID, remoteAddr)

			// Parse SOCKS UDP header
			if n > 3 { // Minimal UDP header
				atyp := buffer[3]
				var targetAddr string
				var targetPort int
				var payload []byte

				switch atyp {
				case 0x01: // IPv4
					addrBytes := buffer[4:8]
					targetAddr = net.IP(addrBytes).String()
					portBytes := buffer[8:10]
					targetPort = int(binary.BigEndian.Uint16(portBytes))
					payload = buffer[10:n]
				case 0x03: // Domain
					addrLen := int(buffer[4])
					addrBytes := buffer[5 : 5+addrLen]
					targetAddr = string(addrBytes)
					portBytes := buffer[5+addrLen : 7+addrLen]
					targetPort = int(binary.BigEndian.Uint16(portBytes))
					payload = buffer[7+addrLen : n]
				case 0x04: // IPv6
					addrBytes := buffer[4:20]
					targetAddr = net.IP(addrBytes).String()
					portBytes := buffer[20:22]
					targetPort = int(binary.BigEndian.Uint16(portBytes))
					payload = buffer[22:n]
				default:
					r.log.Debug().Msg("Cannot parse UDP packet from associated port")
					continue
				}

				msg := DataMessage{
					Type:       TypeData,
					Protocol:   "udp",
					ChannelID:  channelID,
					Data:       hex.EncodeToString(payload),
					TargetAddr: targetAddr,
					TargetPort: targetPort,
				}
				r.logMessage(msg, "send")
				if err := ws.SyncWriteJSON(msg); err != nil {
					errChan <- fmt.Errorf("websocket write error: %w", err)
					return
				}
				r.log.Debug().Int("size", len(payload)).Msg("Sent UDP data to WebSocket")
			}
		}
	}()

	// WebSocket to UDP with SOCKS5 header handling
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case msg := <-msgChan:
				data, err := hex.DecodeString(msg.Data)
				if err != nil {
					errChan <- fmt.Errorf("hex decode error: %w", err)
					return
				}

				// Construct SOCKS UDP header
				udpHeader := []byte{0, 0, 0} // RSV + FRAG

				// Try parsing as IPv4
				if ip := net.ParseIP(msg.Address); ip != nil {
					if ip4 := ip.To4(); ip4 != nil {
						udpHeader = append(udpHeader, 0x01) // IPv4
						udpHeader = append(udpHeader, ip4...)
					} else {
						udpHeader = append(udpHeader, 0x04) // IPv6
						udpHeader = append(udpHeader, ip...)
					}
				} else {
					// Treat as domain name
					domainBytes := []byte(msg.Address)
					udpHeader = append(udpHeader, 0x03) // Domain
					udpHeader = append(udpHeader, byte(len(domainBytes)))
					udpHeader = append(udpHeader, domainBytes...)
				}

				portBytes := make([]byte, 2)
				binary.BigEndian.PutUint16(portBytes, uint16(msg.Port))
				udpHeader = append(udpHeader, portBytes...)
				udpHeader = append(udpHeader, data...)

				addr, ok := r.udpClientAddrs.Load(msg.ChannelID)
				if !ok {
					r.log.Warn().Msg("Dropping UDP packet: no socks client address available")
					continue
				}

				clientAddr := addr.(*net.UDPAddr)
				if _, err := udpConn.WriteToUDP(udpHeader, clientAddr); err != nil {
					errChan <- fmt.Errorf("udp write error: %w", err)
					return
				}
				r.log.Debug().Int("size", len(data)).Str("addr", clientAddr.String()).Msg("Sent UDP data to SOCKS")
			}
		}
	}()

	// Wait for completion or error
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-errChan:
		return err
	case <-done:
		return nil
	}
}

// Add this helper method to Relay struct
func (r *Relay) logMessage(msg BaseMessage, direction string) {
	// Only process if debug level is enabled
	if !r.log.Debug().Enabled() {
		return
	}

	logEvent := r.log.Debug()

	// Create a copy for logging
	data, _ := json.Marshal(msg)
	var msgMap map[string]interface{}
	json.Unmarshal(data, &msgMap)

	// Remove sensitive fields and add data length
	if data, ok := msgMap["data"].(string); ok {
		msgMap["data_length"] = len(data)
		delete(msgMap, "data")
	}
	if _, ok := msgMap["token"]; ok {
		msgMap["token"] = "..."
	}

	logEvent = logEvent.Interface("msg", msgMap)
	logEvent.Msgf("WebSocket message TYPE=%s DIRECTION=%s", msg.GetType(), direction)
}
