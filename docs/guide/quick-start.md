# Quick Start

## Installation

### Golang Version
```bash
go install github.com/linksocks/linksocks/cmd/linksocks@latest
```

Or download pre-built binaries from [releases page](https://github.com/linksocks/linksocks/releases).

### Docker
```bash
docker run --rm -it jackzzs/linksocks --help
```

### Python Version
```bash
pip install linksocks
```

::: info
The python version is a wrapper of the Golang implementation. See: [Python Bindings](/python/)
:::

## Forward Proxy

In forward proxy mode, the server provides network access and the client runs the SOCKS5 interface.

**Server Side:**
```bash
# Start server with WebSocket on port 8765
linksocks server -t example_token
```

**Client Side:**
```bash
# Connect to server and provide SOCKS5 proxy on port 9870
linksocks client -t example_token -u ws://localhost:8765 -p 9870
```

**Test the proxy:**
```bash
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

## Reverse Proxy

In reverse proxy mode, the server runs the SOCKS5 interface and clients provide network access.

**Server Side:**
```bash
# Start server with SOCKS5 proxy on port 9870
linksocks server -t example_token -r -p 9870
```

**Client Side:**
```bash
# Connect as network provider
linksocks client -t example_token -u ws://localhost:8765 -r
```

**Test the proxy:**
```bash
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

## Agent Proxy

In agent proxy mode, the server acts as a relay between two types of clients: providers (who share network access) and connectors (who use the proxy). Each type uses different tokens for controlled access.

**Server Side:**
```bash
# Start server with both provider and connector tokens
linksocks server -t provider_token -c connector_token -p 9870 -r
```

**Provider Side:**
```bash
# Connect as network provider
linksocks provider -t provider_token -u ws://localhost:8765
```

**Connector Side:**
```bash
# Connect to use the proxy
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

**Test the proxy:**
```bash
curl --socks5 127.0.0.1:1180 http://httpbin.org/ip
```

## Autonomy Mode

Autonomy mode is a special type of agent proxy with the following characteristics:

1. The server's SOCKS proxy will not start listening
2. Providers can specify their own connector tokens
3. Load balancing is disabled - each connector's requests are routed only to its corresponding provider

**Server Side:**
```bash
# Start server in autonomy mode
linksocks server -t provider_token -r -a
```

**Provider Side:**
```bash
# Provider sets its own connector token
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765
```

**Connector Side:**
```bash
# Use the specific connector token to access this provider
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

### Use Our Public Server

You can use our public LinkSocks server at `linksocks.zetx.tech` for intranet penetration:

**Step 1: On machine A (inside the network you want to access)**
```bash
linksocks provider -t any_token -u wss://linksocks.zetx.tech -c your_token
```

**Step 2: On machine B (where you want to access the network)**
```bash
linksocks connector -t your_token -u wss://linksocks.zetx.tech -p 1080
```

**Test the connection:**
```bash
curl --socks5 127.0.0.1:1080 http://httpbin.org/ip
```

## Server Deployed on Cloudflare Workers

Deploy LinkSocks server on Cloudflare Workers for serverless operation:

[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/linksocks/linksocks.js)

The server will be started in autonomy mode. After deployment, connect using:


```bash
linksocks client -t your_token -u wss://your-worker.your-subdomain.workers.dev -p 9870
```

## API Server

LinkSocks server provides an HTTP API for dynamic token management, allowing you to add/remove tokens and monitor connections without restarting the server.

```bash
# Start server with API enabled
linksocks server --api-key your_api_key
```

For detailed API usage and examples, see: [HTTP API](/guide/http-api)

## Common Options

### Authentication
```bash
# Server with SOCKS authentication
linksocks server -t token -r -p 9870 -n username -w password

# Client with SOCKS authentication
linksocks client -t token -u ws://localhost:8765 -n username -w password
```

### Debug Mode
```bash
# Enable debug logging
linksocks server -t token -d
linksocks client -t token -u ws://localhost:8765 -d
```

### Custom Addresses
```bash
# Server listening on all interfaces
linksocks server -t token -H 0.0.0.0 -P 8765

# Client with custom SOCKS address
linksocks client -t token -u ws://localhost:8765 -h 0.0.0.0 -p 1080
```

## Next Steps

- Learn about [Command-line Options](/guide/cli-options) for advanced configuration
- Understand [Authentication](/guide/authentication) and security options
- Explore [Python Library](/python/) for integration
- Check [HTTP API](/guide/http-api) for dynamic management