# Introduction

LinkSocks is a secure SOCKS5 proxy over WebSocket. It allows you to traverse firewalls and complex network topologies by tunneling traffic through standard web connections.

Its architecture allows you to hide the proxy server behind a WAF or CDN (like Cloudflare). By disguising proxy traffic as normal HTTPS requests, it protects the server's real IP address and evades deep packet inspection.

Crucially, it supports Serverless Intranet Penetration (compatible with Cloudflare Workers). This allows you to bridge two private networks securely without a dedicated VPS. Since both the user and the internal resource actively connect to the serverless cloud relay, you can access your home or office network without a public IP or opening any risky inbound ports.

![Architecture](/abstract.svg)

## How It Works

LinkSocks enables two main proxy scenarios:

**Forward Proxy**: Client connects to server's network through SOCKS5. Server acts as gateway to internet.

**Reverse Proxy**: Server exposes SOCKS5 interface, multiple clients share their network access with load balancing.

## Use Cases

**CAPTCHA Solving**: Use client IPs instead of server IP to bypass Cloudflare restrictions

**Network Pivoting**: Access internal networks through compromised hosts without exposing attack infrastructure  

**Traffic Disguise**: WebSocket transport appears as normal web traffic, bypassing firewall restrictions
