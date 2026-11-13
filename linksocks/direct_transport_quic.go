package linksocks

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
)

type clientRelayWriter struct {
	c *LinkSocksClient

	mu sync.Mutex
	// link pins this writer to one relay link. One writer carries one
	// channel's frames, and a channel's sealed stream must arrive in order:
	// choosing a link per write spreads one channel over several links, and
	// the peer merges frames by channel id in arrival order, which breaks the
	// Noise nonce chain (a handshake frame that overtakes another makes the
	// peer read the wrong message). The pin is dropped when a write fails so
	// the next frame picks a live link and resumes there.
	link *WSConn
}

func newClientRelayWriter(c *LinkSocksClient) MessageWriter {
	return &clientRelayWriter{c: c}
}

// pinnedLink returns the link this writer is pinned to, pinning the next live
// link when nothing is pinned, the pinned link is gone, or a write failed.
func (w *clientRelayWriter) pinnedLink() *WSConn {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.link != nil && w.c.hasWebSocket(w.link) {
		return w.link
	}
	ws := w.c.getNextWebSocket()
	w.link = ws
	return ws
}

// peekLink reports the pinned link without pinning a new one; used for logging
// and version reporting, which must not change routing.
func (w *clientRelayWriter) peekLink() *WSConn {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.link != nil && w.c.hasWebSocket(w.link) {
		return w.link
	}
	return w.c.getNextWebSocket()
}

// releaseLink drops the pin after a failed write so the retry can pick a live
// link instead of hammering the dead one.
func (w *clientRelayWriter) releaseLink(ws *WSConn) {
	w.mu.Lock()
	if w.link == ws {
		w.link = nil
	}
	w.mu.Unlock()
}

func (w *clientRelayWriter) WriteMessage(msg BaseMessage) error {
	ws := w.pinnedLink()
	if ws == nil {
		return &transportDownError{cause: errors.New("relay writer: no websocket")}
	}
	if w.c.relay.secure != nil {
		// V2 channels rebind with a plaintext ChannelOpen resume that must
		// precede the first sealed frame on the new link, or the relay would
		// drop the frame for an unbound channel.
		if chID, ok := relayChannelID(msg); ok {
			if ch, ok := w.c.relay.logicalChannel(chID); ok && ch.canResume() && ch.suspended() {
				if err := ws.WriteMessage(ChannelOpenMessage{ChannelID: chID, Resume: true}); err != nil {
					w.releaseLink(ws)
					return &transportDownError{cause: err}
				}
				if err := ws.WriteMessage(msg); err != nil {
					w.releaseLink(ws)
					return &transportDownError{cause: err}
				}
				ch.resume()
				return nil
			}
		}
	} else if dm, ok := msg.(DataMessage); ok {
		// After a link drop this channel was suspended; rebind it on the new
		// link before resuming data so the peer keeps routing this channel.
		if ch, ok := w.c.relay.logicalChannel(dm.ChannelID); ok && ch.canResume() && ch.suspended() {
			req := ch.requestMessage()
			req.Resume = true
			if err := ws.WriteMessage(req); err != nil {
				w.releaseLink(ws)
				return &transportDownError{cause: err}
			}
			if err := ws.WriteMessage(msg); err != nil {
				w.releaseLink(ws)
				return &transportDownError{cause: err}
			}
			ch.resume()
			return nil
		}
	}
	if err := ws.WriteMessage(msg); err != nil {
		// A write that fails while the link is shutting down (connection is
		// closed but not yet removed from the pool) is a transport failure,
		// not a channel failure: mark it so writeWithTransportGrace keeps
		// retrying until the link recovers or the grace window expires. The
		// pin is dropped so the retry moves to a live link.
		w.releaseLink(ws)
		return &transportDownError{cause: err}
	}
	return nil
}

// relayChannelID extracts the channel a message belongs to when the message
// can trigger a per-channel resume on a fresh link.
func relayChannelID(msg BaseMessage) (uuid.UUID, bool) {
	switch m := msg.(type) {
	case DataMessage:
		return m.ChannelID, true
	case SealedMessage:
		return m.ChannelID, true
	}
	return uuid.Nil, false
}

// ProtocolVersion reports the protocol generation negotiated with the relay,
// or 0 when no link is available yet.
func (w *clientRelayWriter) ProtocolVersion() byte {
	if ws := w.peekLink(); ws != nil {
		return ws.ProtocolVersion()
	}
	return 0
}

func (w *clientRelayWriter) Label() string {
	ws := w.peekLink()
	if ws == nil {
		return "relay"
	}
	return ws.Label()
}

type directQUICChannelWriter struct {
	label string

	ctx context.Context
	c   *LinkSocksClient
	p   *DirectQUICDataPlane
	// If true, do not start read loop automatically. This is used on the
	// accept/bound side where the QUIC Serve handler already consumed the
	// initial ConnectMessage from the stream.
	disableReadLoop bool

	mu     sync.Mutex
	ch     *directQUICChannel
	readOn sync.Once

	// sealedResume latches whether this channel runs the V2 sealed-resume
	// contract. It must never be re-derived later: the client zeroes its
	// remote version field whenever the direct peer state resets, and a
	// sealed channel must not be swapped to the plaintext relay writer
	// because of that transient zero.
	sealedResume bool
}

// latchSealedResume records the sealed-resume contract for this writer from
// the peer version observed at setup time.
func (w *directQUICChannelWriter) latchSealedResume(version byte) {
	if protocolMajor(version) == protocolMajor(ProtocolVersionV2) {
		w.sealedResume = true
	}
}

func newDirectQUICDialWriter(ctx context.Context, c *LinkSocksClient, p *DirectQUICDataPlane, label string) *directQUICChannelWriter {
	if ctx == nil {
		ctx = context.Background()
	}
	w := &directQUICChannelWriter{ctx: ctx, c: c, p: p, label: label}
	if c != nil {
		c.directMu.Lock()
		version := c.directRemoteProtocolVersion
		c.directMu.Unlock()
		w.latchSealedResume(version)
	}
	return w
}

func newDirectQUICBoundWriter(c *LinkSocksClient, p *DirectQUICDataPlane, ch *directQUICChannel, label string) *directQUICChannelWriter {
	return &directQUICChannelWriter{c: c, p: p, ch: ch, label: label, ctx: context.Background(), disableReadLoop: true}
}

func (w *directQUICChannelWriter) Label() string {
	if w.label == "" {
		return "direct-quic"
	}
	return w.label
}

// DirectLink marks this writer as an already encrypted peer-to-peer transport:
// the relay-side Noise layer must hand it through untouched.
func (w *directQUICChannelWriter) DirectLink() bool { return true }

// RefuseRelayFallback blocks the plaintext relay fallback for channels that
// run the V2 sealed transport. The relay writer carries no Noise session for
// this channel, so switching to it would drop the end-to-end guarantee; the
// connection fails and the entry side re-establishes the channel through a
// fresh sealed session instead.
func (w *directQUICChannelWriter) RefuseRelayFallback() bool {
	return w.sealedResume
}

func (w *directQUICChannelWriter) Fallback() MessageWriter {
	if w.c == nil {
		return nil
	}
	return newClientRelayWriter(w.c)
}

func (w *directQUICChannelWriter) WriteMessage(msg BaseMessage) error {
	if msg == nil {
		return errors.New("direct quic writer: nil message")
	}

	ch, opened, err := w.getOrOpenChannel(msg)
	if err != nil {
		return err
	}
	if req, ok := msg.(ConnectMessage); ok && w.c != nil {
		fallback := newClientRelayWriter(w.c)
		channel, exists := w.c.relay.logicalChannel(req.ChannelID)
		if !exists {
			channel = w.c.relay.registerLogicalChannel(req.ChannelID, req.Protocol, w, fallback, channelPathDirect)
			channel.setRequest(req)
			channel.markResumeOwner()
		} else {
			if err := channel.switchToDirect(w); err != nil {
				return err
			}
		}
		// Sequence bookkeeping and the resume hook apply whichever path
		// registered the channel: the entry side's local proxy registers it
		// before the connect reaches this writer, the direct read loop
		// registers it for inbound streams.
		// The bind must name the peer behind the running plane, not the live
		// remote-session field: with several providers online that field can
		// move to another peer while this plane stays connected, and the
		// relay would then route a later resume to the wrong machine.
		peerSessionID := w.c.directQUICPeerSession()
		w.c.directMu.Lock()
		if peerSessionID == uuid.Nil {
			peerSessionID = w.c.directRemoteSessionID
		}
		remoteProtocolVersion := w.c.directRemoteProtocolVersion
		w.c.directMu.Unlock()
		// Latch the sealed-resume contract before any write: the refuser must
		// stay true for the writer's lifetime regardless of later peer-state
		// resets, or a live sealed channel could be swapped to the plaintext
		// relay writer when a version field is transiently zero.
		w.latchSealedResume(remoteProtocolVersion)
		channel.enableMigration(protocolUsesPlaintextMigration(remoteProtocolVersion) ||
			protocolUsesSealedResume(remoteProtocolVersion, req.Protocol))
		if w.sealedResume && req.Protocol == "tcp" && w.c.relay.secure != nil {
			// The entry side owns the resume handshake; this writer is its
			// direct transport, and a failure here re-establishes the channel
			// through a fresh sealed session on the relay. A direct-only
			// client asked for exactly the opposite guarantee: its payload
			// must never move to the relay, so the connection fails instead.
			if w.c.directMode == DirectModeDirectOnly {
				channel.setResumeFn(func() error {
					return fmt.Errorf("%w: direct-only client", errRelayResumeRefused)
				})
			} else {
				channel.setResumeFn(func() error {
					return w.c.relay.secure.resumeChannel(channel)
				})
			}
		}
		if !req.Resume {
			// Report the direct peer's session to the relay so a later sealed
			// resume reaches the machine that holds the dialed connection
			// instead of a fresh round-robin pick. V1 needs the bind for
			// migration and fails without it; for V2 it only guides resume
			// routing, so a relay link that is down must not take the live
			// direct channel with it.
			bind := ChannelBindMessage{ChannelID: req.ChannelID, Protocol: req.Protocol, PeerSessionID: peerSessionID}
			if err := fallback.WriteMessage(bind); err != nil {
				if protocolUsesPlaintextMigration(remoteProtocolVersion) {
					return err
				}
				w.c.log.Debug().Err(err).Str("channel_id", req.ChannelID.String()).
					Msg("Failed to report direct channel binding to the relay")
			}
		}
		if opened {
			if req.Resume {
				return channel.replayPendingLocked()
			}
			return nil
		}
	}
	return ch.WriteMessage(msg)
}

func (w *directQUICChannelWriter) getOrOpenChannel(msg BaseMessage) (*directQUICChannel, bool, error) {
	if w.p == nil {
		return nil, false, errors.New("direct quic writer: nil plane")
	}

	if req, ok := msg.(ConnectMessage); ok {
		if req.ChannelID == uuid.Nil {
			return nil, false, errors.New("direct quic writer: empty channel_id")
		}

		w.mu.Lock()
		if w.ch != nil {
			ch := w.ch
			w.mu.Unlock()
			return ch, false, nil
		}
		w.mu.Unlock()

		ch, err := w.p.OpenChannel(context.Background(), req)
		if err != nil {
			return nil, false, err
		}

		w.mu.Lock()
		w.ch = ch
		w.mu.Unlock()

		if w.c != nil && !w.disableReadLoop {
			w.readOn.Do(func() {
				go w.c.directQUICChannelReadLoop(w.ctx, ch)
			})
		}
		return ch, true, nil
	}

	w.mu.Lock()
	ch := w.ch
	w.mu.Unlock()
	if ch == nil {
		return nil, false, errors.New("direct quic writer: channel not initialized")
	}
	return ch, false, nil
}

func (c *LinkSocksClient) directQUICChannelReadLoop(ctx context.Context, ch *directQUICChannel) {
	if ch == nil {
		return
	}
	channelID := ch.ID()
	label := "direct-quic/" + channelID.String()

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		msg, err := ch.ReadMessage(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			c.directMu.Lock()
			if c.directReadBackoff < c.reconnectDelay/6 {
				c.directReadBackoff = c.reconnectDelay / 6
			}
			readBackoff := c.directReadBackoff
			c.directReadBackoff = expBackoff(readBackoff, 1.5, 10*time.Minute)
			c.directMu.Unlock()
			c.log.Debug().Err(err).Str("label", label).Dur("backoff", readBackoff).Msg("Direct QUIC channel read error")
			c.directMarkDegraded(time.Now(), readBackoff, err.Error())
			_ = c.handleDirectChannelFailure(channelID, err.Error())
			_ = ch.Close()
			return
		}
		if msg == nil {
			continue
		}

		switch m := msg.(type) {
		case DataMessage:
			c.deliverDataMessage(m)

		case ChannelDataAckMessage:
			if channel, ok := c.relay.logicalChannel(m.ChannelID); ok {
				channel.acknowledge(m.Sequence)
			}

		case ConnectResponseMessage:
			if c.relay.option.FastOpen {
				if m.Success {
					c.relay.SetConnectionSuccess(m.ChannelID)
				} else {
					c.relay.disconnectChannel(m.ChannelID)
				}
			} else if queue, ok := c.relay.messageQueues.Load(m.ChannelID); ok {
				select {
				case queue.(chan BaseMessage) <- m:
				case <-time.After(2 * time.Second):
					c.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Timeout delivering direct QUIC connect response")
				}
			}

		case DisconnectMessage:
			c.relay.disconnectChannel(m.ChannelID)
			_ = ch.Close()
			return

		case ChannelMigrateAckMessage:
			if channel, ok := c.relay.logicalChannel(m.ChannelID); ok && channel.migrationEnabled() && !m.Success {
				_ = c.handleDirectChannelFailure(m.ChannelID, m.Error)
			}

		case ChannelMigrateMessage:
			if channel, ok := c.relay.logicalChannel(m.ChannelID); ok {
				if !channel.migrationEnabled() {
					continue
				}
				ack := ChannelMigrateAckMessage{ChannelID: m.ChannelID, Success: false, Error: "failed to switch to relay"}
				if err := channel.switchToRelay(); err == nil {
					ack.Success = true
					ack.Error = ""
				}
				if writer := channel.currentWriter(); writer != nil {
					_ = writer.WriteMessage(ack)
				}
			}

		default:
			c.log.Debug().Str("type", msg.GetType()).Str("label", label).Msg("Dropped unexpected direct QUIC message")
		}
	}
}

func (c *LinkSocksClient) handleDirectChannelFailure(channelID uuid.UUID, reason string) error {
	channel, ok := c.relay.logicalChannel(channelID)
	if !ok {
		return nil
	}
	if channel.Path() == string(channelPathRelay) {
		return nil
	}
	if err := channel.switchToFallback(reason); err != nil {
		return err
	}
	return nil
}

// directQueueFullWait bounds how long one sequenced frame waits for room in a
// full receive queue before the channel is torn down. Dropping instead would
// punch a hole in the sequence, and the peer discards every frame after a gap.
var directQueueFullWait = 30 * time.Second

func (c *LinkSocksClient) deliverDataMessage(msg DataMessage) {
	if channel, ok := c.relay.logicalChannel(msg.ChannelID); ok {
		if !channel.acceptData(msg) {
			if channel.isDuplicate(msg.Sequence) {
				_ = channel.acknowledgeMessage(msg.Sequence)
			}
			return
		}
		if queue, ok := c.relay.messageQueues.Load(msg.ChannelID); ok {
			queueCh := queue.(chan BaseMessage)
			select {
			case queueCh <- msg:
				return
			default:
			}
			// The queue is full: wait for the consumer instead of dropping.
			// Sequenced data must never be lost silently; if the queue makes
			// no progress at all within the budget, close the channel so the
			// application sees a failure instead of a corrupted stream.
			timer := time.NewTimer(directQueueFullWait)
			defer timer.Stop()
			select {
			case queueCh <- msg:
				return
			case <-timer.C:
				c.log.Error().Str("channel_id", msg.ChannelID.String()).
					Msg("Receive queue stayed full; closing channel instead of dropping sequenced data")
				channel.rejectData(msg.Sequence)
				c.relay.disconnectChannel(msg.ChannelID)
				return
			}
		} else {
			channel.rejectData(msg.Sequence)
		}
		return
	}
	if queue, ok := c.relay.messageQueues.Load(msg.ChannelID); ok {
		queueCh := queue.(chan BaseMessage)
		select {
		case queueCh <- msg:
			return
		default:
		}
		timer := time.NewTimer(directQueueFullWait)
		defer timer.Stop()
		select {
		case queueCh <- msg:
		case <-timer.C:
			c.log.Warn().Str("channel_id", msg.ChannelID.String()).Msg("Receive queue stayed full, dropping unsequenced data")
		}
		return
	}
	// Channel not bound yet: hold the data until the channel binds, like the
	// relay-side orphan buffer, instead of dropping it.
	c.relay.bufferOrphanData(msg)
}
