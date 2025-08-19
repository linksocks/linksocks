# Authentication

LinkSocks supports multiple authentication methods:

1. **WebSocket Token** - Used in forward/reverse proxy modes
2. **Connector Token** - Used by proxy consumers in agent proxy mode
3. **SOCKS Credentials** - Used by programs connecting to the proxy

## WebSocket Token

Used in forward and reverse proxy modes for client-server authentication.

```bash
# Server (auto-generate if omitted)
linksocks server -t server_token

# Client (required)
linksocks client -t server_token -u ws://localhost:8765
```

## Agent Proxy Mode Tokens

In agent proxy mode, the server acts as a relay between providers and connectors, each using different tokens.

### Regular Agent Mode

In regular agent mode, the server manages all tokens centrally:

**Server Side - Define Both Tokens:**
```bash
# Server sets both Provider Token and Connector Token
linksocks server -t provider_token -c connector_token -p 9870 -r
```

**Provider Side - Use Provider Token:**
```bash
# Provider uses the Provider Token set by server
linksocks provider -t provider_token -u ws://localhost:8765
```

**Connector Side - Use Connector Token:**
```bash
# Connector uses the Connector Token set by server
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

### Autonomy Mode Tokens

Autonomy mode allows providers to set their own connector tokens, creating isolated provider-connector pairs.

**Server Side - Only Provider Token:**
```bash
# Server only sets Provider Token, no Connector Token needed
linksocks server -t provider_token -r -a
```

**Provider Side - Sets Own Connector Token:**
```bash
# Provider connects with Provider Token and defines custom Connector Token
linksocks provider -t provider_token -c my_custom_connector_token -u ws://localhost:8765
```

**Connector Side - Uses Provider's Token:**
```bash
# Connector uses the provider's custom token
linksocks connector -t my_custom_connector_token -u ws://localhost:8765 -p 1180
```

### Autonomy Mode Token Flow

1. **Server**: Only validates Provider Tokens (`-t provider_token`), doesn't manage Connector Tokens
2. **Provider**: Authenticates with Provider Token (`-t`) and defines Connector Token (`-c`)
3. **Connector**: Uses the provider's custom token (`-t custom_token`)
4. **Isolation**: Each connector can only access its corresponding provider

## SOCKS5 Credentials

Optional username/password authentication on the SOCKS5 interface itself.

### Where to Set SOCKS5 Credentials

- **Forward mode**: Set on `client` (who runs the SOCKS5 server)
- **Reverse mode**: Set on `server` (who runs the SOCKS5 server)
- **Agent mode**: Set on `connector` (who runs the SOCKS5 server)

```bash
# Forward mode - client provides SOCKS5 server
linksocks client -t token -u ws://localhost:8765 -p 9870 -n user -w pass

# Reverse mode - server provides SOCKS5 server
linksocks server -t token -r -p 9870 -n user -w pass

# Agent mode - connector provides SOCKS5 server
linksocks connector -t connector_token -p 9870 -r -n user -w pass
```

## Token Generation

For production use, generate strong random tokens using these commands:

**Linux/macOS:**
```bash
openssl rand -hex 16
```

**Windows PowerShell:**
```powershell
[System.Web.Security.Membership]::GeneratePassword(32, 8)
```