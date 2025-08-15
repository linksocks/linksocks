package tests

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestHTTPServer(t *testing.T) {
	require.NoError(t, testWebConnection(globalHTTPServer, nil))
}

func TestHTTPServerV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	require.NoError(t, testWebConnection(globalHTTPServerV6, nil))
}

func TestUDPServer(t *testing.T) {
	require.NoError(t, testUDPConnection(t, globalUDPServer, nil))
}

func TestUDPServerV6(t *testing.T) {
	if !hasIPv6Support() {
		t.Skip("IPv6 is not supported")
	}
	require.NoError(t, testUDPConnection(t, globalUDPServerV6, nil))
}
