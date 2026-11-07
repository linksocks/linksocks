# Zone virtual networks

Internal protocol and implementation contract. User instructions are in
[`docs/guide/zones.md`](../docs/guide/zones.md) and
[`docs/zh/guide/zones.md`](../docs/zh/guide/zones.md).

## Model

A zone is a public, preconfigured IPv4 network. A signed-in website user creates
it with a name, a private IPv4 CIDR (/16 through /30), and a member limit (2–256).
Anyone who knows its ID can join without a website account. Membership grants
network access to every other member; it is not a private invitation system.

Each machine keeps a local Ed25519 identity. The coordinator assigns an IPv4
address to that identity, retaining it across temporary disconnections for
24 hours. Explicit leave releases the address and disconnects the active node.
Deleting a zone disconnects its members. Network and broadcast addresses are
never assigned. Zone networks must not overlap a client's existing routes.

The first implementation offers two client modes over one membership model:

- **Proxy mode (default, no root).** `zone join` starts a local SOCKS5 server
  (TCP CONNECT and UDP ASSOCIATE) on `127.0.0.1:1080` (`--socks-port`). Members
  exchange TCP streams and UDP datagrams through the coordinator, addressed by
  member virtual IP and port. Incoming streams are delivered to services bound
  to `127.0.0.1` on the member's machine. No interface is created, so ICMP and
  L2-only protocols are unavailable.
- **VLAN mode (`--vlan`, root, Linux).** The client creates an L2 TAP
  interface and exchanges Ethernet frames. ARP, IPv4 unicast and multicast
  (ICMP/TCP/UDP), broadcast traffic (UDP broadcast, WOL magic packets) and raw
  frames work like a physical segment. The coordinator floods broadcast,
  multicast and unknown unicast frames to every other member and forwards
  unicast to the learned sender of the destination MAC.

The overlay does not advertise LAN subnets, install a default route, provide
DNS, or bridge to the physical LAN. VLAN mode is currently Linux-only next to
the proxy mode that runs on Linux, macOS and Windows.

## Trust boundaries

The website controls zone configuration, address assignment, welcome text,
firewall rules and the peer directory. Every node proves possession of its
Ed25519 key using a fresh coordinator challenge. Peers verify source and
destination addresses inside decrypted payloads. Zone owners can delete only
their own zones; admins can manage all zones. Only admins can create public
zones; zones are private by default.

Each connection generates a fresh X25519 key and a random 16-byte session ID.
Directional payload keys use X25519, HKDF-SHA256 and XChaCha20-Poly1305 over
one shared per-direction sequence space across frames, streams and datagrams.
Ephemeral keys and session IDs are covered by the identity signature. The
coordinator is trusted for the identity/address/key directory. With an honest
directory it cannot read payloads. The coordinator sees membership, the
plaintext frame MAC addresses, stream metadata (member pair, port, byte
counts), datagram ports, sizes and timing, and it can enforce rules from that
metadata. Frame payloads are opaque to it, so VLAN traffic is filtered by the
receiving clients themselves, which inspect every decrypted Ethernet frame and
treat their copy of the rules as authoritative. Clients do not independently
verify peer registration signatures from the roster or pin peer keys, so this
does not authenticate peers against a malicious coordinator that substitutes
directory entries. Sequence numbers reject replays within a connection. Keys
and replay state change when a peer reconnects. Private identity keys are
stored locally with owner-only permissions and are never sent to the website.

## Coordinator transport

The WebSocket endpoint is `/zone/<id>/socket`. Zone management uses the
website's existing authenticated API at `/api/zones`; public discovery uses
`/api/zones/public` (public zones only), and `/api/zones/<id>/info` returns
network metadata, members and settings. Settings updates use
`/api/zones/<id>/settings`, and owners remove a member with
`/api/zones/<id>/members/<identity>/kick`.

The coordinator sends `{"type":"challenge","challenge":"<64 lowercase hex>"}`.
The client responds with the following JSON:

```
{"type":"join","action":"join|leave","identity":"<64 lowercase hex>",
 "key":"<64 lowercase hex>","session":"<32 lowercase hex>","name":"<1–64 characters>",
 "signature":"<128 lowercase hex>"}
```

The signature covers the exact UTF-8 string (no trailing newline):

```
linksocks-zone-v1\n<zone id>\n<challenge>\n<action>\n<key>\n<session>\n<name>
```

Zone IDs use canonical lowercase UUID text. Identity, exchange key, session and
signature fields require lowercase hexadecimal text at the exact lengths above;
signature input uses those exact strings, not decoded or reformatted values.
Roster entries whose exchange keys or identities repeat are rejected, because
identical exchange keys would make two peers indistinguishable to packet
crypto.
Names must be nonempty after trimming, contain at most 64 Unicode code points,
and contain no C0 controls or DEL. The transmitted name, including any surrounding
spaces, is signed verbatim. The Go CLI trims the name before sending it.

For joins, the coordinator rejects low-order or otherwise unusable X25519
public keys before allocating an address or publishing the roster. The check
attempts a probe derivation and rejects errors or an all-zero shared secret.
Clients also reject unusable exchange keys when deriving packet keys from a
roster. A signed `action: "leave"` releases the identity without allocating a
lease or using its exchange key.

After joining, the server sends a full roster to every connected member:

```
{"type":"members","zone":{"id":"...","name":"...","cidr":"10.88.0.0/24","mtu":1500,
 "visibility":"private|public"},
 "self":"10.88.0.1",
 "settings":{"welcome":"...","default_action":"allow|deny",
   "rules":[{"src":"10.88.0.0/24","src_port":"any","proto":"tcp","dst":"any",
     "dst_port":443,"action":"allow"}]},
 "peers":[{"identity":"...","key":"...","session":"...","name":"...","ip":"10.88.0.2"}]}
```

Only online peers appear; self is excluded. A successful leave returns
`{"type":"left"}`. Errors use `{"type":"error","error":"..."}`. Removed members
are closed with code `4003` and must not reconnect; zone deletion and explicit
leave use `4001`. Authentication has a 10-second deadline; each zone accepts at
most 32 pending sockets. Control messages are limited to 2048 UTF-8 bytes.
Duplicate active joins with one identity are rejected. Public member
information includes offline leases, while the transport roster contains only
connected members. Settings are repeated in every roster, so rule updates reach
connected members through the next roster.

### Binary messages

Binary messages start with a kind byte. Clients send kind 1 frames in VLAN
mode, and kinds 2 and 3 in both modes.

**Kind 1 — L2 frame.** Version (1 byte, value 2), source MAC (6), destination
MAC (6), sequence number (8, big-endian), then the sealed Ethernet frame. The
22-byte header is AEAD additional data; the 24-byte nonce is the sender's
16-byte session ID followed by the sequence number; HKDF salt is the UTF-8 zone
ID and info is `linksocks-zone-v1\n<sender key hex>\n<receiver key hex>`.
Decrypted frames are 14–1514 bytes and their inner source and destination MACs
must match the header. The coordinator validates the header, learns the source
MAC for the sending member, and forwards by destination: broadcast (all ones)
and multicast (least significant bit of the first octet) frames go to every
other member; unicast goes to the learned owner, or is flooded when its owner
is unknown. Learned addresses are released when a member disconnects. Frame
payloads are not inspected by the coordinator.

**Kind 2 — stream.** Opcode (1 byte), then a 4-byte stream ID.

- `OPEN (1)`: stream ID, source IPv4 (4), destination IPv4 (4), destination
  port (2). The source address must equal the sender's assigned address. The
  coordinator checks the destination member, evaluates the firewall rules for
  TCP toward that port, and forwards the message to the destination member.
- `ACCEPT (2)`: stream ID; forwarded to the stream initiator.
- `REFUSE (3)`: stream ID, reason code (2), optional UTF-8 reason (≤64 bytes).
- `DATA (4)`: stream ID, sequence number (8, the same per-direction space as
  other sealed payloads), sealed payload (≤16384 bytes). The 14-byte prefix is
  AEAD additional data.
- `CLOSE (5)`: stream ID; graceful close of one direction. The stream ends
  when both directions are closed.
- `RESET (6)`: stream ID, code (2); aborts the stream.

Receivers re-check their own copy of the rules before dialing and refuse with
code 2 (`not allowed`). Refused or reset streams release their state, and the
coordinator resets every stream of a member when it disconnects.

**Kind 3 — datagram.** Source IPv4 (4), destination IPv4 (4), source port (2),
destination port (2), sequence number (8, the same per-direction space as other
sealed payloads), then the sealed UDP payload. The 21-byte prefix is AEAD
additional data. The coordinator verifies the source address, evaluates the
rules for UDP and forwards to the destination member. Receivers keep a
per-source NAT map for 60 seconds and deliver to `127.0.0.1:<destination
port>`; replies swap source and destination and return through the same path.

Client MAC addresses derive from `SHA-256("linksocks-zone-mac-v1" || zoneID ||
raw Ed25519 identity public key)`, first 6 bytes, with the locally-administered
bit set and the multicast bit cleared. Deriving from the stable identity keeps
the address constant across reconnects, so peer ARP caches stay valid. The
coordinator learns MACs from frames.

The coordinator accepts binary messages up to 64 KiB and rejects invalid kinds,
versions, lengths and spoofed source addresses by failing the socket. Frames
for an unknown or offline destination are silently dropped; roster changes can
race with in-flight traffic.

## Firewall rules

Each zone has up to 64 ordered rules and a default action (`allow` or `deny`).
A rule matches IPv4 source and destination networks, protocol (`any`, `tcp`,
`udp`, `icmp`, `arp`) and TCP/UDP ports (`any`, a port, or a range). The first
matching rule decides; otherwise the default action applies. ARP is subject to
rules like any other protocol.

Streams and datagrams are checked by the coordinator and by both endpoints, so
coordinator metadata enforces them for proxy mode. TCP source ports are not
known at the stream layer, so rules with a specific source port apply to VLAN
traffic only, where the receiving client matches every decrypted frame against
the rule table. The coordinator cannot inspect VLAN frames, so client-side
enforcement is authoritative for that traffic.

Text messages after authentication are rejected except `{"type":"leave"}` and
keepalive `{"type":"ping"}` (answered with `{"type":"pong"}`). The Go client
sends keepalives every 25 seconds and has a 75-second receive deadline.

## CLI

The `zone list` subcommand lists public zones. `zone join` runs the network in
the foreground, reconnecting after transient failures, and prints the welcome
message from the zone settings. Ctrl+C leaves it. `zone leave` proves the same
local identity and removes its active membership, including a join running in
another process. Shared flags are `--url`, `--identity` and `--no-env-proxy`;
join also accepts `--name`, `--interface`, `--socks-port` (default 1080, `0`
disables the proxy) and `--vlan` (root; Linux TAP overlay).

In proxy mode the client prints the SOCKS address on startup. Services on a
member's machine are reachable at the member's virtual IP through a local
SOCKS5 client (`curl --socks5-hostname 127.0.0.1:1080 http://10.88.0.2:8080/`)
or, in VLAN mode, directly at the member's virtual IP. Streams terminate on
`127.0.0.1` at the receiving member, so expose services on that machine's
loopback or use VLAN mode for full IP semantics. Kicked members exit with a
message and never reconnect automatically.

Transport retry delays grow from 1 to 8 seconds. Network failures and coordinator
HTTP 5xx responses are retried. HTTP 4xx responses, authentication failures and
local interface errors terminate the command. Interface setup failure sends
leave rather than retaining an unusable lease.

## Verification

Tests cover allocation/rejoin/leave/exhaustion, signature failures, duplicate
identities, source spoofing, missing destinations, zone deletion, encrypted
frame round trips, tampering, replay rejection, peer key rotation, stream
routing and refusal, datagram NAT, and rule matching. End-to-end verification
uses two Linux network namespaces with real TAP devices (`--vlan`) plus two
userspace proxy-mode clients driven by a real SOCKS5 client, through the local
Worker coordinator.
