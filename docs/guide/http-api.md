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
  "token": "my_forward_token"
}
```

**Parameters:**

- `type` (required): Must be "forward"
- `token` (optional): Specific token to use, auto-generated if not provided

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
  "allow_manage_connector": true
}
```

**Parameters:**

- `type` (required): Must be "reverse"
- `token` (optional): Specific token to use, auto-generated if not provided
- `port` (optional): Specific SOCKS5 port, auto-allocated if not provided
- `username` (optional): SOCKS5 authentication username
- `password` (optional): SOCKS5 authentication password  
- `allow_manage_connector` (optional): Allow clients to manage connector tokens (autonomy mode)

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
  "reverse_token": "associated_reverse_token"
}
```

**Parameters:**

- `type` (required): Must be "connector"
- `token` (optional): Specific connector token, auto-generated if not provided
- `reverse_token` (required): Associated reverse proxy token

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
