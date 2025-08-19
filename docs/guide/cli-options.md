# Command-line Options

LinkSocks is a versatile SOCKS proxy implementation over the WebSocket protocol, supporting both forward and reverse proxy configurations. This guide provides detailed instructions on how to use LinkSocks via the command line.

## Basic Commands

LinkSocks provides two primary commands: `server` and `client`.

### Server

Starts the LinkSocks server which listens for incoming WebSocket connections and manages SOCKS proxy services.

### Client

Starts the LinkSocks client which connects to the server and provides SOCKS proxy functionality to the local machine.

## Server Options

### Basic Options

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--ws-host` | `-H` | `0.0.0.0` | WebSocket server listen address |
| `--ws-port` | `-P` | `8765` | WebSocket server listen port |
| `--token` | `-t` | | Authentication token (auto-generate if empty) |
| `--reverse` | `-r` | `false` | Enable reverse proxy mode |

### SOCKS5 Options (Reverse Mode)

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--socks-host` | `-s` | `127.0.0.1` | SOCKS5 server listen address |
| `--socks-port` | `-p` | `9870` | SOCKS5 server listen port |
| `--socks-username` | `-n` | | SOCKS5 authentication username |
| `--socks-password` | `-w` | | SOCKS5 authentication password |
| `--socks-nowait` | `-i` | `false` | Start SOCKS server immediately |

### Agent Proxy Options

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--connector-token` | `-c` | | Connector token for agent proxy |
| `--connector-autonomy` | `-a` | `false` | Allow clients to manage connector tokens |

### Performance Options

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--buffer-size` | `-b` | `32768` | Buffer size for data transfer |
| `--fast-open` | `-f` | `false` | Enable fast open optimization (see: [Fast Open Mode](/guide/fast-open)) |
| `--upstream-proxy` | `-x` | | Upstream SOCKS5 proxy URL |

### Management Options

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--api-key` | `-k` | | Enable HTTP API with specified key |
| `--debug` | `-d` | | Debug logging (use -dd for trace) |

## Client Parameters

### Basic Options

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--token` | `-t` | | Authentication token (required) |
| `--url` | `-u` | `ws://localhost:8765` | WebSocket server URL |
| `--reverse` | `-r` | `false` | Enable reverse proxy mode |
| `--connector-token` | `-c` | | Connector token for agent proxy |

### SOCKS5 Options (Forward Mode)

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--socks-host` | `-s` | `127.0.0.1` | SOCKS5 server listen address |
| `--socks-port` | `-p` | `9870` | SOCKS5 server listen port |
| `--socks-username` | `-n` | | SOCKS5 authentication username |
| `--socks-password` | `-w` | | SOCKS5 authentication password |
| `--socks-no-wait` | `-i` | `false` | Start SOCKS server immediately |

### Connection Options

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--no-reconnect` | `-R` | `false` | Stop when server disconnects |
| `--threads` | `-T` | `1` | Number of WebSocket connections |
| `--fast-open` | `-f` | `false` | Enable fast open optimization (see: [Fast Open](/guide/fast-open)) |
| `--upstream-proxy` | `-x` | | Upstream SOCKS5 proxy URL |
| `--no-env-proxy` | `-E` | `false` | Ignore proxy environment variables |

### Debug Options

| Parameter | Short | Default | Description |
|-----------|-------|---------|-------------|
| `--debug` | `-d` | | Debug logging (use -dd for trace) |

<!-- Environment Variables section removed -->

## Upstream Proxy Format

The `--upstream-proxy` parameter accepts SOCKS5 URLs:

```
socks5://[username[:password]@]host[:port]
```

**Examples:**
- `socks5://proxy.example.com:1080`
- `socks5://user:pass@proxy.example.com:1080`
- `socks5://proxy.example.com` (defaults to port 1080)

<!-- Trim overly long examples; keep minimal combos only -->
## Common Parameter Combinations

### Forward Proxy

```bash
# Basic forward proxy
linksocks server -t my_token
linksocks client -t my_token -u ws://localhost:8765 -p 9870

# With authentication
linksocks client -t my_token -u ws://localhost:8765 -n user -w pass

# With upstream proxy
linksocks client -t my_token -u ws://localhost:8765 -x socks5://upstream:1080
```

### Reverse Proxy

```bash
# Basic reverse proxy
linksocks server -t my_token -r -p 9870
linksocks client -t my_token -u ws://localhost:8765 -r

# With SOCKS authentication
linksocks server -t my_token -r -p 9870 -n user -w pass
linksocks client -t my_token -u ws://localhost:8765 -r
```

### Agent Proxy

```bash
# Server with connector token
linksocks server -t server_token -c connector_token -r -p 9870

# Provider client
linksocks provider -t server_token -u ws://localhost:8765

# Connector client
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

### Autonomy Mode

```bash
# Server with autonomy enabled
linksocks server -t server_token -r -a

# Provider with custom connector
linksocks provider -t server_token -c my_connector -u ws://localhost:8765
```

## Performance Tuning

### Buffer Size

Increase buffer size for high-throughput scenarios:
```bash
linksocks server -b 65536
linksocks client -t token -b 65536
```

### Threading

Use multiple threads for concurrent processing:
```bash
linksocks client -t token -T 8
```

### Fast Open

Enable fast open for lower latency (saves one RTT):
```bash
linksocks server -f
linksocks client -t token -f
```