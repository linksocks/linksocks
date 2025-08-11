package linksocks

import (
	"context"
	"io"
	"net"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// DynamicForwarder handles dynamic batching and sending of network data
type DynamicForwarder struct {
	log                  zerolog.Logger
	channelID            uuid.UUID
	ws                   *WSConn
	relay                *Relay
	bufferSize           int
	enableBatching       bool
	minBatchWaitTime     time.Duration
	maxBatchWaitTime     time.Duration
	highSpeedThreshold   float64
	lowSpeedThreshold    float64
	compressionThreshold int
	protocol             string
	errChan              chan<- error
	ctx                  context.Context
}

// NewSendManager creates a new SendManager
func NewSendManager(
	ctx context.Context,
	log zerolog.Logger,
	channelID uuid.UUID,
	ws *WSConn,
	relay *Relay,
	protocol string,
	errChan chan<- error,
) *DynamicForwarder {
	return &DynamicForwarder{
		log:                  log,
		channelID:            channelID,
		ws:                   ws,
		relay:                relay,
		bufferSize:           relay.option.BufferSize,
		enableBatching:       relay.option.EnableDynamicBatching,
		minBatchWaitTime:     relay.option.MinBatchWaitTime,
		maxBatchWaitTime:     relay.option.MaxBatchWaitTime,
		highSpeedThreshold:   relay.option.HighSpeedThreshold,
		lowSpeedThreshold:    relay.option.LowSpeedThreshold,
		compressionThreshold: relay.option.CompressionThreshold,
		protocol:             protocol,
		errChan:              errChan,
		ctx:                  ctx,
	}
}

// ProcessReads handles reading from a network connection and forwarding to WebSocket with dynamic batching
func (s *DynamicForwarder) ProcessReads(conn io.Reader) {
	// Get a buffer from the pool
	buffer := *(s.relay.bufferPool.Get().(*[]byte))
	// Return the buffer to the pool when done
	defer s.relay.bufferPool.Put(&buffer)

	// If batching is disabled, send immediately
	if !s.enableBatching {
		s.processReadsImmediate(conn, buffer)
		return
	}

	// Setup for dynamic batching (guarded by batchMu to avoid data races)
	var batchMu sync.Mutex
	var currentBatchBuffer []byte
	currentBatchBuffer = make([]byte, 0, s.bufferSize)
	var batchTimer *time.Timer
	currentDelay := time.Duration(0)
	maxDelay := s.maxBatchWaitTime
	minDelay := s.minBatchWaitTime
	if minDelay <= 0 {
		minDelay = 5 * time.Millisecond // Ensure min delay is positive and practical
	}
	delayStep := (maxDelay - minDelay) / 10 // Step for increasing delay
	if delayStep <= 0 {
		delayStep = 1 * time.Millisecond
	}

	// Speed calculation variables
	var bytesSinceLastFlush int64
	timeOfLastFlush := time.Now()
	highSpeedThreshold := s.highSpeedThreshold
	lowSpeedThreshold := s.lowSpeedThreshold

	flushSignalChannel := make(chan struct{}, 1) // Channel to signal timer expiry

	// Function to flush the current batch and adjust delay
	flushBatch := func(reason string) {
		batchMu.Lock()
		defer batchMu.Unlock()
		// Stop the timer if it's running
		if batchTimer != nil {
			batchTimer.Stop()
			batchTimer = nil
		}

		sentBytes := len(currentBatchBuffer)
		if sentBytes > 0 {
			// Send the data - create a copy
			dataToSend := make([]byte, sentBytes)
			copy(dataToSend, currentBatchBuffer)

			msg := DataMessage{
				Protocol:    s.protocol,
				ChannelID:   s.channelID,
				Data:        dataToSend,
				Compression: s.relay.determineCompression(sentBytes),
			}
			s.relay.logMessage(msg, "send", s.ws.Label())
			if err := s.ws.WriteMessage(msg); err != nil {
				select {
				case s.errChan <- err:
				default:
					s.log.Error().Err(err).Msg("Failed to send error to channel")
				}
				return
			}
			s.log.Trace().Int("size", sentBytes).Dur("delay_used", currentDelay).Str("reason", reason).Msg("Flushed batch to WebSocket")
			currentBatchBuffer = currentBatchBuffer[:0] // Reset buffer
		}

		// --- Adjust Delay Based on Speed ---
		elapsed := time.Since(timeOfLastFlush).Seconds()
		var currentSpeed float64
		if elapsed > 0.001 && bytesSinceLastFlush > 0 {
			currentSpeed = float64(bytesSinceLastFlush) / elapsed
		} else {
			currentSpeed = 0
		}

		previousDelay := currentDelay
		if currentSpeed > highSpeedThreshold {
			// High speed detected
			if currentDelay == 0 {
				currentDelay = minDelay // Start batching
			} else {
				currentDelay += delayStep // Increase delay
				if currentDelay > maxDelay {
					currentDelay = maxDelay
				}
			}
			if currentDelay != previousDelay {
				s.log.Trace().Str("channel_id", s.channelID.String()).Int("speed_bps", int(currentSpeed)).Dur("new_delay", currentDelay).Msg("High speed detected, starting/increasing batch delay")
			}
		} else {
			// Speed is below threshold
			if currentDelay > 0 { // Only decrease if we are currently batching
				if currentSpeed < lowSpeedThreshold { // Very low speed, revert to immediate send
					currentDelay = 0
					s.log.Trace().Str("channel_id", s.channelID.String()).Int("speed_bps", int(currentSpeed)).Msg("Very low speed detected, reverting to immediate send")
				} else { // Moderate/low speed, decrease delay towards minimum
					currentDelay /= 2
					if currentDelay < minDelay {
						currentDelay = minDelay
					}
					if currentDelay != previousDelay {
						s.log.Trace().Str("channel_id", s.channelID.String()).Int("speed_bps", int(currentSpeed)).Dur("new_delay", currentDelay).Msg("Low speed detected, decreasing batch delay")
					}
				}
			}
		}

		// Reset counters for next interval
		bytesSinceLastFlush = 0
		timeOfLastFlush = time.Now()
	}

	// Ensure final flush on exit
	defer flushBatch("goroutine exit")

	// Goroutine to handle timer expiry signal
	go func() {
		for {
			select {
			case <-flushSignalChannel:
				// Timer expired, flush and adjust delay based on speed since last flush
				flushBatch("timer expired")
			case <-s.ctx.Done():
				return // Exit timer handler goroutine
			}
		}
	}()

	// --- Main Read Loop (Adaptive Batching) ---
	for {
		// Check context cancellation before blocking read
		select {
		case <-s.ctx.Done():
			return
		default:
			// Continue to read
		}

		n, err := conn.Read(buffer)
		if err != nil {
			// Flush any pending batched data BEFORE signaling error, to preserve
			// ordering of final response bytes relative to the upcoming disconnect.
			// This avoids races that can make clients observe EOF before headers.
			flushBatch("read error")

			// Always propagate the read error (including io.EOF) so caller can decide how to signal
			select {
			case s.errChan <- err:
			default:
				s.log.Error().Err(err).Msg("Failed to send error to channel")
			}
			if err == io.EOF {
				s.log.Trace().Str("channel_id", s.channelID.String()).Msg("Connection closed (EOF)")
			}
			return // Exit goroutine on any read error/EOF
		}
		if n == 0 {
			continue // Nothing read, loop again
		}

		s.relay.updateActivityTime(s.channelID) // Update channel activity
		batchMu.Lock()
		bytesSinceLastFlush += int64(n) // Track bytes for speed calculation

		// Append read data to batch buffer
		currentBatchBuffer = append(currentBatchBuffer, buffer[:n]...)
		curLen := len(currentBatchBuffer)
		curDelay := currentDelay
		hasTimer := batchTimer != nil
		batchMu.Unlock()

		// --- Flush Logic ---
		if curLen >= s.bufferSize {
			// Flush immediately if buffer is full
			flushBatch("buffer full")
		} else if curDelay == 0 {
			// Flush immediately if batching is not active (delay is zero)
			flushBatch("immediate send")
		} else {
			// Batching is active (delay > 0)
			if !hasTimer {
				// Start timer only if it's not already running
				batchMu.Lock()
				// Re-check condition under lock
				if batchTimer == nil {
					d := currentDelay
					batchTimer = time.AfterFunc(d, func() {
						// Use channel to signal expiry
						select {
						case flushSignalChannel <- struct{}{}:
						default:
						}
					})
				}
				batchMu.Unlock()
			}
			// If timer is already running, do nothing, let it expire or be stopped by buffer full
		}
	}
}

// processReadsImmediate handles reading directly without batching
func (s *DynamicForwarder) ProcessReadsImmediate(conn io.Reader) {
	// Get a buffer from the pool
	buffer := *(s.relay.bufferPool.Get().(*[]byte))
	// Return the buffer to the pool when done
	defer s.relay.bufferPool.Put(&buffer)

	s.processReadsImmediate(conn, buffer)
}

// processReadsImmediate is an internal method that handles immediate sending
func (s *DynamicForwarder) processReadsImmediate(conn io.Reader, buffer []byte) {
	for {
		// Check context cancellation before blocking read
		select {
		case <-s.ctx.Done():
			return
		default:
			// Continue to read
		}

		n, err := conn.Read(buffer)
		if err != nil {
			// Always propagate the read error (including io.EOF)
			select {
			case s.errChan <- err:
			default:
				s.log.Error().Err(err).Msg("Failed to send error to channel")
			}
			return
		}
		if n == 0 {
			continue
		}

		s.relay.updateActivityTime(s.channelID)

		data := make([]byte, n)
		copy(data, buffer[:n])

		msg := DataMessage{
			Protocol:    s.protocol,
			ChannelID:   s.channelID,
			Data:        data,
			Compression: s.relay.determineCompression(n),
		}

		s.relay.logMessage(msg, "send", s.ws.Label())
		if err := s.ws.WriteMessage(msg); err != nil {
			select {
			case s.errChan <- err:
			default:
				s.log.Error().Err(err).Msg("Failed to send error to channel")
			}
			return
		}
		s.log.Trace().Int("size", n).Msg("Sent data to WebSocket (immediate mode)")
	}
}

// ProcessUDPReads handles reading from a UDP connection with appropriate metadata
func (s *DynamicForwarder) ProcessUDPReads(conn *net.UDPConn) {
	buffer := make([]byte, s.bufferSize)
	for {
		n, remoteAddr, err := conn.ReadFromUDP(buffer)
		if err != nil {
			if opErr, ok := err.(*net.OpError); ok {
				if opErr.Err.Error() == "use of closed network connection" {
					s.log.Trace().Msg("UDP connection closed as instructed by connector")
				} else {
					s.log.Debug().Err(err).Msg("Remote UDP read error")
					select {
					case s.errChan <- err:
					default:
						s.log.Error().Err(err).Msg("Failed to send error to channel")
					}
				}
			} else {
				select {
				case s.errChan <- err:
				default:
					s.log.Error().Err(err).Msg("Failed to send error to channel")
				}
			}
			return
		}

		// Update activity time
		s.relay.updateActivityTime(s.channelID)

		msg := DataMessage{
			Protocol:    s.protocol,
			ChannelID:   s.channelID,
			Data:        buffer[:n],
			Address:     remoteAddr.IP.String(),
			Port:        remoteAddr.Port,
			Compression: s.relay.determineCompression(n),
		}
		s.relay.logMessage(msg, "send", s.ws.Label())
		if err := s.ws.WriteMessage(msg); err != nil {
			select {
			case s.errChan <- err:
			default:
				s.log.Error().Err(err).Msg("Failed to send error to channel")
			}
			return
		}
		s.log.Trace().Int("size", n).Str("addr", remoteAddr.String()).Msg("Sent UDP data to WebSocket")
	}
}
