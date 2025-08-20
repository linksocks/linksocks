[![GitHub Stars](https://img.shields.io/github/stars/linksocks/linksocks?style=flat&logo=github)](https://github.com/linksocks/linksocks) [![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/linksocks/linksocks/ci.yml?logo=github&label=Tests)](https://github.com/linksocks/linksocks/actions) [![Codecov](https://img.shields.io/codecov/c/github/linksocks/linksocks?logo=codecov&logoColor=white)](https://app.codecov.io/gh/linksocks/linksocks/tree/main) ![Python Version](https://img.shields.io/badge/python_version-%3E%203.8-blue?logo=python&logoColor=white) [![PyPI - Version](https://img.shields.io/pypi/v/linksocks?logo=pypi&logoColor=white)](https://pypi.org/project/linksocks/) ![Go Version](https://img.shields.io/github/go-mod/go-version/linksocks/linksocks) ![License](https://img.shields.io/github/license/linksocks/linksocks)

# LinkSocks

LinkSocks is a SOCKS proxy implementation over WebSocket protocol.

[中文文档 / Chinese README](README.cn.md)

## Overview

This tool allows you to securely expose SOCKS proxy services under Web Application Firewall (WAF) protection (forward socks), or enable clients to connect and serve as SOCKS proxy servers when they don't have public network access (reverse socks).

![Main Diagram](https://github.com/linksocks/linksocks/raw/main/images/abstract.svg)

📖 **Documentation**: https://linksocks-docs.zetx.tech/

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

## Quick Start

### Forward Proxy

```bash
# Server Side: Start server with WebSocket on port 8765
linksocks server -t example_token

# Client Side: Connect to server and provide SOCKS5 proxy on port 9870
linksocks client -t example_token -u ws://localhost:8765 -p 9870

# Test the proxy
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

### Reverse Proxy

```bash
# Server Side: Start server with SOCKS5 proxy on port 9870
linksocks server -t example_token -r -p 9870

# Client Side: Connect as network provider
linksocks client -t example_token -u ws://localhost:8765 -r

# Test the proxy
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

### Agent Proxy

```bash
# Server Side: Start server with both provider and connector tokens
linksocks server -t provider_token -c connector_token -p 9870 -r

# Provider Side: Connect as network provider
linksocks provider -t provider_token -u ws://localhost:8765

# Connector Side: Connect to use the proxy
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180

# Test the proxy
curl --socks5 127.0.0.1:1180 http://httpbin.org/ip
```

### Autonomy Mode

```bash
# Server Side: Start server in autonomy mode
linksocks server -t provider_token -r -a

# Provider Side: Provider sets its own connector token
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765

# Connector Side: Use the specific connector token to access this provider
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

You can also use our public server (for autonomy mode proxy) at `ws://linksocks.zetx.tech`.

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

> The python version is a wrapper of the Golang implementation. See: [Python Bindings](https://linksocks-docs.zetx.tech/python/)

## Cloudflare Worker

LinkSocks server (for autonomy mode proxy) can be hosted on Cloudflare Worker, see: [linksocks/linksocks.js](https://github.com/linksocks/linksocks.js)

[![Deploy to Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/linksocks/linksocks.js)

The linksocks.js version is a lite version and does not contain API functionality.

## API Server

LinkSocks server provides an HTTP API for dynamic token management:

```bash
# Start server with API enabled
linksocks server --api-key your_api_key
```

For detailed API usage and examples, see: [HTTP API](https://linksocks-docs.zetx.tech/guide/http-api)

## License

LinkSocks is open source under the MIT license.
