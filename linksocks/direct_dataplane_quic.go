package linksocks

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/quic-go/quic-go"
	"github.com/rs/zerolog"
)

const (
	directQUICFrameHeaderLen = 4
	directQUICMaxFrameSize   = MaxWebSocketMessageSize
	directQUICAcceptTimeout  = 5 * time.Second
)

// DirectQUICDataPlane multiplexes LinkSocks binary messages over QUIC streams.
//
// Each ChannelID maps to one bidirectional QUIC stream. Messages are framed as:
//
//	Len(4, big-endian) + PackMessage(...) bytes
//
// This layer is transport-only and intentionally does not implement routing
// policy (relay vs direct). Routing is handled by higher-level state machine.
type DirectQUICDataPlane struct {
	log  zerolog.Logger
	conn *quic.Conn

	mu       sync.Mutex
	channels map[uuid.UUID]*directQUICChannel
	closed   bool

	serveOnce sync.Once
}

func NewDirectQUICDataPlane(conn *quic.Conn, logger zerolog.Logger) (*DirectQUICDataPlane, error) {
	if conn == nil {
		return nil, errors.New("direct quic dataplane: nil connection")
	}
	if logger.GetLevel() == zerolog.NoLevel {
		logger = zerolog.Nop()
	}
	return &DirectQUICDataPlane{
		log:      logger,
		conn:     conn,
		channels: make(map[uuid.UUID]*directQUICChannel),
	}, nil
}

func (p *DirectQUICDataPlane) Close() error {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil
	}
	p.closed = true
	chs := make([]*directQUICChannel, 0, len(p.channels))
	for _, ch := range p.channels {
		chs = append(chs, ch)
	}
	p.channels = make(map[uuid.UUID]*directQUICChannel)
	p.mu.Unlock()

	for _, ch := range chs {
		_ = ch.Close()
	}
	return nil
}

func (p *DirectQUICDataPlane) removeChannel(id uuid.UUID) {
	p.mu.Lock()
	delete(p.channels, id)
	p.mu.Unlock()
}

func (p *DirectQUICDataPlane) OpenChannel(ctx context.Context, req ConnectMessage) (*directQUICChannel, error) {
	if req.ChannelID == uuid.Nil {
		return nil, errors.New("direct quic dataplane: empty channel_id")
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		return nil, fmt.Errorf("direct quic dataplane: unsupported protocol: %s", req.Protocol)
	}

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return nil, errors.New("direct quic dataplane: closed")
	}
	if _, exists := p.channels[req.ChannelID]; exists {
		p.mu.Unlock()
		return nil, errors.New("direct quic dataplane: channel already exists")
	}
	p.mu.Unlock()

	s, err := p.conn.OpenStreamSync(ctx)
	if err != nil {
		return nil, err
	}
	ch := newDirectQUICChannel(p, req.ChannelID, s, p.log)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ch.Close()
		return nil, errors.New("direct quic dataplane: closed")
	}
	p.channels[req.ChannelID] = ch
	p.mu.Unlock()

	if err := ch.WriteMessage(req); err != nil {
		p.removeChannel(req.ChannelID)
		_ = ch.Close()
		return nil, err
	}
	return ch, nil
}

// Serve starts accepting inbound QUIC streams. The first message on a stream
// must be a ConnectMessage. The provided handler is called for each accepted
// channel.
func (p *DirectQUICDataPlane) Serve(ctx context.Context, onConnect func(ctx context.Context, ch *directQUICChannel, req ConnectMessage) error) error {
	if onConnect == nil {
		return errors.New("direct quic dataplane: nil onConnect")
	}

	p.serveOnce.Do(func() {
		go func() {
			<-ctx.Done()
			_ = p.Close()
		}()

		go func() {
			for {
				s, err := p.conn.AcceptStream(ctx)
				if err != nil {
					return
				}
				go p.handleIncomingStream(ctx, s, onConnect)
			}
		}()
	})
	return nil
}

func (p *DirectQUICDataPlane) handleIncomingStream(ctx context.Context, s *quic.Stream, onConnect func(ctx context.Context, ch *directQUICChannel, req ConnectMessage) error) {
	if s == nil {
		return
	}

	acceptCtx, cancel := context.WithTimeout(ctx, directQUICAcceptTimeout)
	defer cancel()

	ch := newDirectQUICChannel(p, uuid.Nil, s, p.log)
	msg, err := ch.ReadMessage(acceptCtx)
	if err != nil {
		_ = ch.Close()
		return
	}
	req, ok := msg.(ConnectMessage)
	if !ok {
		_ = ch.Close()
		return
	}
	if req.ChannelID == uuid.Nil {
		_ = ch.Close()
		return
	}
	if req.Protocol != "tcp" && req.Protocol != "udp" {
		_ = ch.Close()
		return
	}

	ch.setID(req.ChannelID)

	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		_ = ch.Close()
		return
	}
	if _, exists := p.channels[req.ChannelID]; exists {
		p.mu.Unlock()
		_ = ch.Close()
		return
	}
	p.channels[req.ChannelID] = ch
	p.mu.Unlock()

	if err := onConnect(ctx, ch, req); err != nil {
		p.log.Debug().Err(err).Str("channel_id", req.ChannelID.String()).Msg("Direct QUIC channel handler error")
		p.removeChannel(req.ChannelID)
		_ = ch.Close()
		return
	}
}

type directQUICChannel struct {
	log zerolog.Logger
	p   *DirectQUICDataPlane

	mu sync.Mutex
	id uuid.UUID
	s  *quic.Stream

	writeMu sync.Mutex
	closed  bool
}

func newDirectQUICChannel(p *DirectQUICDataPlane, id uuid.UUID, s *quic.Stream, logger zerolog.Logger) *directQUICChannel {
	if logger.GetLevel() == zerolog.NoLevel {
		logger = zerolog.Nop()
	}
	return &directQUICChannel{p: p, id: id, s: s, log: logger}
}

func (c *directQUICChannel) ID() uuid.UUID {
	c.mu.Lock()
	id := c.id
	c.mu.Unlock()
	return id
}

func (c *directQUICChannel) setID(id uuid.UUID) {
	c.mu.Lock()
	c.id = id
	c.mu.Unlock()
}

func (c *directQUICChannel) Close() error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil
	}
	c.closed = true
	p := c.p
	id := c.id
	s := c.s
	c.mu.Unlock()

	if p != nil && id != uuid.Nil {
		p.removeChannel(id)
	}
	if s != nil {
		_ = s.Close()
		// Ensure both directions are unblocked.
		s.CancelRead(0)
		s.CancelWrite(0)
	}
	return nil
}

func (c *directQUICChannel) WriteMessage(msg BaseMessage) error {
	if msg == nil {
		return errors.New("direct quic channel: nil message")
	}
	data, err := PackMessage(msg)
	if err != nil {
		return err
	}
	if len(data) > directQUICMaxFrameSize {
		return fmt.Errorf("direct quic channel: frame too large: %d", len(data))
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	hdr := make([]byte, directQUICFrameHeaderLen)
	binary.BigEndian.PutUint32(hdr, uint32(len(data)))
	if _, err := c.s.Write(hdr); err != nil {
		_ = c.Close()
		return err
	}
	if _, err := c.s.Write(data); err != nil {
		_ = c.Close()
		return err
	}
	return nil
}

func (c *directQUICChannel) ReadMessage(ctx context.Context) (BaseMessage, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = c.s.SetReadDeadline(deadline)
	} else {
		_ = c.s.SetReadDeadline(time.Time{})
	}

	hdr := make([]byte, directQUICFrameHeaderLen)
	if _, err := io.ReadFull(c.s, hdr); err != nil {
		_ = c.Close()
		return nil, err
	}
	l := int(binary.BigEndian.Uint32(hdr))
	if l <= 0 {
		return nil, errors.New("direct quic channel: invalid frame length")
	}
	if l > directQUICMaxFrameSize {
		return nil, fmt.Errorf("direct quic channel: frame too large: %d", l)
	}
	buf := make([]byte, l)
	if _, err := io.ReadFull(c.s, buf); err != nil {
		_ = c.Close()
		return nil, err
	}
	return ParseMessage(buf)
}
