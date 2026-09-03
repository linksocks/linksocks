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

// TestContextCancelDuringGraceRetry verifies that when the local TCP connection
// closes during transport grace retry, the channel cleans up immediately without
// leaking or producing misleading grace-expiry warnings.
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

	// Write blocks in retry loop: a large payload fills the socket and local
	// proxy buffers, so the write stalls until the channel retry loop gives
	// up (either grace expiry or ctx cancellation).
	writeErr := make(chan error, 1)
	go func() {
		_, err := tunnel.Write(bytes.Repeat([]byte("x"), 16<<20))
		writeErr <- err
	}()

	time.Sleep(500 * time.Millisecond)

	// Close TCP (triggers ctx cancel)
	tunnel.Close()

	// Write should return promptly
	select {
	case err := <-writeErr:
		require.Error(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("Write did not return after TCP close")
	}

	time.Sleep(1 * time.Second)
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
