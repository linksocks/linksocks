# Connect Machines in a Zone

A zone is a virtual IPv4 network shared by a group of machines. Each machine
that joins receives an address on the network, and applications reach other
members through a local SOCKS5 proxy or, in VLAN mode, directly at their
virtual addresses.

Zones are private by default. A private zone is not listed anywhere: only
machines that are given its ID can join. Administrators can create public
zones, which appear in the *Public zones* view on the website and in
`linksocks zone list`; anyone who sees a public zone can join it and reach the
services its members expose.

Open [Zones](https://l.zetx.tech/zones) on the website to browse public zones,
view members and copy a join command. Browsing and joining do not require an
account; signed-in users land on **My zones**, which lists the zones they
created, and can switch to **Public zones** to see every public zone; there is
no combined list. Zone IDs are lowercase UUIDs.

## Before You Connect

- Use a LinkSocks CLI build that includes `zone`. There is no Python library
  API; installing the Python bindings does not provide a `Zone` class.
- The default join mode runs a local SOCKS5 proxy and needs no special
  privileges. The optional `--vlan` mode needs an L2 TAP interface and works on
  Linux (with `/dev/net/tun` and the `ip` command) and on Windows (with a
  TAP-Windows6 adapter); both need root or administrator privileges.
- Choose a zone network that does not overlap any machine's existing LAN, VPN,
  container networks or other routes. The client checks interface addresses on
  every platform, and all IPv4 routing tables on Linux, excluding default
  routes.
- Proxy mode runs on Linux, macOS and Windows.

Linux has been verified end to end with real TAP interfaces and with the proxy
mode. The Windows TAP path and the macOS proxy path compile; macOS cannot run
`--vlan` (it only offers L3 tunnels) and the Windows TAP path has not been
verified on a Windows machine yet.

Proxy mode carries TCP streams and UDP datagrams (SOCKS5 UDP ASSOCIATE) and
creates no interface, so ICMP and other IP protocols are unavailable. `--vlan`
carries complete Ethernet frames: ARP, ICMP/ping, TCP, UDP, broadcast (UDP
broadcast and Wake-on-LAN magic packets), multicast and raw Ethernet frames
travel between members like on a physical network segment. The VLAN interface
MTU is 1500.

Neither mode provides IPv6, P2P direct connections, DNS, LAN subnet sharing or
a default internet route. Use a member's virtual IP or, in proxy mode, its
member name instead of relying on LAN discovery.

## Create a Network

1. Open `/zones` on your linksocks.js website and sign in.
2. Select **Create zone**.
3. Enter a **Zone name**, **IPv4 network (CIDR)** and **Member limit**.
4. Choose the visibility: **Private** (the default) or, if you are an
   administrator, **Public**.
5. Select **Create zone**.

Names contain 1–64 characters. The entire network must be inside an RFC1918
private range: `10.0.0.0/8`, `172.16.0.0/12` or `192.168.0.0/16`. Prefixes from
`/16` through `/30` are accepted. The member limit is 2–256 and must fit the
usable addresses: a `/30` fits 2 members, a `/29` fits 6, and a `/24` fits 254.
Network and broadcast addresses are not assigned.

New zones are private: they are not listed anywhere, and only machines you give
the ID to can join. Only administrators can create public zones. A public zone
is listed on the website and in `linksocks zone list`, and anyone who sees it
can join and reach the services its members expose, so share its ID
deliberately and make sure each member's firewall exposes only the intended
services.

Signed-in users create zones and delete their own zones; administrators can
delete any zone. Deleting a zone disconnects every member and releases all
addresses; it cannot be undone.

## Connect Each Machine

1. Find the zone on `/zones` and select **Copy command**. For a public zone,
   the ID and command are masked until you tick the confirmation that you have
   checked the machine's firewall; **Copy command** opens the same warning
   first. If the browser does not allow automatic copying, the command is
   selected so you can press Ctrl+C.
2. Run the copied command on each machine. Proxy mode needs no elevation;
   `--vlan` must run as root. Keep the command running. Its `Joined` line
   shows the assigned virtual IP, the interface in VLAN mode and the online
   peer count.
3. Use a member's virtual IP with your application, or its member name in
   proxy mode. The service must listen on its virtual IP in VLAN mode or on
   its loopback in proxy mode, and its firewall must allow the connection.

The join command takes a lowercase zone UUID. Use `--name` to replace the
hostname shown in the member list.

### Proxy Mode (Default)

`linksocks zone join <zone-id>` starts a local SOCKS5 proxy on
`127.0.0.1:6080` and prints its address:

```text
SOCKS5 proxy listening on 127.0.0.1:6080
```

Change the port with `--socks-port`; `--socks-port 0` disables the listener.
Applications address members by virtual IP or by name and should let the proxy
resolve names:

```bash
curl --socks5-hostname 127.0.0.1:6080 http://10.88.0.2:8080/
curl --socks5-hostname 127.0.0.1:6080 http://machine-b:8080/
```

Proxy mode carries TCP and UDP (SOCKS5 UDP ASSOCIATE). A connection to a
member's virtual IP arrives at the service listening on that member's
`127.0.0.1`, so no public interface or inbound port is needed. There is no
interface, so ICMP and protocols other than TCP and UDP are not carried.

### VLAN Mode (`--vlan`)

With `--vlan`, the client creates an L2 TAP interface — `lz0` by default, or
another name via `--interface` — with MTU 1500. Machines behave like hosts on
one Ethernet segment: ARP, ICMP/ping, TCP, UDP, UDP broadcast, Wake-on-LAN
magic packets, multicast and raw Ethernet frames all pass between members.
Applications connect directly to member virtual IPs with no proxy
configuration. Adding `--socks-port` in VLAN mode additionally starts the
SOCKS5 listener.

When joining several zones on one machine, give each join a distinct interface
name and use non-overlapping networks.

### Windows and macOS

`--vlan` needs a layer-2 (Ethernet) virtual adapter, because the zone protocol
treats each member's adapter MAC as its identity on the segment and filters
inbound frames by it. That adapter exists on Linux out of the box; Windows needs
a driver, and macOS cannot do layer 2 at all.

**Windows (TAP-Windows6).** Three steps, once per machine:

1. **Install the driver.** Download the current TAP-Windows installer from
   OpenVPN's release directory, <https://build.openvpn.net/downloads/releases/>
   (file `tap-windows-<version>-W6.exe`; also mirrored at
   <https://swupdate.openvpn.net/community/releases/>), and run it as
   administrator with the **TAP Virtual Ethernet Adapter** component selected —
   that component installs the driver *and* creates the first adapter. If you
   already have OpenVPN installed, its installer ships the same driver, so
   nothing extra is needed.
2. **Add one adapter per zone.** The installer creates the first one; for each
   further zone use the Start Menu shortcut
   **TAP-Windows → Utilities → Add a new TAP virtual ethernet adapter**, or run
   `C:\Program Files\TAP-Windows\bin\addtap.bat` from an elevated prompt.
   `Get-NetAdapter` lists what exists; an adapter is enough as-is, no IP
   settings are needed. Rename one with `Rename-NetAdapter` if you want a
   memorable `--interface` name.
3. **Join from an elevated PowerShell or Command Prompt** (Windows needs
   administrator rights for this, exactly as Linux needs root):

```powershell
linksocks zone join <zone-id> --vlan
# Several adapters: name the one to use
linksocks zone join <zone-id> --vlan --interface "Zone B"
```

The client checks the elevation and the adapter before it contacts the
coordinator, so a missing driver or a non-elevated shell fails immediately
instead of after a join. It then picks the first TAP-Windows6 adapter when
`--interface` is omitted, pins its MAC to the machine's member MAC (Windows
applies that on adapter restart, which the client performs), enables the
adapter, and sets the member address. Until it does, Windows shows the adapter
as "network cable unplugged"; that is expected.

**macOS** only offers L3 tunnels (utun), so `--vlan` is not available: join
without it and use the SOCKS5 proxy, which reaches members by address or name.

**WSL2** is Linux, so `--vlan` works there for programs running inside WSL.
Windows programs still cannot join that segment: point them at a proxy, which
WSL2 forwards on `127.0.0.1`.

### Command Reference

| Command or option | Use |
|---|---|
| `zone list` | List public zone IDs, names, networks and member limits |
| `zone join <zone-id>` | Join in the foreground; start a SOCKS5 proxy or, with `--vlan`, a TAP interface |
| `zone leave <zone-id>` | Release this machine's address and stop its active join |
| `--url`, `-u` | Coordinator website; defaults to `https://l.zetx.tech` |
| `--identity` | Path to the local machine key; use the same file for join and leave |
| `--name` | Machine name for join; defaults to the hostname |
| `--socks-port` | Local SOCKS5 proxy port, default `6080`; `0` disables the listener; with `--vlan` the proxy starts only when the flag is given |
| `--vlan` | Use an L2 TAP interface instead of proxy mode (Linux with `/dev/net/tun`, or Windows with TAP-Windows6; root or administrator) |
| `--interface` | TAP interface name for `--vlan`; default `lz0` on Linux, the first TAP-Windows6 adapter on Windows |
| `--no-env-proxy`, `-E` | Ignore environment proxy settings |
| `--ech` | Encrypted Client Hello for the coordinator connection; default `auto` |
| `--debug`, `-d` | Show debug logs; `-dd` adds trace logs (streams, datagrams, firewall drops, coordinator pings) |

Run these subcommands under `linksocks`. `--url` accepts HTTP(S) and WebSocket
URLs. Use HTTPS for a remote coordinator. The `--name`, `--socks-port`,
`--vlan` and `--interface` options belong to `join`; the other listed options
are shared by all zone commands.

A joined machine logs what it does: joining and the assigned address, later
peer-count changes, the SOCKS5 listener and, in VLAN mode, the interface. Add
`-d` to see why traffic flows or not — an opened stream, a refused one with its
reason (blocked by the zone firewall, the peer is offline, the local service is
unreachable) — and `-dd` for per-frame detail such as datagrams and firewall
drops.

## Manage a Zone

The zone owner and site administrators manage members and settings from
`/zones`:

- **View members** shows every machine with its name, status, virtual IP and
  identity. Online machines can be kicked; offline machines with retained
  addresses can be removed. Both disconnect a running join immediately and
  release its address. The removed process exits with `Removed from the zone.`
  and does not reconnect automatically; running the join command again lets
  the machine rejoin.
- **Settings** edits the **welcome message** and the **firewall rules**. The
  welcome message (up to 512 characters) is shown on machines when they join
  the zone.
- A zone holds up to 64 ordered firewall rules. Each rule matches a source and
  destination IPv4 network, a source and destination port (any, one port or a
  range), a protocol (`any`, `tcp`, `udp`, `icmp`, `arp`) and an allow or deny
  action. The first matching rule decides; traffic that matches no rule
  follows the zone's default policy (allow or deny).

Rule enforcement differs by mode. In proxy mode the relay and both members
check every TCP connection and UDP datagram against the rules. In `--vlan`
mode the relay cannot see inside Ethernet frames, so the receiving members
filter every frame against their copy of the rules. Rules with a specific
source port match VLAN traffic only, because proxy-mode streams carry no
source port to match.

## Keep Your Machine Identity

The first join generates an Ed25519 identity locally. Its private key stays on
the machine. The default file is `linksocks/zone.key` under the operating
system's user configuration directory: typically `~/.config` on Linux,
`~/Library/Application Support` on macOS, and `%AppData%` on Windows.

The client creates the key file with mode `0600` and any missing parent
directories with mode `0700` on Unix. It rejects an existing key file that
grants any group or other-user permissions. On Windows, restrict access
through the file's access controls. Do not copy one identity to several
machines: a zone accepts only one active connection per identity.

Use an explicit absolute `--identity` path if you need a predictable location
when running with elevated privileges. Changing the user or key file changes
the machine identity. Keep the original file to reconnect to a retained
address or release it later.

In VLAN mode the interface MAC address is derived from this identity and stays
the same across reconnects, so peer ARP caches remain valid.

## Disconnect or Recover After an Outage

Press **Ctrl+C** in the running join command to leave and release its address.
To stop it from another terminal, run `zone leave` with the same zone ID,
coordinator URL and identity file. This also disconnects a join running in
another process. A later join may receive a different address.

After a network outage or an abrupt process exit, the website marks the
machine **Offline** and retains its address for 24 hours. Offline leases still
count toward the member limit. A running client automatically reconnects after
transport failures; restarting with the same identity within the lease period
also retains the address. Explicit leave releases the lease immediately. A
machine removed by an owner or administrator stops instead of reconnecting.

Deleting a zone disconnects every member and releases all addresses. This
cannot be undone. To delete it, select **Delete zone** on `/zones` and
confirm. Clients stop when the zone is deleted or their membership is
explicitly removed.

## Understand What Is Encrypted

Each connection uses a fresh X25519 exchange key and XChaCha20-Poly1305
packet encryption between members. Reconnection replaces the exchange key.
With the coordinator providing the correct peer directory, the relay forwards
encrypted payloads and cannot read them.

You trust the coordinator to supply machine identities, peer keys and address
assignments. This does not protect against a malicious coordinator replacing
the peer directory. The coordinator can see membership, source and destination
virtual IPs, packet sizes and timing; in VLAN mode it can also see the
Ethernet addresses in the frames it forwards.

## Troubleshoot a Connection

| Message or symptom | What to check |
|---|---|
| `administrator privileges required` | `--vlan` needs root; run the join in an elevated terminal and check that `/dev/net/tun` exists |
| `zone --vlan needs a layer-2 virtual adapter` | VLAN mode needs Linux or a TAP-Windows6 adapter on Windows; macOS uses proxy mode |
| `configure zone interface` | Check that the platform's network commands are available and allowed |
| `Note: --interface applies to --vlan mode` | Drop `--interface` in proxy mode, or add `--vlan` if you want a TAP interface |
| `overlaps` | Choose a zone whose CIDR does not overlap the reported interface or route |
| `Identity is already online` | Stop the other join for this identity, or use `zone leave` with its key before joining again |
| `Zone is full` | Offline leases also occupy slots; their owners can leave, or wait for the 24-hour lease to expire |
| `Zone not found` | The zone was deleted or the ID is wrong; copy the ID and URL from the same website |
| `Zone service is not configured` | The operator must configure the Worker's `DB` and `ZONE` bindings; see the [Worker setup](https://github.com/linksocks/linksocks.js#zone-networks) |
| `Removed from the zone.` | An owner or administrator removed this machine; run the join command again to rejoin, or contact them |
| A connection through the proxy is refused | Check the zone's firewall rules and default policy with the zone owner |
| Joined, but an application cannot connect | Check the peer is online, the destination is its virtual IP or name, and that the SOCKS client uses remote name resolution (`--socks5-hostname`); in VLAN mode check the service listens on its virtual IP and the local firewall permits access |

## A Verified Linux Run

This output comes from two Linux network namespaces connected through a local
Worker, using a `/29` network with a six-member limit. One run exercised the
default proxy mode; the other used `--vlan` with real TAP interfaces and
checked ARP, ICMP, TCP, UDP, reconnect and leave. The test zones were deleted
after verification; copy your own zone's command from the website.

### VLAN Mode with Real TAP Interfaces

The two join processes reported:

```text
Joined Zone network test (9b89a4eb-8dff-4a3f-acab-2f192349d9a1, private) as 10.249.231.1 on lz0; 0 peer(s) online.
Joined Zone network test (9b89a4eb-8dff-4a3f-acab-2f192349d9a1, private) as 10.249.231.2 on lz0; 1 peer(s) online.
```

ARP resolution, ICMP at the interface MTU, a TCP transfer and a UDP echo all
passed:

```text
PING 10.249.231.2 (10.249.231.2) 1472(1500) bytes of data.
1480 bytes from 10.249.231.2: icmp_seq=1 ttl=64 time=0.938 ms
1480 bytes from 10.249.231.2: icmp_seq=2 ttl=64 time=0.775 ms
1480 bytes from 10.249.231.2: icmp_seq=3 ttl=64 time=0.687 ms

--- 10.249.231.2 ping statistics ---
3 packets transmitted, 3 received, 0% packet loss, time 2055ms
rtt min/avg/max/mdev = 0.687/0.800/0.938/0.103 ms
TCP: 262144 bytes, SHA-256 81d0141af58569f91edf2d036b67a6b82c062d1829fd93aec50ed96b4773d225
UDP: 1200-byte echo verified.
```

After an abrupt exit, the second machine rejoined with its original address. A
firewall deny rule for ICMP stopped pings until the rule was cleared. Explicit
leave then stopped the running join and released that lease:

```text
Rejoin retained 10.249.231.2.
Firewall: an ICMP deny rule stopped pings in VLAN mode.
Firewall: clearing the rule restored pings.
Left zone 9b89a4eb-8dff-4a3f-acab-2f192349d9a1.
Leave disconnected the running machine and released its IPv4 lease.
Signal-driven leave removed the final membership.
PASS: real TAP Ethernet (ARP), ICMP, TCP, UDP, reconnect and leave.
```

### Proxy Mode

Two members joined the same zone through a local Worker. Machine B served TCP
and UDP on its loopback. A SOCKS5 client on machine A reached it by virtual IP
and by member name; a deny rule blocked a new TCP connection until it was
cleared:

```text
SOCKS5 proxy listening on 127.0.0.1:20485
TCP: curl --socks5-hostname reached machine-b by virtual IP and by name.
UDP: echoed 16 bytes through SOCKS5 UDP ASSOCIATE.
Firewall: a deny rule stopped a new TCP connection through the proxy.
Firewall: clearing the rule restored access.
PASS: proxy-mode SOCKS5 TCP and UDP between two members.
```
