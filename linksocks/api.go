package linksocks

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

// APIHandler handles HTTP API requests for LinkSocksServer
type APIHandler struct {
	server *LinkSocksServer
	apiKey string // Primary key; authentication only
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
	mux.HandleFunc("/api/config/access", h.handleConfigAccess)
}

// TokenRequest represents a request to create a new token
type TokenRequest struct {
	Type                 string       `json:"type"`          // "forward" or "reverse" or "connector"
	Token                string       `json:"token"`         // Optional: specific token to use
	Port                 int          `json:"port"`          // Optional: specific port for reverse proxy
	Username             string       `json:"username"`      // Optional: SOCKS auth username
	Password             string       `json:"password"`      // Optional: SOCKS auth password
	ReverseToken         string       `json:"reverse_token"` // Optional: reverse token for connector token
	AllowManageConnector bool         `json:"allow_manage_connector"`
	Rules                []AccessRule `json:"rules"` // Optional: destination allow entries for reverse token
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
	Direct  *DirectStatus `json:"direct,omitempty"`
}

type DirectPeerStatus struct {
	ClientID        string `json:"client_id"`
	InternalToken   string `json:"internal_token"`
	Role            string `json:"role"`
	ReverseToken    string `json:"reverse_token,omitempty"`
	SupportsDirect  bool   `json:"supports_direct"`
	UpdatedAt       string `json:"updated_at,omitempty"`
	LastSessionID   string `json:"last_session_id,omitempty"`
	LastDirectState string `json:"last_direct_state,omitempty"`
}

type DirectStatus struct {
	Enabled bool               `json:"enabled"`
	Peers   []DirectPeerStatus `json:"peers,omitempty"`
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

// authenticatedKey reports whether the request carries a valid API key. The
// primary key and additional keys authenticate alike; API keys are credentials
// only and never carry access rules.
func (h *APIHandler) authenticatedKey(r *http.Request) bool {
	providedKey := r.Header.Get("X-API-Key")

	// Primary key: constant-time compare.
	if subtle.ConstantTimeCompare([]byte(providedKey), []byte(h.apiKey)) == 1 {
		return true
	}

	// Additional keys (ServerOption.APIKeys): authentication only.
	for key := range h.server.apiKeys {
		if subtle.ConstantTimeCompare([]byte(providedKey), []byte(key)) == 1 {
			return true
		}
	}
	return false
}

// checkAPIKey verifies the API key in the request header, writing a 401
// response when the key is missing or unknown.
func (h *APIHandler) checkAPIKey(w http.ResponseWriter, r *http.Request) bool {
	if !h.authenticatedKey(r) {
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

	// Rules are bound to tokens only: the request must supply them explicitly
	// via TokenRequest.Rules. API keys never contribute rules.

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
			token, err := h.server.AddForwardTokenWithRules(req.Token, req.Rules)
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
			if rules := req.Rules; len(rules) > 0 {
				ac, err := NewAccessControl(rules)
				if err != nil {
					json.NewEncoder(w).Encode(TokenResponse{
						Success: false,
						Error:   err.Error(),
					})
					return
				}
				opts.AccessControl = ac
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

			token, err := h.server.AddConnectorTokenWithRules(req.Token, req.ReverseToken, req.Rules)
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
				ClientsCount: h.server.getTokenClientCountLocked(token),
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
			ClientsCount: h.server.getTokenClientCountLocked(token),
		})
	}

	var directStatus *DirectStatus
	if h.server.directEnable {
		peers := make([]DirectPeerStatus, 0, len(h.server.clientMeta))
		for id, meta := range h.server.clientMeta {
			if meta == nil {
				continue
			}
			lastSessionID := ""
			lastDirectState := ""
			if meta.LastStatus != nil {
				lastSessionID = meta.LastStatus.SessionID.String()
				lastDirectState = meta.LastStatus.Status
			} else if meta.LastCapabilities != nil {
				lastSessionID = meta.LastCapabilities.SessionID.String()
			} else if meta.LastRendezvous != nil {
				lastSessionID = meta.LastRendezvous.SessionID.String()
			}
			updatedAt := ""
			if !meta.UpdatedAt.IsZero() {
				updatedAt = meta.UpdatedAt.UTC().Format(time.RFC3339)
			}
			peers = append(peers, DirectPeerStatus{
				ClientID:        id.String(),
				InternalToken:   meta.InternalToken,
				Role:            string(meta.Role),
				ReverseToken:    meta.ReverseToken,
				SupportsDirect:  meta.SupportsDirect,
				UpdatedAt:       updatedAt,
				LastSessionID:   lastSessionID,
				LastDirectState: lastDirectState,
			})
		}
		directStatus = &DirectStatus{Enabled: true, Peers: peers}
	} else {
		directStatus = &DirectStatus{Enabled: false}
	}
	h.server.mu.RUnlock()

	json.NewEncoder(w).Encode(StatusResponse{
		Version: Version,
		Tokens:  tokens,
		Direct:  directStatus,
	})
}

// AccessConfigRequest is the PUT body for /api/config/access. Fields may be
// provided independently; an absent side keeps its current value.
type AccessConfigRequest struct {
	Entry *[]AccessRule `json:"entry"`
	Dial  *[]AccessRule `json:"dial"`
}

// AccessConfigResponse is the GET response for /api/config/access.
type AccessConfigResponse struct {
	Entry []AccessRule `json:"entry"`
	Dial  []AccessRule `json:"dial"`
}

// handleConfigAccess reads or updates the server-wide entry and dial access
// control rules. These rules are enforced on the server for every request that
// has no token-level override, matching the WithEntryAccessControl /
// WithDialAccessControl server options.
func (h *APIHandler) handleConfigAccess(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if !h.checkAPIKey(w, r) {
		return
	}

	switch r.Method {
	case http.MethodGet:
		json.NewEncoder(w).Encode(AccessConfigResponse{
			Entry: h.server.relay.EntryAccessControl().RawRules(),
			Dial:  h.server.relay.DialAccessControl().RawRules(),
		})

	case http.MethodPut:
		var req AccessConfigRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(TokenResponse{
				Success: false,
				Error:   "invalid request body",
			})
			return
		}
		if req.Entry != nil {
			ac, err := NewAccessControl(*req.Entry)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(TokenResponse{
					Success: false,
					Error:   err.Error(),
				})
				return
			}
			h.server.relay.SetEntryAccessControl(ac)
		}
		if req.Dial != nil {
			ac, err := NewAccessControl(*req.Dial)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(TokenResponse{
					Success: false,
					Error:   err.Error(),
				})
				return
			}
			h.server.relay.SetDialAccessControl(ac)
		}
		json.NewEncoder(w).Encode(TokenResponse{Success: true})

	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}
