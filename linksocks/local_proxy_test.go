package linksocks

import (
	"bufio"
	"bytes"
	"net"
	"strings"
	"testing"
)

func TestDetectLocalProxyProtocol(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		payload  string
		expected localProxyProtocol
	}{
		{name: "socks5", payload: "\x05\x01\x00", expected: localProxyProtocolSOCKS5},
		{name: "http-connect", payload: "CONNECT example.com:443 HTTP/1.1\r\n", expected: localProxyProtocolHTTP},
		{name: "http-get", payload: "GET http://example.com/ HTTP/1.1\r\n", expected: localProxyProtocolHTTP},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			reader := bufio.NewReader(strings.NewReader(testCase.payload))
			protocol, err := detectLocalProxyProtocol(reader)
			if err != nil {
				t.Fatalf("detectLocalProxyProtocol error: %v", err)
			}
			if protocol != testCase.expected {
				t.Fatalf("expected %v, got %v", testCase.expected, protocol)
			}
		})
	}
}

func TestParseHTTPProxyTarget(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name         string
		rawHost      string
		defaultPort  int
		expectedHost string
		expectedPort int
	}{
		{name: "host-port", rawHost: "example.com:8080", defaultPort: 80, expectedHost: "example.com", expectedPort: 8080},
		{name: "host-default", rawHost: "example.com", defaultPort: 80, expectedHost: "example.com", expectedPort: 80},
		{name: "ipv6-with-port", rawHost: "[2001:db8::1]:443", defaultPort: 80, expectedHost: "2001:db8::1", expectedPort: 443},
		{name: "ipv6-default", rawHost: "[2001:db8::1]", defaultPort: 443, expectedHost: "2001:db8::1", expectedPort: 443},
	}

	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			host, port, err := parseHTTPProxyTarget(testCase.rawHost, testCase.defaultPort)
			if err != nil {
				t.Fatalf("parseHTTPProxyTarget error: %v", err)
			}
			if host != testCase.expectedHost || port != testCase.expectedPort {
				t.Fatalf("expected %s:%d, got %s:%d", testCase.expectedHost, testCase.expectedPort, host, port)
			}
		})
	}
}

func TestBufferedConnPreservesPeekedBytes(t *testing.T) {
	t.Parallel()

	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	go func() {
		_, _ = client.Write([]byte("CONNECT example.com:443 HTTP/1.1\r\n\r\n"))
	}()

	buffered := newBufferedConn(server)
	protocol, err := detectLocalProxyProtocol(buffered.Reader())
	if err != nil {
		t.Fatalf("detect error: %v", err)
	}
	if protocol != localProxyProtocolHTTP {
		t.Fatalf("expected HTTP protocol")
	}

	line, err := buffered.Reader().ReadString('\n')
	if err != nil {
		t.Fatalf("read line error: %v", err)
	}
	if !strings.HasPrefix(line, "CONNECT example.com:443") {
		t.Fatalf("peek consumed bytes unexpectedly: %q", line)
	}

	// Drain remaining request terminator so the pipe can close cleanly.
	rest := make([]byte, 64)
	_, _ = buffered.Read(rest)
	_ = bytes.Contains(rest, []byte("\r\n"))
}
