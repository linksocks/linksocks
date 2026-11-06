package tests

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/linksocks/linksocks/linksocks"
	"github.com/stretchr/testify/require"
)

// lockedBuffer is a concurrency-safe log sink.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestProtocolV2RelaySeesNoPlaintext proves the protocol V2 promise end to
// end: a connector and a provider exchange a real tunneled request through a
// relay whose every forwarded frame is captured in the log, and the captured
// relay view must contain the sealed (channel_open/sealed) frames and the
// channel ID -- but never the destination address, port, or payload.
func TestProtocolV2RelaySeesNoPlaintext(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	targetHost, targetPort := splitHostPort(t, echoAddr)
	targetPortStr := fmt.Sprintf("%d", targetPort)

	var sink lockedBuffer
	server := reverseServer(t, &ProxyTestServerOption{
		ConnectorToken: "CONNECTOR",
		LoggerPrefix:   "SRV-V2",
		LogSink:        &sink,
	})
	defer server.Close()

	provider := reverseClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        server.Token,
		LoggerPrefix: "CLT1",
	})
	defer provider.Close()

	connector := forwardClient(t, &ProxyTestClientOption{
		WSPort:       server.WSPort,
		Token:        "CONNECTOR",
		LoggerPrefix: "CLT2",
	})
	defer connector.Close()

	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", connector.SocksPort), targetHost, targetPort)
	defer tunnel.Close()

	payload := "v2-secret-payload-marker"
	echoOnce(t, tunnel, payload, 5*time.Second)

	// The relay logs forwarded frames at trace level; give any in-flight
	// trace writes a moment to land in the sink.
	require.Eventually(t, func() bool {
		return strings.Contains(sink.String(), "TYPE=sealed")
	}, 3*time.Second, 50*time.Millisecond, "relay never logged a sealed frame")

	logs := sink.String()
	require.Contains(t, logs, "TYPE=channel_open", "relay should bind the channel with channel_open")
	require.Contains(t, logs, "TYPE=sealed", "relay should only forward sealed frames")
	require.NotContains(t, logs, "TYPE=connect ", "relay must never see a plaintext connect for a V2 channel")
	require.NotContains(t, logs, "TYPE=data ", "relay must never see plaintext data for a V2 channel")
	require.NotContains(t, logs, `"address":"`, "relay must not see a destination address")
	require.NotContains(t, logs, ":"+targetPortStr, "relay must not see the destination port")
	require.NotContains(t, logs, payload, "relay must not see the payload")
}

// TestProtocolV2MessageRoundTrip covers the wire format of the two protocol V2
// message types and the dual-generation parser.
func TestProtocolV2MessageRoundTrip(t *testing.T) {
	id := uuid.New()

	openWithResume := linksocks.ChannelOpenMessage{ChannelID: id, Resume: true}
	frame, err := linksocks.PackMessage(openWithResume)
	require.NoError(t, err)
	require.Equal(t, byte(0x20), frame[0], "V2 frames are stamped with protocol generation 2")
	msg, version, err := linksocks.ParseMessageWithVersion(frame)
	require.NoError(t, err)
	require.Equal(t, byte(0x20), version)
	open, ok := msg.(linksocks.ChannelOpenMessage)
	require.True(t, ok)
	require.Equal(t, id, open.ChannelID)
	require.True(t, open.Resume)

	sealed := linksocks.SealedMessage{ChannelID: id, Data: []byte{0x01, 0x02, 0x03, 0xFC}}
	frame, err = linksocks.PackMessage(sealed)
	require.NoError(t, err)
	msg, _, err = linksocks.ParseMessageWithVersion(frame)
	require.NoError(t, err)
	got, ok := msg.(linksocks.SealedMessage)
	require.True(t, ok)
	require.Equal(t, id, got.ChannelID)
	require.Equal(t, sealed.Data, got.Data)

	// Legacy generation frames still parse; unknown major generations do not.
	legacy, err := linksocks.PackMessageWithVersion(openWithResume, linksocks.ProtocolVersionV1)
	require.NoError(t, err)
	_, version, err = linksocks.ParseMessageWithVersion(legacy)
	require.NoError(t, err)
	require.Equal(t, linksocks.ProtocolVersionV1, version)

	future, err := linksocks.PackMessageWithVersion(openWithResume, 0x30)
	require.NoError(t, err)
	_, _, err = linksocks.ParseMessageWithVersion(future)
	require.Error(t, err)
}
