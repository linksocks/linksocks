"""
Command-line interface for linksocks.

This module provides all CLI commands for the linksocks SOCKS5 over WebSocket proxy tool.
"""

import asyncio
import logging
import platform
from typing import Optional

import click

from ._logging_config import init_logging
from ._utils import get_env_or_flag, parse_socks_proxy, validate_required_token


@click.group()
def cli():
    """SOCKS5 over WebSocket proxy tool."""
    pass


@click.command()
def version():
    """Print version and platform information."""
    try:
        from linksocks import __version__
        version_str = __version__
    except ImportError:
        version_str = "unknown"
    
    platform_str = platform.platform()
    click.echo(f"linksocks version {version_str} {platform_str}")


@click.command()
@click.option("--token", "-t", help="Authentication token (env: LINKSOCKS_TOKEN)")
@click.option("--url", "-u", default="ws://localhost:8765", help="WebSocket server address")
@click.option("--reverse", "-r", is_flag=True, default=False, help="Use reverse SOCKS5 proxy")
@click.option("--connector-token", "-c", default=None, help="Connector token for reverse proxy (env: LINKSOCKS_CONNECTOR_TOKEN)")
@click.option("--socks-host", "-s", default="127.0.0.1", help="SOCKS5 server listen address")
@click.option("--socks-port", "-p", default=9870, help="SOCKS5 server listen port")
@click.option("--socks-username", "-n", help="SOCKS5 authentication username")
@click.option("--socks-password", "-w", help="SOCKS5 authentication password (env: LINKSOCKS_SOCKS_PASSWORD)")
@click.option("--socks-no-wait", "-i", is_flag=True, default=False, help="Start SOCKS server immediately")
@click.option("--no-reconnect", "-R", is_flag=True, default=False, help="Stop when server disconnects")
@click.option("--debug", "-d", count=True, help="Debug logging (-d for debug, -dd for trace)")
@click.option("--threads", "-T", default=1, help="Number of threads for data transfer")
@click.option("--upstream-proxy", "-x", help="Upstream SOCKS5 proxy (socks5://[user:pass@]host[:port])")
@click.option("--fast-open", "-f", is_flag=True, default=False, help="Assume connection success and allow data transfer immediately")
@click.option("--no-env-proxy", "-E", is_flag=True, default=False, help="Ignore proxy env vars for WebSocket connection")
def client(
    token: Optional[str],
    url: str,
    reverse: bool,
    connector_token: Optional[str],
    socks_host: str,
    socks_port: int,
    socks_username: Optional[str],
    socks_password: Optional[str],
    socks_no_wait: bool,
    no_reconnect: bool,
    debug: int,
    threads: int,
    upstream_proxy: Optional[str],
    fast_open: bool,
    no_env_proxy: bool,
):
    """Start SOCKS5 over WebSocket proxy client."""
    from ._client import Client

    async def main():
        # Get values from flags or environment
        actual_token = get_env_or_flag(token, "LINKSOCKS_TOKEN")
        actual_connector_token = get_env_or_flag(connector_token, "LINKSOCKS_CONNECTOR_TOKEN")
        actual_socks_password = get_env_or_flag(socks_password, "LINKSOCKS_SOCKS_PASSWORD")
        
        # Validate required token
        try:
            validated_token = validate_required_token(actual_token)
        except ValueError as e:
            raise click.ClickException(str(e)) from e
        
        # Setup logging
        if debug == 0:
            log_level = logging.INFO
        elif debug == 1:
            log_level = logging.DEBUG
        else:
            log_level = 5  # TRACE level
        
        init_logging(level=log_level)

        # Parse upstream proxy
        if upstream_proxy:
            try:
                upstream_host, upstream_username, upstream_password = parse_socks_proxy(upstream_proxy)
            except ValueError as e:
                raise click.ClickException(f"Invalid upstream proxy: {e}") from e
        else:
            upstream_host = upstream_username = upstream_password = None

        # Create client
        client = Client(
            ws_url=url,
            token=validated_token,
            reverse=reverse,
            socks_host=socks_host,
            socks_port=socks_port,
            socks_username=socks_username,
            socks_password=actual_socks_password,
            socks_wait_server=not socks_no_wait,
            reconnect=not no_reconnect,
            upstream_proxy=upstream_host,
            upstream_username=upstream_username,
            upstream_password=upstream_password,
            threads=threads,
            fast_open=fast_open,
            no_env_proxy=no_env_proxy,
        )
        
        # Add connector for reverse mode
        if actual_connector_token and reverse:
            await client.async_add_connector(actual_connector_token)
            await asyncio.sleep(0.2)
        
        # Run client
        async with client:
            await asyncio.Future()

    asyncio.run(main())


@click.command()
@click.option("--ws-host", "-H", default="0.0.0.0", help="WebSocket server listen address")
@click.option("--ws-port", "-P", default=8765, help="WebSocket server listen port")
@click.option("--token", "-t", default=None, help="Auth token, auto-generated if not provided (env: LINKSOCKS_TOKEN)")
@click.option("--connector-token", "-c", default=None, help="Connector token for reverse proxy (env: LINKSOCKS_CONNECTOR_TOKEN)")
@click.option("--connector-autonomy", "-a", is_flag=True, default=False, help="Allow clients to manage connector tokens")
@click.option("--buffer-size", "-b", default=4096, help="Buffer size for data transfer")
@click.option("--reverse", "-r", is_flag=True, default=False, help="Use reverse SOCKS5 proxy")
@click.option("--socks-host", "-s", default="127.0.0.1", help="SOCKS5 server listen address for reverse proxy")
@click.option("--socks-port", "-p", default=9870, help="SOCKS5 server listen port for reverse proxy")
@click.option("--socks-username", "-n", help="SOCKS5 username for authentication")
@click.option("--socks-password", "-w", help="SOCKS5 password for authentication (env: LINKSOCKS_SOCKS_PASSWORD)")
@click.option("--socks-nowait", "-i", is_flag=True, default=False, help="Start SOCKS server immediately")
@click.option("--debug", "-d", count=True, help="Debug logging (-d for debug, -dd for trace)")
@click.option("--api-key", "-k", help="Enable HTTP API with specified key")
@click.option("--upstream-proxy", "-x", help="Upstream SOCKS5 proxy (socks5://[user:pass@]host[:port])")
@click.option("--fast-open", "-f", is_flag=True, default=False, help="Assume connection success and allow data transfer immediately")
def server(
    ws_host: str,
    ws_port: int,
    token: Optional[str],
    connector_token: Optional[str],
    connector_autonomy: bool,
    buffer_size: int,
    reverse: bool,
    socks_host: str,
    socks_port: int,
    socks_username: Optional[str],
    socks_password: Optional[str],
    socks_nowait: bool,
    debug: int,
    api_key: Optional[str],
    upstream_proxy: Optional[str],
    fast_open: bool,
):
    """Start SOCKS5 over WebSocket proxy server."""
    from ._server import Server

    async def main():
        # Get values from flags or environment
        actual_token = get_env_or_flag(token, "LINKSOCKS_TOKEN")
        actual_connector_token = get_env_or_flag(connector_token, "LINKSOCKS_CONNECTOR_TOKEN")
        actual_socks_password = get_env_or_flag(socks_password, "LINKSOCKS_SOCKS_PASSWORD")
        
        # Setup logging
        if debug == 0:
            log_level = logging.INFO
        elif debug == 1:
            log_level = logging.DEBUG
        else:
            log_level = 5  # TRACE level
        
        init_logging(level=log_level)

        # Parse upstream proxy
        if upstream_proxy:
            try:
                upstream_host, upstream_username, upstream_password = parse_socks_proxy(upstream_proxy)
            except ValueError as e:
                raise click.ClickException(f"Invalid upstream proxy: {e}") from e
        else:
            upstream_host = upstream_username = upstream_password = None

        # Create server
        server = Server(
            ws_host=ws_host,
            ws_port=ws_port,
            socks_host=socks_host,
            socks_wait_client=not socks_nowait,
            upstream_proxy=upstream_host,
            upstream_username=upstream_username,
            upstream_password=upstream_password,
            buffer_size=buffer_size,
            api_key=api_key,
            fast_open=fast_open,
        )

        # Configure tokens if no API key
        if not api_key:
            if reverse:
                result = await server.async_add_reverse_token(
                    token=actual_token,
                    port=socks_port,
                    username=socks_username,
                    password=actual_socks_password,
                    allow_manage_connector=connector_autonomy,
                )
                use_token = result.token
                
                if not connector_autonomy:
                    use_connector_token = await server.async_add_connector_token(
                        actual_connector_token, use_token
                    )
                await asyncio.sleep(0.2)

                server.log.info("Configuration:")
                server.log.info("  Mode: reverse proxy (SOCKS5 on server -> client -> network)")
                server.log.info(f"  Token: {use_token}")
                server.log.info(f"  SOCKS5 port: {result.port}")
                if socks_username and actual_socks_password:
                    server.log.info(f"  SOCKS5 username: {socks_username}")
                if not connector_autonomy:
                    server.log.info(f"  Connector Token: {use_connector_token}")
                if connector_autonomy:
                    server.log.info("  Connector autonomy: enabled")
            else:
                use_token = await server.async_add_forward_token(actual_token)
                await asyncio.sleep(0.2)
                server.log.info("Configuration:")
                server.log.info("  Mode: forward proxy (SOCKS5 on client -> server -> network)")
                server.log.info(f"  Token: {use_token}")

        # Run server
        async with server:
            await asyncio.Future()

    asyncio.run(main())


def create_provider_command():
    """Create provider command as alias for client -r."""
    def provider_wrapper(*args, **kwargs):
        kwargs['reverse'] = True
        return client(*args, **kwargs)
    
    return click.Command(
        name="provider",
        callback=provider_wrapper,
        params=client.params.copy(),
        help="Start reverse SOCKS5 proxy client (alias for 'client -r')"
    )


def create_connector_command():
    """Create connector command as alias for client."""
    return click.Command(
        name="connector",
        callback=client,
        params=client.params.copy(),
        help="Start SOCKS5 proxy client (alias for 'client')"
    )


# Create command aliases
provider_cmd = create_provider_command()
connector_cmd = create_connector_command()

# Register commands
cli.add_command(client)
cli.add_command(connector_cmd)
cli.add_command(provider_cmd)
cli.add_command(server)
cli.add_command(version)


if __name__ == "__main__":
    cli()