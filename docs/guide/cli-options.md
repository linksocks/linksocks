# Command-line Options

This page is ordered so you can:

1. Pick a mode
2. Fill in the key flags
3. Add other flags only when you need them

Most setups only need the key flags. Other flags cover authentication, performance, and direct transport.

## Modes at a Glance

| Mode | Commands | Purpose |
|------|----------|---------|
| Forward proxy | `linksocks server` + `linksocks client` | Server exits to the internet; hybrid local proxy (SOCKS5 + HTTP) listens on the client |
| Reverse proxy | `linksocks server -r` + `linksocks client -r` | Client exits to the internet; hybrid local proxy (SOCKS5 + HTTP) listens on the server |
| Relay proxy | `linksocks server -r -c ...` + `linksocks provider` + `linksocks connector` | Server is only a relay; provider exits, connector exposes the hybrid local proxy |
| Relay proxy (self-managed connectors) | `linksocks server -r -a` + `linksocks provider -c ...` + `linksocks connector` | Relay proxy variant where each provider registers its own connector token |
| Direct transport | Client `--direct-*`, server `--direct-enable` | Prefer peer-to-peer after relay handshake succeeds |

### Roles

| Role | Responsibility |
|------|----------------|
| **server** | WebSocket relay. May also listen for the hybrid local proxy in reverse proxy mode |
| **client** | General client. Default: forward hybrid local proxy endpoint. With `-r`: acts as provider |
| **provider** | Shortcut for `client -r`. Shares this machine's outbound network |
| **connector** | Uses a connector token, opens the hybrid local proxy, traffic exits via the matching provider |

## Server Command

`server` runs the WebSocket relay. Add `-r` for reverse / relay-style modes (local proxy on the server, or left to connectors).

The local listen port is hybrid: it accepts SOCKS5 and HTTP proxy clients on the same port. HTTP supports `CONNECT` (recommended for HTTPS) and absolute-form HTTP requests. UDP remains SOCKS5-only. When `--socks-username` / `--socks-password` are set, both SOCKS5 username/password and HTTP `Proxy-Authorization: Basic` use the same credentials.

### Key Flags

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--token` | `-t` | auto-generated when omitted on server-managed modes | Main authentication token. Also `LINKSOCKS_TOKEN`. |
| `--ws-host` | `-H` | `0.0.0.0` | WebSocket listen host. Also `LINKSOCKS_WEBSOCKET_HOST`. |
| `--ws-port` | `-P` | `8765` | WebSocket listen port. Also `LINKSOCKS_WEBSOCKET_PORT`. |
| `--reverse` | `-r` | `false` | Switch from forward relay to reverse / relay-style modes |
| `--socks-host` | `-s` | `127.0.0.1` | Hybrid local proxy listen host in reverse mode |
| `--socks-port` | `-p` | `9870` | Hybrid local proxy listen port in reverse mode |

### Other Flags

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--connector-token` | `-c` | auto-generated when omitted | Connector token for relay proxy. Also `LINKSOCKS_CONNECTOR_TOKEN`. |
| `--connector-autonomy` | `-a` | `false` | Let providers register their own connector tokens. Also `LINKSOCKS_CONNECTOR_AUTONOMY`. |
| `--socks-username` | `-n` | | Local proxy username (SOCKS5 and HTTP Basic) in reverse mode. Also `LINKSOCKS_SOCKS_USERNAME`. |
| `--socks-password` | `-w` | | Local proxy password (SOCKS5 and HTTP Basic) in reverse mode. Also `LINKSOCKS_SOCKS_PASSWORD`. |
| `--socks-nowait` | `-i` | `false` | Start the hybrid local proxy immediately without waiting for a provider |
| `--api-key` | `-k` | | Enable the HTTP API. Also `LINKSOCKS_API_KEY`. |
| `--buffer-size` | `-b` | `1048576` | Transfer buffer size in bytes |
| `--upstream-proxy` | `-x` | | Outbound proxy for server-side connections. Also `LINKSOCKS_UPSTREAM_PROXY`. |
| `--fast-open` | `-f` | `false` | Allow data transfer before the remote side is fully confirmed. Also `LINKSOCKS_FASTOPEN`. |
| `--connector-wait-provider` | | `5s` | How long a connector waits for a provider to reconnect |
| `--direct-enable` | | `false` | Enable direct signaling for compatible clients |
| `--direct-rendezvous-udp` | | `false` | Enable server-side UDP rendezvous. Needs a real UDP listener; not supported on Cloudflare Workers. |
| `--direct-rendezvous-host` | | same as `ws-host` | Rendezvous UDP host |
| `--direct-rendezvous-port` | | same as `ws-port` | Rendezvous UDP port |
| `--debug` | `-d` | | Debug logging; use `-dd` for trace |

## Client, Provider, and Connector

These three share the same client implementation. The difference is the default role and the flags you usually set:

| Command | Equivalent to | Typical use |
|---------|---------------|-------------|
| `linksocks client` | Forward proxy client | Local hybrid proxy, exit via server |
| `linksocks client -r` | Provider in reverse / relay | Share this machine's outbound network |
| `linksocks provider` | Shortcut for `client -r` | Same as above |
| `linksocks connector` | Client alias for connector tokens | Local hybrid proxy, exit via a matched provider |

### Key Flags

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--token` | `-t` | | Authentication token. Also `LINKSOCKS_TOKEN`. |
| `--url` | `-u` | `ws://localhost:8765` | WebSocket server URL. Also `LINKSOCKS_URL`. |
| `--reverse` | `-r` | `false` | Turn `client` into a provider (reverse / relay exit side) |
| `--socks-host` | `-s` | `127.0.0.1` | Local hybrid proxy host for forward or connector mode. Also `LINKSOCKS_SOCKS_HOST`. |
| `--socks-port` | `-p` | `9870` | Local hybrid proxy port for forward or connector mode. Also `LINKSOCKS_SOCKS_PORT`. |

### Other Flags

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--connector-token` | `-c` | | Connector token registered by a provider in relay / self-managed mode. Also `LINKSOCKS_CONNECTOR_TOKEN`. |
| `--socks-username` | `-n` | | Local proxy username (SOCKS5 and HTTP Basic). Also `LINKSOCKS_SOCKS_USERNAME`. |
| `--socks-password` | `-w` | | Local proxy password (SOCKS5 and HTTP Basic). Also `LINKSOCKS_SOCKS_PASSWORD`. |
| `--socks-no-wait` | `-i` | `false` | Start the hybrid local proxy immediately |
| `--no-reconnect` | `-R` | `false` | Exit when the server disconnects (default: reconnect) |
| `--threads` | `-T` | `1` | Number of transfer threads |
| `--upstream-proxy` | `-x` | | Outbound proxy used to reach the WebSocket server. Also `LINKSOCKS_UPSTREAM_PROXY`. |
| `--no-env-proxy` | `-E` | `false` | Ignore proxy environment variables |
| `--fast-open` | `-f` | `false` | Allow data transfer before the remote side is fully confirmed. Also `LINKSOCKS_FASTOPEN`. |
| `--direct-mode` | | `auto` | `relay-only`, `auto`, or `direct-only` |
| `--direct-discovery` | | `stun` | Direct candidate discovery method |
| `--direct-host-candidates` | | `auto` | Host candidate advertisement policy |
| `--stun-server` | | built-in pool | Additional STUN server, repeatable |
| `--direct-only-action` | | `exit` | What to do when `direct-only` cannot connect |
| `--direct-upnp` | | `false` | Enable UPnP mapping for direct transport |
| `--direct-upnp-lease` | | `30m` | UPnP lease duration |
| `--direct-upnp-keep` | | `false` | Keep UPnP mapping on exit |
| `--direct-upnp-external-port` | | `0` | Explicit UPnP external port (`0` = auto) |
| `--debug` | `-d` | | Debug logging; use `-dd` for trace |

## Mode Recipes

### 1. Forward Proxy

```bash
linksocks server -t my_token
linksocks client -t my_token -u ws://localhost:8765 -p 9870
```

### 2. Reverse Proxy

```bash
linksocks server -t my_token -r -p 9870
linksocks client -t my_token -u ws://localhost:8765 -r
# or: linksocks provider -t my_token -u ws://localhost:8765
```

### 3. Relay Proxy

The server manages both provider and connector tokens. The hybrid local proxy listens on the connector.

```bash
linksocks server -t provider_token -c connector_token -r -p 9870
linksocks provider -t provider_token -u ws://localhost:8765
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

### 4. Relay Proxy (Self-Managed Connectors)

The server only authenticates providers. Each provider registers its own connector token with `-c`. A connector reaches only its matching provider; there is no cross-provider load balancing.

```bash
linksocks server -t provider_token -r -a
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

### 5. Direct Transport

```bash
linksocks server -t my_token --direct-enable
linksocks client -t my_token -u ws://localhost:8765 --direct-mode auto
```

## Environment Variables

These flags can also be provided as environment variables (handy for Docker / Compose):

| Environment Variable | Flag |
|----------------------|------|
| `LINKSOCKS_MODE` | root command mode alias (`server`, `client`, `provider`, `connector`) |
| `LINKSOCKS_URL` | `--url` |
| `LINKSOCKS_WEBSOCKET_HOST` | `--ws-host` |
| `LINKSOCKS_WEBSOCKET_PORT` | `--ws-port` |
| `LINKSOCKS_SOCKS_HOST` | `--socks-host` |
| `LINKSOCKS_SOCKS_PORT` | `--socks-port` |
| `LINKSOCKS_TOKEN` | `--token` |
| `LINKSOCKS_CONNECTOR_TOKEN` | `--connector-token` |
| `LINKSOCKS_SOCKS_USERNAME` | `--socks-username` |
| `LINKSOCKS_SOCKS_PASSWORD` | `--socks-password` |
| `LINKSOCKS_API_KEY` | `--api-key` |
| `LINKSOCKS_CONNECTOR_AUTONOMY` | `--connector-autonomy` |
| `LINKSOCKS_UPSTREAM_PROXY` | `--upstream-proxy` |
| `LINKSOCKS_FASTOPEN` | `--fast-open` |

## Upstream Proxy Format

`--upstream-proxy` accepts both SOCKS5 and HTTP proxy URLs:

```text
socks5://[username[:password]@]host[:port]
http://[username[:password]@]host[:port]
```

Examples:

- `socks5://proxy.example.com:1080`
- `socks5://user:pass@proxy.example.com:1080`
- `http://user:pass@proxy.example.com:8080`
