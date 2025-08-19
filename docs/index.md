---
layout: home

hero:
  name: "LinkSocks"
  text: "SOCKS5 over WebSocket"
  image:
    src: /hero.png
    alt: LinkSocks
  tagline: "Zero-Configuration Intranet Penetration Tool"
  actions:
    - theme: brand
      text: Get Started
      link: /guide/
    - theme: alt
      text: GitHub
      link: https://github.com/linksocks/linksocks

features:
  - icon: 🌐
    title: Zero Configuration
    details: Designed for non-specific, dynamic clients; clients can join/leave anytime
  - icon: ☁️
    title: Serverless Architecture
    details: Relay server can be deployed on Cloudflare Workers. Fast & Global.
  - icon: ⚖️
    title: Load Balancing
    details: Dynamically increase or decrease clients as backends and achieve load balancing
  - icon: 🌍
    title: IPv6 + UDP Support
    details: Full SOCKS5 protocol support including IPv6 and UDP over SOCKS5
  - icon: 🐍
    title: Python Bindings
    details: Python API for easy integration into existing applications
  - icon: 📱
    title: Multi-Platform
    details: Provides Go binaries and Docker images for cross-platform support
---

## Quick Start

```bash
go install github.com/linksocks/linksocks/cmd/linksocks@latest
```

Or download pre-built binaries from [releases page](https://github.com/linksocks/linksocks/releases).

### Forward Proxy

```bash
# Server (WebSockets at port 8765, as network provider)
linksocks server -t example_token

# Client (SOCKS5 at port 9870)
linksocks client -t example_token -u http://localhost:8765 -p 9870
```

### Reverse Proxy

```bash
# Server (WebSockets at port 8765, SOCKS at port 9870)
linksocks server -t example_token -p 9870 -r

# Client (as network provider)
linksocks client -t example_token -u http://localhost:8765 -r
```