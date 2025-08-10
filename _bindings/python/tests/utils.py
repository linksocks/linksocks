import logging
import socket
from typing import Optional, Tuple
import random
import struct
import asyncio

logger = logging.getLogger()


def get_free_port(ipv6=False, min_port=10000, max_port=65535):
    """Get a free port for either IPv4 or IPv6 within specified range"""
    addr_family = socket.AF_INET6 if ipv6 else socket.AF_INET

    for _ in range(1000):
        port = random.randint(min_port, max_port)
        with socket.socket(addr_family, socket.SOCK_STREAM) as s:
            try:
                s.bind(("" if not ipv6 else "::", port))
                s.listen(1)
                return port
            except OSError:
                continue
    raise OSError("No free ports available in the specified range")


def assert_web_connection(
    website,
    socks_port: Optional[int] = None,
    socks_auth: Optional[Tuple[str, str]] = None,
    timeout: float = 6.0,
):
    """Helper function to test connection to the local http server with or without proxy"""
    import requests

    msg = f"Requesting web connection test for {website}"
    if socks_port:
        msg += f" using SOCKS5 at 127.0.0.1:{socks_port}"
    if socks_auth:
        msg += " with auth"
    msg += "."
    logger.info(msg)

    session = requests.Session()
    session.trust_env = False
    if socks_port:
        proxy_url = f"socks5h://127.0.0.1:{socks_port}"
        if socks_auth:
            proxy_url = (
                f"socks5h://{socks_auth[0]}:{socks_auth[1]}@127.0.0.1:{socks_port}"
            )
        proxies = {
            "http": proxy_url,
            "https": proxy_url,
        }
    else:
        proxies = None
    try:
        response = session.get(
            website,
            proxies=proxies,
            timeout=timeout,
        )
    except Exception as e:
        raise RuntimeError(
            f"Web connection test FAILED: {e.__class__.__name__}: {e}"
        ) from None
    assert response.status_code == 204


def has_ipv6_support():
    """Check if the system supports IPv6"""
    try:
        with socket.socket(socket.AF_INET6, socket.SOCK_STREAM) as s:
            s.bind(("::1", 0))
            return True
    except (socket.error, OSError):
        return False


async def async_assert_web_connection(
    website,
    socks_port: Optional[int] = None,
    socks_auth: Optional[Tuple[str, str]] = None,
    timeout: float = 6.0,
):
    """Helper function to test async connection to the local http server with or without proxy"""
    import httpx

    msg = f"Requesting web connection test for {website}"
    if socks_port:
        msg += f" using SOCKS5 at 127.0.0.1:{socks_port}"
    if socks_auth:
        msg += " with auth"
    msg += "."
    logger.info(msg)

    if socks_port:
        proxy_url = f"socks5://127.0.0.1:{socks_port}"
        if socks_auth:
            proxy_url = (
                f"socks5://{socks_auth[0]}:{socks_auth[1]}@127.0.0.1:{socks_port}"
            )
    else:
        proxy_url = None

    async with httpx.AsyncClient(proxy=proxy_url, timeout=timeout) as client:
        try:
            response = await asyncio.wait_for(client.get(website), timeout=timeout)
            assert response.status_code == 204
            return response
        except asyncio.TimeoutError:
            raise RuntimeError(
                "Web connection test FAILED: Operation timed out"
            ) from None
        except Exception as e:
            raise RuntimeError(
                f"Web connection test FAILED: {e.__class__.__name__}: {e}"
            ) from None


def parse_host_port(address_string):
    """Parse host:port string, handling IPv6 addresses with brackets"""
    if address_string.startswith('['):
        # IPv6 address with brackets: [host]:port
        bracket_end = address_string.find(']')
        if bracket_end == -1:
            raise ValueError(f"Invalid IPv6 address format: {address_string}")
        host = address_string[1:bracket_end]
        if len(address_string) > bracket_end + 1 and address_string[bracket_end + 1] == ':':
            port = int(address_string[bracket_end + 2:])
        else:
            raise ValueError(f"Invalid IPv6 address format: {address_string}")
    else:
        # IPv4 address or hostname: host:port
        if ':' not in address_string:
            raise ValueError(f"Invalid address format: {address_string}")
        host, port_str = address_string.rsplit(':', 1)
        port = int(port_str)
    return host, port


async def async_assert_udp_connection(udp_server, socks_port=None, socks_auth=None):
    """Helper function to async connect to the local udp echo server with or without proxy"""
    host, port = parse_host_port(udp_server)

    # Determine address family
    try:
        socket.inet_aton(host)
        family = socket.AF_INET
    except OSError:
        try:
            socket.inet_pton(socket.AF_INET6, host)
            family = socket.AF_INET6
        except OSError:
            # Hostname - resolve to determine family
            try:
                addr_info = socket.getaddrinfo(host, port, family=socket.AF_UNSPEC, type=socket.SOCK_DGRAM)
                if addr_info:
                    family = addr_info[0][0]  # First result's family
                else:
                    family = socket.AF_INET  # Default to IPv4
            except socket.gaierror:
                family = socket.AF_INET  # Default to IPv4 if resolution fails

    loop = asyncio.get_event_loop()

    def _recv_exact(sock_obj: socket.socket, nbytes: int) -> asyncio.Future:
        async def _inner() -> bytes:
            chunks: list[bytes] = []
            remaining = nbytes
            while remaining > 0:
                chunk = await loop.sock_recv(sock_obj, remaining)
                if not chunk:
                    raise RuntimeError("Socket closed while receiving data")
                chunks.append(chunk)
                remaining -= len(chunk)
            return b"".join(chunks)
        return _inner()

    def _parse_udp_assoc_response(head: bytes, rest: bytes) -> tuple[str, int]:
        # head: VER, REP, RSV, ATYP
        ver, rep, _rsv, atyp = head[0], head[1], head[2], head[3]
        if ver != 0x05:
            raise RuntimeError("SOCKS5 protocol error: bad version")
        if rep != 0x00:
            raise RuntimeError(f"SOCKS5 UDP associate failed with code {rep}")
        if atyp == 0x01:  # IPv4
            if len(rest) < 4 + 2:
                raise RuntimeError("Invalid UDP ASSOC reply (IPv4)")
            addr = socket.inet_ntop(socket.AF_INET, rest[:4])
            port = struct.unpack("!H", rest[4:6])[0]
            return addr, port
        elif atyp == 0x04:  # IPv6
            if len(rest) < 16 + 2:
                raise RuntimeError("Invalid UDP ASSOC reply (IPv6)")
            addr = socket.inet_ntop(socket.AF_INET6, rest[:16])
            port = struct.unpack("!H", rest[16:18])[0]
            return addr, port
        elif atyp == 0x03:  # Domain
            if not rest:
                raise RuntimeError("Invalid UDP ASSOC reply (DOMAIN)")
            ln = rest[0]
            need = 1 + ln + 2
            if len(rest) < need:
                raise RuntimeError("Invalid UDP ASSOC reply (DOMAIN length)")
            host = rest[1 : 1 + ln].decode()
            port = struct.unpack("!H", rest[1 + ln : 1 + ln + 2])[0]
            return host, port
        else:
            raise RuntimeError(f"Unsupported ATYP in reply: {atyp}")

    def _socks5_udp_header_length(packet: bytes) -> int:
        if len(packet) < 4:
            return 0
        atyp = packet[3]
        if atyp == 0x01:
            return 10  # 2 RSV + 1 FRAG + 1 ATYP + 4 ADDR + 2 PORT
        if atyp == 0x04:
            return 22  # 2 + 1 + 1 + 16 + 2
        if atyp == 0x03:
            if len(packet) < 5:
                return 0
            ln = packet[4]
            return 7 + ln  # 2 + 1 + 1 + 1(len) + ln + 2
        return 0

    relay_host: Optional[str] = None
    relay_port: Optional[int] = None
    relay_family = family

    if socks_port:
        # Create TCP socket for SOCKS5 negotiation (IPv4 localhost)
        tcp_sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        tcp_sock.setblocking(False)

        try:
            await loop.sock_connect(tcp_sock, ("127.0.0.1", socks_port))

            # SOCKS5 handshake
            if socks_auth:
                await loop.sock_sendall(tcp_sock, b"\x05\x02\x00\x02")
            else:
                await loop.sock_sendall(tcp_sock, b"\x05\x01\x00")

            auth_resp = await _recv_exact(tcp_sock, 2)
            if auth_resp[0] != 0x05:
                raise RuntimeError("SOCKS5 protocol error")

            if auth_resp[1] == 0x02 and socks_auth:
                username, password = socks_auth
                auth_msg = struct.pack(
                    "!B%dsB%ds" % (len(username), len(password)),
                    1,
                    username.encode(),
                    len(password),
                    password.encode(),
                )
                await loop.sock_sendall(tcp_sock, auth_msg)
                auth_resp = await _recv_exact(tcp_sock, 2)
                if auth_resp[1] != 0:
                    raise RuntimeError("SOCKS5 authentication failed")

            # UDP associate request (bind addr 0.0.0.0:0)
            udp_req = struct.pack("!BBBBIH", 0x05, 0x03, 0x00, 0x01, 0, 0)
            await loop.sock_sendall(tcp_sock, udp_req)

            # Parse variable-length reply
            head = await _recv_exact(tcp_sock, 4)
            atyp = head[3]
            if atyp == 0x01:
                rest = await _recv_exact(tcp_sock, 4 + 2)
            elif atyp == 0x04:
                rest = await _recv_exact(tcp_sock, 16 + 2)
            elif atyp == 0x03:
                ln_b = await _recv_exact(tcp_sock, 1)
                ln = ln_b[0]
                rest = ln_b + await _recv_exact(tcp_sock, ln + 2)
            else:
                raise RuntimeError(f"Unsupported ATYP in reply: {atyp}")

            assoc_host, assoc_port = _parse_udp_assoc_response(head, rest)

            # Determine relay address: prefer TCP peer (proxy address)
            peer_addr = tcp_sock.getpeername()[0]
            relay_host = peer_addr
            relay_port = assoc_port
            relay_family = tcp_sock.family

            # Create UDP socket matching relay address family
            sock = socket.socket(relay_family, socket.SOCK_DGRAM)
            sock.setblocking(False)
        except Exception as e:
            tcp_sock.close()
            raise RuntimeError(f"Failed to setup SOCKS5 UDP associate: {e}") from None
    else:
        tcp_sock = None
        sock = socket.socket(family, socket.SOCK_DGRAM)
        sock.setblocking(False)

    class UDPProtocol(asyncio.DatagramProtocol):
        def __init__(
            self,
        ):
            self.received_data = asyncio.Queue()

        def datagram_received(self, data, addr):
            if socks_port:
                header_len = _socks5_udp_header_length(data)
                if header_len > 0 and len(data) >= header_len:
                    data = data[header_len:]
            self.received_data.put_nowait(data)

    try:
        test_data = b"Hello UDP"
        success_count = 0
        total_attempts = 10

        # Create UDP endpoint
        transport, protocol = await loop.create_datagram_endpoint(
            UDPProtocol, sock=sock
        )

        try:
            for _ in range(total_attempts):
                try:
                    if socks_port:
                        # Build SOCKS5 UDP request header for IPv4 or IPv6
                        # For SOCKS5, we need to resolve hostnames to IP addresses
                        resolved_host = host
                        try:
                            # Try IPv4 address first
                            socket.inet_aton(host)
                            # IPv4 address
                            udp_header = struct.pack(
                                "!BBBB4sH",
                                0,  # RSV
                                0,  # FRAG
                                0,  # Reserved
                                0x01,  # ATYP (IPv4)
                                socket.inet_aton(host),  # IPv4 address
                                port,  # Port
                            )
                        except OSError:
                            try:
                                # Try IPv6 address
                                ipv6_bytes = socket.inet_pton(socket.AF_INET6, host)
                                udp_header = struct.pack(
                                    "!BBBB16sH",
                                    0,  # RSV
                                    0,  # FRAG
                                    0,  # Reserved
                                    0x04,  # ATYP (IPv6)
                                    ipv6_bytes,  # IPv6 address (16 bytes)
                                    port,  # Port
                                )
                            except OSError:
                                # Hostname - resolve it to IP
                                try:
                                    addr_info = socket.getaddrinfo(host, port, family=family, type=socket.SOCK_DGRAM)
                                    if addr_info:
                                        resolved_host = addr_info[0][4][0]  # Extract IP address
                                        if family == socket.AF_INET:
                                            udp_header = struct.pack(
                                                "!BBBB4sH",
                                                0,  # RSV
                                                0,  # FRAG
                                                0,  # Reserved
                                                0x01,  # ATYP (IPv4)
                                                socket.inet_aton(resolved_host),  # IPv4 address
                                                port,  # Port
                                            )
                                        else:  # IPv6
                                            ipv6_bytes = socket.inet_pton(socket.AF_INET6, resolved_host)
                                            udp_header = struct.pack(
                                                "!BBBB16sH",
                                                0,  # RSV
                                                0,  # FRAG
                                                0,  # Reserved
                                                0x04,  # ATYP (IPv6)
                                                ipv6_bytes,  # IPv6 address (16 bytes)
                                                port,  # Port
                                            )
                                    else:
                                        raise ValueError(f"Could not resolve hostname: {host}")
                                except socket.gaierror as e:
                                    raise ValueError(f"Could not resolve hostname {host}: {e}")
                        
                        # Send to UDP relay (match family of TCP peer)
                        target_host = relay_host if relay_host is not None else "127.0.0.1"
                        target_port = relay_port if relay_port is not None else 0
                        transport.sendto(udp_header + test_data, (target_host, target_port))
                    else:
                        transport.sendto(test_data, (host, port))

                    data = await asyncio.wait_for(
                        protocol.received_data.get(), timeout=0.5
                    )
                    if data == test_data:
                        success_count += 1
                except asyncio.TimeoutError:
                    continue

            if success_count < total_attempts / 2:
                raise AssertionError(
                    f"UDP connection test failed: only {success_count}/{total_attempts} "
                    f"packets were successfully echoed"
                )
        finally:
            transport.close()
    finally:
        sock.close()
        if tcp_sock:
            tcp_sock.close()


def assert_udp_connection(udp_server, socks_port=None, socks_auth=None):
    """Helper function to connect to the local udp echo server with or without proxy"""
    import socks

    host, port = parse_host_port(udp_server)

    # Determine address family
    try:
        socket.inet_aton(host)
        family = socket.AF_INET
    except OSError:
        try:
            socket.inet_pton(socket.AF_INET6, host)
            family = socket.AF_INET6
        except OSError:
            # Hostname - resolve to determine family
            try:
                addr_info = socket.getaddrinfo(host, port, family=socket.AF_UNSPEC, type=socket.SOCK_DGRAM)
                if addr_info:
                    family = addr_info[0][0]  # First result's family
                else:
                    family = socket.AF_INET  # Default to IPv4
            except socket.gaierror:
                family = socket.AF_INET  # Default to IPv4 if resolution fails

    if socks_port:
        sock = socks.socksocket(family, socket.SOCK_DGRAM)
        if socks_auth:
            sock.set_proxy(
                socks.SOCKS5,
                "127.0.0.1",
                socks_port,
                username=socks_auth[0],
                password=socks_auth[1],
            )
        else:
            sock.set_proxy(socks.SOCKS5, "127.0.0.1", socks_port)
    else:
        sock = socket.socket(family, socket.SOCK_DGRAM)

    try:
        test_data = b"Hello UDP"
        success_count = 0
        total_attempts = 10
        sock.settimeout(1)

        for i in range(total_attempts):
            try:
                sock.sendto(test_data, (host, port))
                data, _ = sock.recvfrom(1024)
                if data == test_data:
                    success_count += 1
            except socket.timeout:
                continue

        if success_count < total_attempts / 2:
            raise AssertionError(
                f"UDP connection test failed: only {success_count}/{total_attempts} "
                f"packets were successfully echoed"
            )
    finally:
        sock.close()


def show_logs_on_failure(func):
    def wrapper(*args, **kwargs):
        caplog = kwargs.get("caplog", None)
        if caplog:
            caplog.set_level(logging.DEBUG)
        try:
            return func(*args, **kwargs)
        except Exception:
            if caplog:
                print("\nTest logs:")
                print(caplog.text)
            raise

    return wrapper