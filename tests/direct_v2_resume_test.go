package tests

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// TestDirectV2ResumeKeepsConnectionAlive covers the lazy re-handshake: a
// channel running on the V2 direct dataplane loses its QUIC transport, and the
// entry side re-establishes the same channel through a fresh sealed session on
// the relay. The proxied connection must survive, and every byte must arrive
// exactly once: the resume replays only what the dead transport left
// unacknowledged, and both sides drop what they already consumed.
func TestDirectV2ResumeKeepsConnectionAlive(t *testing.T) {
	stunAddr, stunClose := startTestStunServer(t)
	defer stunClose()

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
		WithLogger(createPrefixedLogger("SRV-V2-RESUME")).
		WithAPI("TOKEN").
		WithDirectEnable(true))
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	reverse, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	_, err = server.AddConnectorToken("CONNECTOR", reverse.Token)
	require.NoError(t, err)

	reverseClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverse.Token,
		LoggerPrefix:    "CLT-V2-RESUME-PROVIDER",
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer reverseClient.Close()

	connectorSink := &lockedLogSink{}
	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT-V2-RESUME-CONNECTOR",
		LogSink:         connectorSink,
		LogLevel:        zerolog.DebugLevel,
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer connectorClient.Close()

	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 20*time.Second, 200*time.Millisecond, "V2 direct QUIC dataplane did not become active")

	proxyConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", connectorClient.SocksPort), 5*time.Second)
	require.NoError(t, err)
	defer proxyConn.Close()
	require.NoError(t, proxyConn.SetDeadline(time.Now().Add(60*time.Second)))

	_, err = proxyConn.Write([]byte{0x05, 0x01, 0x00})
	require.NoError(t, err)
	methodReply := make([]byte, 2)
	_, err = io.ReadFull(proxyConn, methodReply)
	require.NoError(t, err)
	require.Equal(t, []byte{0x05, 0x00}, methodReply)

	connectRequest := []byte{0x05, 0x01, 0x00, 0x01}
	connectRequest = append(connectRequest, echoAddr.IP.To4()...)
	connectRequest = append(connectRequest, byte(echoAddr.Port>>8), byte(echoAddr.Port))
	_, err = proxyConn.Write(connectRequest)
	require.NoError(t, err)
	connectReply := make([]byte, 10)
	_, err = io.ReadFull(proxyConn, connectReply)
	require.NoError(t, err)
	require.Equal(t, byte(0x00), connectReply[1], "proxy refused the connection")

	// Stream a paced payload so the transport can be broken mid-transfer,
	// while a substantial part of the stream is still in flight.
	const chunkSize = 4 * 1024
	const chunks = 96
	pattern := make([]byte, chunkSize)
	for i := range pattern {
		pattern[i] = byte(i % 251)
	}

	var receivedBytes atomic.Int64
	sendErr := make(chan error, 1)
	go func() {
		for i := 0; i < chunks; i++ {
			if _, werr := proxyConn.Write(pattern); werr != nil {
				sendErr <- fmt.Errorf("write chunk %d: %w", i, werr)
				return
			}
			time.Sleep(15 * time.Millisecond)
		}
		sendErr <- nil
	}()

	received := make([]byte, 0, chunkSize*chunks)
	var receivedMu sync.Mutex
	buf := make([]byte, chunkSize)
	readErr := make(chan error, 1)
	go func() {
		for {
			receivedMu.Lock()
			done := len(received) >= chunkSize*chunks
			receivedMu.Unlock()
			if done {
				readErr <- nil
				return
			}
			n, rerr := io.ReadFull(proxyConn, buf)
			receivedMu.Lock()
			received = append(received, buf[:n]...)
			receivedMu.Unlock()
			receivedBytes.Store(int64(len(received)))
			if rerr != nil {
				readErr <- fmt.Errorf("read at %d bytes: %w", len(received), rerr)
				return
			}
		}
	}()

	// Break the direct transport once the transfer is well underway but far
	// from finished, so the resume has to carry live data.
	require.Eventually(t, func() bool {
		return receivedBytes.Load() >= int64(10*chunkSize)
	}, 10*time.Second, 10*time.Millisecond, "transfer did not start")
	require.Less(t, int(receivedBytes.Load()), chunkSize*chunks, "transfer finished before the break")
	reverseClient.Client.ForceCloseDirectQUIC()
	connectorClient.Client.ForceCloseDirectQUIC()

	select {
	case err := <-sendErr:
		require.NoError(t, err, "sending over the resumed connection failed")
	case <-time.After(45 * time.Second):
		t.Fatal("sending did not finish after the direct transport broke")
	}
	select {
	case err := <-readErr:
		require.NoError(t, err, "reading over the resumed connection failed")
	case <-time.After(45 * time.Second):
		t.Fatal("reading did not finish after the direct transport broke")
	}

	receivedMu.Lock()
	got := append([]byte(nil), received...)
	receivedMu.Unlock()

	want := bytes.Repeat(pattern, chunks)
	require.Equal(t, len(want), len(got), "byte count changed across the resume")
	require.True(t, bytes.Equal(want, got), "payload corrupted across the resume")

	require.Contains(t, connectorSink.String(), "Resumed direct channel through the relay",
		"the connection survived without the channel being resumed on the relay")
	require.Greater(t, int(receivedBytes.Load()), 10*chunkSize,
		"the transfer did not continue after the break")
}

// A proxy connection that is only used after the direct plane died must not
// hang: the local proxy falls back to a fresh relay channel, keeping the
// payload sealed.
func TestDirectV2NewConnectionAfterBreakUsesRelay(t *testing.T) {
	stunAddr, stunClose := startTestStunServer(t)
	defer stunClose()

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
		WithLogger(createPrefixedLogger("SRV-V2-POSTBREAK")).
		WithAPI("TOKEN").
		WithDirectEnable(true))
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	reverse, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	_, err = server.AddConnectorToken("CONNECTOR", reverse.Token)
	require.NoError(t, err)

	reverseClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverse.Token,
		LoggerPrefix:    "CLT-V2-POSTBREAK-PROVIDER",
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT-V2-POSTBREAK-CONNECTOR",
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer connectorClient.Close()

	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 20*time.Second, 200*time.Millisecond, "V2 direct QUIC dataplane did not become active")

	forceBreak := func() {
		reverseClient.Client.ForceCloseDirectQUIC()
		connectorClient.Client.ForceCloseDirectQUIC()
	}
	forceBreak()
	require.Eventually(t, func() bool {
		return !connectorClient.Client.IsDirectQUICActive()
	}, 3*time.Second, 50*time.Millisecond, "direct dataplane still active after the forced break")

	var wg sync.WaitGroup
	defer wg.Wait()
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", connectorClient.SocksPort), 5*time.Second)
			require.NoError(t, dialErr)
			defer conn.Close()
			require.NoError(t, conn.SetDeadline(time.Now().Add(20*time.Second)))

			_, err := conn.Write([]byte{0x05, 0x01, 0x00})
			require.NoError(t, err)
			methodReply := make([]byte, 2)
			_, err = io.ReadFull(conn, methodReply)
			require.NoError(t, err)

			request := []byte{0x05, 0x01, 0x00, 0x01}
			request = append(request, echoAddr.IP.To4()...)
			request = append(request, byte(echoAddr.Port>>8), byte(echoAddr.Port))
			_, err = conn.Write(request)
			require.NoError(t, err)
			reply := make([]byte, 10)
			_, err = io.ReadFull(conn, reply)
			require.NoError(t, err)
			require.Equal(t, byte(0x00), reply[1], "connection %d refused after the direct break", i)

			payload := []byte(fmt.Sprintf("after-break-%d", i))
			_, err = conn.Write(payload)
			require.NoError(t, err)
			echo := make([]byte, len(payload))
			_, err = io.ReadFull(conn, echo)
			require.NoError(t, err)
			require.Equal(t, payload, echo, "echo mismatch on connection %d", i)
		}(i)
	}
}

// direct-only promises that the payload never falls back to the relay: when
// the direct dataplane dies mid-connection, the connection must fail instead
// of resuming through a sealed relay session.
func TestDirectOnlyFailsInsteadOfResumingThroughRelay(t *testing.T) {
	stunAddr, stunClose := startTestStunServer(t)
	defer stunClose()

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
		WithLogger(createPrefixedLogger("SRV-V2-DIRECT-ONLY")).
		WithAPI("TOKEN").
		WithDirectEnable(true))
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	reverse, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	_, err = server.AddConnectorToken("CONNECTOR", reverse.Token)
	require.NoError(t, err)

	reverseClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverse.Token,
		LoggerPrefix:    "CLT-V2-DIRECT-ONLY-PROVIDER",
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeDirectOnly,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer reverseClient.Close()

	connectorSink := &lockedLogSink{}
	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT-V2-DIRECT-ONLY-CONNECTOR",
		LogSink:         connectorSink,
		LogLevel:        zerolog.DebugLevel,
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeDirectOnly,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer connectorClient.Close()

	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 20*time.Second, 200*time.Millisecond, "V2 direct QUIC dataplane did not become active")

	proxyConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", connectorClient.SocksPort), 5*time.Second)
	require.NoError(t, err)
	defer proxyConn.Close()

	_, err = proxyConn.Write([]byte{0x05, 0x01, 0x00})
	require.NoError(t, err)
	methodReply := make([]byte, 2)
	_, err = io.ReadFull(proxyConn, methodReply)
	require.NoError(t, err)
	require.Equal(t, []byte{0x05, 0x00}, methodReply)

	connectRequest := []byte{0x05, 0x01, 0x00, 0x01}
	connectRequest = append(connectRequest, echoAddr.IP.To4()...)
	connectRequest = append(connectRequest, byte(echoAddr.Port>>8), byte(echoAddr.Port))
	_, err = proxyConn.Write(connectRequest)
	require.NoError(t, err)
	connectReply := make([]byte, 10)
	_, err = io.ReadFull(proxyConn, connectReply)
	require.NoError(t, err)
	require.Equal(t, byte(0x00), connectReply[1], "proxy refused the connection")

	// The connection runs on the direct dataplane: an echo proves it.
	probe := []byte("direct-only-probe")
	_, err = proxyConn.Write(probe)
	require.NoError(t, err)
	echo := make([]byte, len(probe))
	_, err = io.ReadFull(proxyConn, echo)
	require.NoError(t, err)
	require.Equal(t, probe, echo)

	reverseClient.Client.ForceCloseDirectQUIC()
	connectorClient.Client.ForceCloseDirectQUIC()
	require.Eventually(t, func() bool {
		return !connectorClient.Client.IsDirectQUICActive()
	}, 5*time.Second, 50*time.Millisecond, "direct dataplane still active after the forced break")

	// Any attempt to use the connection must now fail within the deadline;
	// direct-only must never quietly continue over the relay.
	closed := false
	deadline := time.Now().Add(20 * time.Second)
	scratch := make([]byte, len(probe))
	for time.Now().Before(deadline) {
		_ = proxyConn.SetDeadline(time.Now().Add(2 * time.Second))
		if _, werr := proxyConn.Write(probe); werr != nil {
			closed = true
			break
		}
		if _, rerr := io.ReadFull(proxyConn, scratch); rerr != nil {
			if ne, ok := rerr.(net.Error); ok && ne.Timeout() {
				continue
			}
			closed = true
			break
		}
	}
	require.True(t, closed, "direct-only connection stayed usable after the direct transport broke; connector logs:\n%s", connectorSink.String())
	require.NotContains(t, connectorSink.String(), "Resumed direct channel through the relay",
		"direct-only resumed the channel through the relay")
}

// sinkDigest keeps the routing-relevant lines of a capture sink for failure
// diagnostics: resume decisions, unknown channels, and errors.
func sinkDigest(s *lockedLogSink) string {
	var out []string
	for _, line := range strings.Split(s.String(), "\n") {
		if strings.Contains(line, "Resum") || strings.Contains(line, "resume") ||
			strings.Contains(line, "unknown channel") || strings.Contains(line, "expired") ||
			strings.Contains(line, "Binding") || strings.Contains(line, "binding") ||
			strings.Contains(line, "degraded") || strings.Contains(line, "refus") ||
			strings.Contains(line, "error") || strings.Contains(line, "Error") {
			out = append(out, strings.TrimSpace(line))
		}
	}
	const maxLines = 120
	if len(out) > maxLines {
		out = out[len(out)-maxLines:]
	}
	return strings.Join(out, "\n")
}

// With several providers online, a sealed resume must reach the provider that
// holds the dialed connection. A fresh round-robin pick can land on a machine
// without the channel, which would kill the connection instead of resuming it.
func TestDirectV2ResumeReachesTheBoundProvider(t *testing.T) {
	stunAddr, stunClose := startTestStunServer(t)
	defer stunClose()

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
		WithLogger(createPrefixedLogger("SRV-V2-BOUND-PROVIDER")).
		WithAPI("TOKEN").
		WithDirectEnable(true))
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	reverse, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	_, err = server.AddConnectorToken("CONNECTOR", reverse.Token)
	require.NoError(t, err)

	sinkA := &lockedLogSink{}
	providerA := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverse.Token,
		LoggerPrefix:    "CLT-V2-BOUND-PROVIDER-A",
		LogSink:         sinkA,
		LogLevel:        zerolog.DebugLevel,
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer providerA.Close()

	sinkB := &lockedLogSink{}
	providerB := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverse.Token,
		LoggerPrefix:    "CLT-V2-BOUND-PROVIDER-B",
		LogSink:         sinkB,
		LogLevel:        zerolog.DebugLevel,
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer providerB.Close()

	connectorSink := &lockedLogSink{}
	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT-V2-BOUND-PROVIDER-CONNECTOR",
		LogSink:         connectorSink,
		LogLevel:        zerolog.DebugLevel,
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer connectorClient.Close()

	require.Eventually(t, func() bool {
		if !connectorClient.Client.IsDirectQUICActive() {
			return false
		}
		return providerA.Client.IsDirectQUICActive() || providerB.Client.IsDirectQUICActive()
	}, 25*time.Second, 200*time.Millisecond, "no V2 direct QUIC dataplane became active")

	proxyConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", connectorClient.SocksPort), 5*time.Second)
	require.NoError(t, err)
	defer proxyConn.Close()
	require.NoError(t, proxyConn.SetDeadline(time.Now().Add(90*time.Second)))

	_, err = proxyConn.Write([]byte{0x05, 0x01, 0x00})
	require.NoError(t, err)
	methodReply := make([]byte, 2)
	_, err = io.ReadFull(proxyConn, methodReply)
	require.NoError(t, err)
	require.Equal(t, []byte{0x05, 0x00}, methodReply)

	connectRequest := []byte{0x05, 0x01, 0x00, 0x01}
	connectRequest = append(connectRequest, echoAddr.IP.To4()...)
	connectRequest = append(connectRequest, byte(echoAddr.Port>>8), byte(echoAddr.Port))
	_, err = proxyConn.Write(connectRequest)
	require.NoError(t, err)
	connectReply := make([]byte, 10)
	_, err = io.ReadFull(proxyConn, connectReply)
	require.NoError(t, err)
	require.Equal(t, byte(0x00), connectReply[1], "proxy refused the connection")

	const chunkSize = 4 * 1024
	const chunks = 96
	pattern := make([]byte, chunkSize)
	for i := range pattern {
		pattern[i] = byte(i % 251)
	}

	var receivedBytes atomic.Int64
	sendErr := make(chan error, 1)
	go func() {
		for i := 0; i < chunks; i++ {
			if _, werr := proxyConn.Write(pattern); werr != nil {
				sendErr <- fmt.Errorf("write chunk %d: %w", i, werr)
				return
			}
			time.Sleep(15 * time.Millisecond)
		}
		sendErr <- nil
	}()

	received := make([]byte, 0, chunkSize*chunks)
	var receivedMu sync.Mutex
	buf := make([]byte, chunkSize)
	readErr := make(chan error, 1)
	go func() {
		for {
			receivedMu.Lock()
			done := len(received) >= chunkSize*chunks
			receivedMu.Unlock()
			if done {
				readErr <- nil
				return
			}
			n, rerr := io.ReadFull(proxyConn, buf)
			receivedMu.Lock()
			received = append(received, buf[:n]...)
			receivedMu.Unlock()
			receivedBytes.Store(int64(len(received)))
			if rerr != nil {
				readErr <- fmt.Errorf("read at %d bytes: %w", len(received), rerr)
				return
			}
		}
	}()

	require.Eventually(t, func() bool {
		return receivedBytes.Load() >= int64(10*chunkSize)
	}, 10*time.Second, 10*time.Millisecond, "transfer did not start")
	require.Less(t, int(receivedBytes.Load()), chunkSize*chunks, "transfer finished before the break")

	providerA.Client.ForceCloseDirectQUIC()
	providerB.Client.ForceCloseDirectQUIC()
	connectorClient.Client.ForceCloseDirectQUIC()

	select {
	case err := <-sendErr:
		require.NoError(t, err, "sending over the resumed connection failed\nconnector:\n%s\nprovider A:\n%s\nprovider B:\n%s",
			sinkDigest(connectorSink), sinkDigest(sinkA), sinkDigest(sinkB))
	case <-time.After(60 * time.Second):
		t.Fatalf("sending did not finish after the direct transport broke\nconnector:\n%s\nprovider A:\n%s\nprovider B:\n%s",
			sinkDigest(connectorSink), sinkDigest(sinkA), sinkDigest(sinkB))
	}
	select {
	case err := <-readErr:
		require.NoError(t, err, "reading over the resumed connection failed\nconnector:\n%s\nprovider A:\n%s\nprovider B:\n%s",
			sinkDigest(connectorSink), sinkDigest(sinkA), sinkDigest(sinkB))
	case <-time.After(60 * time.Second):
		t.Fatalf("reading did not finish after the direct transport broke\nconnector:\n%s\nprovider A:\n%s\nprovider B:\n%s",
			sinkDigest(connectorSink), sinkDigest(sinkA), sinkDigest(sinkB))
	}

	receivedMu.Lock()
	got := append([]byte(nil), received...)
	receivedMu.Unlock()

	want := bytes.Repeat(pattern, chunks)
	require.Equal(t, len(want), len(got), "byte count changed across the resume")
	require.True(t, bytes.Equal(want, got), "payload corrupted across the resume")

	// The resume must never have landed on a provider without the channel:
	// that provider would log a dropped sealed frame or an unknown-channel
	// resume instead of continuing the transfer.
	for name, sink := range map[string]*lockedLogSink{"provider A": sinkA, "provider B": sinkB} {
		require.NotContains(t, sink.String(), "Dropped sealed frame for unknown channel", "resume landed on %s", name)
		require.NotContains(t, sink.String(), "resume requested for unknown channel", "resume landed on %s", name)
	}
	require.Contains(t, connectorSink.String(), "Resumed direct channel through the relay",
		"the connection survived without the channel being resumed on the relay")
}
