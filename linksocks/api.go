package linksocks

import (
	"encoding/json"
	"net/http"
	"strings"
)

// APIHandler handles HTTP API requests for LinkSocksServer
type APIHandler struct {
	server *LinkSocksServer
	apiKey string
}

// NewAPIHandler creates a new API handler for the given server
func NewAPIHandler(server *LinkSocksServer, apiKey string) *APIHandler {
	return &APIHandler{
		server: server,
		apiKey: apiKey,
	}
}

// RegisterHandlers registers API endpoints with the provided mux
func (h *APIHandler) RegisterHandlers(mux *http.ServeMux) {
	mux.HandleFunc("/api/token", h.handleToken)
	mux.HandleFunc("/api/token/", h.handleToken)
	mux.HandleFunc("/api/status", h.handleStatus)
}

// TokenRequest represents a request to create a new token
type TokenRequest struct {
	Type                 string `json:"type"`          // "forward" or "reverse" or "connector"
	Token                string `json:"token"`         // Optional: specific token to use
	Port                 int    `json:"port"`          // Optional: specific port for reverse proxy
	Username             string `json:"username"`      // Optional: SOCKS auth username
	Password             string `json:"password"`      // Optional: SOCKS auth password
	ReverseToken         string `json:"reverse_token"` // Optional: reverse token for connector token
	AllowManageConnector bool   `json:"allow_manage_connector"`
}

// TokenResponse represents the response for token operations
type TokenResponse struct {
	Success bool   `json:"success"`
	Token   string `json:"token,omitempty"`
	Port    int    `json:"port,omitempty"`
	Error   string `json:"error,omitempty"`
}

// StatusResponse represents the server status
type StatusResponse struct {
	Version string        `json:"version"`
	Tokens  []interface{} `json:"tokens"`
}

// TokenStatus represents the status of a token
type TokenStatus struct {
	Token        string `json:"token"`
	Type         string `json:"type"` // "forward" or "reverse"
	ClientsCount int    `json:"clients_count"`
}

// ReverseTokenStatus represents the status of a reverse token
type ReverseTokenStatus struct {
	TokenStatus
	Port            int      `json:"port"`
	ConnectorTokens []string `json:"connector_tokens,omitempty"` // List of associated connector tokens
}

// checkAPIKey verifies the API key in the request header
func (h *APIHandler) checkAPIKey(w http.ResponseWriter, r *http.Request) bool {
	providedKey := r.Header.Get("X-API-Key")
	if providedKey != h.apiKey {
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(TokenResponse{
			Success: false,
			Error:   "invalid API key",
		})
		return false
	}
	return true
}

func (h *APIHandler) handleToken(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !h.checkAPIKey(w, r) {
		return
	}

	switch r.Method {
	case http.MethodDelete:
		token := strings.TrimPrefix(r.URL.Path, "/api/token/")
		if token == "" || r.URL.Path == "/api/token" {
			var req TokenRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Token == "" {
				json.NewEncoder(w).Encode(TokenResponse{
					Success: false,
					Error:   "token not specified",
				})
				return
			}
			token = req.Token
		}

		success := h.server.RemoveToken(token)
		json.NewEncoder(w).Encode(TokenResponse{
			Success: success,
			Token:   token,
		})

	case http.MethodPost:
		var req TokenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(TokenResponse{
				Success: false,
				Error:   "invalid request body",
			})
			return
		}

		switch req.Type {
		case "forward":
			token, err := h.server.AddForwardToken(req.Token)
			if err != nil {
				json.NewEncoder(w).Encode(TokenResponse{
					Success: false,
					Error:   err.Error(),
				})
				return
			}
			json.NewEncoder(w).Encode(TokenResponse{
				Success: true,
				Token:   token,
			})

		case "reverse":
			opts := &ReverseTokenOptions{
				Token:                req.Token,
				Port:                 req.Port,
				Username:             req.Username,
				Password:             req.Password,
				AllowManageConnector: req.AllowManageConnector,
			}
			result, err := h.server.AddReverseToken(opts)
			if err != nil {
				json.NewEncoder(w).Encode(TokenResponse{
					Success: false,
					Error:   err.Error(),
				})
				return
			}
			json.NewEncoder(w).Encode(TokenResponse{
				Success: true,
				Token:   result.Token,
				Port:    result.Port,
			})

		case "connector":
			if req.ReverseToken == "" {
				json.NewEncoder(w).Encode(TokenResponse{
					Success: false,
					Error:   "reverse_token is required for connector token",
				})
				return
			}

			token, err := h.server.AddConnectorToken(req.Token, req.ReverseToken)
			if err != nil {
				json.NewEncoder(w).Encode(TokenResponse{
					Success: false,
					Error:   err.Error(),
				})
				return
			}
			json.NewEncoder(w).Encode(TokenResponse{
				Success: true,
				Token:   token,
			})

		default:
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(TokenResponse{
				Success: false,
				Error:   "invalid token type",
			})
		}

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func (h *APIHandler) handleStatus(w http.ResponseWriter, r *http.Request) {
	if !h.checkAPIKey(w, r) {
		return
	}

	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "application/json")

	h.server.mu.RLock()
	tokens := make([]interface{}, 0)

	// Create map of reverse tokens to their connector tokens
	reverseToConnectors := make(map[string][]string)
	for connectorToken, reverseToken := range h.server.connectorTokens {
		reverseToConnectors[reverseToken] = append(reverseToConnectors[reverseToken], connectorToken)
	}

	// Add reverse tokens with their connector tokens
	for token, port := range h.server.tokens {
		tokens = append(tokens, ReverseTokenStatus{
			TokenStatus: TokenStatus{
				Token:        token,
				Type:         "reverse",
				ClientsCount: h.server.GetTokenClientCount(token),
			},
			Port:            port,
			ConnectorTokens: reverseToConnectors[token],
		})
	}

	// Add forward tokens
	for token := range h.server.forwardTokens {
		tokens = append(tokens, TokenStatus{
			Token:        token,
			Type:         "forward",
			ClientsCount: h.server.GetTokenClientCount(token),
		})
	}
	h.server.mu.RUnlock()

	json.NewEncoder(w).Encode(StatusResponse{
		Version: Version,
		Tokens:  tokens,
	})
}
