package linksocks

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func buildStunBindingResponseXorMapped(txID [12]byte, ip net.IP, port int) []byte {
	// XOR-MAPPED-ADDRESS
	// 0: reserved
	// 1: family
	// 2-3: x-port
	// 4..: x-address

	fam := byte(0x01)
	ip4 := ip.To4()
	if ip4 == nil {
		fam = 0x02
	}

	attr := make([]byte, 0, 4+20)
	attr = append(attr, 0, 0, 0, 0)
	binary.BigEndian.PutUint16(attr[0:2], stunAttrXorMappedAddress)

	var av []byte
	if fam == 0x01 {
		av = make([]byte, 8)
		av[0] = 0
		av[1] = fam
		xPort := uint16(port) ^ uint16(stunMagicCookie>>16)
		binary.BigEndian.PutUint16(av[2:4], xPort)
		cookie := make([]byte, 4)
		binary.BigEndian.PutUint32(cookie, stunMagicCookie)
		av[4] = ip4[0] ^ cookie[0]
		av[5] = ip4[1] ^ cookie[1]
		av[6] = ip4[2] ^ cookie[2]
		av[7] = ip4[3] ^ cookie[3]
	} else {
		av = make([]byte, 20)
		av[0] = 0
		av[1] = fam
		xPort := uint16(port) ^ uint16(stunMagicCookie>>16)
		binary.BigEndian.PutUint16(av[2:4], xPort)
		mask := make([]byte, 16)
		binary.BigEndian.PutUint32(mask[0:4], stunMagicCookie)
		copy(mask[4:16], txID[:])
		for i := 0; i < 16; i++ {
			av[4+i] = ip[i] ^ mask[i]
		}
	}

	binary.BigEndian.PutUint16(attr[2:4], uint16(len(av)))
	attr = append(attr, av...)
	// pad to 4 bytes
	pad := (4 - (len(av) % 4)) % 4
	for i := 0; i < pad; i++ {
		attr = append(attr, 0)
	}

	msgLen := len(attr)
	resp := make([]byte, 20)
	binary.BigEndian.PutUint16(resp[0:2], stunTypeBindingResponse)
	binary.BigEndian.PutUint16(resp[2:4], uint16(msgLen))
	binary.BigEndian.PutUint32(resp[4:8], stunMagicCookie)
	copy(resp[8:20], txID[:])
	resp = append(resp, attr...)
	return resp
}

func TestParseStunBindingResponse_XorMappedAddress_IPv4(t *testing.T) {
	var txID [12]byte
	_, err := rand.Read(txID[:])
	require.NoError(t, err)

	resp := buildStunBindingResponseXorMapped(txID, net.IPv4(203, 0, 113, 7), 54321)
	addr, port, err := ParseStunBindingResponse(resp, txID)
	require.NoError(t, err)
	require.Equal(t, "203.0.113.7", addr)
	require.Equal(t, 54321, port)
}

func TestStunDiscover_ParallelPickFastest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Start two local UDP STUN-like servers. One replies slower.
	fastConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer fastConn.Close()

	slowConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer slowConn.Close()

	serve := func(c *net.UDPConn, delay time.Duration, mappedIP net.IP, mappedPort int) {
		buf := make([]byte, 1500)
		for {
			_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, ra, err := c.ReadFromUDP(buf)
			if err != nil {
				ne, ok := err.(net.Error)
				if ok && ne.Timeout() {
					select {
					case <-ctx.Done():
						return
					default:
						continue
					}
				}
				return
			}
			if n < 20 {
				continue
			}
			var txID [12]byte
			copy(txID[:], buf[8:20])
			resp := buildStunBindingResponseXorMapped(txID, mappedIP, mappedPort)
			time.Sleep(delay)
			_, _ = c.WriteToUDP(resp, ra)
		}
	}

	go serve(fastConn, 10*time.Millisecond, net.IPv4(198, 51, 100, 1), 40001)
	go serve(slowConn, 120*time.Millisecond, net.IPv4(198, 51, 100, 2), 40002)

	opt := DefaultStunDiscoverOption()
	opt.Timeout = 500 * time.Millisecond
	opt.Servers = []string{
		fastConn.LocalAddr().String(),
		slowConn.LocalAddr().String(),
	}

	res, err := StunDiscover(ctx, opt)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "198.51.100.1", res.Addr)
	require.Equal(t, 40001, res.Port)
	require.Equal(t, "srflx", res.Candidate.Kind)
}

func TestStunDiscoverFromConn_ParallelPickFastest(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	fastConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer fastConn.Close()

	slowConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer slowConn.Close()

	serve := func(c *net.UDPConn, delay time.Duration, mappedIP net.IP, mappedPort int) {
		buf := make([]byte, 1500)
		for {
			_ = c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
			n, ra, err := c.ReadFromUDP(buf)
			if err != nil {
				ne, ok := err.(net.Error)
				if ok && ne.Timeout() {
					select {
					case <-ctx.Done():
						return
					default:
						continue
					}
				}
				return
			}
			if n < 20 {
				continue
			}
			var txID [12]byte
			copy(txID[:], buf[8:20])
			resp := buildStunBindingResponseXorMapped(txID, mappedIP, mappedPort)
			time.Sleep(delay)
			_, _ = c.WriteToUDP(resp, ra)
		}
	}

	go serve(fastConn, 10*time.Millisecond, net.IPv4(203, 0, 113, 10), 51010)
	go serve(slowConn, 120*time.Millisecond, net.IPv4(203, 0, 113, 20), 52020)

	sharedConn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(0, 0, 0, 0), Port: 0})
	require.NoError(t, err)
	defer sharedConn.Close()

	opt := DefaultStunDiscoverOption()
	opt.Timeout = 800 * time.Millisecond
	opt.Servers = []string{fastConn.LocalAddr().String(), slowConn.LocalAddr().String()}

	res, err := StunDiscoverFromConn(ctx, sharedConn, opt)
	require.NoError(t, err)
	require.NotNil(t, res)
	require.Equal(t, "203.0.113.10", res.Addr)
	require.Equal(t, 51010, res.Port)
	require.Equal(t, "srflx", res.Candidate.Kind)
}

func TestDefaultStunServers_ContainsRequestedServers(t *testing.T) {
	servers := defaultStunServers()
	require.Contains(t, servers, "stun.miwifi.com:3478")
	require.Contains(t, servers, "stun.chat.bilibili.com:3478")
	require.Contains(t, servers, "stun.cloudflare.com:3478")
	require.Contains(t, servers, "stun.qq.com:3478")
}
