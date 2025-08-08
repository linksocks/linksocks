package tests

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/zetxtech/wssocks/wssocks"

	"github.com/stretchr/testify/require"
)

func setupTestServer(t *testing.T) (*wssocks.WSSocksServer, string, int) {
	wsPort, err := getFreePort()
	require.NoError(t, err)

	logger := createPrefixedLogger("SRV0")
	serverOpt := wssocks.DefaultServerOption().
		WithWSPort(wsPort).
		WithLogger(logger).
		WithAPI("TOKEN")
	server := wssocks.NewWSSocksServer(serverOpt)
	require.NoError(t, server.WaitReady(context.Background(), 5*time.Second))

	baseURL := fmt.Sprintf("http://localhost:%d", wsPort)
	return server, baseURL, wsPort
}

func TestApiRoot(t *testing.T) {
	server, baseURL, _ := setupTestServer(t)
	defer server.Close()

	resp, err := apiRequest(t, "GET", baseURL+"/", "", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Contains(t, string(body), "API endpoints available")
}

func TestApiCreateForwardToken(t *testing.T) {
	server, baseURL, wsPort := setupTestServer(t)
	defer server.Close()

	resp, err := apiRequest(t, "POST", baseURL+"/api/token", "TOKEN", wssocks.TokenRequest{
		Type: "forward",
	})
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var tokenResp wssocks.TokenResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&tokenResp))
	require.True(t, tokenResp.Success)
	require.NotEmpty(t, tokenResp.Token)
	require.Empty(t, tokenResp.Error)

	// Test the created token with a client
	client := forwardClient(t, &ProxyTestClientOption{
		WSPort:       wsPort,
		Token:        tokenResp.Token,
		LoggerPrefix: "CLT0",
	})
	defer client.Close()
	require.NoError(t, testWebConnection(globalHTTPServer, &ProxyConfig{Port: client.SocksPort}))
}

func TestApiStatus(t *testing.T) {
	server, baseURL, _ := setupTestServer(t)
	defer server.Close()

	_, err := server.AddForwardToken("")
	require.NoError(t, err)

	result, err := server.AddReverseToken(nil)
	require.NoError(t, err)
	reverseToken := result.Token

	_, err = server.AddConnectorToken("", reverseToken)
	require.NoError(t, err)

	resp, err := apiRequest(t, "GET", baseURL+"/api/status", "TOKEN", nil)
	require.NoError(t, err)
	defer resp.Body.Close()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	var statusResp wssocks.StatusResponse
	require.NoError(t, json.NewDecoder(resp.Body).Decode(&statusResp))
	require.Len(t, statusResp.Tokens, 2)
}

func TestApiUnauthorized(t *testing.T) {
	server, baseURL, _ := setupTestServer(t)
	defer server.Close()

	resp, err := apiRequest(t, "GET", baseURL+"/api/status", "WRONG_KEY", nil)
	require.NoError(t, err)
	defer resp.Body.Close()
	require.Equal(t, http.StatusUnauthorized, resp.StatusCode)
}
