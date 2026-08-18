# Access Control

Access Control adds firewall-like rules that restrict which destinations a tunnel may reach, with two independent rule sets: **entry access control** (enforced when the SOCKS5/HTTP request is parsed on the local proxy port) and **dial access control** (enforced by the provider right before it dials the destination). A shared token can only reach the subnets and ports you explicitly allow.

## Roles

| Role | Command | Behaviour |
|------|---------|-----------|
| Provider | `linksocks provider` / `client -r` | Performs the actual dial to the destination (provides the outbound network) |
| Connector | `linksocks connector` / `client` | Runs the local SOCKS5/HTTP proxy port that applications use |
| Server | `linksocks server` | Relays messages between the two sides |

There are **two independent access controls**, each configured and enforced on its own side:

1. **Entry access control** — applied on the local proxy port when the SOCKS5/HTTP request is parsed.
2. **Dial access control** — applied by the side that performs the actual connection, right before dialing.

They are configured separately and do not share rules. Blocking at either layer fails the request to the application.

## Rules

Access control is a list of **independent allow entries**. Each entry pairs an address set with a port set. A destination is allowed when it matches **at least one entry**, matching both the address and the port within that same entry:

```text
Rule A: 192.168.1.0/24  +  port 22
Rule B: 10.0.0.0/8      +  ports 8000-9000
```

With these rules, `192.168.1.5:22` is allowed (Rule A), `10.1.2.3:8500` is allowed (Rule B), but `192.168.1.5:80` is blocked (Rule A port mismatch, Rule B subnet mismatch).

An empty address set means any address; an empty port set means any port. No rules at all means everything is allowed (default).

## Address Formats

Each entry accepts one or more of the following:

| Format | Example | Meaning |
|--------|---------|---------|
| CIDR | `192.168.0.0/16` | Subnet |
| Bare IP | `192.168.1.5` | Single host (`/32` for IPv4, `/128` for IPv6) |
| Last-octet range | `192.168.1.1-255` | `192.168.1.1` through `192.168.1.255` |
| Full IP range | `192.168.1.1-192.168.2.255` | Any IP between the two (inclusive) |

## Port Formats

Ports are **numbers**. A single port is a number; a range is a `[start, end]` pair:

```json
22
[90, 150]
```

## Matching Semantics

- Domain names are resolved via DNS and must resolve to an IP inside one of the entry's address sets; unresolvable domains are **rejected**.
- Both dimensions must match within the same entry.

## Configuration

### Entry Access Control (local proxy port)

Applied when the SOCKS5/HTTP request is parsed on the local proxy port.

**Server side** — the port the server exposes in reverse mode (server-wide default and per-token override):

```go
srv := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
    WithSocksWaitClient(false).
    WithEntryAccessControl(ac))

srv.AddReverseToken(&linksocks.ReverseTokenOptions{
    Token:         "my_token",
    AccessControl: ac, // per-token override
})
```

**HTTP API** — create a reverse token with a `rules` field (enforced on the server's local proxy port):

```bash
curl -X POST \
     -H "X-API-Key: your_secret_api_key" \
     -H "Content-Type: application/json" \
     -d '{
       "type": "reverse",
       "token": "my_token",
       "rules": [
         {
           "addrs": ["192.168.1.0/24", "192.168.1.1-255"],
           "ports": [22, [90, 150]]
         },
         {
           "addrs": ["10.0.0.5"],
           "ports": [443]
         }
       ]
     }' \
     http://localhost:8765/api/token
```

Invalid addresses or port ranges are rejected with an error.

**Client side** — the local SOCKS5/HTTP port the client opens (forward / connector mode):

```go
client := linksocks.NewLinkSocksClient(token, linksocks.DefaultClientOption().
    WithWSURL("ws://your-server:8765").
    WithSocksPort(9870).
    WithEntryAccessControl(ac))
```

### Dial Access Control (provider)

Applied by the side that performs the actual connection, right before dialing. When the destination does not match any rule, the connection is refused and the SOCKS5 client receives `0x04` (host unreachable).

**Client side** — the provider process (`linksocks provider` / `client -r`):

```go
ac, err := linksocks.NewAccessControl([]linksocks.AccessRule{
    {
        Addrs: []string{"192.168.1.0/24", "10.0.0.5"},
        Ports: []linksocks.PortSpec{linksocks.SinglePort(22), linksocks.PortRange(90, 150)},
    },
})
if err != nil {
    log.Fatal(err)
}

client := linksocks.NewLinkSocksClient(token, linksocks.DefaultClientOption().
    WithWSURL("ws://your-server:8765").
    WithReverse(true).
    WithSocksWaitServer(true).
    WithDialAccessControl(ac))
```

**Server side** — when the server performs the dial itself (forward proxy mode):

```go
srv := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
    WithDialAccessControl(ac))
```

## Per-Token Rules

Rules can also be attached to individual tokens. A token-level rule **overrides** the corresponding server-wide rule for that token; when present, only the token rule is consulted.

**Forward tokens** — enforced on the server right before it dials on behalf of that token:

```go
token, err := srv.AddForwardTokenWithRules("my_token", []linksocks.AccessRule{
    {Addrs: []string{"192.168.1.0/24"}, Ports: []linksocks.PortSpec{linksocks.SinglePort(22)}},
})
```

**Connector tokens** — enforced on the server when a request from that connector is forwarded to a reverse provider:

```go
token, err := srv.AddConnectorTokenWithRules("my_connector", "my_reverse_token", []linksocks.AccessRule{
    {Addrs: []string{"10.0.0.0/8"}, Ports: []linksocks.PortSpec{linksocks.PortRange(8000, 9000)}},
})
```

**HTTP API** — both token types accept an optional `rules` field:

```bash
curl -X POST \
     -H "X-API-Key: your_secret_api_key" \
     -H "Content-Type: application/json" \
     -d '{
       "type": "forward",
       "token": "my_token",
       "rules": [
         {"addrs": ["192.168.1.0/24"], "ports": [22]}
       ]
     }' \
     http://localhost:8765/api/token

curl -X POST \
     -H "X-API-Key: your_secret_api_key" \
     -H "Content-Type: application/json" \
     -d '{
       "type": "connector",
       "token": "my_connector",
       "reverse_token": "my_reverse_token",
       "rules": [
         {"addrs": ["10.0.0.0/8"], "ports": [[8000, 9000]]}
       ]
     }' \
     http://localhost:8765/api/token
```

## CLI

Both the Go and the Python (`linksocks` pip package) CLIs take a single repeatable `--access-rule` flag in `ADDR:PORT` form: the address part is a CIDR, bare IP, or range; the port part a single port or a `lo-hi` range. The side it is enforced on is chosen automatically from the command role:

| Command | Role | `--access-rule` maps to |
|---------|------|-------------------------|
| `linksocks client` (forward) | connector | entry (local SOCKS5/HTTP port) |
| `linksocks connector` | connector | entry (local SOCKS5/HTTP port) |
| `linksocks client -r` / `linksocks provider` | provider | dial (before outbound connect) |
| `linksocks server` (forward mode) | dialing server | dial (server-wide) |
| `linksocks server -r` (reverse mode) | relay server | per-token entry (applied to the reverse token) |

```bash
linksocks server -u ws://localhost:9870 -r -p 1080 \
    --access-rule 192.168.1.0/24:22 \
    --access-rule 192.168.1.1-192.168.2.255:22-100 \
    --access-rule 10.0.0.5:443
```

When the server runs in **API server mode** (`--api-key`), `--access-rule` is rejected: server-wide rules must be configured through the HTTP API instead. Two tiers of rules are available in this mode:

- **Server-wide rules** — set via `GET/PUT /api/config/access` (equivalent to `WithEntryAccessControl` / `WithDialAccessControl`).
- **Per-token / per-key rules** — via the `rules` field of `POST /api/token`, or by registering an additional API key with default rules (`ServerOption.WithAPIKeys`).

See [HTTP API access control](/guide/http-api#server-wide-access-control).

## Behavior When Blocked

| Where | Protocol | Response |
|-------|----------|----------|
| Dial side (provider / forward server) | TCP / UDP connect | SOCKS5 `0x04` (host unreachable) |
| Forward token rule (server dialing) | TCP / UDP connect | SOCKS5 `0x04` (host unreachable) |
| Connector token rule (server forwarding) | TCP / UDP connect | SOCKS5 `0x04` (host unreachable) |
| Local proxy entry | SOCKS5 CONNECT | `0x02` (connection not allowed by ruleset) |
| Local proxy entry | SOCKS5 UDP datagram | Datagram silently dropped |
| Local proxy entry | HTTP CONNECT | `403 Forbidden` |
| Local proxy entry | HTTP absolute request | `403 Forbidden` |

All blocked attempts are logged with the target address and port.

## Example: SSH-only Tunnel

Allow the tunnel to reach only SSH (port 22) on one machine and the web (ports 80/443) on another, enforced on the provider dial side:

```go
ac, _ := linksocks.NewAccessControl([]linksocks.AccessRule{
    {Addrs: []string{"10.0.0.5"}, Ports: []linksocks.PortSpec{linksocks.SinglePort(22)}},
    {Addrs: []string{"10.0.1.0/24"}, Ports: []linksocks.PortSpec{linksocks.SinglePort(80), linksocks.SinglePort(443)}},
})
client := linksocks.NewLinkSocksClient(token, linksocks.DefaultClientOption().
    WithWSURL("ws://your-server:8765").
    WithReverse(true).
    WithSocksWaitServer(true).
    WithDialAccessControl(ac))
```
