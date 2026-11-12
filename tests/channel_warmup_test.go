package tests

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// lockedLogSink is a log destination safe for the client's concurrent writers
// and readable from the test goroutine.
type lockedLogSink struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *lockedLogSink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *lockedLogSink) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.String()
}

// TestPreparedChannelsSkipTheHandshake covers the prepared-channel pool that
// keeps the client's handshake off the connection path: after one proxied
// connection the client holds an established channel, and the next connection
// takes that channel instead of running a handshake of its own.
func TestPreparedChannelsSkipTheHandshake(t *testing.T) {
	echoListener, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	defer echoListener.Close()
	echoAddr := echoListener.Addr().(*net.TCPAddr)
	go func() {
		for {
			conn, acceptErr := echoListener.Accept()
			if acceptErr != nil {
				return
			}
			go func() {
				defer conn.Close()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	wsPort, err := getFreePort()
	require.NoError(t, err)

	server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(createPrefixedLogger("SRV-WARM")))
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	reverse, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	_, err = server.AddConnectorToken("CONNECTOR", reverse.Token)
	require.NoError(t, err)

	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:       wsPort,
		Token:        reverse.Token,
		LoggerPrefix: "CLT-WARM-PROVIDER",
		DirectMode:   linksocks.DirectModeRelayOnly,
	})
	defer provider.Close()

	sink := &lockedLogSink{}
	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:       wsPort,
		Token:        "CONNECTOR",
		LoggerPrefix: "CLT-WARM-CONNECTOR",
		LogSink:      sink,
		LogLevel:     zerolog.DebugLevel,
		DirectMode:   linksocks.DirectModeRelayOnly,
	})
	defer connector.Close()

	echoThroughProxy := func() {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", connector.SocksPort), 5*time.Second)
		require.NoError(t, dialErr)
		defer conn.Close()
		require.NoError(t, conn.SetDeadline(time.Now().Add(15*time.Second)))

		_, err = conn.Write([]byte{0x05, 0x01, 0x00})
		require.NoError(t, err)
		methodReply := make([]byte, 2)
		_, err = io.ReadFull(conn, methodReply)
		require.NoError(t, err)
		require.Equal(t, []byte{0x05, 0x00}, methodReply)

		request := []byte{0x05, 0x01, 0x00, 0x01}
		request = append(request, echoAddr.IP.To4()...)
		request = append(request, byte(echoAddr.Port>>8), byte(echoAddr.Port))
		_, err = conn.Write(request)
		require.NoError(t, err)
		connectReply := make([]byte, 10)
		_, err = io.ReadFull(conn, connectReply)
		require.NoError(t, err)
		require.Equal(t, byte(0x00), connectReply[1], "proxy refused the connection")

		payload := []byte("prepared-channel")
		_, err = conn.Write(payload)
		require.NoError(t, err)
		reply := make([]byte, len(payload))
		_, err = io.ReadFull(conn, reply)
		require.NoError(t, err)
		require.Equal(t, payload, reply, "echo mismatch through the proxy")
	}

	echoThroughProxy()

	// The pool refills in the background once the first channel is gone.
	require.Eventually(t, func() bool {
		return strings.Contains(sink.String(), "Prepared channel ready")
	}, 10*time.Second, 50*time.Millisecond, "client never prepared a channel")

	echoThroughProxy()

	require.Contains(t, sink.String(), "Using prepared channel",
		"the second proxied connection did not take the prepared channel")
}

// TestPreparedChannelsSkippedOnLegacyRelay covers the guard: a client that
// talks to a legacy relay must not try to prepare channels the relay cannot
// route.
func TestPreparedChannelsSkippedOnLegacyRelay(t *testing.T) {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(createPrefixedLogger("SRV-WARM-V1")))
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	reverse, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	_, err = server.AddConnectorToken("CONNECTOR", reverse.Token)
	require.NoError(t, err)

	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverse.Token,
		LoggerPrefix:    "CLT-WARM-V1-PROVIDER",
		ProtocolVersion: linksocks.ProtocolVersionV1,
	})
	defer provider.Close()

	sink := &lockedLogSink{}
	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT-WARM-V1-CONNECTOR",
		LogSink:         sink,
		LogLevel:        zerolog.DebugLevel,
		ProtocolVersion: linksocks.ProtocolVersionV1,
	})
	defer connector.Close()

	// Give the client time to warm a channel if it wrongly tried to.
	time.Sleep(1500 * time.Millisecond)
	require.NotContains(t, sink.String(), "Prepared channel ready",
		"a legacy client prepared an encrypted channel")
}
