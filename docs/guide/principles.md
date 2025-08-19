# How It Works

LinkSocks operates in two main proxy modes, each designed for different network scenarios.

## Forward Proxy

Forward proxy allows a server to expose its internal network environment while protecting its IP address from being exposed.

First, start a LinkSocks server on a machine that can host websites and be accessed from the public internet, and add a token.

Then, on the device that needs to access the server's internal network environment, start the LinkSocks client and connect to the designated URL using the token. Since the transmission uses the WebSocket protocol, any WebSocket-supporting web firewall such as Cloudflare can be used as an intermediary layer to protect the server's IP address from being exposed.

After connecting, the client will open a configurable SOCKS5 port for other services to connect to. All requests will be forwarded through the established bidirectional channel, with the server performing the actual connections and sending data.

![Forward Proxy Diagram](/forward_proxy_diagram.svg)

### Forward Proxy Use Cases

- **Accessing restricted networks**: Connect to internal services through a central server
- **IP protection**: Hide your real IP when making requests
- **Bypassing firewalls**: Use WebSocket transport to bypass traditional firewall restrictions

## Reverse Proxy

Reverse proxy allows devices, which cannot be directly accessed from the public internet, to expose their internal network environment.

First, start a LinkSocks server on a machine that can host websites and be accessed from the public internet, and add a token.

Then, start a LinkSocks client on the internal network server and connect to the designated URL using the token. Since the transmission uses the WebSocket protocol, any WebSocket-supporting web firewall such as Cloudflare can be used as an intermediary layer to protect the server's IP from being exposed.

After connecting, the server will expose a SOCKS5 port for other services to connect to. All requests will be forwarded through the established bidirectional channel, with the client performing the actual connections and sending data.

![Reverse Proxy Diagram](/reverse_proxy_diagram.svg)

### Reverse Proxy Use Cases

- **Internal network access**: Access services on machines without public IP addresses
- **Load balancing**: Multiple clients can provide network access with automatic load balancing
- **CAPTCHA solving**: Use diverse client networks for CAPTCHA solving and IP rotation
- **Penetration testing**: After compromising internal network servers, directly penetrate outward

## Technical Advantages

### WebSocket Transport

- **Firewall friendly**: WebSocket traffic appears as normal HTTP traffic
- **WAF compatible**: Works through Web Application Firewalls like Cloudflare
- **CDN support**: Can be deployed behind CDNs for enhanced security
- **SSL/TLS support**: Secure transmission with WSS protocol

### Dynamic Client Management

- **No configuration required**: Clients can join and leave without server reconfiguration
- **Automatic scaling**: System scales based on available client capacity
- **Load balancing**: Round-robin distribution across available clients
- **Fault tolerance**: Automatic failover when clients disconnect

### Protocol Support

- **Full SOCKS5**: Complete SOCKS5 protocol implementation
- **Authentication**: Token-based authentication for secure connections
- **IPv6 support**: Full IPv6 connectivity support
- **UDP support**: UDP over SOCKS5 for comprehensive protocol coverage
