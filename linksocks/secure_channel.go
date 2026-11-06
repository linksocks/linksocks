package linksocks

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/flynn/noise"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// secure_channel.go implements the protocol V2 end-to-end encrypted channel
// layer.
//
// Two peers run a Noise_XX handshake directly between themselves, carried
// through the relay as opaque SealedMessage frames. Every payload exchanged
// afterwards (the ConnectMessage with the destination address, DataMessages,
// peer decisions) travels inside the resulting cipher states, so the relay
// only ever sees channel IDs and ciphertext lengths.
//
// The initiator is whichever side holds the local proxy entry for a channel:
// a connector client, or the relay itself when it hosts the entry for a
// server-side proxy port. The responder is always the provider that dials.
//
// Channel teardown is deliberately kept outside the cipher: DisconnectMessage
// frames stay plaintext so the relay can release the channel binding and
// forward the close to the peer. A channel ID and the fact that it closed are
// routing metadata, not payload.
//
// Direct mode is not available on V2 channels: its rendezvous messages would
// hand peer IP candidates to the relay in plaintext, which is exactly what V2
// exists to prevent.

const (
	// secureNoisePattern fixes the handshake pattern both peers must use.
	secureNoisePattern = "Noise_XX_25519_ChaChaPoly_SHA256"
	// secureInboundQueue bounds buffered ciphertext frames before the client
	// read loop applies backpressure.
	secureInboundQueue = 512
	// secureHandshakeStepTimeout bounds waiting for one handshake frame.
	secureHandshakeStepTimeout = 30 * time.Second
)

var errSecureSessionClosed = errors.New("secure channel: session closed")

func newSecureCipherSuite() noise.CipherSuite {
	return noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256)
}

// secureHub owns every secure channel of one relay and bridges raw frames
// arriving from the transport to the per-channel sessions.
type secureHub struct {
	// client is set on client-side relays and nil on the server relay, where
	// only the initiator role is used for server-side proxy entries.
	client   *LinkSocksClient
	relay    *Relay
	log      zerolog.Logger
	sessions sync.Map // channelID -> *secureSession
}

func newClientSecureHub(c *LinkSocksClient) *secureHub {
	return &secureHub{client: c, relay: c.relay, log: c.log}
}

func newServerSecureHub(r *Relay, log zerolog.Logger) *secureHub {
	return &secureHub{relay: r, log: log}
}

func (h *secureHub) has(channelID uuid.UUID) bool {
	_, ok := h.sessions.Load(channelID)
	return ok
}

func (h *secureHub) session(channelID uuid.UUID) (*secureSession, bool) {
	v, ok := h.sessions.Load(channelID)
	if !ok {
		return nil, false
	}
	return v.(*secureSession), true
}

func (h *secureHub) register(s *secureSession) {
	h.sessions.Store(s.channelID, s)
}

// remove drops a session registration if it still points at s, so a session
// being torn down can never unregister a newer one that took its place.
func (h *secureHub) remove(s *secureSession) {
	h.sessions.CompareAndDelete(s.channelID, s)
}

// close tears the session for a channel down, if any, and unregisters it.
func (h *secureHub) close(channelID uuid.UUID) {
	if v, ok := h.sessions.LoadAndDelete(channelID); ok {
		v.(*secureSession).close(errSecureSessionClosed)
	}
}

// deliver hands one ciphertext frame from the transport to its session.
// Frames for unknown channels are dropped: peers never send sealed data for a
// channel the local side has not opened or accepted.
func (h *secureHub) deliver(channelID uuid.UUID, data []byte) {
	s, ok := h.session(channelID)
	if !ok {
		h.log.Debug().
			Str("channel_id", channelID.String()).
			Msg("Dropped sealed frame for unknown channel")
		return
	}
	s.deliver(data)
}

// openChannel drives the initiator side of one relay channel: it asks the
// relay to bind the channel (ChannelOpenMessage) through the given link,
// performs the handshake with the assigned provider over sealed frames and
// returns the session once the transport keys are ready. Relay refusals
// arrive as plaintext ConnectResponseMessage failures on the channel queue
// and abort the wait.
func (h *secureHub) openChannel(link MessageWriter, channelID uuid.UUID, queue chan BaseMessage) (*secureSession, error) {
	sess, err := h.newSession(link, channelID, true)
	if err != nil {
		return nil, err
	}
	sess.queue = queue
	h.register(sess)

	open := ChannelOpenMessage{ChannelID: channelID}
	if err := sess.writeWithGrace(func(link MessageWriter) error { return link.WriteMessage(open) }); err != nil {
		h.close(channelID)
		return nil, err
	}

	go sess.run()

	timer := time.NewTimer(h.relay.option.ConnectTimeout + 10*time.Second)
	defer timer.Stop()
	for {
		select {
		case <-sess.ready:
			return sess, nil
		case <-sess.done:
			return nil, sess.resultErr()
		case msg := <-queue:
			// The relay refuses an open with a plaintext ConnectResponse.
			if resp, ok := msg.(ConnectResponseMessage); ok && !resp.Success {
				h.close(channelID)
				reason := resp.Error
				if reason == "" {
					reason = "channel open refused by relay"
				}
				return nil, errors.New(reason)
			}
			// Nothing else can legitimately precede the handshake; put it
			// back for the connect wait that follows this call.
			select {
			case queue <- msg:
			default:
			}
		case <-timer.C:
			h.close(channelID)
			return nil, errors.New("secure channel handshake timeout")
		}
	}
}

func (h *secureHub) newSession(link MessageWriter, channelID uuid.UUID, initiator bool) (*secureSession, error) {
	keypair, err := newSecureCipherSuite().GenerateKeypair(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("secure channel keypair: %w", err)
	}
	hs, err := noise.NewHandshakeState(noise.Config{
		CipherSuite:   newSecureCipherSuite(),
		Pattern:       noise.HandshakeXX,
		Initiator:     initiator,
		StaticKeypair: keypair,
	})
	if err != nil {
		return nil, fmt.Errorf("secure channel handshake: %w", err)
	}
	return &secureSession{
		hub:       h,
		link:      link,
		channelID: channelID,
		initiator: initiator,
		log:       h.log.With().Str("channel_id", channelID.String()).Logger(),
		inbound:   make(chan []byte, secureInboundQueue),
		ready:     make(chan struct{}),
		done:      make(chan struct{}),
		hs:        hs,
	}, nil
}

// acceptChannel starts the responder side when a ChannelOpen arrives. It
// serves both the provider role (a client dials for the entry peer) and the
// relay-dialer role (the relay dials for a forward client's entry). A resume
// for an unknown channel is answered with a plaintext disconnect so the relay
// tears the stale binding down.
//
// accessCheck, when non-nil, is consulted on the relay-dialer role before the
// sealed destination is dialed: the relay is a party to the handshake there,
// so token-bound destination rules stay enforceable even though the address
// never crosses the wire in plaintext.
func (h *secureHub) acceptChannel(link MessageWriter, m ChannelOpenMessage, accessCheck func(ConnectMessage) error) {
	// Client-side responders write through the dynamic relay writer so their
	// frames follow the live link across reconnects; the relay keeps the
	// forward client's link and updates it on resume.
	if h.client != nil {
		link = newClientRelayWriter(h.client)
	}
	if sess, ok := h.session(m.ChannelID); ok {
		// Resume after a link swap: the cipher state is still valid; point
		// the session at the fresh link and keep routing by channel ID.
		sess.setLink(link)
		h.log.Debug().
			Str("channel_id", m.ChannelID.String()).
			Msg("Secure channel resumed on a new link")
		return
	}
	if m.Resume {
		h.log.Warn().
			Str("channel_id", m.ChannelID.String()).
			Msg("Secure channel resume for an unknown channel")
		disc := DisconnectMessage{ChannelID: m.ChannelID, Error: "channel expired"}
		_ = link.WriteMessage(disc)
		return
	}
	sess, err := h.newSession(link, m.ChannelID, false)
	if err != nil {
		h.log.Warn().Err(err).Msg("Failed to start secure channel responder")
		return
	}
	sess.accessCheck = accessCheck
	h.register(sess)
	go sess.run()
}

// secureSession is one end-to-end encrypted channel endpoint. It implements
// MessageWriter for the channel's pumps: WriteMessage seals, and decrypted
// inbound traffic is either delivered to the channel queue (initiator) or
// dispatched directly into the client (responder).
type secureSession struct {
	hub       *secureHub
	link      MessageWriter
	channelID uuid.UUID
	initiator bool
	log       zerolog.Logger

	// queue receives decrypted messages on the initiator side, where the
	// local proxy pump consumes them as if they came from the relay.
	queue chan BaseMessage

	// accessCheck, when set on a relay-dialer responder, vets the decrypted
	// connect request before the relay dials its destination.
	accessCheck func(ConnectMessage) error

	inbound chan []byte

	ready  chan struct{}
	done   chan struct{}
	once   sync.Once
	errMu  sync.Mutex
	failed error

	mu          sync.Mutex
	writeMu     sync.Mutex
	hs          *noise.HandshakeState
	send        *noise.CipherState
	recv        *noise.CipherState
	established bool
	closed      bool
}

func (s *secureSession) Label() string {
	return fmt.Sprintf("secure/%s", s.channelID)
}

// WriteMessage seals one protocol message and ships it as a SealedMessage
// frame. Disconnect stays plaintext so the relay can release the binding.
func (s *secureSession) WriteMessage(msg BaseMessage) error {
	if _, ok := msg.(DisconnectMessage); ok {
		return s.writePlain(msg)
	}

	plain, err := PackMessageWithVersion(msg, ProtocolVersionV2)
	if err != nil {
		return err
	}

	// Encrypt once, then write the same ciphertext until it lands or the
	// transport grace expires. Retrying a byte-identical frame keeps the
	// Noise nonce chain contiguous; producing a second ciphertext for the
	// same message would desynchronize the peer, so a frame that cannot be
	// delivered closes the whole session instead.
	s.writeMu.Lock()
	defer s.writeMu.Unlock()

	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return errSecureSessionClosed
	}
	if !s.established || s.send == nil {
		s.mu.Unlock()
		return errors.New("secure channel: handshake not complete")
	}
	ct, err := s.send.Encrypt(nil, nil, plain)
	s.mu.Unlock()
	if err != nil {
		return fmt.Errorf("secure channel encrypt: %w", err)
	}

	if werr := s.writeCiphertext(ct); werr != nil {
		s.fail(fmt.Errorf("secure channel write: %w", werr))
		return werr
	}
	return nil
}

// writeCiphertext ships one ciphertext frame, retrying the same bytes while
// the link is down and the transport grace lasts. Transport frames and
// handshake frames share this path: a link drop mid-handshake must not punch
// a hole into the handshake state any more than into the nonce chain.
func (s *secureSession) writeCiphertext(ct []byte) error {
	return s.writeWithGrace(func(link MessageWriter) error {
		return link.WriteMessage(SealedMessage{ChannelID: s.channelID, Data: ct})
	})
}

// writeWithGrace retries one logical write while the transport link is down
// and the grace window lasts. Consumers always replay the exact same bytes:
// the relay-visible outcome must be at-most-once for a given frame.
func (s *secureSession) writeWithGrace(write func(link MessageWriter) error) error {
	grace := s.hub.relay.option.TransportGrace
	deadline := time.Now().Add(grace)
	for {
		s.mu.Lock()
		link := s.link
		established := s.established
		s.mu.Unlock()
		werr := write(link)
		if werr == nil {
			s.log.Trace().Bool("established", established).Msg("Sealed frame sent")
			return nil
		}
		if !isTransportDown(werr) || grace <= 0 || time.Now().After(deadline) {
			return werr
		}
		select {
		case <-s.done:
			return errSecureSessionClosed
		case <-s.hub.relay.done:
			// The relay is shutting down; stop retrying and drop the session.
			return errSecureSessionClosed
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// writePlain sends a control message (ChannelOpen/Disconnect) unsealed through
// the channel's link.
func (s *secureSession) writePlain(msg BaseMessage) error {
	s.mu.Lock()
	link := s.link
	s.mu.Unlock()
	return link.WriteMessage(msg)
}

// setLink points the session's outbound frames at a fresh transport link;
// used when a resume binds the channel onto a reconnected socket.
func (s *secureSession) setLink(link MessageWriter) {
	s.mu.Lock()
	s.link = link
	s.mu.Unlock()
}

// deliver buffers a ciphertext frame for the session loop. It blocks instead
// of dropping: the nonce chain must stay contiguous, so a full buffer applies
// backpressure to the whole link.
func (s *secureSession) deliver(data []byte) {
	s.log.Trace().Int("bytes", len(data)).Msg("Sealed frame received")
	select {
	case s.inbound <- data:
	case <-s.done:
	}
}

func (s *secureSession) receive() ([]byte, bool) {
	select {
	case <-s.done:
		return nil, false
	case ct := <-s.inbound:
		return ct, true
	}
}

func (s *secureSession) awaitFrame(timeout time.Duration) ([]byte, error) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-s.done:
		return nil, errSecureSessionClosed
	case ct := <-s.inbound:
		return ct, nil
	case <-t.C:
		return nil, errors.New("secure channel: handshake step timed out")
	}
}

func (s *secureSession) close(err error) {
	s.finish(err)
}

func (s *secureSession) fail(err error) {
	s.log.Debug().Err(err).Msg("Secure channel closed")
	s.finish(err)
}

func (s *secureSession) finish(err error) {
	s.errMu.Lock()
	if s.failed == nil && err != nil {
		s.failed = err
	}
	s.errMu.Unlock()
	s.once.Do(func() { close(s.done) })
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
}

func (s *secureSession) resultErr() error {
	s.errMu.Lock()
	defer s.errMu.Unlock()
	if s.failed != nil {
		return s.failed
	}
	return errSecureSessionClosed
}

// run drives the whole session lifecycle: handshake first, then transport
// frame decryption and dispatch.
func (s *secureSession) run() {
	defer s.hub.remove(s)

	var err error
	if s.initiator {
		err = s.runInitiatorHandshake()
	} else {
		err = s.runResponderHandshake()
	}
	if err != nil {
		s.fail(err)
		return
	}
	s.mu.Lock()
	s.established = true
	s.mu.Unlock()
	close(s.ready)
	s.log.Trace().Msg("Secure channel established")

	for {
		ct, ok := s.receive()
		if !ok {
			return
		}
		s.mu.Lock()
		recv := s.recv
		s.mu.Unlock()
		if recv == nil {
			s.fail(errors.New("secure channel: transport key missing"))
			return
		}
		pt, err := recv.Decrypt(nil, nil, ct)
		if err != nil {
			s.fail(fmt.Errorf("secure channel decrypt: %w", err))
			return
		}
		msg, _, err := ParseMessageWithVersion(pt)
		if err != nil {
			s.fail(fmt.Errorf("secure channel payload parse: %w", err))
			return
		}
		s.dispatch(msg)
	}
}

// runInitiatorHandshake performs the XX initiator flight: -> e, <- e ee s es,
// -> s se.
func (s *secureSession) runInitiatorHandshake() error {
	m1, _, _, err := s.hs.WriteMessage(nil, nil)
	if err != nil {
		return fmt.Errorf("secure channel handshake write 1: %w", err)
	}
	if err := s.writeCiphertext(m1); err != nil {
		return err
	}

	m2, err := s.awaitFrame(secureHandshakeStepTimeout)
	if err != nil {
		return err
	}
	if _, _, _, err := s.hs.ReadMessage(nil, m2); err != nil {
		return fmt.Errorf("secure channel handshake read 2: %w", err)
	}

	m3, initToResp, respToInit, err := s.hs.WriteMessage(nil, nil)
	if err != nil {
		return fmt.Errorf("secure channel handshake write 3: %w", err)
	}
	if err := s.writeCiphertext(m3); err != nil {
		return err
	}
	s.installCiphers(initToResp, respToInit)
	return nil
}

// runResponderHandshake performs the XX responder flight: read msg1, write
// msg2, read msg3.
func (s *secureSession) runResponderHandshake() error {
	m1, err := s.awaitFrame(secureHandshakeStepTimeout)
	if err != nil {
		return err
	}
	if _, _, _, err := s.hs.ReadMessage(nil, m1); err != nil {
		return fmt.Errorf("secure channel handshake read 1: %w", err)
	}

	m2, _, _, err := s.hs.WriteMessage(nil, nil)
	if err != nil {
		return fmt.Errorf("secure channel handshake write 2: %w", err)
	}
	if err := s.writeCiphertext(m2); err != nil {
		return err
	}

	m3, err := s.awaitFrame(secureHandshakeStepTimeout)
	if err != nil {
		return err
	}
	if _, initToResp, respToInit, err := s.hs.ReadMessage(nil, m3); err != nil {
		return fmt.Errorf("secure channel handshake read 3: %w", err)
	} else {
		s.installCiphers(initToResp, respToInit)
	}
	return nil
}

// installCiphers wires the split ciphers. Split always returns the
// initiator->responder cipher first; each side sends with the cipher pointing
// the other way.
func (s *secureSession) installCiphers(initToResp, respToInit *noise.CipherState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.initiator {
		s.send, s.recv = initToResp, respToInit
	} else {
		s.send, s.recv = respToInit, initToResp
	}
}

// dispatch routes one decrypted message. The initiator feeds its local proxy
// pump through the channel queue; the responder dials (either a provider
// client or the relay itself for a forward client's entry).
func (s *secureSession) dispatch(msg BaseMessage) {
	c := s.hub.client
	switch m := msg.(type) {
	case ConnectMessage:
		if s.initiator {
			s.log.Warn().Msg("Dropping unexpected connect on the entry side")
			return
		}
		if c != nil {
			c.handleSecureConnect(s, m)
			return
		}
		// The relay dials for a forward client: token-bound destination
		// rules still apply here because the relay is a party to this
		// handshake and decrypts the request.
		if s.accessCheck != nil {
			if err := s.accessCheck(m); err != nil {
				s.hub.log.Warn().
					Str("channel_id", m.ChannelID.String()).
					Err(err).
					Msg("Sealed connect blocked by relay access control")
				resp := ConnectResponseMessage{ChannelID: m.ChannelID, Success: false, Error: err.Error()}
				if werr := s.WriteMessage(resp); werr != nil {
					s.hub.log.Debug().Err(werr).Msg("Failed to report blocked sealed connect")
				}
				return
			}
		}
		// Reserve the channel queue before the dial so replies cannot
		// overtake it, then run the same network handler the plaintext path
		// uses.
		if _, ok := s.hub.relay.messageQueues.Load(m.ChannelID); !ok {
			msgChan := make(chan BaseMessage, 1000)
			s.hub.relay.messageQueues.Store(m.ChannelID, msgChan)
		}
		go func() {
			if err := s.hub.relay.HandleNetworkConnection(context.Background(), s, m); err != nil && !errors.Is(err, context.Canceled) {
				s.hub.log.Debug().Err(err).Msg("Error handling secure network connection")
			}
		}()

	case ConnectResponseMessage:
		if !s.initiator {
			s.log.Warn().Msg("Dropping unexpected connect response on the dialer side")
			return
		}
		s.deliverToQueue(m)

	case DataMessage:
		if s.initiator {
			s.deliverToQueue(m)
			return
		}
		if c != nil {
			c.deliverDataMessage(m)
			return
		}
		if q, ok := s.hub.relay.messageQueues.Load(m.ChannelID); ok {
			select {
			case q.(chan BaseMessage) <- m:
			case <-s.done:
			}
		} else {
			s.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Dropping sealed data without a channel queue")
		}

	case DisconnectMessage:
		// Normal teardown Disconnects travel plaintext; a sealed one is only
		// a safety net.
		if s.initiator {
			s.deliverToQueue(m)
			return
		}
		if c != nil {
			c.handlePeerDisconnect(m)
		}

	default:
		s.log.Warn().Str("type", msg.GetType()).Msg("Dropping unexpected sealed message")
	}
}

func (s *secureSession) deliverToQueue(msg BaseMessage) {
	if s.queue == nil {
		return
	}
	select {
	case s.queue <- msg:
	case <-s.done:
	}
}

// openChannelTransport prepares the writer that carries one channel's traffic.
// Protocol V2 entries first establish the end-to-end encrypted session for
// the channel and hand back a sealing writer; legacy links keep writing
// plaintext. On the server-side entry, links that speak legacy generations
// stay plaintext because those providers cannot parse ChannelOpen.
func (r *Relay) openChannelTransport(channelID uuid.UUID, queue chan BaseMessage, ws MessageWriter) (MessageWriter, error) {
	if r.secure == nil {
		return ws, nil
	}
	if vr, ok := ws.(interface{ ProtocolVersion() byte }); ok {
		peerVersion := vr.ProtocolVersion()
		if protocolMajor(peerVersion) != protocolMajor(ProtocolVersionV2) {
			// A V2 client must never fall back to plaintext against a relay
			// that speaks an older generation; fail loudly instead of
			// degrading the channel it believes is encrypted. The relay
			// itself keeps serving legacy peers on their own generation.
			if r.secure.client != nil && peerVersion != 0 {
				return nil, fmt.Errorf(
					"relay speaks legacy protocol 0x%02X: protocol V2 channels need an upgraded relay, or pass --protocol-version v1 to match it",
					peerVersion)
			}
			return ws, nil
		}
	}
	sess, err := r.secure.openChannel(ws, channelID, queue)
	if err != nil {
		return nil, err
	}
	return sess, nil
}
