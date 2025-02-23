package wssocks

import (
	"context"
	"encoding/binary"
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
	// DefaultBufferSize is the size of reusable buffers
	// Larger buffers improve throughput but consume more memory
	DefaultBufferSize     = 32 * 1024 // 32KB buffer size
	DefaultChannelTimeout = 12 * time.Hour
	DefaultConnectTimeout = 10 * time.Second
)

// RelayOption contains configuration options for Relay
type RelayOption struct {
	// BufferSize controls the size of reusable buffers
	// Larger values may improve performance but increase memory usage
	BufferSize     int
	ChannelTimeout time.Duration
	ConnectTimeout time.Duration
}

// Global buffer pool using pointer type
var bufferPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, DefaultBufferSize)
		return &b
	},
}

// NewDefaultRelayOption creates a RelayOption with default values
func NewDefaultRelayOption() *RelayOption {
	return &RelayOption{
		BufferSize:     DefaultBufferSize,
		ChannelTimeout: DefaultChannelTimeout,
		ConnectTimeout: DefaultConnectTimeout,
	}
}

// WithBufferSize sets the buffer size for the relay
func (o *RelayOption) WithBufferSize(size int) *RelayOption {
	o.BufferSize = size
	return o
}

// WithChannelTimeout sets the channel timeout for the relay
func (o *RelayOption) WithChannelTimeout(timeout time.Duration) *RelayOption {
	o.ChannelTimeout = timeout
	return o
}

// WithConnectTimeout sets the connect timeout for the relay
func (o *RelayOption) WithConnectTimeout(timeout time.Duration) *RelayOption {
	o.ConnectTimeout = timeout
	return o
}

// Relay handles stream transport between SOCKS5 and WebSocket
type Relay struct {
	log            zerolog.Logger
	messageQueues  sync.Map // map[uuid.UUID]chan Message
	tcpChannels    sync.Map // map[uuid.UUID]context.CancelFunc
	udpChannels    sync.Map // map[uuid.UUID]context.CancelFunc
	udpClientAddrs sync.Map // map[uuid.UUID]*net.UDPAddr
	lastActivity   sync.Map // map[uuid.UUID]time.Time
	option         *RelayOption
	done           chan struct{}
}

// NewRelay creates a new Relay instance
func NewRelay(logger zerolog.Logger, option *RelayOption) *Relay {
	if option == nil {
		option = NewDefaultRelayOption()
	}

	r := &Relay{
		log:    logger,
		option: option,
		done:   make(chan struct{}),
	}

	go r.channelCleaner()

	return r
}

func (r *Relay) channelCleaner() {
	ticker := time.NewTicker(60 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.done:
			return
		case <-ticker.C:
			now := time.Now()

			// Check TCP channels
			r.tcpChannels.Range(func(key, value interface{}) bool {
				channelID := key.(uuid.UUID)
				cancel := value.(context.CancelFunc)

				if lastTime, ok := r.lastActivity.Load(channelID); ok {
					if now.Sub(lastTime.(time.Time)) > r.option.ChannelTimeout {
						r.log.Trace().
							Str("channel_id", channelID.String()).
							Str("type", "tcp").
							Dur("timeout", r.option.ChannelTimeout).
							Msg("Channel timed out, closing")
						cancel()
						r.tcpChannels.Delete(channelID)
						r.lastActivity.Delete(channelID)
					}
				}
				return true
			})

			// Check UDP channels
			r.udpChannels.Range(func(key, value interface{}) bool {
				channelID := key.(uuid.UUID)
				cancel := value.(context.CancelFunc)

				if lastTime, ok := r.lastActivity.Load(channelID); ok {
					if now.Sub(lastTime.(time.Time)) > r.option.ChannelTimeout {
						r.log.Trace().
							Str("channel_id", channelID.String()).
							Str("type", "udp").
							Dur("timeout", r.option.ChannelTimeout).
							Msg("Channel timed out, closing")
						cancel()
						r.udpChannels.Delete(channelID)
						r.lastActivity.Delete(channelID)
					}
				}
				return true
			})
		}
	}
}

// Add new method to update activity timestamp
func (r *Relay) updateActivityTime(channelID uuid.UUID) {
	r.lastActivity.Store(channelID, time.Now())
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
		if err == io.EOF {
			r.log.Debug().Msg("Client closed SOCKS connection")
			return nil
		}
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
	channelID := uuid.New()

	if request.Port <= 0 || request.Port > 65535 {
		return fmt.Errorf("invalid port number: %d", request.Port)
	}

	// Connect to target
	targetAddr := fmt.Sprintf("%s:%d", request.Address, request.Port)
	r.log.Debug().Str("address", request.Address).Int("port", request.Port).
		Str("target", targetAddr).Msg("Attempting TCP connection to")

	conn, err := net.DialTimeout("tcp", targetAddr, r.option.ConnectTimeout)
	if err != nil {
		r.log.Debug().
			Err(err).
			Str("address", request.Address).
			Int("port", request.Port).
			Str("target", targetAddr).
			Msg("Failed to connect to target")

		response := ConnectResponseMessage{
			Success:   false,
			Error:     err.Error(),
			ConnectID: request.ConnectID,
		}
		r.logMessage(response, "send", ws.Label())
		if err := ws.WriteMessage(response); err != nil {
			return fmt.Errorf("write error response error: %w", err)
		}
		return nil
	}

	// Create child context
	childCtx, cancel := context.WithCancel(ctx)
	r.tcpChannels.Store(channelID, cancel)
	defer func() {
		cancel()
		conn.Close()
		r.tcpChannels.Delete(channelID)
		r.lastActivity.Delete(channelID)
	}()

	// Send success response
	response := ConnectResponseMessage{
		Success:   true,
		ChannelID: channelID,
		ConnectID: request.ConnectID,
		Protocol:  "tcp",
	}
	r.logMessage(response, "send", ws.Label())
	if err := ws.WriteMessage(response); err != nil {
		return fmt.Errorf("write success response error: %w", err)
	}

	// Start relay with child context
	return r.HandleRemoteTCPForward(childCtx, ws, conn, channelID)
}

// HandleUDPConnection handles UDP network connection
func (r *Relay) HandleUDPConnection(ctx context.Context, ws *WSConn, request ConnectMessage) error {
	// Generate channel_id
	channelID := uuid.New()

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
				Success:   false,
				Error:     err.Error(),
				ConnectID: request.ConnectID,
			}
			r.logMessage(response, "send", ws.Label())
			if err := ws.WriteMessage(response); err != nil {
				return fmt.Errorf("write error response error: %w", err)
			}
			return fmt.Errorf("udp listen error: %w", err)
		}
	}

	// Create child context
	childCtx, cancel := context.WithCancel(ctx)
	r.udpChannels.Store(channelID, cancel)
	defer func() {
		cancel()
		conn.Close()
		r.udpChannels.Delete(channelID)
		r.lastActivity.Delete(channelID)
	}()

	// Send success response
	response := ConnectResponseMessage{
		Success:   true,
		ChannelID: channelID,
		ConnectID: request.ConnectID,
		Protocol:  "udp",
	}
	r.logMessage(response, "send", ws.Label())
	if err := ws.WriteMessage(response); err != nil {
		return fmt.Errorf("write success response error: %w", err)
	}

	// Start relay with child context
	return r.HandleRemoteUDPForward(childCtx, ws, conn, channelID)
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
		if err == io.EOF {
			r.log.Debug().Msg("Client closed SOCKS connection")
			return nil
		}
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
	connectID := uuid.New()
	r.log.Trace().Str("connect_id", connectID.String()).Msg("Starting SOCKS request handling")

	// Handle different commands
	switch cmd {
	case 0x01: // CONNECT
		// Create temporary queue for connection response
		connectQueue := make(chan ConnectResponseMessage, 1)
		r.messageQueues.Store(connectID, connectQueue)
		defer r.messageQueues.Delete(connectID)

		// Send connection request to server
		requestData := ConnectMessage{
			Protocol:  "tcp",
			Address:   targetAddr,
			Port:      int(targetPort),
			ConnectID: connectID,
		}
		r.log.Debug().Str("address", targetAddr).Int("port", int(targetPort)).Msg("Requesting TCP connecting to")
		r.logMessage(requestData, "send", ws.Label())
		if err := ws.WriteMessage(requestData); err != nil {
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
		case <-time.After(r.option.ConnectTimeout + 5*time.Second):
			// Return connection failure response to SOCKS client (0x04 = Host unreachable)
			resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
			socksConn.Write(resp)
			r.log.Debug().Str("addr", targetAddr).Int("port", int(targetPort)).Msg("Remote connection response timeout")
			return nil
		}

		if !response.Success {
			// Return connection failure response to SOCKS client (0x04 = Host unreachable)
			resp := []byte{0x05, 0x04, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00}
			if _, err := socksConn.Write(resp); err != nil {
				return fmt.Errorf("write failure response error: %w", err)
			}
			r.log.Debug().Str("error", response.Error).Msg("Remote connection failed")
			return nil
		}

		r.log.Trace().Str("addr", targetAddr).Int("port", int(targetPort)).Msg("Remote successfully connected")

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
			Protocol:  "udp",
			ConnectID: connectID,
		}
		r.log.Debug().Msg("Requesting UDP Associate")
		r.logMessage(requestData, "send", ws.Label())
		if err := ws.WriteMessage(requestData); err != nil {
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

		r.log.Trace().Int("port", localAddr.Port).Msg("UDP association established")

		// Monitor TCP connection for closure
		go func() {
			buffer := make([]byte, 1)
			socksConn.Read(buffer)
			udpConn.Close()
		}()

		// Start UDP relay
		return r.HandleSocksUDPForward(ctx, ws, udpConn, socksConn, response.ChannelID)

	default:
		return fmt.Errorf("unsupported command: %d", cmd)
	}
}

// HandleRemoteTCPForward handles remote TCP forwarding
func (r *Relay) HandleRemoteTCPForward(ctx context.Context, ws *WSConn, remoteConn net.Conn, channelID uuid.UUID) error {
	// Initialize activity time
	r.updateActivityTime(channelID)

	msgChan := make(chan DataMessage, 100)
	r.messageQueues.Store(channelID, msgChan)
	defer r.messageQueues.Delete(channelID)

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// TCP to WebSocket
	go func() {
		defer wg.Done()

		// Get a buffer from the pool
		buffer := *(bufferPool.Get().(*[]byte))
		// Return the buffer to the pool when done
		defer bufferPool.Put(&buffer)

		for {
			n, err := remoteConn.Read(buffer)
			if err != nil {
				if err == io.EOF {
					r.log.Trace().Msg("Remote connection closed")
					disconnectMsg := DisconnectMessage{
						ChannelID: channelID,
					}
					r.logMessage(disconnectMsg, "send", ws.Label())
					ws.WriteMessage(disconnectMsg)
				} else if opErr, ok := err.(*net.OpError); ok {
					if errors.Is(opErr.Err, net.ErrClosed) {
						r.log.Trace().Msg("TCP connection closed as instructed by connector")
					} else {
						r.log.Debug().Err(err).Msg("Remote TCP read error")
						errChan <- fmt.Errorf("remote read error: %w", err)
					}
				} else {
					r.log.Debug().Err(err).Msg("Unexpected connection error")
					errChan <- fmt.Errorf("remote read error: %w", err)
				}
				return
			}
			if n == 0 {
				return
			}

			// Update activity time
			r.updateActivityTime(channelID)

			// Create a new slice only for the actual data to be sent
			// This ensures we don't hold onto more memory than needed
			data := make([]byte, n)
			copy(data, buffer[:n])

			// Prepare the message for WebSocket transmission
			msg := DataMessage{
				Protocol:    "tcp",
				ChannelID:   channelID,
				Data:        data,
				Compression: r.determineCompression(n),
			}

			// Log and send the message
			r.logMessage(msg, "send", ws.Label())
			if err := ws.WriteMessage(msg); err != nil {
				errChan <- fmt.Errorf("websocket write error: %w", err)
				return
			}
			r.log.Trace().Int("size", n).Msg("Sent TCP data to WebSocket")
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
				// Update activity time
				r.updateActivityTime(channelID)

				_, err := remoteConn.Write(msg.Data)
				if err != nil {
					errChan <- fmt.Errorf("remote write error: %w", err)
					return
				}
				r.log.Trace().Int("size", len(msg.Data)).Msg("Sent TCP data to target")
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
func (r *Relay) HandleRemoteUDPForward(ctx context.Context, ws *WSConn, udpConn *net.UDPConn, channelID uuid.UUID) error {
	// Initialize activity time
	r.updateActivityTime(channelID)

	msgChan := make(chan DataMessage, 100)
	r.messageQueues.Store(channelID, msgChan)
	defer r.messageQueues.Delete(channelID)

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// UDP to WebSocket
	go func() {
		defer wg.Done()
		buffer := make([]byte, r.option.BufferSize)
		for {
			n, remoteAddr, err := udpConn.ReadFromUDP(buffer)
			if err != nil {
				if opErr, ok := err.(*net.OpError); ok {
					if errors.Is(opErr.Err, net.ErrClosed) {
						r.log.Trace().Msg("UDP connection closed as instructed by connector")
					} else {
						r.log.Debug().Err(err).Msg("Remote UDP read error")
						errChan <- fmt.Errorf("udp read error: %w", err)
					}
				} else {
					errChan <- fmt.Errorf("udp read error: %w", err)
				}
				return
			}

			// Update activity time
			r.updateActivityTime(channelID)

			msg := DataMessage{
				Protocol:    "udp",
				ChannelID:   channelID,
				Data:        buffer[:n],
				Address:     remoteAddr.IP.String(),
				Port:        remoteAddr.Port,
				Compression: r.determineCompression(n),
			}
			r.logMessage(msg, "send", ws.Label())
			if err := ws.WriteMessage(msg); err != nil {
				errChan <- fmt.Errorf("websocket write error: %w", err)
				return
			}
			r.log.Trace().Int("size", n).Str("addr", remoteAddr.String()).Msg("Sent UDP data to WebSocket")
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
				// Update activity time
				r.updateActivityTime(channelID)

				// Resolve domain name if necessary
				var targetIP net.IP
				if ip := net.ParseIP(msg.TargetAddr); ip != nil {
					targetIP = ip
				} else {
					// Attempt to resolve domain name
					addrs, err := net.LookupHost(msg.TargetAddr)
					if err != nil {
						r.log.Debug().
							Err(err).
							Str("domain", msg.TargetAddr).
							Msg("Failed to resolve domain name")
						continue
					}
					// Parse the first resolved address
					targetIP = net.ParseIP(addrs[0])
					if targetIP == nil {
						r.log.Debug().
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

				_, err := udpConn.WriteToUDP(msg.Data, targetAddr)
				if err != nil {
					errChan <- fmt.Errorf("udp write error: %w", err)
					return
				}
				r.log.Trace().
					Int("size", len(msg.Data)).
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
func (r *Relay) HandleSocksTCPForward(ctx context.Context, ws *WSConn, socksConn net.Conn, channelID uuid.UUID) error {
	// Create a child context that can be cancelled
	ctx, cancel := context.WithCancel(ctx)
	r.tcpChannels.Store(channelID, cancel)
	defer func() {
		cancel()
		r.tcpChannels.Delete(channelID)
		r.lastActivity.Delete(channelID)
	}()

	// Send disconnect message
	defer func() {
		disconnectMsg := DisconnectMessage{
			ChannelID: channelID,
		}
		r.logMessage(disconnectMsg, "send", ws.Label())
		ws.WriteMessage(disconnectMsg)
	}()

	// Create message queue for this channel
	msgChan := make(chan DataMessage, 100)
	r.messageQueues.Store(channelID, msgChan)
	defer r.messageQueues.Delete(channelID)

	var wg sync.WaitGroup
	wg.Add(2)
	errChan := make(chan error, 2)

	// SOCKS to WebSocket
	go func() {
		defer wg.Done()
		defer cancel()

		// Get a buffer from the pool
		buffer := *(bufferPool.Get().(*[]byte))
		// Return the buffer to the pool when done
		defer bufferPool.Put(&buffer)

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

			// Update last activity timestamp
			r.updateActivityTime(channelID)

			// Create a new slice only for the actual data to be sent
			data := make([]byte, n)
			copy(data, buffer[:n])

			// Prepare the message for WebSocket transmission
			msg := DataMessage{
				Protocol:    "tcp",
				ChannelID:   channelID,
				Data:        data,
				Compression: r.determineCompression(n),
			}

			// Log and send the message
			r.logMessage(msg, "send", ws.Label())
			if err := ws.WriteMessage(msg); err != nil {
				errChan <- fmt.Errorf("websocket write error: %w", err)
				return
			}
			r.log.Trace().Int("size", n).Msg("Sent TCP data to WebSocket")
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
				// Update activity time
				r.updateActivityTime(channelID)

				_, err := socksConn.Write(msg.Data)
				if err != nil {
					errChan <- fmt.Errorf("socks write error: %w", err)
					return
				}
				r.log.Trace().Int("size", len(msg.Data)).Msg("Sent TCP data to SOCKS")
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
func (r *Relay) HandleSocksUDPForward(ctx context.Context, ws *WSConn, udpConn *net.UDPConn, socksConn net.Conn, channelID uuid.UUID) error {
	// Create a child context that can be cancelled
	ctx, cancel := context.WithCancel(ctx)
	r.udpChannels.Store(channelID, cancel)
	defer func() {
		cancel()
		r.udpChannels.Delete(channelID)
		r.lastActivity.Delete(channelID)
	}()

	// Send disconnect message on exit
	defer func() {
		disconnectMsg := DisconnectMessage{
			ChannelID: channelID,
		}
		r.logMessage(disconnectMsg, "send", ws.Label())
		ws.WriteMessage(disconnectMsg)
	}()

	msgChan := make(chan DataMessage, 100)
	r.messageQueues.Store(channelID, msgChan)
	defer r.messageQueues.Delete(channelID)

	var wg sync.WaitGroup
	wg.Add(3)
	errChan := make(chan error, 3)

	// Monitor TCP connection for closure
	go func() {
		defer wg.Done()
		defer cancel() // Cancel context when TCP connection closes
		buffer := make([]byte, 1)
		socksConn.Read(buffer)
		udpConn.Close()
		r.log.Trace().Msg("SOCKS TCP connection closed")
	}()

	// UDP to WebSocket with SOCKS5 header handling
	go func() {
		defer wg.Done()
		defer cancel() // Cancel context when this goroutine exits
		buffer := make([]byte, r.option.BufferSize)
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
					r.log.Trace().Msg("Cannot parse UDP packet from associated port")
					continue
				}

				// Update activity time
				r.updateActivityTime(channelID)

				msg := DataMessage{
					Protocol:    "udp",
					ChannelID:   channelID,
					Data:        payload,
					TargetAddr:  targetAddr,
					TargetPort:  targetPort,
					Compression: r.determineCompression(len(payload)),
				}
				r.logMessage(msg, "send", ws.Label())
				if err := ws.WriteMessage(msg); err != nil {
					errChan <- fmt.Errorf("websocket write error: %w", err)
					return
				}
				r.log.Trace().Int("size", len(payload)).Msg("Sent UDP data to WebSocket")
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
				// Update activity time
				r.updateActivityTime(channelID)

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
				udpHeader = append(udpHeader, msg.Data...)

				addr, ok := r.udpClientAddrs.Load(msg.ChannelID)
				if !ok {
					r.log.Debug().Msg("Dropping UDP packet: no socks client address available")
					continue
				}

				clientAddr := addr.(*net.UDPAddr)
				if _, err := udpConn.WriteToUDP(udpHeader, clientAddr); err != nil {
					errChan <- fmt.Errorf("udp write error: %w", err)
					return
				}
				r.log.Trace().Int("size", len(msg.Data)).Str("addr", clientAddr.String()).Msg("Sent UDP data to SOCKS")
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
func (r *Relay) logMessage(msg BaseMessage, direction string, label string) {
	// Only process if debug level is enabled
	if !r.log.Trace().Enabled() {
		return
	}

	logEvent := r.log.Trace().Str("label", label)

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

// Close gracefully shuts down the Relay
func (r *Relay) Close() {
	close(r.done)

	// Cancel all active TCP channels
	r.tcpChannels.Range(func(key, value interface{}) bool {
		if cancel, ok := value.(context.CancelFunc); ok {
			cancel()
		}
		r.tcpChannels.Delete(key)
		return true
	})

	// Cancel all active UDP channels
	r.udpChannels.Range(func(key, value interface{}) bool {
		if cancel, ok := value.(context.CancelFunc); ok {
			cancel()
		}
		r.udpChannels.Delete(key)
		return true
	})

	// Clear all maps
	r.messageQueues.Range(func(key, value interface{}) bool {
		r.messageQueues.Delete(key)
		return true
	})
	r.udpClientAddrs.Range(func(key, value interface{}) bool {
		r.udpClientAddrs.Delete(key)
		return true
	})
	r.lastActivity.Range(func(key, value interface{}) bool {
		r.lastActivity.Delete(key)
		return true
	})
}

// determineCompression decides compression method based on data size
func (r *Relay) determineCompression(dataSize int) byte {
	const compressionThreshold = 1024 // 1KB threshold
	if dataSize >= compressionThreshold {
		return DataCompressionGzip
	}
	return DataCompressionNone
}
