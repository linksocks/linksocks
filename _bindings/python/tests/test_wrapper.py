import asyncio
import time
import logging
import pytest

from .utils import (
    get_free_port,
    has_ipv6_support,
    async_assert_udp_connection,
    async_assert_web_connection,
)


start_time_limit = 60


def _ws_url(port: int) -> str:
    return f"ws://localhost:{port}"


# ==================== Basic ====================


def test_import_wrapper():
    from linksocks import Server, Client

    assert Server is not None and Client is not None


def test_forward_basic(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_basic(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Token removal ====================


def test_forward_remove_token(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_web_connection(website, socks_port)
                srv.remove_token(token)
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_remove_token(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_web_connection(website, res.port)
                srv.remove_token(res.token)
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, res.port, timeout=3)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== IPv6 ====================


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_forward_ipv6(website_v6):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_web_connection(website_v6, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_reverse_ipv6(website_v6):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_web_connection(website_v6, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== UDP ====================


def test_udp_forward_proxy(udp_server):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_udp_connection(udp_server, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_reverse_proxy(udp_server):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_udp_connection(udp_server, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_forward_proxy_v6(udp_server_v6):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_udp_connection(udp_server_v6, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_reverse_proxy_v6(udp_server_v6):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_udp_connection(udp_server_v6, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Reconnect ====================


def test_forward_reconnect(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()

        # first server
        async with Server(ws_port=ws_port) as srv1:
            token = await srv1.async_add_forward_token()
            async with Client(
                token,
                ws_url=_ws_url(ws_port),
                socks_port=socks_port,
                reconnect=True,
                reconnect_delay=1,
                no_env_proxy=True,
            ) as cli:
                await async_assert_web_connection(website, socks_port)
                # stop server
                await srv1.async_close()
                await asyncio.sleep(2)
                # start new server same port
                async with Server(ws_port=ws_port) as srv2:
                    await srv2.async_add_forward_token(token)
                    await asyncio.sleep(3)
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 45))


def test_reverse_reconnect(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            # first client
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli1:
                await async_assert_web_connection(website, res.port)

            await asyncio.sleep(2)
            # second client
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli2:
                await asyncio.sleep(2)
                await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 45))


# ==================== Connector ====================


def test_connector(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            # add connector token
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)

            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as rcli:
                async with Client(connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), no_env_proxy=True) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_connector_autonomy(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token(allow_manage_connector=True)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as rcli:
                # client creates connector
                token = await rcli.async_add_connector("CONNECTOR")
                assert token
                # Forward client using connector
                async with Client("CONNECTOR", ws_url=_ws_url(ws_port), socks_port=get_free_port(), no_env_proxy=True) as fcli:
                    # reverse should fail due to autonomy
                    with pytest.raises(Exception):
                        await async_assert_web_connection(website, res.port, timeout=3)
                    # forward works
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Threads ====================


def test_client_threads(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, threads=2, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Fast Open ====================


def test_fast_open_forward(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, fast_open=True, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_reverse(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_forward(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, fast_open=True, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_reverse(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_connector(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), fast_open=True, no_env_proxy=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Mixed Fast Open ====================


def test_mixed_fast_open_connector_all_fast_open(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), fast_open=True, no_env_proxy=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_server_fast_open(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), fast_open=True, no_env_proxy=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_connector_fast_open(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as rcli:
                await rcli.async_wait_ready(timeout=start_time_limit)
                with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), fast_open=True, no_env_proxy=True
                ) as fcli:
                    await fcli.async_wait_ready(timeout=start_time_limit)
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_client_fast_open(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as rcli:
                async with Client(connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), no_env_proxy=True) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))