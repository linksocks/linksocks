# Quick Start

## Installation

### Golang Version
```bash
go install github.com/linksocks/linksocks/cmd/linksocks@latest
```

Or download pre-built binaries from the [releases page](https://github.com/linksocks/linksocks/releases).

### Docker
```bash
docker run --rm -it jackzzs/linksocks --help
```

### Python Version
```bash
pip install linksocks
```

::: info
The Python package wraps the Golang implementation. See: [Python Bindings](/python/)
:::

## Which Mode Should I Use?

| Mode | Best for | Where SOCKS5 listens | Who exits to the network |
|------|----------|----------------------|--------------------------|
| **Forward proxy** | Share the server's network with clients | Client machine | Server |
| **Reverse proxy** | Share a client's (intranet) network on the server | Server | Client (provider) |
| **Relay proxy** | Server only relays; exit and SOCKS5 live on different machines | Connector | Provider |
| **Relay proxy (self-managed connectors)** | Public / serverless relays; each provider issues its own connector token | Connector | Matching provider |

For typical intranet penetration where both sides dial out to a relay, prefer **relay proxy (self-managed connectors)**. You can also use the public relay at `l.zetx.tech`.

## Forward Proxy

The server provides network access. The client exposes SOCKS5 locally.

**Server:**
```bash
# Start WebSocket server on port 8765
linksocks server -t example_token
```

**Client:**
```bash
# Connect and provide SOCKS5 on port 9870
linksocks client -t example_token -u ws://localhost:8765 -p 9870
```

**Test:**
```bash
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

## Reverse Proxy

The server exposes SOCKS5. Clients join as network providers.

**Server:**
```bash
# Start SOCKS5 proxy on port 9870
linksocks server -t example_token -r -p 9870
```

**Client (provider):**
```bash
# Connect as network provider
linksocks client -t example_token -u ws://localhost:8765 -r
# or: linksocks provider -t example_token -u ws://localhost:8765
```

**Test:**
```bash
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

## Relay Proxy

The server only relays traffic. Two token types separate roles:

- **Provider token** (`-t`): who may share network access
- **Connector token** (`-c`): who may use the proxy

**Server:**
```bash
linksocks server -t provider_token -c connector_token -p 9870 -r
```

**Provider (inside the network you want to share):**
```bash
linksocks provider -t provider_token -u ws://localhost:8765
```

**Connector (where you need SOCKS5):**
```bash
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

**Test:**
```bash
curl --socks5 127.0.0.1:1180 http://httpbin.org/ip
```
## Relay Proxy (Self-Managed Connectors)

A relay-proxy variant suited to public relays and Cloudflare Workers:

1. The server usually does not listen for SOCKS5
2. Each provider sets its own connector token (`-c`)
3. No cross-provider load balancing: a connector only reaches the provider that registered that token

**Server:**
```bash
linksocks server -t provider_token -r -a
```

**Provider:**
```bash
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765
```

**Connector:**
```bash
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

### Use the Public Server

The public relay `l.zetx.tech` runs in this mode. No self-hosted server required:

**Step 1: Machine A (inside the network you want to access)**
```bash
linksocks provider -t any_token -u wss://l.zetx.tech -c your_token
```

**Step 2: Machine B (where you need the proxy)**
```bash
linksocks connector -t your_token -u wss://l.zetx.tech -p 1080
```

**Test:**
```bash
curl --socks5 127.0.0.1:1080 http://httpbin.org/ip
```

::: warning
Use a strong connector token. Anyone who has it can use your provider network.
:::

Generate a strong token:

```bash
openssl rand -hex 16
```

## Server on Cloudflare Workers

Deploy a serverless relay on Cloudflare Workers:

[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/linksocks/linksocks.js)

The Worker runs as **relay proxy (self-managed connectors)**. Example:

```bash
# Provider
linksocks provider -t any_token -c your_token -u wss://your-worker.your-subdomain.workers.dev

# Connector
linksocks connector -t your_token -u wss://your-worker.your-subdomain.workers.dev -p 9870
```

## P2P Direct Mode (QUIC)

In reverse proxy, relay proxy, and its variants, P2P direct mode is enabled by default (`--direct-mode auto`, discovery `--direct-discovery stun`). When the provider and connector can establish direct UDP connectivity, data skips the server and travels over encrypted QUIC, reducing latency and increasing throughput.

With no STUN server specified, the program probes a built-in public STUN pool and picks the fastest node. Override with `--stun-server`, or change behavior via `--direct-mode` / `--direct-discovery`.

**Linux performance tip:** High-throughput QUIC may log `failed to sufficiently increase receive buffer size` when default UDP buffers are small. The program still works; for best performance (~7MB ideal buffer), raise:

```bash
sudo sysctl -w net.core.rmem_max=2500000
sudo sysctl -w net.core.wmem_max=2500000
```

## HTTP API

Enable the HTTP API to add/remove tokens and inspect connections without restarting:

```bash
linksocks server --api-key your_api_key
```

Details: [HTTP API](/guide/http-api)
## Common Options

### SOCKS Authentication
```bash
linksocks server -t token -r -p 9870 -n username -w password
linksocks client -t token -u ws://localhost:8765 -n username -w password
```

### Debug Logging
```bash
linksocks server -t token -d
linksocks client -t token -u ws://localhost:8765 -d
```

### Custom Listen Addresses
```bash
# Server WebSocket on all interfaces
linksocks server -t token -H 0.0.0.0 -P 8765

# Client custom SOCKS address
linksocks client -t token -u ws://localhost:8765 -h 0.0.0.0 -p 1080
```

## Next Steps

- [Command-line Options](/guide/cli-options): full flag reference and mode recipes
- [Authentication](/guide/authentication): tokens and SOCKS credentials
- [Python Library](/python/): in-process integration
- [HTTP API](/guide/http-api): dynamic management

## Docker Compose

Run provider and connector on two machines. Both connect to the public relay `l.zetx.tech` by default.

::: warning
Use a strong connector token. Anyone who has it can use your provider.
:::

### Provider Side

Create `compose.yaml` on the provider machine:

```yaml
services:
  linksocks-provider:
    image: jackzzs/linksocks:latest
    environment:
      LINKSOCKS_MODE: provider
      LINKSOCKS_URL: l.zetx.tech
      LINKSOCKS_CONNECTOR_TOKEN: your_connector_token
    restart: unless-stopped
```

Start:

```bash
docker compose up -d
docker compose logs -f linksocks-provider
```

### Connector Side

Create `compose.yaml` on the connector machine:

```yaml
services:
  linksocks-connector:
    image: jackzzs/linksocks:latest
    environment:
      LINKSOCKS_MODE: connector
      LINKSOCKS_URL: l.zetx.tech
      LINKSOCKS_TOKEN: your_connector_token
    ports:
      - "127.0.0.1:1080:1080"
    restart: unless-stopped
```

Start and test:

```bash
docker compose up -d
curl --socks5 127.0.0.1:1080 http://httpbin.org/ip
```

Stop:

```bash
docker compose down
```

All flags can be provided as environment variables — see [Environment Variables](/guide/cli-options#environment-variables).
