package tests

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

const (
	udpTestAttempts = 10 // Number of UDP test attempts
)

// ProxyConfig contains proxy configuration options
type ProxyConfig struct {
	Port     int
	Username string
	Password string
}

// testWebConnection tests HTTP connection through the proxy
func testWebConnection(targetURL string, proxyConfig *ProxyConfig) error {
	var httpClient *http.Client

	if proxyConfig != nil {
		proxyURL := fmt.Sprintf("socks5://%s", net.JoinHostPort("127.0.0.1", fmt.Sprint(proxyConfig.Port)))
		if proxyConfig.Username != "" || proxyConfig.Password != "" {
			proxyURL = fmt.Sprintf("socks5://%s:%s@%s",
				url.QueryEscape(proxyConfig.Username),
				url.QueryEscape(proxyConfig.Password),
				net.JoinHostPort("127.0.0.1", fmt.Sprint(proxyConfig.Port)))
		}

		parsedURL, err := url.Parse(proxyURL)
		if err != nil {
			return err
		}

		httpClient = &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(parsedURL),
			},
			Timeout: 5 * time.Second,
		}
	} else {
		httpClient = &http.Client{
			Timeout: 5 * time.Second,
		}
	}

	// Log test start
	if proxyConfig != nil {
		TestLogger.Info().
			Str("url", targetURL).
			Int("proxy_port", proxyConfig.Port).
			Msg("Starting web connection test with proxy")
	} else {
		TestLogger.Info().
			Str("url", targetURL).
			Msg("Starting web connection test without proxy")
	}

	resp, err := httpClient.Get(targetURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	// Log test completion
	TestLogger.Info().
		Str("url", targetURL).
		Int("status", resp.StatusCode).
		Msg("Web connection test completed")

	return nil
}

// assertUDPConnection tests UDP connection through the proxy
func assertUDPConnection(t *testing.T, serverAddr string, proxyConfig *ProxyConfig) {
	testData := []byte("Hello UDP")
	successCount := 0

	var proxyAddr string
	if proxyConfig != nil {
		proxyAddr = net.JoinHostPort("127.0.0.1", fmt.Sprint(proxyConfig.Port))
	} else {
		proxyAddr = serverAddr
	}

	var conn net.Conn
	if proxyConfig != nil {
		// Create TCP connection for SOCKS5 negotiation
		tcpConn, err := net.Dial("tcp", proxyAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer tcpConn.Close()

		// SOCKS5 handshake
		_, err = tcpConn.Write([]byte{0x05, 0x01, 0x00})
		if err != nil {
			t.Fatal(err)
		}

		// Read handshake response
		resp := make([]byte, 2)
		_, err = io.ReadFull(tcpConn, resp)
		if err != nil {
			t.Fatal(err)
		}
		if resp[0] != 0x05 || resp[1] != 0x00 {
			t.Fatal("SOCKS5 handshake failed")
		}

		// UDP ASSOCIATE request
		_, err = tcpConn.Write([]byte{0x05, 0x03, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
		if err != nil {
			t.Fatal(err)
		}

		// Read UDP ASSOCIATE response
		resp = make([]byte, 10)
		_, err = io.ReadFull(tcpConn, resp)
		if err != nil {
			t.Fatal(err)
		}
		if resp[1] != 0x00 {
			t.Fatal("UDP ASSOCIATE failed")
		}

		// Get UDP relay address and port
		relayPort := binary.BigEndian.Uint16(resp[8:10])
		proxyAddr = fmt.Sprintf("127.0.0.1:%d", relayPort)

		// Create UDP connection
		conn, err = net.Dial("udp", proxyAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		// Parse target address
		host, portStr, err := net.SplitHostPort(serverAddr)
		if err != nil {
			t.Fatal(err)
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			t.Fatal(err)
		}

		// Test UDP communication
		for i := 0; i < udpTestAttempts; i++ {
			conn.SetDeadline(time.Now().Add(3 * time.Second))

			// Create SOCKS5 UDP header
			var header []byte
			ip := net.ParseIP(host)
			if ip == nil {
				// Domain name
				header = []byte{0, 0, 0, 0x03, byte(len(host))}
				header = append(header, []byte(host)...)
			} else if ip4 := ip.To4(); ip4 != nil {
				// IPv4
				header = []byte{0, 0, 0, 0x01}
				header = append(header, ip4...)
			} else {
				// IPv6
				header = []byte{0, 0, 0, 0x04}
				header = append(header, ip.To16()...)
			}
			header = append(header, byte(port>>8), byte(port))

			// Send data with UDP header
			sendData := append(header, testData...)
			_, err = conn.Write(sendData)
			if err != nil {
				TestLogger.Error().Err(err).Msg("UDP test failed")
				continue
			}
			// TestLogger.Info().Int("bytes", len(sendData)).Msg("UDP tester sent")

			// Read response with a larger buffer to accommodate any header format
			buf := make([]byte, 1024) // Large enough for any SOCKS5 UDP header + data
			n, err := conn.Read(buf)
			if err != nil {
				TestLogger.Error().Err(err).Msg("UDP test failed")
				continue
			}
			// TestLogger.Info().Int("bytes", n).Msg("UDP tester received")

			// Find the actual data after the UDP header
			var responseData []byte
			if n > 10 { // Minimum SOCKS5 UDP header size is 10 bytes
				switch buf[3] { // Address type
				case 0x01: // IPv4
					responseData = buf[10:n]
				case 0x03: // Domain
					domainLen := int(buf[4])
					responseData = buf[5+domainLen+2 : n]
				case 0x04: // IPv6
					responseData = buf[22:n]
				}
			}

			if responseData != nil && bytes.Equal(responseData, testData) {
				successCount++
			}
		}
	} else {
		conn, err := net.Dial("udp", proxyAddr)
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()

		for i := 0; i < udpTestAttempts; i++ {
			conn.SetDeadline(time.Now().Add(3 * time.Second))
			_, err = conn.Write(testData)
			if err != nil {
				TestLogger.Error().Err(err).Msg("UDP test failed")
				continue
			}
			// TestLogger.Info().Int("data", len(testData)).Msg("UDP tester sent")

			buf := make([]byte, len(testData))
			n, err := conn.Read(buf)
			if err != nil {
				TestLogger.Error().Err(err).Msg("UDP test failed")
				continue
			}
			// TestLogger.Info().Int("bytes", n).Msg("UDP tester received")

			if n == len(testData) && string(buf[:n]) == string(testData) {
				successCount++
			}
		}
	}

	if successCount < udpTestAttempts {
		t.Errorf("UDP test failed: only %d/%d packets were successfully echoed", successCount, udpTestAttempts)
	}
}

// apiRequest is a helper function to send API requests to the test server
func apiRequest(t *testing.T, method, url string, apiKey string, body interface{}) (*http.Response, error) {
	var bodyReader io.Reader
	if body != nil {
		bodyBytes, err := json.Marshal(body)
		require.NoError(t, err)
		bodyReader = bytes.NewReader(bodyBytes)
	}

	req, err := http.NewRequest(method, url, bodyReader)
	require.NoError(t, err)

	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	client := &http.Client{}
	return client.Do(req)
}
