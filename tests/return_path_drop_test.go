package tests

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startDelayedEchoServer starts a TCP echo server that delays each echoed
// response by delay, allowing tests to drop links while data is in flight.
func startDelayedEchoServer(t *testing.T, delay time.Duration) (string, func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				buf := make([]byte, 4096)
				for {
					n, err := c.Read(buf)
					if err != nil {
						return
					}
					time.Sleep(delay)
					if _, err := c.Write(buf[:n]); err != nil {
						return
					}
				}
			}(conn)
		}
	}()
	return ln.Addr().String(), func() {
		ln.Close()
		<-done
	}
}

// TestConnectorReturnPathDataDuringConnectorDrop verifies that return-path
// data (provider -> connector) arriving at the proxy while the connector link
// is down is not silently dropped within the grace window.
func TestConnectorReturnPathDataDuringConnectorDrop(t *testing.T) {
	grace := 30 * time.Second
	server, provider, connector := connectorEnv(t, grace)
	defer server.Close()
	defer provider.Close()
	defer connector.Close()

	// Echo with an 800ms delay so the reply lands at the proxy after the
	// connector link is torn down (the reconnect backoff starts at 1s).
	echoAddr, stopEcho := startDelayedEchoServer(t, 800*time.Millisecond)
	defer stopEcho()

	host, port := splitHostPort(t, echoAddr)
	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", connector.SocksPort), host, port)
	defer tunnel.Close()

	// Warm up and finalize the channel before the drop.
	tunnel.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := tunnel.Write([]byte("warmup\n")); err != nil {
		t.Fatal(err)
	}
	wbuf := make([]byte, len("warmup\n"))
	if _, err := io.ReadFull(tunnel, wbuf); err != nil {
		t.Fatalf("warmup read: %v", err)
	}

	// Send a request, then drop the connector link while the reply is still
	// being delayed at the echo server.
	tunnel.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err := tunnel.Write([]byte("return-path\n")); err != nil {
		t.Fatal(err)
	}

	time.Sleep(150 * time.Millisecond) // let the request reach the echo server
	connector.Client.DisconnectWebSockets()
	waitForChan(t, connector.Client.DisconnectedChan(), 5*time.Second, "connector disconnection")

	// The reply arrives at the proxy while the connector is down; it must be
	// buffered and delivered after the channel is rebound.
	waitForChan(t, connector.Client.ConnectedChan(), 10*time.Second, "connector reconnection")

	buf := make([]byte, len("return-path\n"))
	tunnel.SetReadDeadline(time.Now().Add(3 * time.Second))
	if _, err := io.ReadFull(tunnel, buf); err != nil {
		t.Fatal("return-path data lost during connector drop:", err)
	}
	require.Equal(t, "return-path\n", string(buf))
}