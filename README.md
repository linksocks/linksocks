[![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/zetxtech/wssocks/ci.yml?logo=github&label=Tests)](https://github.com/zetxtech/wssocks/actions) [![Codecov](https://img.shields.io/codecov/c/github/zetxtech/wssocks?logo=codecov&logoColor=white)](https://app.codecov.io/gh/zetxtech/wssocks/tree/main) [![Docker Pulls](https://img.shields.io/docker/pulls/jackzzs/wssocks?logo=docker&logoColor=white)](https://hub.docker.com/r/jackzzs/wssocks)

# WSSocks

WSSocks is a SOCKS proxy implementation over WebSocket protocol.

## Overview

This tool allows you to securely expose SOCKS proxy services under Web Application Firewall (WAF) protection (forward socks), or enable clients to connect and serve as SOCKS proxy servers when they don't have public network access (reverse socks).

![Main Diagram](https://github.com/zetxtech/wssocks/raw/main/images/abstract.svg)

For python version, please check [zetxtech/pywssocks](https://github.com/zetxtech/pywssocks). But note that the Python version typically has lower performance compared to this implementation.

## Features

1. Supporting command-line usage, API server, and library integration.
2. Forward, reverse and agent proxy modes.
3. Round-robin load balancing for reverse proxy.
4. SOCKS proxy authentication support.
5. IPv6 over SOCKS5 support.
6. UDP over SOCKS5 support.

## Potential Applications

1. Distributed HTTP backend.
2. Bypassing CAPTCHA using client-side proxies.
3. Secure intranet penetration, using CDN network.

## Usage

### As a tool

Forward Proxy:

```bash
# Server (WebSockets at port 8765, as network connector)
wssocks server -t example_token

# Client (SOCKS5 at port 1080)
wssocks client -t example_token -u ws://localhost:8765 -p 1080
```

Reverse Proxy (with `-r` flag):

```bash
# Server (WebSockets at port 8765, SOCKS at port 1080)
wssocks server -t example_token -p 1080 -r

# Client (as network connector)
wssocks client -t example_token -u ws://localhost:8765 -r
```

Agent Proxy (with `-c` flag for connectors' token):

```bash
# Server (WebSockets at port 8765, SOCKS at port 1080)
wssocks server -t example_token -c example_connector_token -p 1080 -r

# Client (as network connector)
wssocks client -t example_token -u ws://localhost:8765 -r

# Connector (SOCKS5 at port 1180)
wssocks client -t example_connector_token -u ws://localhost:8765 -p 1180
```

Autonomy Agent Proxy (with `-a` flag):

```bash
# Server (WebSocket at port 8765, autonomy mode)
wssocks server -r -t example_token -a

# Client (as network connector, with connector token)
wssocks client -r -t example_token -c example_connector_token
```

In autonomy mode:
1. The server's SOCKS proxy will not start listening.
2. Reverse clients can specify their own connector tokens.
3. Load balancing is disabled - each connector's requests will only be routed to its corresponding reverse client.

## Installation

WSSocks can be installed by:

```bash
go install github.com/zetxtech/wssocks/cmd@latest
```

You can also download pre-built binaries for your architecture from the [releases page](https://github.com/zetxtech/wssocks/releases).

WSSocks is also available via Docker:

```bash
docker run --rm -it jackzzs/wssocks --help
```

## API Server

WSSocks server provides an HTTP API when enabled with the `--api-key` flag:

```bash
# Start server with API enabled
wssocks server --api-key your_api_key
```

### API Endpoints

All API requests require the `X-API-Key` header with your configured API key.

#### Get Server Status

```
GET /api/status
```

Returns server version and a list of all tokens with their types and active client counts.

#### Add Forward Token

```
POST /api/token
Content-Type: application/json

{
    "type": "forward",
    "token": "new_token"  // Optional: auto-generated if not provided
}
```

Adds a new forward proxy token.

#### Add Reverse Token

```
POST /api/token
Content-Type: application/json

{
    "type": "reverse",
    "token": "new_token",  // Optional: auto-generated if not provided
    "port": 1080,          // Optional: auto-allocated if not provided
    "username": "user",    // Optional: SOCKS authentication
    "password": "pass"     // Optional: SOCKS authentication
}
```

Adds a new reverse proxy token with specified SOCKS settings.

#### Remove Token

```
DELETE /api/token/{token}
```

Or

```
DELETE /api/token

Content-Type: application/json

{
    "token": "token_to_delete"
}
```

Removes the specified token.

## License

WSSocks is open source under the MIT license.
