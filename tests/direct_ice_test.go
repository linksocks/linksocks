package tests

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/linksocks/linksocks/linksocks"
	"github.com/stretchr/testify/require"
)

const (
	testStunMagicCookie uint32 = 0x2112A442
	stunBindingRequest  uint16 = 0x0001
	stunBindingResponse uint16 = 0x0101
	stunAttrXorMapped   uint16 = 0x0020
)

func startTestStunServer(t *testing.T) (addr string, closeFn func()) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		defer conn.Close()
		buf := make([]byte, 2048)
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}
			_ = conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
			n, from, err := conn.ReadFromUDP(buf)
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				return
			}
			if n < 20 {
				continue
			}
			if binary.BigEndian.Uint16(buf[0:2]) != stunBindingRequest {
				continue
			}
			if binary.BigEndian.Uint32(buf[4:8]) != testStunMagicCookie {
				continue
			}

			var txID [12]byte
			copy(txID[:], buf[8:20])

			resp := buildStunBindingResponse(txID, from)
			_, _ = conn.WriteToUDP(resp, from)
		}
	}()

	la := conn.LocalAddr().(*net.UDPAddr)
	return fmt.Sprintf("127.0.0.1:%d", la.Port), func() { cancel() }
}

func buildStunBindingResponse(txID [12]byte, from *net.UDPAddr) []byte {
	ip4 := from.IP.To4()
	if ip4 == nil {
		ip4 = net.IPv4(127, 0, 0, 1)
	}
	port := uint16(from.Port)

	xPort := port ^ uint16(testStunMagicCookie>>16)
	xIP := binary.BigEndian.Uint32(ip4) ^ testStunMagicCookie

	attr := make([]byte, 4+8)
	binary.BigEndian.PutUint16(attr[0:2], stunAttrXorMapped)
	binary.BigEndian.PutUint16(attr[2:4], 8)
	attr[4] = 0
	attr[5] = 0x01 // IPv4
	binary.BigEndian.PutUint16(attr[6:8], xPort)
	binary.BigEndian.PutUint32(attr[8:12], xIP)

	msg := make([]byte, 20+len(attr))
	binary.BigEndian.PutUint16(msg[0:2], stunBindingResponse)
	binary.BigEndian.PutUint16(msg[2:4], uint16(len(attr)))
	binary.BigEndian.PutUint32(msg[4:8], testStunMagicCookie)
	copy(msg[8:20], txID[:])
	copy(msg[20:], attr)
	return msg
}

func TestDirectCandidateExchangeAndProbing_Auto(t *testing.T) {
	stunAddr, stunClose := startTestStunServer(t)
	defer stunClose()

	wsPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := linksocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithAPI("TOKEN").
		WithDirectEnable(true)
	server := linksocks.NewLinkSocksServer(serverOpt)
	defer server.Close()
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	rev, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	reverseToken := rev.Token

	_, err = server.AddConnectorToken("CONNECTOR", reverseToken)
	require.NoError(t, err)

	reverseClient := reverseClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           reverseToken,
		LoggerPrefix:    "CLT1",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer reverseClient.Close()

	connectorClient := forwardClient(t, &ProxyTestClientOption{
		WSPort:          wsPort,
		Token:           "CONNECTOR",
		LoggerPrefix:    "CLT2",
		DirectMode:      linksocks.DirectModeAuto,
		DirectDiscovery: linksocks.DirectDiscoverySTUN,
		StunServers:     []string{stunAddr},
	})
	defer connectorClient.Close()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", wsPort)
	deadline := time.Now().Add(6 * time.Second)
	var lastPeers []linksocks.DirectPeerStatus

	for time.Now().Before(deadline) {
		resp, err := apiRequest(t, "GET", baseURL+"/api/status", "TOKEN", nil)
		require.NoError(t, err)
		var status linksocks.StatusResponse
		require.NoError(t, json.NewDecoder(resp.Body).Decode(&status))
		_ = resp.Body.Close()

		if status.Direct != nil {
			lastPeers = status.Direct.Peers
			ready := 0
			for _, p := range status.Direct.Peers {
				if !p.SupportsDirect {
					continue
				}
				if p.Role != "reverse" && p.Role != "connector" {
					continue
				}
				if p.LastDirectState == "ready" {
					ready++
				}
			}
			if ready >= 2 {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}

	t.Fatalf("direct probing did not become ready in time; peers=%v", lastPeers)
}
