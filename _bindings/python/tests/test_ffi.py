import asyncio
import contextlib
import importlib.util
import logging
from pathlib import Path
from typing import Optional

import pytest

from .utils import (
    async_assert_udp_connection,
    async_assert_web_connection,
    assert_udp_connection,
    assert_web_connection,
    get_free_port,
    has_ipv6_support,
)


test_logger = logging.getLogger(__name__)
start_time_limit = 60


def _ffi_artifact_present() -> bool:
    spec = importlib.util.find_spec("linksocks_ffi")
    if spec is None or not spec.origin:
        return False
    ffi_dir = Path(spec.origin).resolve().parent
    if not ffi_dir.exists():
        return False
    return any(
        p.is_file()
        and p.suffix in {".so", ".dylib", ".dll"}
        and "linksocks_ffi" in p.name
        for p in ffi_dir.iterdir()
    )


_ffi_spec = importlib.util.find_spec("linksocks_ffi")
_ffi_available = (_ffi_spec is not None) and _ffi_artifact_present()

pytestmark = []
if not _ffi_available:
    pytestmark = [
        pytest.mark.skip(
            reason="linksocks_ffi (ffi backend) is not available; skip ffi-only tests",
        ),
    ]
else:
    try:
        import linksocks_ffi as _ffi
    except Exception as e:
        raise RuntimeError(
            "linksocks_ffi artifact is present but import failed; this indicates a broken build"
        ) from e

def _ns(seconds: float) -> int:
    return int(seconds * 1_000_000_000)


@contextlib.asynccontextmanager
async def forward_server(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
):
    ws_port = ws_port or get_free_port()

    server = None
    try:
        server = _ffi.Server({"ws_port": int(ws_port)})
        server.add_forward_token(token or "")
        await asyncio.to_thread(server.wait_ready, _ns(start_time_limit))
        test_logger.info(f"Created forward server on port {ws_port} with token {token}")
    except Exception as e:
        test_logger.error(f"Failed to create forward server: {e}")
        if server:
            server.close()
        raise

    try:
        yield server, ws_port, token
    finally:
        server.close()


@contextlib.asynccontextmanager
async def forward_client(ws_port: int, token: str):
    socks_port = get_free_port()

    client = None
    try:
        client = _ffi.Client(
            token,
            {
                "ws_url": f"ws://localhost:{ws_port}",
                "socks_port": int(socks_port),
                "reconnect_delay_ns": _ns(1),
                "no_env_proxy": True,
            },
        )
        await asyncio.to_thread(client.wait_ready, _ns(start_time_limit))
        test_logger.info(f"Created forward client connecting to ws://localhost:{ws_port}")
    except Exception as e:
        test_logger.error(f"Failed to create forward client: {e}")
        if client:
            client.close()
        raise

    try:
        yield client, socks_port
    finally:
        client.close()


@contextlib.asynccontextmanager
async def forward_proxy(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
):
    async with forward_server(token=token, ws_port=ws_port) as (server, ws_port, token):
        async with forward_client(ws_port, token) as (client, socks_port):
            yield server, client, socks_port


@contextlib.asynccontextmanager
async def reverse_server(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
):
    ws_port = ws_port or get_free_port()

    server = None
    try:
        server = _ffi.Server({"ws_port": int(ws_port)})
        res = server.add_reverse_token({"token": token or ""})
        socks_port = int(res.port)
        await asyncio.to_thread(server.wait_ready, _ns(start_time_limit))
        test_logger.info(f"Created reverse server on ws_port={ws_port}, socks_port={socks_port}")
    except Exception as e:
        test_logger.error(f"Failed to create reverse server: {e}")
        if server:
            server.close()
        raise

    try:
        yield server, ws_port, token, socks_port
    finally:
        server.close()


@contextlib.asynccontextmanager
async def reverse_client(ws_port: int, token: str):
    client = None
    try:
        client = _ffi.Client(
            token,
            {
                "ws_url": f"ws://localhost:{ws_port}",
                "reverse": True,
                "reconnect_delay_ns": _ns(1),
                "no_env_proxy": True,
            },
        )
        await asyncio.to_thread(client.wait_ready, _ns(start_time_limit))
    except Exception as e:
        test_logger.error(f"Failed to create reverse client: {e}")
        if client:
            client.close()
        raise

    try:
        yield client
    finally:
        client.close()


@contextlib.asynccontextmanager
async def reverse_proxy(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
):
    async with reverse_server(token=token, ws_port=ws_port) as (server, ws_port, token, socks_port):
        async with reverse_client(ws_port, token) as client:
            yield server, client, socks_port


# ==================== Basic Tests ====================


def test_import():
    assert hasattr(_ffi, "Client")
    assert hasattr(_ffi, "Server")


def test_website(website):
    assert_web_connection(website)


def test_website_async_tester(website):
    async def _main():
        await async_assert_web_connection(website)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_website_ipv6(website_v6):
    assert_web_connection(website_v6)


def test_udp_server(udp_server):
    assert_udp_connection(udp_server)


def test_udp_server_async_tester(udp_server):
    async def _main():
        await async_assert_udp_connection(udp_server)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_forward_basic(website):
    async def _main():
        async with forward_proxy() as (_server, _client, socks_port):
            await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_basic(website):
    async def _main():
        async with reverse_proxy() as (_server, _client, socks_port):
            await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Token removal ====================


def test_forward_remove_token(website):
    async def _main():
        ws_port = get_free_port()
        async with forward_server(ws_port=ws_port) as (srv, _ws_port, token):
            async with forward_client(ws_port, token) as (_cli, socks_port):
                await async_assert_web_connection(website, socks_port)
                srv.remove_token(token)
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_remove_token(website):
    async def _main():
        ws_port = get_free_port()
        async with reverse_server(ws_port=ws_port) as (srv, _ws_port, token, socks_port):
            async with reverse_client(ws_port, token) as _cli:
                await async_assert_web_connection(website, socks_port)
                srv.remove_token(token)
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== IPv6 ====================


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_forward_ipv6(website_v6):
    async def _main():
        ws_port = get_free_port()
        async with forward_server(ws_port=ws_port) as (_srv, _ws_port, token):
            async with forward_client(ws_port, token) as (_cli, socks_port):
                await async_assert_web_connection(website_v6, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_reverse_ipv6(website_v6):
    async def _main():
        async with reverse_proxy() as (_srv, _cli, socks_port):
            await async_assert_web_connection(website_v6, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== UDP ====================


def test_udp_forward_proxy(udp_server):
    async def _main():
        ws_port = get_free_port()
        async with forward_server(ws_port=ws_port) as (_srv, _ws_port, token):
            async with forward_client(ws_port, token) as (_cli, socks_port):
                await async_assert_udp_connection(udp_server, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_reverse_proxy(udp_server):
    async def _main():
        async with reverse_proxy() as (_srv, _cli, socks_port):
            await async_assert_udp_connection(udp_server, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_forward_proxy_v6(udp_server_v6):
    async def _main():
        ws_port = get_free_port()
        async with forward_server(ws_port=ws_port) as (_srv, _ws_port, token):
            async with forward_client(ws_port, token) as (_cli, socks_port):
                await async_assert_udp_connection(udp_server_v6, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_reverse_proxy_v6(udp_server_v6):
    async def _main():
        async with reverse_proxy() as (_srv, _cli, socks_port):
            await async_assert_udp_connection(udp_server_v6, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_forward_proxy_domain(udp_server_domain):
    async def _main():
        ws_port = get_free_port()
        async with forward_server(ws_port=ws_port) as (_srv, _ws_port, token):
            async with forward_client(ws_port, token) as (_cli, socks_port):
                await async_assert_udp_connection(udp_server_domain, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_reverse_proxy_domain(udp_server_domain):
    async def _main():
        async with reverse_proxy() as (_srv, _cli, socks_port):
            await async_assert_udp_connection(udp_server_domain, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Connector ====================


@contextlib.asynccontextmanager
async def reverse_server_with_connector(
    token: Optional[str] = "<token>",
    connector_token: str = "CONNECTOR",
    ws_port: Optional[int] = None,
):
    ws_port = ws_port or get_free_port()

    server = None
    try:
        server = _ffi.Server({"ws_port": int(ws_port)})
        res = server.add_reverse_token({"token": token or ""})
        socks_port = int(res.port)
        server.add_connector_token(connector_token, res.token)
        await asyncio.to_thread(server.wait_ready, _ns(start_time_limit))
    except Exception as e:
        test_logger.error(f"Failed to create reverse server with connector: {e}")
        if server:
            server.close()
        raise

    try:
        yield server, ws_port, res.token, socks_port, connector_token
    finally:
        server.close()


def test_connector(website):
    async def _main():
        async with reverse_server_with_connector() as (_srv, ws_port, token, socks_port, connector_token):
            async with reverse_client(ws_port, token) as rcli:
                _ = rcli
                async with forward_client(ws_port, connector_token) as (_fcli, forward_socks_port):
                    await async_assert_web_connection(website, socks_port)
                    await async_assert_web_connection(website, forward_socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_connector_autonomy(website):
    async def _main():
        ws_port = get_free_port()
        srv = _ffi.Server({"ws_port": int(ws_port)})
        try:
            res = srv.add_reverse_token({"token": "<token>", "allow_manage_connector": True})
            socks_port = int(res.port)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            # Reverse client: can create connector token.
            rcli = _ffi.Client(
                res.token,
                {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "no_env_proxy": True},
            )
            try:
                await asyncio.to_thread(rcli.wait_ready, _ns(start_time_limit))
                connector_token = await asyncio.to_thread(rcli.add_connector, "CONNECTOR")
                assert connector_token

                # Forward client: uses connector token and must work.
                forward_socks_port = get_free_port()
                fcli = _ffi.Client(
                    "CONNECTOR",
                    {
                        "ws_url": f"ws://localhost:{ws_port}",
                        "socks_port": int(forward_socks_port),
                        "no_env_proxy": True,
                    },
                )
                try:
                    await asyncio.to_thread(fcli.wait_ready, _ns(start_time_limit))

                    # Reverse proxy should fail (autonomy enabled; server does not manage connector)
                    with pytest.raises(Exception):
                        await async_assert_web_connection(website, socks_port, timeout=3)

                    await async_assert_web_connection(website, forward_socks_port)
                finally:
                    fcli.close()
            finally:
                rcli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Proxy auth ====================


def test_proxy_auth(website):
    async def _main():
        username = "test_user"
        password = "test_pass"
        ws_port = get_free_port()

        srv = _ffi.Server({"ws_port": int(ws_port)})
        try:
            res = srv.add_reverse_token({"token": "<token>", "username": username, "password": password})
            socks_port = int(res.port)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            rcli = _ffi.Client(res.token, {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "no_env_proxy": True})
            try:
                await asyncio.to_thread(rcli.wait_ready, _ns(start_time_limit))

                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

                await async_assert_web_connection(website, socks_port, socks_auth=(username, password))
            finally:
                rcli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Reconnect ====================


def test_forward_reconnect(website):
    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()

        srv1 = _ffi.Server({"ws_port": int(ws_port)})
        try:
            token = srv1.add_forward_token("")
            await asyncio.to_thread(srv1.wait_ready, _ns(start_time_limit))

            cli = _ffi.Client(
                token,
                {
                    "ws_url": f"ws://localhost:{ws_port}",
                    "socks_port": int(socks_port),
                    "reconnect": True,
                    "reconnect_delay_ns": _ns(1),
                    "no_env_proxy": True,
                },
            )
            try:
                await asyncio.to_thread(cli.wait_ready, _ns(start_time_limit))
                await async_assert_web_connection(website, socks_port)

                srv1.close()
                await asyncio.sleep(2)

                srv2 = _ffi.Server({"ws_port": int(ws_port)})
                try:
                    srv2.add_forward_token(token)
                    await asyncio.to_thread(srv2.wait_ready, _ns(start_time_limit))
                    await asyncio.sleep(3)
                    await async_assert_web_connection(website, socks_port)
                finally:
                    srv2.close()
            finally:
                cli.close()
        finally:
            try:
                srv1.close()
            except Exception:
                pass

    return asyncio.run(asyncio.wait_for(_main(), 45))


def test_reverse_reconnect(website):
    async def _main():
        ws_port = get_free_port()
        srv = _ffi.Server({"ws_port": int(ws_port)})
        try:
            res = srv.add_reverse_token({"token": "<token>"})
            socks_port = int(res.port)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            cli1 = _ffi.Client(res.token, {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "no_env_proxy": True})
            try:
                await asyncio.to_thread(cli1.wait_ready, _ns(start_time_limit))
                await async_assert_web_connection(website, socks_port)
            finally:
                cli1.close()

            await asyncio.sleep(2)

            cli2 = _ffi.Client(res.token, {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "no_env_proxy": True})
            try:
                await asyncio.to_thread(cli2.wait_ready, _ns(start_time_limit))
                await asyncio.sleep(2)
                await async_assert_web_connection(website, socks_port)
            finally:
                cli2.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 45))


# ==================== Threads ====================


def test_client_thread(website):
    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()

        srv = _ffi.Server({"ws_port": int(ws_port)})
        try:
            token = srv.add_forward_token("")
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            cli = _ffi.Client(
                token,
                {
                    "ws_url": f"ws://localhost:{ws_port}",
                    "socks_port": int(socks_port),
                    "threads": 2,
                    "reconnect_delay_ns": _ns(1),
                    "no_env_proxy": True,
                },
            )
            try:
                await asyncio.to_thread(cli.wait_ready, _ns(start_time_limit))
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)
            finally:
                cli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Fast Open ====================


def test_fast_open_forward(website):
    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()

        srv = _ffi.Server({"ws_port": int(ws_port), "fast_open": True})
        try:
            token = srv.add_forward_token("")
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            cli = _ffi.Client(
                token,
                {
                    "ws_url": f"ws://localhost:{ws_port}",
                    "socks_port": int(socks_port),
                    "fast_open": True,
                    "no_env_proxy": True,
                },
            )
            try:
                await asyncio.to_thread(cli.wait_ready, _ns(start_time_limit))
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)
            finally:
                cli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_reverse(website):
    async def _main():
        ws_port = get_free_port()
        srv = _ffi.Server({"ws_port": int(ws_port), "fast_open": True})
        try:
            res = srv.add_reverse_token({"token": "<token>"})
            socks_port = int(res.port)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            cli = _ffi.Client(
                res.token,
                {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "fast_open": True, "no_env_proxy": True},
            )
            try:
                await asyncio.to_thread(cli.wait_ready, _ns(start_time_limit))
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)
            finally:
                cli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_connector(website):
    async def _main():
        ws_port = get_free_port()
        srv = _ffi.Server({"ws_port": int(ws_port), "fast_open": True})
        try:
            res = srv.add_reverse_token({"token": "<token>"})
            socks_port = int(res.port)
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            rcli = _ffi.Client(
                res.token,
                {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "fast_open": True, "no_env_proxy": True},
            )
            fcli_socks_port = get_free_port()
            fcli = _ffi.Client(
                connector,
                {"ws_url": f"ws://localhost:{ws_port}", "socks_port": int(fcli_socks_port), "fast_open": True, "no_env_proxy": True},
            )
            try:
                await asyncio.to_thread(rcli.wait_ready, _ns(start_time_limit))
                await asyncio.to_thread(fcli.wait_ready, _ns(start_time_limit))
                await async_assert_web_connection(website, socks_port)
                await async_assert_web_connection(website, fcli_socks_port)
            finally:
                fcli.close()
                rcli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Mixed Fast Open ====================


def test_mixed_fast_open_connector_all_fast_open(website):
    async def _main():
        ws_port = get_free_port()
        srv = _ffi.Server({"ws_port": int(ws_port), "fast_open": True})
        try:
            res = srv.add_reverse_token({"token": "<token>"})
            socks_port = int(res.port)
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            rcli = _ffi.Client(res.token, {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "fast_open": True, "no_env_proxy": True})
            fcli_socks_port = get_free_port()
            fcli = _ffi.Client(connector, {"ws_url": f"ws://localhost:{ws_port}", "socks_port": int(fcli_socks_port), "fast_open": True, "no_env_proxy": True})
            try:
                await asyncio.to_thread(rcli.wait_ready, _ns(start_time_limit))
                await asyncio.to_thread(fcli.wait_ready, _ns(start_time_limit))
                await async_assert_web_connection(website, socks_port)
                await async_assert_web_connection(website, fcli_socks_port)
            finally:
                fcli.close()
                rcli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_server_fast_open(website):
    async def _main():
        ws_port = get_free_port()
        srv = _ffi.Server({"ws_port": int(ws_port), "fast_open": True})
        try:
            res = srv.add_reverse_token({"token": "<token>"})
            socks_port = int(res.port)
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            rcli = _ffi.Client(res.token, {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "no_env_proxy": True})
            fcli_socks_port = get_free_port()
            fcli = _ffi.Client(connector, {"ws_url": f"ws://localhost:{ws_port}", "socks_port": int(fcli_socks_port), "fast_open": True, "no_env_proxy": True})
            try:
                await asyncio.to_thread(rcli.wait_ready, _ns(start_time_limit))
                await asyncio.to_thread(fcli.wait_ready, _ns(start_time_limit))
                await async_assert_web_connection(website, socks_port)
                await async_assert_web_connection(website, fcli_socks_port)
            finally:
                fcli.close()
                rcli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_connector_fast_open(website):
    async def _main():
        ws_port = get_free_port()
        srv = _ffi.Server({"ws_port": int(ws_port)})
        try:
            res = srv.add_reverse_token({"token": "<token>"})
            socks_port = int(res.port)
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            rcli = _ffi.Client(res.token, {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "no_env_proxy": True})
            fcli_socks_port = get_free_port()
            fcli = _ffi.Client(connector, {"ws_url": f"ws://localhost:{ws_port}", "socks_port": int(fcli_socks_port), "fast_open": True, "no_env_proxy": True})
            try:
                await asyncio.to_thread(rcli.wait_ready, _ns(start_time_limit))
                await asyncio.to_thread(fcli.wait_ready, _ns(start_time_limit))
                await async_assert_web_connection(website, socks_port)
                await async_assert_web_connection(website, fcli_socks_port)
            finally:
                fcli.close()
                rcli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_client_fast_open(website):
    async def _main():
        ws_port = get_free_port()
        srv = _ffi.Server({"ws_port": int(ws_port)})
        try:
            res = srv.add_reverse_token({"token": "<token>"})
            socks_port = int(res.port)
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            await asyncio.to_thread(srv.wait_ready, _ns(start_time_limit))

            rcli = _ffi.Client(res.token, {"ws_url": f"ws://localhost:{ws_port}", "reverse": True, "fast_open": True, "no_env_proxy": True})
            fcli_socks_port = get_free_port()
            fcli = _ffi.Client(connector, {"ws_url": f"ws://localhost:{ws_port}", "socks_port": int(fcli_socks_port), "no_env_proxy": True})
            try:
                await asyncio.to_thread(rcli.wait_ready, _ns(start_time_limit))
                await asyncio.to_thread(fcli.wait_ready, _ns(start_time_limit))
                await async_assert_web_connection(website, socks_port)
                await async_assert_web_connection(website, fcli_socks_port)
            finally:
                fcli.close()
                rcli.close()
        finally:
            srv.close()

    return asyncio.run(asyncio.wait_for(_main(), 30))
