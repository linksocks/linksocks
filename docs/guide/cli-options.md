# Command-line Options

Read this page from top to bottom:

1. Pick a mode.
2. Fill in the key flags.
3. Add other flags only when needed.

## Modes at a Glance

| Mode | Commands | Purpose |
|------|----------|---------|
| Forward proxy | `linksocks server` + `linksocks client` | SOCKS5 listens on the client side |
| Reverse proxy | `linksocks server -r` + `linksocks client -r` | SOCKS5 listens on the server side |
| Agent mode | `linksocks server -r -c ...` + `linksocks provider` + `linksocks connector` | Separate provider and connector access |
| Autonomy mode | `linksocks server -r -a` + `linksocks provider -c ...` + `linksocks connector` | Provider manages its own connector token |
| Direct transport | Add `--direct-*` flags to compatible clients and `--direct-enable` on server | Prefer peer-to-peer transport when available |

## Server Command

The `server` command runs the WebSocket relay. Add `-r` when the SOCKS5 listener should be exposed on the server side.

### Key Flags

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--token` | `-t` | auto-generated when omitted on server-managed modes | Main authentication token. Also supports `LINKSOCKS_TOKEN`. |
| `--ws-host` | `-H` | `0.0.0.0` | WebSocket listen host |
| `--ws-port` | `-P` | `8765` | WebSocket listen port |
| `--reverse` | `-r` | `false` | Switch from forward relay mode to reverse/agent mode |
| `--socks-host` | `-s` | `127.0.0.1` | SOCKS5 listen host in reverse mode |
| `--socks-port` | `-p` | `9870` | SOCKS5 listen port in reverse mode |

### Other Flags

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--connector-token` | `-c` | auto-generated when omitted | Connector token for agent mode. Also supports `LINKSOCKS_CONNECTOR_TOKEN`. |
| `--connector-autonomy` | `-a` | `false` | Let providers manage connector tokens themselves |
| `--socks-username` | `-n` | | SOCKS5 username for reverse mode |
| `--socks-password` | `-w` | | SOCKS5 password for reverse mode. Also supports `LINKSOCKS_SOCKS_PASSWORD`. |
| `--socks-nowait` | `-i` | `false` | Start SOCKS5 immediately without waiting for a provider |

| `--api-key` | `-k` | | Enable the HTTP API |
| `--buffer-size` | `-b` | `1048576` | Transfer buffer size |
| `--upstream-proxy` | `-x` | | Outbound proxy for server-side connections |
| `--fast-open` | `-f` | `false` | Allow data transfer before the remote side is fully confirmed |
| `--connector-wait-provider` | | `5s` | How long a connector waits for a provider to reconnect |
| `--direct-enable` | | `false` | Enable direct signaling for compatible clients |
| `--direct-rendezvous-udp` | | `false` | Enable server-side UDP rendezvous. Requires a real UDP listener and is not supported on Cloudflare Workers. |
| `--direct-rendezvous-host` | | `ws-host` | Rendezvous UDP host |
| `--direct-rendezvous-port` | | `ws-port` | Rendezvous UDP port |
| `--debug` | `-d` | | Debug logging, use `-dd` for trace |

## Client, Provider, and Connector Commands

Use `client` as the general-purpose command:

- `linksocks client` = forward proxy client
- `linksocks client -r` = reverse provider
- `linksocks provider` = shortcut for `linksocks client -r`
- `linksocks connector` = client alias typically used with connector tokens

### Key Flags

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--token` | `-t` | | Authentication token. Also supports `LINKSOCKS_TOKEN`. |
| `--url` | `-u` | `ws://localhost:8765` | WebSocket server URL |
| `--reverse` | `-r` | `false` | Turn `client` into a reverse provider |
| `--socks-host` | `-s` | `127.0.0.1` | Local SOCKS5 listen host for forward or connector mode |
| `--socks-port` | `-p` | `9870` | Local SOCKS5 listen port for forward or connector mode |

### Other Flags

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--connector-token` | `-c` | | Connector token for agent/autonomy mode. Also supports `LINKSOCKS_CONNECTOR_TOKEN`. |
| `--socks-username` | `-n` | | Local SOCKS5 username |
| `--socks-password` | `-w` | | Local SOCKS5 password. Also supports `LINKSOCKS_SOCKS_PASSWORD`. |
| `--socks-no-wait` | `-i` | `false` | Start SOCKS5 immediately |
| `--no-reconnect` | `-R` | `false` | Exit when the server disconnects |

| `--threads` | `-T` | `1` | Number of transfer threads |
| `--upstream-proxy` | `-x` | | Outbound proxy used to reach the WebSocket server |
| `--no-env-proxy` | `-E` | `false` | Ignore proxy environment variables |
| `--fast-open` | `-f` | `false` | Allow data transfer before the remote side is fully confirmed |
| `--direct-mode` | | `relay-only` | `relay-only`, `auto`, or `direct-only` |
| `--direct-discovery` | | `stun` | Direct candidate discovery method |
| `--direct-host-candidates` | | `auto` | Host candidate advertisement policy |
| `--stun-server` | | built-in pool | Additional STUN server, repeatable |
| `--direct-only-action` | | `exit` | What to do when `direct-only` cannot connect |
| `--direct-upnp` | | `false` | Enable UPnP mapping for direct transport |
| `--direct-upnp-lease` | | `30m` | UPnP lease duration |
| `--direct-upnp-keep` | | `false` | Keep UPnP mapping on exit |
| `--direct-upnp-external-port` | | `0` | Explicit UPnP external port |
| `--debug` | `-d` | | Debug logging, use `-dd` for trace |

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
```

### 3. Agent Mode

```bash
linksocks server -t provider_token -c connector_token -r -p 9870
linksocks provider -t provider_token -u ws://localhost:8765
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

### 4. Autonomy Mode

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

These flags can also be provided through environment variables:

| Environment Variable | Flag |
|----------------------|------|
| `LINKSOCKS_TOKEN` | `--token` |
| `LINKSOCKS_CONNECTOR_TOKEN` | `--connector-token` |
| `LINKSOCKS_SOCKS_PASSWORD` | `--socks-password` |

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