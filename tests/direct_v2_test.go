package tests

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/stretchr/testify/require"
)

// TestDirectV2BuildsDataPlane covers direct mode on protocol V2. The peers
// exchange their X25519 keys through the relay, seal the candidate exchange
// with the derived key, and then carry the channel over direct QUIC.
func TestDirectV2BuildsDataPlane(t *testing.T) {
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
		WithLogger(createPrefixedLogger("SRV-V2-DIRECT")).
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
		LoggerPrefix:    "CLT-V2-DIRECT-REVERSE",
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT-V2-DIRECT-CONNECTOR",
		ProtocolVersion: linksocks.ProtocolVersionV2,
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer connectorClient.Close()

	// Both sides must turn on the direct QUIC dataplane; the relay signaling
	// that got them there never carried their addresses in the clear.
	require.Eventually(t, func() bool {
		return reverseClient.Client.IsDirectQUICActive() && connectorClient.Client.IsDirectQUICActive()
	}, 20*time.Second, 200*time.Millisecond, "V2 direct QUIC dataplane did not become active")

	proxyConn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", connectorClient.SocksPort), 5*time.Second)
	require.NoError(t, err)
	defer proxyConn.Close()
	_ = proxyConn.SetDeadline(time.Now().Add(15 * time.Second))

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
	require.Equal(t, byte(0x00), connectReply[1])

	payload := []byte("v2-direct-payload")
	_, err = proxyConn.Write(payload)
	require.NoError(t, err)
	reply := make([]byte, len(payload))
	_, err = io.ReadFull(proxyConn, reply)
	require.NoError(t, err)
	require.True(t, bytes.Equal(payload, reply), "echo mismatch over the V2 direct path: got %q", reply)
}

// TestDirectV2DirectOnlyDoesNotFallBack checks that direct-only keeps its
// meaning on V2: without a direct dataplane the local proxy refuses instead of
// silently sending the request over the relay.
func TestDirectV2DirectOnlyDoesNotFallBack(t *testing.T) {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	// Direct signaling stays disabled on the relay, so no direct path can form.
	server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(createPrefixedLogger("SRV-V2-DIRECT-ONLY")).
		WithAPI("TOKEN"))
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	reverse, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	_, err = server.AddConnectorToken("CONNECTOR", reverse.Token)
	require.NoError(t, err)

	reverseClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:       wsPort,
		Token:        reverse.Token,
		LoggerPrefix: "CLT-V2-DO-REVERSE",
		DirectMode:   linksocks.DirectModeAuto,
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:           wsPort,
		Token:            "CONNECTOR",
		LoggerPrefix:     "CLT-V2-DO-CONNECTOR",
		ProtocolVersion:  linksocks.ProtocolVersionV2,
		DirectMode:       linksocks.DirectModeDirectOnly,
		DirectOnlyAction: linksocks.DirectOnlyActionRefuse,
	})
	defer connectorClient.Close()

	require.Eventually(t, func() bool {
		conn, dialErr := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", connectorClient.SocksPort), 2*time.Second)
		if dialErr != nil {
			return false
		}
		defer conn.Close()
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, werr := conn.Write([]byte{0x05, 0x01, 0x00}); werr != nil {
			return false
		}
		greeting := make([]byte, 2)
		if _, rerr := io.ReadFull(conn, greeting); rerr != nil || greeting[0] != 0x05 {
			return false
		}
		if greeting[1] == 0xff {
			// The proxy already refused during method negotiation.
			return true
		}
		// Ask for a connection; direct-only must refuse it instead of quietly
		// moving the request (and its destination) onto the relay.
		request := []byte{0x05, 0x01, 0x00, 0x01, 127, 0, 0, 1, 0x00, 0x09}
		if _, werr := conn.Write(request); werr != nil {
			return false
		}
		reply := make([]byte, 10)
		if _, rerr := io.ReadFull(conn, reply); rerr != nil {
			return false
		}
		return reply[0] == 0x05 && reply[1] != 0x00
	}, 25*time.Second, 250*time.Millisecond, "direct-only did not refuse the proxy request on V2")
}
