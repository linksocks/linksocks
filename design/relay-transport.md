# Relay connection transport

How the Go client reaches a relay (or the zone coordinator), and what was
measured on the way there.

## Current: TLS over TCP with Encrypted Client Hello

Every relay connection is a WebSocket over TLS. On top of that the client
offers **Encrypted Client Hello** by default (`--ech auto`):

1. The client looks for an `ech` parameter in the relay host's HTTPS DNS
   record. Discovery in order: `--ech-config` / `LINKSOCKS_ECH_CONFIG`, the
   in-process cache, plaintext DNS (the system resolver plus well-known public
   resolvers, type-`HTTPS` queries for every name on the CNAME chain, two
   passes, UDP falling back to TCP), DNS-over-HTTPS
   providers (AliDNS; Cloudflare by IP and by name), the relay's `/api/ech`
   endpoint, and finally a config already accepted for another relay host in
   this process. Nothing is written to disk.
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

### Why the relay serves /api/ech

The networks that need ECH most are the ones that also block the lookups: a
client that cannot reach a DNS-over-HTTPS endpoint and whose resolver hides the
HTTPS record behind a rotating CNAME chain has no way to learn the config, and
then the redirect hop gets reset by SNI. The relay itself is reachable on its
entry hostname, so it resolves its own HTTPS record from Cloudflare's network
and answers `GET /api/ech` with the config. Measured: the entry point on a
filtered network answered with a config that the backend accepted
(`ech_accepted=true` on both hops), and a config from an unrelated zone of the
same edge was accepted as well, which is why reusing a discovered config is
safe.

A deployment whose entry point is a separate load balancer should serve the
same endpoint there; `lb-entry.js` in the linksocks.js repository does that
without redirecting.

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
