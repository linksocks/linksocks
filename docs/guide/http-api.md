# HTTP API

LinkSocks server provides an HTTP API for dynamic token management and server monitoring when enabled with the `--api-key` flag.

## Quick Start

### Enable API

```bash
# Start server with API enabled
linksocks server --api-key your_secret_api_key
```

The API will be available at the same host and port as the WebSocket server (default: `http://localhost:8765`).

### Basic Usage

```bash
# Get server status
curl -H "X-API-Key: your_secret_api_key" \
     http://localhost:8765/api/status

# Add a forward token
curl -X POST \
     -H "X-API-Key: your_secret_api_key" \
     -H "Content-Type: application/json" \
     -d '{"type":"forward","token":"my_token"}' \
     http://localhost:8765/api/token

# Remove a token
curl -X DELETE \
     -H "X-API-Key: your_secret_api_key" \
     http://localhost:8765/api/token/my_token
```

## Authentication

All API requests require the `X-API-Key` header with your configured API key:

```http
X-API-Key: your_secret_api_key
```

Additional API keys can be registered at server startup via
`ServerOption.WithAPIKeys(map[string][]AccessRule)`. Every additional key may
carry its own default destination rules: tokens created with that key without
explicit `rules` inherit the key's rules. The primary `--api-key` key never
carries rules.

### Error Response

If authentication fails, the API returns:

```json
{
  "success": false,
  "error": "invalid API key"
}
```

## Endpoints Overview

| Method | Endpoint | Description |
|--------|----------|-------------|
| GET    | `/api/status` | Get server status and token list |
| POST   | `/api/token` | Add a new token |
| DELETE | `/api/token/{token}` | Remove a token by URL path |
| DELETE | `/api/token` | Remove a token by request body |
| GET    | `/api/config/access` | Get server-wide entry/dial access rules |
| PUT    | `/api/config/access` | Update server-wide entry/dial access rules |

## Server Status

### GET /api/status

Returns server version and a list of all tokens with their types and active client counts.

**Response:**

```json
{
  "version": "3.0.12",
  "tokens": [
    {
      "token": "forward_token_123",
      "type": "forward", 
      "clients_count": 2
    },
    {
      "token": "reverse_token_456",
      "type": "reverse",
      "clients_count": 1,
      "port": 9870,
      "connector_tokens": ["connector_abc", "connector_def"]
    }
  ]
}
```

**Token Object Fields:**

- `token` (string): The authentication token
- `type` (string): Token type - "forward" or "reverse"  
- `clients_count` (number): Number of active client connections
- `port` (number): SOCKS5 port (reverse tokens only)
- `connector_tokens` (array): Associated connector tokens (reverse tokens only)

## Token Management

### Add Forward Token

**POST /api/token**

```json
{
  "type": "forward",
  "token": "my_forward_token",
  "rules": [
    {
      "addrs": ["192.168.1.0/24"],
      "ports": [22]
    }
  ]
}
```

**Parameters:**

- `type` (required): Must be "forward"
- `token` (optional): Specific token to use, auto-generated if not provided
- `rules` (optional): Destination allow entries enforced by the server before dialing on behalf of this token. See [Access Control](/guide/access-control) for details.

**Response:**

```json
{
  "success": true,
  "token": "my_forward_token"
}
```

### Add Reverse Token

**POST /api/token**

```json
{
  "type": "reverse",
  "token": "my_reverse_token",
  "port": 9870,
  "username": "socks_user",
  "password": "socks_pass",
  "allow_manage_connector": true,
  "rules": [
    {
      "addrs": ["192.168.1.0/24", "192.168.1.1-255"],
      "ports": [22, [90, 150]]
    },
    {
      "addrs": ["10.0.0.5"],
      "ports": [443]
    }
  ]
}
```

**Parameters:**

- `type` (required): Must be "reverse"
- `token` (optional): Specific token to use, auto-generated if not provided
- `port` (optional): Specific SOCKS5 port, auto-allocated if not provided
- `username` (optional): SOCKS5 authentication username
- `password` (optional): SOCKS5 authentication password  
- `allow_manage_connector` (optional): Allow clients to manage connector tokens (self-managed connectors)
- `rules` (optional): Destination allow entries for the token's local proxy entry. Each entry pairs `addrs` (CIDRs, bare IPs, or IP ranges) with `ports` (single ports or `[start, end]` ranges); a destination is allowed when it matches at least one entry. See [Access Control](/guide/access-control) for details.

**Response:**

```json
{
  "success": true,
  "token": "my_reverse_token",
  "port": 9871
}
```

### Add Connector Token

**POST /api/token**

```json
{
  "type": "connector",
  "token": "my_connector_token",
  "reverse_token": "associated_reverse_token",
  "rules": [
    {
      "addrs": ["10.0.0.0/8"],
      "ports": [[8000, 9000]]
    }
  ]
}
```

**Parameters:**

- `type` (required): Must be "connector"
- `token` (optional): Specific connector token, auto-generated if not provided
- `reverse_token` (required): Associated reverse proxy token
- `rules` (optional): Destination allow entries enforced by the server before forwarding a request from this connector to a reverse provider. See [Access Control](/guide/access-control) for details.

**Response:**

```json
{
  "success": true,
  "token": "my_connector_token"
}
```

### Remove Token (URL Path)

**DELETE /api/token/{token}**

Remove a token by specifying it in the URL path.

**Example:**

```bash
curl -X DELETE \
     -H "X-API-Key: your_api_key" \
     http://localhost:8765/api/token/token_to_remove
```

**Response:**

```json
{
  "success": true,
  "token": "token_to_remove"
}
```

### Remove Token (Request Body)

**DELETE /api/token**

```json
{
  "token": "token_to_remove"
}
```

**Response:**

```json
{
  "success": true,
  "token": "token_to_remove"
}
```

## Server-Wide Access Control

`/api/config/access` reads and updates the server-wide **entry** and **dial**
destination rules. These are the server-level defaults enforced on the server
for any request that has no per-token override — the same rules you would set
with the `WithEntryAccessControl` / `WithDialAccessControl` server options. In
API server mode this is how you configure global restrictions, since the CLI's
`--access-rule` is rejected there.

### GET /api/config/access

Returns the current server-wide rules.

**Response:**

```json
{
  "entry": [
    {
      "addrs": ["192.168.0.0/16"],
      "ports": [22]
    }
  ],
  "dial": [
    {
      "addrs": ["10.0.0.0/8"],
      "ports": [[8000, 9000]]
    }
  ]
}
```

### PUT /api/config/access

Set the server-wide rules. Either field may be provided independently; an
absent field keeps its current value. Use an empty array `[]` to clear a side
(allow everything there).

```bash
curl -X PUT \
     -H "X-API-Key: your_api_key" \
     -H "Content-Type: application/json" \
     -d '{
       "entry": [
         {"addrs": ["192.168.1.0/24"], "ports": [22]}
       ],
       "dial": []
     }' \
     http://localhost:8765/api/config/access
```

Illegal address or port ranges are rejected with `400 Bad Request`.

## Error Responses

All endpoints return error responses in this format:

```json
{
  "success": false,
  "error": "error description"
}
```

**Common Errors:**

- `"invalid API key"` - Authentication failed
- `"invalid request body"` - Malformed JSON request
- `"invalid token type"` - Unsupported token type
- `"token not specified"` - Missing required token parameter
- `"reverse_token is required for connector token"` - Missing reverse_token for connector
