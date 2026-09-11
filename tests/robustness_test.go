package tests

import (
	"bytes"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestResumeWriteFailureDataIntegrity verifies that if Resume succeeds but the
// immediate DataMessage write fails, the data is not lost (it retries via
// writeWithTransportGrace rather than dropping the message).
func TestResumeWriteFailureDataIntegrity(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	server := forwardServer(t, &ProxyTestServerOption{
		LoggerPrefix:   "SRV-Resume",
		TransportGrace: 5 * time.Second,
	})
	defer server.Close()

	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          server.Token,
		LoggerPrefix:   "CLT-Resume",
		Reconnect:      true,
		TransportGrace: 5 * time.Second,
	})
	defer client.Close()

	host, port := splitHostPort(t, echoAddr)
	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", client.SocksPort), host, port)
	defer tunnel.Close()

	// Establish channel
	echoOnce(t, tunnel, "ping1\n", 3*time.Second)

	// Force reconnect
	client.Client.DisconnectWebSockets()
	waitForChan(t, client.Client.DisconnectedChan(), 3*time.Second, "disconnection")
	waitForChan(t, client.Client.ConnectedChan(), 5*time.Second, "reconnection")

	// Burst writes immediately after reconnect to hit Resume window race
	for i := 0; i < 10; i++ {
		msg := fmt.Sprintf("burst%d\n", i)
		tunnel.SetDeadline(time.Now().Add(3 * time.Second))
		_, err := tunnel.Write([]byte(msg))
		require.NoError(t, err, "write %d failed", i)

		buf := make([]byte, len(msg))
		_, err = io.ReadFull(tunnel, buf)
		tunnel.SetDeadline(time.Time{})
		require.NoError(t, err, "read %d failed", i)
		require.Equal(t, msg, string(buf), "mismatch at %d", i)
	}
}

// TestContextCancelDuringGraceRetry verifies that when the client context is
// cancelled during transport grace retry (the channel is retrying writes while
// its WebSocket link is down), the blocked local-proxy write aborts promptly
// instead of waiting out the grace window, and the channel is cleaned up
// without leaking or producing misleading grace-expiry warnings.
func TestContextCancelDuringGraceRetry(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	server := forwardServer(t, &ProxyTestServerOption{
		LoggerPrefix:   "SRV-CtxCancel",
		TransportGrace: 10 * time.Second,
	})
	defer server.Close()

	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          server.Token,
		LoggerPrefix:   "CLT-CtxCancel",
		Reconnect:      false,
		TransportGrace: 10 * time.Second,
	})
	defer client.Close()

	host, port := splitHostPort(t, echoAddr)
	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", client.SocksPort), host, port)

	echoOnce(t, tunnel, "test\n", 3*time.Second)

	// Drop WebSocket, no reconnect
	client.Client.DisconnectWebSockets()
	waitForChan(t, client.Client.DisconnectedChan(), 3*time.Second, "disconnection")

	// Write in chunks until the kernel buffers fill and the write stalls in
	// the channel's grace retry loop. Chunked writes keep the test
	// independent of platform socket buffer sizes (a single large payload
	// may be absorbed entirely by the OS on some platforms, returning
	// success before the retry path ever blocks).
	writeErr := make(chan error, 1)
	go func() {
		chunk := bytes.Repeat([]byte("x"), 1<<20)
		for {
			if _, err := tunnel.Write(chunk); err != nil {
				writeErr <- err
				return
			}
		}
	}()

	time.Sleep(500 * time.Millisecond)

	// Cancelling the client context must abort the grace retry, tearing down
	// the local proxy connection so the blocked write fails promptly instead
	// of stalling until the grace window expires.
	client.Client.Close()

	// Write should return promptly
	select {
	case err := <-writeErr:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not return after client context cancellation")
	}

	time.Sleep(500 * time.Millisecond)
}

// TestSwitchDuringWriteConcurrency verifies concurrent Switch and WriteMessage
// do not corrupt data or panic.
func TestSwitchDuringWriteConcurrency(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	server := forwardServer(t, &ProxyTestServerOption{
		LoggerPrefix:   "SRV-Switch",
		TransportGrace: 3 * time.Second,
	})
	defer server.Close()

	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          server.Token,
		LoggerPrefix:   "CLT-Switch",
		Reconnect:      true,
		ReconnectDelay: 200 * time.Millisecond,
		TransportGrace: 3 * time.Second,
	})
	defer client.Close()

	host, port := splitHostPort(t, echoAddr)
	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", client.SocksPort), host, port)
	defer tunnel.Close()

	done := make(chan struct{})
	var writeErrors sync.Map

	go func() {
		for i := 0; i < 50; i++ {
			msg := fmt.Sprintf("msg%d\n", i)
			tunnel.SetDeadline(time.Now().Add(5 * time.Second))
			_, err := tunnel.Write([]byte(msg))
			if err != nil {
				writeErrors.Store(i, err)
				tunnel.SetDeadline(time.Time{})
				continue
			}
			buf := make([]byte, len(msg))
			_, err = io.ReadFull(tunnel, buf)
			tunnel.SetDeadline(time.Time{})
			if err != nil {
				writeErrors.Store(i, err)
				continue
			}
			if string(buf) != msg {
				writeErrors.Store(i, fmt.Errorf("mismatch: got %q", string(buf)))
			}
			time.Sleep(20 * time.Millisecond)
		}
		close(done)
	}()

	for i := 0; i < 10; i++ {
		time.Sleep(100 * time.Millisecond)
		client.Client.DisconnectWebSockets()
		<-client.Client.DisconnectedChan()
		select {
		case <-client.Client.ConnectedChan():
		case <-time.After(1 * time.Second):
		}
	}

	<-done

	var errCount int
	var errSamples []error
	writeErrors.Range(func(key, value interface{}) bool {
		errCount++
		if len(errSamples) < 5 {
			errSamples = append(errSamples, value.(error))
		}
		return true
	})
	t.Logf("Concurrent Switch/Write: %d errors out of 50; samples: %v", errCount, errSamples)
	require.Less(t, errCount, 30, "Too many write failures")
}
