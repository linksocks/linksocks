import asyncio
import contextlib
from typing import Optional, Iterable
import logging
import pytest

from .utils import *

test_logger = logging.getLogger(__name__)

start_time_limit = 60


@contextlib.asynccontextmanager
async def forward_server(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
):
    """Create a forward server using the Go bindings"""
    from linksockslib import linksocks

    ws_port = ws_port or get_free_port()
    assert ws_port

    server = None
    try:
        # Create server using Go bindings - all initialization steps in try block
        server_opt = linksocks.DefaultServerOption()
        server_opt.WithWSPort(ws_port)
        server = linksocks.NewLinkSocksServer(server_opt)
        test_logger.info(f"Created forward server on port {ws_port} with token {token}")
        
        # Add forward token to server
        server.AddForwardToken(token)
        await asyncio.to_thread(
            server.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create forward server: {e}")
        if server:
            server.Close()
        raise
    
    try:
        yield server, ws_port, token
    finally:
        server.Close()


@contextlib.asynccontextmanager
async def forward_client(ws_port: int, token: str):
    """Create a forward client using the Go bindings"""
    from linksockslib import linksocks

    socks_port = get_free_port()
    assert socks_port

    client = None
    try:
        # Create client using Go bindings - all initialization steps in try block
        client_opt = linksocks.DefaultClientOption()
        client_opt.WithWSURL(f"ws://localhost:{ws_port}")
        client_opt.WithSocksPort(socks_port)
        client_opt.WithReconnectDelay(1 * linksocks.Second())
        client_opt.WithNoEnvProxy(True)
        client = linksocks.NewLinkSocksClient(token, client_opt)
        
        ctx = linksocks.NewContext()
        await asyncio.to_thread(
            client.WaitReady, ctx=ctx, timeout=start_time_limit * linksocks.Second()
        )
        test_logger.info(f"Created forward client connecting to ws://localhost:{ws_port}")
    except Exception as e:
        test_logger.error(f"Failed to create forward client: {e}")
        if client:
            client.Close()
        raise
    
    # yield outside initialization try block so user code exceptions are not caught
    try:
        yield client, socks_port
    finally:
        client.Close()


@contextlib.asynccontextmanager
async def forward_proxy(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
):
    """Create forward proxy (server + client) using Go bindings"""
    async with forward_server(token=token, ws_port=ws_port) as (server, ws_port, token):
        async with forward_client(ws_port, token) as (client, socks_port):
            yield server, client, socks_port


@contextlib.asynccontextmanager
async def reverse_server(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
):
    """Create a reverse server using the Go bindings"""
    from linksockslib import linksocks

    ws_port = ws_port or get_free_port()

    server = None
    try:
        # Create server - all initialization steps in try block
        server_opt = linksocks.DefaultServerOption()
        server_opt.WithWSPort(ws_port)
        server = linksocks.NewLinkSocksServer(server_opt)

        # Add reverse token to server
        reverse_opts = linksocks.DefaultReverseTokenOptions()
        reverse_opts.Token = token
        result: linksocks.ReverseTokenResult = server.AddReverseToken(reverse_opts)
        socks_port = result.Port
        test_logger.info(f"Created reverse server on ws_port={ws_port}, socks_port={socks_port}")
        ctx = linksocks.NewContext()
        await asyncio.to_thread(
            server.WaitReady, ctx=ctx, timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create reverse server: {e}")
        if server:
            server.Close()
        raise
    
    try:
        yield server, ws_port, token, socks_port
    finally:
        server.Close()


@contextlib.asynccontextmanager
async def reverse_client(ws_port: int, token: str):
    """Create a reverse client using the Go bindings"""
    from linksockslib import linksocks

    client = None
    try:
        # Create client - all initialization steps in try block
        client_opt = linksocks.DefaultClientOption()
        client_opt.WithWSURL(f"ws://localhost:{ws_port}")
        client_opt.WithReconnectDelay(1 * linksocks.Second())
        client_opt.WithReverse(True)
        client_opt.WithNoEnvProxy(True)
        client = linksocks.NewLinkSocksClient(token, client_opt)
        
        ctx = linksocks.NewContext()
        await asyncio.to_thread(
            client.WaitReady, ctx=ctx, timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create reverse client: {e}")
        if client:
            client.Close()
        raise
    
    try:
        yield client
    finally:
        client.Close()


@contextlib.asynccontextmanager
async def reverse_proxy(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
):
    """Create reverse proxy (server + client) using Go bindings"""
    async with reverse_server(token=token, ws_port=ws_port) as (server, ws_port, token, socks_port):
        async with reverse_client(ws_port, token) as client:
            yield server, client, socks_port


# ==================== Basic Tests ====================


def test_import():
    """Test importing the Go linksocks bindings"""
    from linksockslib import linksocks

    assert hasattr(linksocks, "NewLinkSocksClient")

def test_website(website):
    """Test direct website access"""
    assert_web_connection(website)


def test_website_async_tester(website):
    """Test async website access"""

    async def _main():
        await async_assert_web_connection(website)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_website_ipv6(website_v6):
    """Test IPv6 website access"""
    assert_web_connection(website_v6)


def test_udp_server(udp_server):
    """Test UDP server access"""
    assert_udp_connection(udp_server)


def test_udp_server_async_tester(udp_server):
    """Test async UDP server access"""

    async def _main():
        await async_assert_udp_connection(udp_server)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_forward_basic(caplog, website):
    """Test basic forward proxy functionality"""

    async def _main():
        async with forward_proxy() as (server, client, socks_port):
            await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_basic(caplog, website):
    """Test basic reverse proxy functionality"""

    async def _main():
        async with reverse_proxy() as (server, client, socks_port):
            await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_forward_remove_token(caplog, website):
    """Test forward proxy token removal"""

    async def _main():
        async with forward_server() as (server, ws_port, token):
            async with forward_client(ws_port, token) as (client, socks_port):
                await async_assert_web_connection(website, socks_port)

                # Remove token via Go bindings
                server.RemoveToken(token)

                # Connection should fail after token removal
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_remove_token(caplog, website):
    """Test reverse proxy token removal"""

    async def _main():
        async with reverse_server() as (server, ws_port, token, socks_port):
            async with reverse_client(ws_port, token) as (client):
                await async_assert_web_connection(website, socks_port)

                # Remove token via Go bindings
                server.RemoveToken(token)

                # Connection should fail after token removal
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_forward_ipv6(caplog, website_v6):
    """Test forward proxy with IPv6"""

    async def _main():
        async with forward_proxy() as (server, client, socks_port):
            await async_assert_web_connection(website_v6, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_reverse_ipv6(caplog, website_v6):
    """Test reverse proxy with IPv6"""

    async def _main():
        async with reverse_proxy() as (server, client, socks_port):
            await async_assert_web_connection(website_v6, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== UDP Tests ====================


def test_udp_forward_proxy(caplog, udp_server):
    """Test UDP through forward proxy"""

    async def _main():
        async with forward_proxy() as (server, client, socks_port):
            await async_assert_udp_connection(udp_server, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_reverse_proxy(caplog, udp_server):
    """Test UDP through reverse proxy"""

    async def _main():
        async with reverse_proxy() as (server, client, socks_port):
            await async_assert_udp_connection(udp_server, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_forward_proxy_v6(caplog, udp_server_v6):
    """Test UDP through forward proxy with IPv6"""

    async def _main():
        async with forward_proxy() as (server, client, socks_port):
            await async_assert_udp_connection(udp_server_v6, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_reverse_proxy_v6(caplog, udp_server_v6):
    """Test UDP through reverse proxy with IPv6"""

    async def _main():
        async with reverse_proxy() as (server, client, socks_port):
            await async_assert_udp_connection(udp_server_v6, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_forward_proxy_domain(caplog, udp_server_domain):
    """Test UDP through forward proxy using domain name"""

    async def _main():
        # Check localhost resolution first
        import socket

        try:
            addrs = socket.getaddrinfo("localhost", None, socket.AF_UNSPEC)
            if not addrs:
                pytest.skip("localhost resolution failed")

            # Use the server regardless of localhost resolution preference
            async with forward_proxy() as (server, client, socks_port):
                await async_assert_udp_connection(udp_server_domain, socks_port)
        except socket.gaierror:
            pytest.skip("localhost resolution not available")

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_reverse_proxy_domain(caplog, udp_server_domain):
    """Test UDP through reverse proxy using domain name"""

    async def _main():
        # Check localhost resolution first
        import socket

        try:
            addrs = socket.getaddrinfo("localhost", None, socket.AF_UNSPEC)
            if not addrs:
                pytest.skip("localhost resolution failed")

            # Use the server regardless of localhost resolution preference
            async with reverse_proxy() as (server, client, socks_port):
                await async_assert_udp_connection(udp_server_domain, socks_port)
        except socket.gaierror:
            pytest.skip("localhost resolution not available")

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Proxy Authentication Tests ====================


@contextlib.asynccontextmanager
async def reverse_server_with_auth(
    token: Optional[str] = "<token>",
    username: str = "test_user",
    password: str = "test_pass",
    ws_port: Optional[int] = None,
    **kw,
):
    """Create a reverse server with SOCKS authentication"""
    from linksockslib import linksocks

    ws_port = ws_port or get_free_port()

    server = None
    try:
        # Create server - all initialization steps in try block
        server_opt = linksocks.DefaultServerOption()
        server_opt.WithWSPort(ws_port)
        server = linksocks.NewLinkSocksServer(server_opt)

        # Add reverse token with authentication
        reverse_opts = linksocks.DefaultReverseTokenOptions()
        reverse_opts.Token = token
        reverse_opts.Username = username
        reverse_opts.Password = password
        result: linksocks.ReverseTokenResult = server.AddReverseToken(reverse_opts)
        socks_port = result.Port

        await asyncio.to_thread(
            server.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create reverse server with auth: {e}")
        if server:
            server.Close()
        raise
    
    try:
        yield server, ws_port, token, socks_port, username, password
    finally:
        server.Close()


def test_proxy_auth(caplog, website):
    """Test proxy authentication"""

    async def _main():
        username = "test_user"
        password = "test_pass"

        async with reverse_server_with_auth(username=username, password=password) as (
            server,
            ws_port,
            token,
            socks_port,
            user,
            pwd,
        ):
            async with reverse_client(ws_port, token) as client:
                # Connection without auth should fail
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

                # Connection with auth should succeed
                await async_assert_web_connection(website, socks_port, socks_auth=(user, pwd))

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Reconnection Tests ====================


@contextlib.asynccontextmanager
async def forward_client_with_reconnect(ws_port: int, token: str, **kw):
    """Create a forward client with reconnection enabled"""
    from linksockslib import linksocks

    socks_port = get_free_port()

    client = None
    try:
        # Create client with reconnection - all initialization steps in try block
        client_opt = linksocks.DefaultClientOption()
        client_opt.WithWSURL(f"ws://localhost:{ws_port}")
        client_opt.WithSocksPort(socks_port)
        client_opt.WithReconnectDelay(1 * linksocks.Second())
        client_opt.WithReconnect(True)
        client = linksocks.NewLinkSocksClient(token, client_opt)

        await asyncio.to_thread(
            client.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create forward client with reconnect: {e}")
        if client:
            client.Close()
        raise
    
    try:
        yield client, socks_port
    finally:
        client.Close()


def test_forward_reconnect(caplog, website):
    """Test forward proxy reconnection"""

    async def _main():
        # First server
        async with forward_server() as (server1, ws_port, token):
            async with forward_client_with_reconnect(ws_port, token) as (client, socks_port):
                # Test initial connection
                await async_assert_web_connection(website, socks_port)

                # Close server
                server1.Close()

                # Wait a bit for disconnection detection
                await asyncio.sleep(2)

                # Start new server with same port and token
                async with forward_server(ws_port=ws_port, token=token) as (server2, _, _):
                    # Wait for reconnection
                    await asyncio.sleep(3)

                    # Test connection after reconnect
                    await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 45))


def test_reverse_reconnect(caplog, website):
    """Test reverse proxy reconnection"""

    async def _main():
        async with reverse_server() as (server, ws_port, token, socks_port):
            # First client
            async with reverse_client(ws_port, token) as client1:
                await async_assert_web_connection(website, socks_port)

                # Close client
                client1.Close()

                # Wait a bit for disconnection detection
                await asyncio.sleep(2)

                # Start new client
                async with reverse_client(ws_port, token) as client2:
                    # Wait for connection
                    await asyncio.sleep(2)

                    # Test connection with new client
                    await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 45))


# ==================== Connector Tests ====================


@contextlib.asynccontextmanager
async def reverse_server_with_connector(
    token: Optional[str] = "<token>",
    connector_token: str = "CONNECTOR",
    ws_port: Optional[int] = None,
    **kw,
):
    """Create a reverse server with connector token"""
    from linksockslib import linksocks

    ws_port = ws_port or get_free_port()

    server = None
    try:
        # Create server - all initialization steps in try block
        server_opt = linksocks.DefaultServerOption()
        server_opt.WithWSPort(ws_port)
        server = linksocks.NewLinkSocksServer(server_opt)

        # Add reverse token
        reverse_opts = linksocks.DefaultReverseTokenOptions()
        reverse_opts.Token = token
        result: linksocks.ReverseTokenResult = server.AddReverseToken(reverse_opts)
        socks_port = result.Port

        # Add connector token
        server.AddConnectorToken(connector_token, result.Token)

        await asyncio.to_thread(
            server.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create reverse server with connector: {e}")
        if server:
            server.Close()
        raise
    
    try:
        yield server, ws_port, result.Token, socks_port, connector_token
    finally:
        server.Close()


def test_connector(caplog, website):
    """Test connector functionality"""

    async def _main():
        async with reverse_server_with_connector() as (server, ws_port, token, socks_port, connector_token):
            # Reverse client
            async with reverse_client(ws_port, token) as client1:
                # Forward client using connector token
                async with forward_client(ws_port, connector_token) as (client2, forward_socks_port):
                    # Test both connections
                    await async_assert_web_connection(website, socks_port)
                    await async_assert_web_connection(website, forward_socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Connector Autonomy Tests ====================


@contextlib.asynccontextmanager
async def reverse_server_with_autonomy(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
    **kw,
):
    """Create a reverse server with connector autonomy"""
    from linksockslib import linksocks

    ws_port = ws_port or get_free_port()

    server = None
    try:
        # Create server - all initialization steps in try block
        server_opt = linksocks.DefaultServerOption()
        server_opt.WithWSPort(ws_port)
        server = linksocks.NewLinkSocksServer(server_opt)

        # Add reverse token with autonomy
        reverse_opts = linksocks.DefaultReverseTokenOptions()
        reverse_opts.Token = token
        reverse_opts.AllowManageConnector = True
        result: linksocks.ReverseTokenResult = server.AddReverseToken(reverse_opts)
        socks_port = result.Port

        await asyncio.to_thread(
            server.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create reverse server with autonomy: {e}")
        if server:
            server.Close()
        raise
    
    try:
        yield server, ws_port, result.Token, socks_port
    finally:
        server.Close()


def test_connector_autonomy(caplog, website):
    """Test connector autonomy functionality"""

    async def _main():
        async with reverse_server_with_autonomy() as (server, ws_port, token, socks_port):
            async with reverse_client(ws_port, token) as client1:
                # Client can add its own connector
                connector_token = await asyncio.to_thread(client1.AddConnector, "CONNECTOR")
                assert connector_token

                # Forward client using the connector token
                async with forward_client(ws_port, "CONNECTOR") as (client2, forward_socks_port):
                    # Reverse proxy should fail (autonomy mode)
                    with pytest.raises(Exception):
                        await async_assert_web_connection(website, socks_port, timeout=3)

                    # Forward proxy should work
                    await async_assert_web_connection(website, forward_socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Multi-thread Client Tests ====================


@contextlib.asynccontextmanager
async def forward_client_with_threads(ws_port: int, token: str, threads: int = 2, **kw):
    """Create a forward client with multiple threads"""
    from linksockslib import linksocks

    socks_port = get_free_port()

    client = None
    try:
        # Create client with multiple threads - all initialization steps in try block
        client_opt = linksocks.DefaultClientOption()
        client_opt.WithWSURL(f"ws://localhost:{ws_port}")
        client_opt.WithSocksPort(socks_port)
        client_opt.WithReconnectDelay(1 * linksocks.Second())
        client_opt.WithThreads(threads)
        client = linksocks.NewLinkSocksClient(token, client_opt)

        await asyncio.to_thread(
            client.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create forward client with threads: {e}")
        if client:
            client.Close()
        raise
    
    try:
        yield client, socks_port
    finally:
        client.Close()


def test_client_thread(caplog, website):
    """Test multi-threaded client"""

    async def _main():
        async with forward_server() as (server, ws_port, token):
            async with forward_client_with_threads(ws_port, token, threads=2) as (client, socks_port):
                # Execute multiple connection tests
                for i in range(3):
                    await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Fast Open Mode Tests ====================


@contextlib.asynccontextmanager
async def forward_server_fast_open(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
    **kw,
):
    """Create a forward server with fast open mode"""
    from linksockslib import linksocks

    ws_port = ws_port or get_free_port()

    server = None
    try:
        # Create server with fast open mode - all initialization steps in try block
        server_opt = linksocks.DefaultServerOption()
        server_opt.WithWSPort(ws_port)
        server_opt.WithFastOpen(True)
        server = linksocks.NewLinkSocksServer(server_opt)

        # Add forward token
        server.AddForwardToken(token)
        await asyncio.to_thread(
            server.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create forward server with fast open: {e}")
        if server:
            server.Close()
        raise
    
    try:
        yield server, ws_port, token
    finally:
        server.Close()


@contextlib.asynccontextmanager
async def forward_client_fast_open(ws_port: int, token: str, **kw):
    """Create a forward client with fast open mode"""
    from linksockslib import linksocks

    socks_port = get_free_port()

    client = None
    try:
        # Create client with fast open mode - all initialization steps in try block
        client_opt = linksocks.DefaultClientOption()
        client_opt.WithWSURL(f"ws://localhost:{ws_port}")
        client_opt.WithSocksPort(socks_port)
        client_opt.WithReconnectDelay(1 * linksocks.Second())
        client_opt.WithFastOpen(True)
        client = linksocks.NewLinkSocksClient(token, client_opt)

        await asyncio.to_thread(
            client.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create forward client with fast open: {e}")
        if client:
            client.Close()
        raise
    
    try:
        yield client, socks_port
    finally:
        client.Close()


@contextlib.asynccontextmanager
async def reverse_server_fast_open(
    token: Optional[str] = "<token>",
    ws_port: Optional[int] = None,
    **kw,
):
    """Create a reverse server with fast open mode"""
    from linksockslib import linksocks

    ws_port = ws_port or get_free_port()

    server = None
    try:
        # Create server with fast open mode - all initialization steps in try block
        server_opt = linksocks.DefaultServerOption()
        server_opt.WithWSPort(ws_port)
        server_opt.WithFastOpen(True)
        server = linksocks.NewLinkSocksServer(server_opt)

        # Add reverse token
        reverse_opts = linksocks.DefaultReverseTokenOptions()
        reverse_opts.Token = token
        result: linksocks.ReverseTokenResult = server.AddReverseToken(reverse_opts)
        socks_port = result.Port

        await asyncio.to_thread(
            server.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create reverse server with fast open: {e}")
        if server:
            server.Close()
        raise
    
    try:
        yield server, ws_port, result.Token, socks_port
    finally:
        server.Close()


@contextlib.asynccontextmanager
async def reverse_client_fast_open(ws_port: int, token: str, **kw):
    """Create a reverse client with fast open mode"""
    from linksockslib import linksocks

    client = None
    try:
        # Create client with fast open mode - all initialization steps in try block
        client_opt = linksocks.DefaultClientOption()
        client_opt.WithWSURL(f"ws://localhost:{ws_port}")
        client_opt.WithReconnectDelay(1 * linksocks.Second())
        client_opt.WithReverse(True)
        client_opt.WithFastOpen(True)
        client = linksocks.NewLinkSocksClient(token, client_opt)

        await asyncio.to_thread(
            client.WaitReady, ctx=linksocks.NewContext(), timeout=start_time_limit * linksocks.Second()
        )
    except Exception as e:
        test_logger.error(f"Failed to create reverse client with fast open: {e}")
        if client:
            client.Close()
        raise
    
    try:
        yield client
    finally:
        client.Close()


def test_fast_open_forward(caplog, website):
    """Test fast open forward proxy"""

    async def _main():
        async with forward_server() as (server, ws_port, token):
            async with forward_client_fast_open(ws_port, token) as (client, socks_port):
                # Execute multiple connection tests
                for i in range(3):
                    await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_reverse(caplog, website):
    """Test fast open reverse proxy"""

    async def _main():
        async with reverse_server_fast_open() as (server, ws_port, token, socks_port):
            async with reverse_client(ws_port, token) as client:
                # Execute multiple connection tests
                for i in range(3):
                    await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_forward(caplog, website):
    """Test fast open server with fast open forward client"""

    async def _main():
        async with forward_server_fast_open() as (server, ws_port, token):
            async with forward_client_fast_open(ws_port, token) as (client, socks_port):
                # Execute multiple connection tests
                for i in range(3):
                    await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_reverse(caplog, website):
    """Test fast open server with fast open reverse client"""

    async def _main():
        async with reverse_server_fast_open() as (server, ws_port, token, socks_port):
            async with reverse_client_fast_open(ws_port, token) as client:
                # Execute multiple connection tests
                for i in range(3):
                    await async_assert_web_connection(website, socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_connector(caplog, website):
    """Test fast open connector functionality"""

    async def _main():
        connector_token = "CONNECTOR"

        # Create fast open server with connector
        async with reverse_server_fast_open() as (server, ws_port, token, socks_port):
            # Add connector token
            server.AddConnectorToken(connector_token, token)

            async with reverse_client_fast_open(ws_port, token) as client1:
                async with forward_client_fast_open(ws_port, connector_token) as (client2, forward_socks_port):
                    # Test both connections
                    await async_assert_web_connection(website, socks_port)
                    await async_assert_web_connection(website, forward_socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Mixed Fast Open Mode Tests ====================


def test_mixed_fast_open_connector_all_fast_open(caplog, website):
    """Test mixed fast open modes - all fast open"""

    async def _main():
        connector_token = "CONNECTOR"

        async with reverse_server_fast_open() as (server, ws_port, token, socks_port):
            server.AddConnectorToken(connector_token, token)

            async with reverse_client_fast_open(ws_port, token) as client1:
                async with forward_client_fast_open(ws_port, connector_token) as (client2, forward_socks_port):
                    await async_assert_web_connection(website, socks_port)
                    await async_assert_web_connection(website, forward_socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_server_fast_open(caplog, website):
    """Test mixed fast open modes - server fast open only"""

    async def _main():
        connector_token = "CONNECTOR"

        async with reverse_server_fast_open() as (server, ws_port, token, socks_port):
            server.AddConnectorToken(connector_token, token)

            async with reverse_client(ws_port, token) as client1:
                async with forward_client(ws_port, connector_token) as (client2, forward_socks_port):
                    await async_assert_web_connection(website, socks_port)
                    await async_assert_web_connection(website, forward_socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_connector_fast_open(caplog, website):
    """Test mixed fast open modes - connector fast open only"""

    async def _main():
        connector_token = "CONNECTOR"

        async with reverse_server_fast_open() as (server, ws_port, token, socks_port):
            server.AddConnectorToken(connector_token, token)

            async with reverse_client(ws_port, token) as client1:
                async with forward_client_fast_open(ws_port, connector_token) as (client2, forward_socks_port):
                    await async_assert_web_connection(website, socks_port)
                    await async_assert_web_connection(website, forward_socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_client_fast_open(caplog, website):
    """Test mixed fast open modes - client fast open only"""

    async def _main():
        connector_token = "CONNECTOR"

        async with reverse_server_fast_open() as (server, ws_port, token, socks_port):
            server.AddConnectorToken(connector_token, token)

            async with reverse_client_fast_open(ws_port, token) as client1:
                async with forward_client_fast_open(ws_port, connector_token) as (client2, forward_socks_port):
                    await async_assert_web_connection(website, socks_port)
                    await async_assert_web_connection(website, forward_socks_port)

    return asyncio.run(asyncio.wait_for(_main(), 30))
