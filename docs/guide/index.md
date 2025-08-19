# Introduction

LinkSocks is a SOCKS5 proxy over WebSocket protocol. It allows you to create secure proxy connections that work through firewalls and Web Application Firewalls.

LinkSocks' reverse proxy mode can be used for intranet penetration. But unlike other intranet penetration tools that focus on specific machines, LinkSocks works with dynamic clients that can join and leave anytime. No server-side configuration needed for new clients.

LinkSocks' forward proxy mode shares server network via WebSocket, disguising proxy traffic as web traffic to bypass firewalls and prevent IP leakage.

![Architecture](/abstract.svg)

## How It Works

LinkSocks enables two main proxy scenarios:

**Forward Proxy**: Client connects to server's network through SOCKS5. Server acts as gateway to internet.

**Reverse Proxy**: Server exposes SOCKS5 interface, multiple clients share their network access with load balancing.

## Use Cases

**CAPTCHA Solving**: Use client IPs instead of server IP to bypass Cloudflare restrictions

**Network Pivoting**: Access internal networks through compromised hosts without exposing attack infrastructure  

**Traffic Disguise**: WebSocket transport appears as normal web traffic, bypassing firewall restrictions