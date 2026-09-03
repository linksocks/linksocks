package tests

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// startEchoServer starts a TCP echo server on 127.0.0.1 that keeps each
// accepted connection open until the client closes it. It returns the
// listener address and a stop function.
func startEchoServer(t *testing.T) (string, func()) {
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
				io.Copy(c, c)
			}(conn)
		}
	}()
	return ln.Addr().String(), func() {
		ln.Close()
		<-done
	}
}

// socks5Dial performs a no-auth SOCKS5 CONNECT through proxyAddr to
// host:port and returns the tunneled connection. The connection stays open
// for further reads and writes.
func socks5Dial(t *testing.T, proxyAddr string, host string, port int) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", proxyAddr, 3*time.Second)
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(10 * time.Second))

	if _, err := conn.Write([]byte{0x05, 0x01, 0x00}); err != nil {
		t.Fatalf("write socks greeting: %v", err)
	}
	reply := make([]byte, 2)
	if _, err := io.ReadFull(conn, reply); err != nil {
		t.Fatalf("read socks auth reply: %v", err)
	}
	require.Equal(t, []byte{0x05, 0x00}, reply, "socks auth failed")

	req := []byte{0x05, 0x01, 0x00}
	if ip := net.ParseIP(host); ip != nil && ip.To4() != nil {
		req = append(req, 0x01)
		req = append(req, ip.To4()...)
	} else {
		req = append(req, 0x03, byte(len(host)))
		req = append(req, host...)
	}
	req = append(req, byte(port>>8), byte(port))
	if _, err := conn.Write(req); err != nil {
		t.Fatalf("write socks connect request: %v", err)
	}

	// Read 4-byte header, then the variable bound address.
	header := make([]byte, 4)
	if _, err := io.ReadFull(conn, header); err != nil {
		t.Fatalf("read socks connect reply: %v", err)
	}
	require.Equal(t, byte(0x05), header[0], "unexpected socks version in reply")
	require.Equal(t, byte(0x00), header[1], "socks connect failed")
	addrLen := 0
	switch header[3] {
	case 0x01:
		addrLen = 4
	case 0x04:
		addrLen = 16
	case 0x03:
		b := make([]byte, 1)
		if _, err := io.ReadFull(conn, b); err != nil {
			t.Fatalf("read socks bound address length: %v", err)
		}
		addrLen = int(b[0])
	}
	buf := make([]byte, addrLen+2)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read socks bound address: %v", err)
	}
	conn.SetDeadline(time.Time{})
	return conn
}

// echoOnce sends payload through the tunnel and expects the exact echo back.
func echoOnce(t *testing.T, conn net.Conn, payload string, timeout time.Duration) {
	t.Helper()
	require.NoError(t, conn.SetDeadline(time.Now().Add(timeout)))
	if _, err := conn.Write([]byte(payload)); err != nil {
		t.Fatalf("write through tunnel: %v", err)
	}
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(conn, got); err != nil {
		t.Fatalf("read echo through tunnel: %v", err)
	}
	require.Equal(t, payload, string(got))
	require.NoError(t, conn.SetDeadline(time.Time{}))
}

// waitForChan is a convenience wrapper that times out like testify require.
func waitForChan(t *testing.T, ch <-chan struct{}, d time.Duration, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(d):
		t.Fatalf("timeout waiting for %s", what)
	}
}

// TestForwardChannelSurvivesWSDisconnect verifies that an established channel
// keeps flowing after the WebSocket link is dropped and the client reconnects:
// the same TCP connection carries data after the link comes back.
func TestForwardChannelSurvivesWSDisconnect(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	server := forwardServer(t, &ProxyTestServerOption{
		LoggerPrefix:   "SRV0",
		TransportGrace: 30 * time.Second,
	})
	defer server.Close()

	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          server.Token,
		LoggerPrefix:   "CLT0",
		Reconnect:      true,
		TransportGrace: 30 * time.Second,
	})
	defer client.Close()

	host, port := splitHostPort(t, echoAddr)
	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", client.SocksPort), host, port)
	defer tunnel.Close()

	// Baseline: the channel works before the drop.
	echoOnce(t, tunnel, "ping-before-drop", 10*time.Second)

	// Drop all WebSocket links; the client stays running and reconnects.
	client.Client.DisconnectWebSockets()
	waitForChan(t, client.Client.DisconnectedChan(), 5*time.Second, "client disconnection")

	// Data written while the link is down is buffered by the graceful write
	// path and must not be lost: it is replayed once the channel resumes.
	require.NoError(t, tunnel.SetDeadline(time.Now().Add(30*time.Second)))
	if _, err := tunnel.Write([]byte("ping-during-drop")); err != nil {
		t.Fatalf("write during drop: %v", err)
	}

	waitForChan(t, client.Client.ConnectedChan(), 15*time.Second, "client reconnection")

	// First the buffered message arrives, proving nothing was dropped...
	got := make([]byte, len("ping-during-drop"))
	if _, err := io.ReadFull(tunnel, got); err != nil {
		t.Fatalf("read replayed echo: %v", err)
	}
	require.Equal(t, "ping-during-drop", string(got))

	// ...then fresh traffic flows again in both directions.
	echoOnce(t, tunnel, "ping-after-reconnect", 10*time.Second)
}

// TestForwardChannelExpiresAfterTransportGrace verifies that when the link does
// not come back, traffic flowing into the dead channel is not retried forever:
// the grace window expires and the channel is torn down on the client side,
// closing the local SOCKS connection.
func TestForwardChannelExpiresAfterTransportGrace(t *testing.T) {
	echoAddr, stopEcho := startEchoServer(t)
	defer stopEcho()

	server := forwardServer(t, &ProxyTestServerOption{
		LoggerPrefix:   "SRV0",
		TransportGrace: 1500 * time.Millisecond,
	})
	defer server.Close()

	// No reconnect: the grace window expires and the channel must die.
	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:         server.WSPort,
		Token:          server.Token,
		LoggerPrefix:   "CLT0",
		Reconnect:      false,
		TransportGrace: 1500 * time.Millisecond,
	})
	defer client.Close()

	host, port := splitHostPort(t, echoAddr)
	tunnel := socks5Dial(t, fmt.Sprintf("127.0.0.1:%d", client.SocksPort), host, port)
	defer tunnel.Close()
	echoOnce(t, tunnel, "ping-before-drop", 10*time.Second)

	client.Client.DisconnectWebSockets()
	waitForChan(t, client.Client.DisconnectedChan(), 5*time.Second, "client disconnection")

	// Keep pushing traffic; the tunnel must fail closed around the grace
	// deadline instead of blocking forever. Write errors may surface on the
	// write side (grace expiry) or as echoes on the read side.
	start := time.Now()
	deadline := start.Add(15 * time.Second)
	payload := []byte("x")
	require.NoError(t, tunnel.SetDeadline(deadline))
	for {
		if _, err := tunnel.Write(payload); err != nil {
			require.Less(t, time.Since(start), 10*time.Second,
				"tunnel write blocked far beyond the grace window")
			t.Logf("tunnel closed after %s: %v", time.Since(start), err)
			return
		}
		if _, err := tunnel.Read(make([]byte, 1)); err != nil {
			require.Less(t, time.Since(start), 10*time.Second,
				"tunnel read failed far beyond the grace window")
			t.Logf("tunnel failed after %s: %v", time.Since(start), err)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}