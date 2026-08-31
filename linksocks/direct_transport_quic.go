package linksocks

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/quic-go/quic-go"
)

type clientRelayWriter struct {
	c *LinkSocksClient
}

func newClientRelayWriter(c *LinkSocksClient) MessageWriter {
	return &clientRelayWriter{c: c}
}

func (w *clientRelayWriter) WriteMessage(msg BaseMessage) error {
	ws := w.c.getNextWebSocket()
	if ws == nil {
		return errors.New("relay writer: no websocket")
	}
	return ws.WriteMessage(msg)
}

func (w *clientRelayWriter) Label() string {
	ws := w.c.getNextWebSocket()
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
}

func newDirectQUICDialWriter(ctx context.Context, c *LinkSocksClient, p *DirectQUICDataPlane, label string) *directQUICChannelWriter {
	if ctx == nil {
		ctx = context.Background()
	}
	return &directQUICChannelWriter{ctx: ctx, c: c, p: p, label: label}
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
			w.c.directMu.Lock()
			remoteProtocolVersion := w.c.directRemoteProtocolVersion
			w.c.directMu.Unlock()
			channel.enableMigration(protocolSupportsMigration(remoteProtocolVersion))
			if !req.Resume {
				w.c.directMu.Lock()
				peerSessionID := w.c.directRemoteSessionID
				remoteProtocolVersion := w.c.directRemoteProtocolVersion
				w.c.directMu.Unlock()
				if protocolSupportsMigration(remoteProtocolVersion) {
					if err := fallback.WriteMessage(ChannelBindMessage{ChannelID: req.ChannelID, Protocol: req.Protocol, PeerSessionID: peerSessionID}); err != nil {
						return err
					}
				}
			}
		} else {
			if err := channel.switchToDirect(w); err != nil {
				return err
			}
			if !req.Resume {
				w.c.directMu.Lock()
				peerSessionID := w.c.directRemoteSessionID
				remoteProtocolVersion := w.c.directRemoteProtocolVersion
				w.c.directMu.Unlock()
				if protocolSupportsMigration(remoteProtocolVersion) {
					if err := fallback.WriteMessage(ChannelBindMessage{ChannelID: req.ChannelID, Protocol: req.Protocol, PeerSessionID: peerSessionID}); err != nil {
						return err
					}
				}
			}
		}
		if opened {
			if req.Resume {
				return channel.replayPending()
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

func (c *LinkSocksClient) deliverDataMessage(msg DataMessage) {
	if channel, ok := c.relay.logicalChannel(msg.ChannelID); ok {
		if !channel.acceptData(msg) {
			if channel.isDuplicate(msg.Sequence) {
				_ = channel.acknowledgeMessage(msg.Sequence)
			}
			return
		}
		if queue, ok := c.relay.messageQueues.Load(msg.ChannelID); ok {
			select {
			case queue.(chan BaseMessage) <- msg:
			default:
				c.log.Warn().Str("channel_id", msg.ChannelID.String()).Msg("Direct QUIC message queue full, dropping data")
				channel.rejectData(msg.Sequence)
				return
			}
		} else {
			channel.rejectData(msg.Sequence)
		}
		return
	}
	if queue, ok := c.relay.messageQueues.Load(msg.ChannelID); ok {
		select {
		case queue.(chan BaseMessage) <- msg:
		default:
			c.log.Warn().Str("channel_id", msg.ChannelID.String()).Msg("Direct QUIC message queue full, dropping data")
		}
	}
}

func directQUICIsExpectedReadClose(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.EOF) {
		return true
	}
	// When the peer closes a stream (e.g. after DisconnectMessage), quic-go may
	// surface it as a StreamError with application error code 0.
	var se *quic.StreamError
	if errors.As(err, &se) {
		return se.ErrorCode == 0
	}
	var ae *quic.ApplicationError
	if errors.As(err, &ae) {
		return ae.ErrorCode == 0
	}
	return false
}
