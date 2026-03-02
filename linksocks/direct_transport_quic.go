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

func newDirectQUICBoundWriter(p *DirectQUICDataPlane, ch *directQUICChannel, label string) *directQUICChannelWriter {
	return &directQUICChannelWriter{p: p, ch: ch, label: label, ctx: context.Background(), disableReadLoop: true}
}

func (w *directQUICChannelWriter) Label() string {
	if w.label == "" {
		return "direct-quic"
	}
	return w.label
}

func (w *directQUICChannelWriter) WriteMessage(msg BaseMessage) error {
	if msg == nil {
		return errors.New("direct quic writer: nil message")
	}

	ch, err := w.getOrOpenChannel(msg)
	if err != nil {
		return err
	}
	return ch.WriteMessage(msg)
}

func (w *directQUICChannelWriter) getOrOpenChannel(msg BaseMessage) (*directQUICChannel, error) {
	if w.p == nil {
		return nil, errors.New("direct quic writer: nil plane")
	}

	if req, ok := msg.(ConnectMessage); ok {
		if req.ChannelID == uuid.Nil {
			return nil, errors.New("direct quic writer: empty channel_id")
		}

		w.mu.Lock()
		if w.ch != nil {
			ch := w.ch
			w.mu.Unlock()
			return ch, nil
		}
		w.mu.Unlock()

		ch, err := w.p.OpenChannel(context.Background(), req)
		if err != nil {
			return nil, err
		}

		w.mu.Lock()
		w.ch = ch
		w.mu.Unlock()

		if w.c != nil && !w.disableReadLoop {
			w.readOn.Do(func() {
				go w.c.directQUICChannelReadLoop(w.ctx, ch)
			})
		}
		return ch, nil
	}

	w.mu.Lock()
	ch := w.ch
	w.mu.Unlock()
	if ch == nil {
		return nil, errors.New("direct quic writer: channel not initialized")
	}
	return ch, nil
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

		readCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		msg, err := ch.ReadMessage(readCtx)
		cancel()
		if err != nil {
			if directQUICIsExpectedReadClose(err) {
				c.log.Trace().Err(err).Str("label", label).Msg("Direct QUIC channel closed")
				c.relay.disconnectChannel(channelID)
				_ = ch.Close()
				return
			}
			c.log.Debug().Err(err).Str("label", label).Msg("Direct QUIC channel read error")
			c.directMarkDegraded(time.Now(), 30*time.Second, err.Error())
			c.relay.disconnectChannel(channelID)
			_ = ch.Close()
			return
		}
		if msg == nil {
			continue
		}

		switch m := msg.(type) {
		case DataMessage:
			if queue, ok := c.relay.messageQueues.Load(m.ChannelID); ok {
				select {
				case queue.(chan BaseMessage) <- m:
				default:
					c.log.Warn().Str("channel_id", m.ChannelID.String()).Msg("Direct QUIC message queue full, dropping data")
				}
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

		default:
			c.log.Debug().Str("type", msg.GetType()).Str("label", label).Msg("Dropped unexpected direct QUIC message")
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
