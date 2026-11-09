# Relay connection transport

How the Go client reaches a relay (or the zone coordinator), and what was
measured on the way there.

## Current: TLS over TCP with Encrypted Client Hello

Every relay connection is a WebSocket over TLS. On top of that the client
offers **Encrypted Client Hello** by default (`--ech auto`):

1. The client looks up the relay host's HTTPS DNS record for an `ech`
   parameter — first through the system resolver (a DNS query for type
   `HTTPS`, following CNAME chains), and if the local resolver does not answer
   HTTPS queries, through a DNS-over-HTTPS resolver using the same proxy
   settings as the relay connection.
2. The config is cached with the record's TTL and re-fetched on later
   connections; redirects re-look up the config for each new host, so a
   load-balancer entry point that sends the client to a backend gets ECH for
   both hops.
3. When the server rejects ECH the handshake ends with
   `*tls.ECHRejectionError`. The client then retries once: with the server's
   retry configs when it offered any, otherwise with a plain ClientHello. A
   privacy feature must never cost connectivity, and a stale config must never
   make a relay unreachable.

Verified against the public relay (2026-11-09):

```
DBG Relay TLS handshake finished ech_accepted=true host=l.zetx.tech
DBG Relay TLS handshake finished ech_accepted=true host=cdn.team1.cc.cd
INF Authentication successful for reverse proxy
```

## Why not QUIC

`--protocol-version v2` and the zone protocol encrypt end-to-end, but the
transport to the relay is still TCP. Moving it to HTTP/3 would need the client
to speak WebSocket over HTTP/3 (RFC 9220), which requires the edge to advertise
`SETTINGS_ENABLE_CONNECT_PROTOCOL` (RFC 8441). Probing the deployed relay
(CNAME target `cfwsrc.zetx.tech`) over HTTP/2 shows the edge does not:

```
tls: version=304 alpn="h2" echAccepted=true
edge setting 0x3 = 256
edge setting 0x4 = 65536
edge setting 0x5 = 16777215
=> ENABLE_CONNECT_PROTOCOL NOT advertised: no extended CONNECT
```

Without extended CONNECT there is no standards-track way to run a WebSocket
(and therefore the relay link) over HTTP/3 against this edge, so the client
keeps TLS over TCP. Direct mode (peer-to-peer QUIC between provider and
connector) is unaffected and still the transport for direct connections.

Re-check after a Cloudflare change:

```bash
LINKSOCKS_EDGE_PROBE=l.zetx.tech:443 go test ./tests -run TestEdgeAdvertisesExtendedConnect -v
```

The test prints the edge's SETTINGS; `SETTINGS_ENABLE_CONNECT_PROTOCOL`
(`0x8`) is the gate. It also reports whether the TLS handshake negotiated ECH.
