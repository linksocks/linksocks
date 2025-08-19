"""
WebSocket SOCKS5 proxy client implementation.

This module provides the Client class for connecting to a SOCKS5 proxy server
over WebSocket and establishing proxy functionality.
"""

from __future__ import annotations

import asyncio
import logging
from typing import Optional

# Underlying Go bindings module (generated)
from linksockslib import linksocks  # type: ignore

from ._base import (
    _SnakePassthrough,
    _to_duration,
    _logger,
    BufferZerologLogger,
    DurationLike,
)


class Client(_SnakePassthrough):
    """WebSocket SOCKS5 proxy client.
    
    The Client class connects to a WebSocket server and establishes SOCKS5 proxy
    functionality. It supports both forward and reverse proxy modes.
    
    In forward mode, the client runs a local SOCKS5 server and forwards connections
    through WebSocket to the server, which then connects to targets.
    
    In reverse mode, the client connects to targets directly and forwards data
    back through WebSocket to a SOCKS5 server running on the server side.
    """

    def __init__(
        self,
        token: str,
        *,
        logger: Optional[logging.Logger] = None,
        ws_url: Optional[str] = None,
        reverse: Optional[bool] = None,
        socks_host: Optional[str] = None,
        socks_port: Optional[int] = None,
        socks_username: Optional[str] = None,
        socks_password: Optional[str] = None,
        socks_wait_server: Optional[bool] = None,
        reconnect: Optional[bool] = None,
        reconnect_delay: Optional[DurationLike] = None,
        buffer_size: Optional[int] = None,
        channel_timeout: Optional[DurationLike] = None,
        connect_timeout: Optional[DurationLike] = None,
        threads: Optional[int] = None,
        fast_open: Optional[bool] = None,
        upstream_proxy: Optional[str] = None,
        upstream_username: Optional[str] = None,
        upstream_password: Optional[str] = None,
        no_env_proxy: Optional[bool] = None,
    ) -> None:
        """Initialize the WebSocket SOCKS5 proxy client.
        
        Args:
            token: Authentication token for WebSocket connection
            logger: Python logger instance for this client
            ws_url: WebSocket server URL to connect to
            reverse: Whether to use reverse proxy mode
            socks_host: SOCKS5 server listen address (for forward mode)
            socks_port: SOCKS5 server listen port (for forward mode)
            socks_username: SOCKS5 authentication username
            socks_password: SOCKS5 authentication password
            socks_wait_server: Whether to wait for server connection before starting SOCKS5
            reconnect: Whether to automatically reconnect on disconnection
            reconnect_delay: Delay between reconnection attempts
            buffer_size: Buffer size for data transfer
            channel_timeout: Timeout for WebSocket channels
            connect_timeout: Timeout for outbound connections
            threads: Number of threads for concurrent processing
            fast_open: Assume connection success and allow data transfer immediately
            upstream_proxy: Upstream proxy address for chaining
            upstream_username: Username for upstream proxy authentication
            upstream_password: Password for upstream proxy authentication
            no_env_proxy: Whether to ignore proxy environment variables
        """
        opt = linksocks.DefaultClientOption()
        if logger is None:
            logger = _logger
        # Use buffer-based logger system
        self._managed_logger = BufferZerologLogger(logger, f"client_{id(self)}")
        opt.WithLogger(self._managed_logger.go_logger)
        if ws_url is not None:
            opt.WithWSURL(ws_url)
        if reverse is not None:
            opt.WithReverse(bool(reverse))
        if socks_host is not None:
            opt.WithSocksHost(socks_host)
        if socks_port is not None:
            opt.WithSocksPort(int(socks_port))
        if socks_username is not None:
            opt.WithSocksUsername(socks_username)
        if socks_password is not None:
            opt.WithSocksPassword(socks_password)
        if socks_wait_server is not None:
            opt.WithSocksWaitServer(bool(socks_wait_server))
        if reconnect is not None:
            opt.WithReconnect(bool(reconnect))
        if reconnect_delay is not None:
            opt.WithReconnectDelay(_to_duration(reconnect_delay))
        if buffer_size is not None:
            opt.WithBufferSize(int(buffer_size))
        if channel_timeout is not None:
            opt.WithChannelTimeout(_to_duration(channel_timeout))
        if connect_timeout is not None:
            opt.WithConnectTimeout(_to_duration(connect_timeout))
        if threads is not None:
            opt.WithThreads(int(threads))
        if fast_open is not None:
            opt.WithFastOpen(bool(fast_open))
        if upstream_proxy is not None:
            opt.WithUpstreamProxy(upstream_proxy)
        if upstream_username or upstream_password:
            opt.WithUpstreamAuth(upstream_username or "", upstream_password or "")
        if no_env_proxy is not None:
            opt.WithNoEnvProxy(bool(no_env_proxy))

        self._raw = linksocks.NewLinkSocksClient(token, opt)
        self._ctx = None

    @property
    def log(self) -> logging.Logger:
        """Access the Python logger for this client instance."""
        return self._managed_logger.py_logger
    
    def wait_ready(self, timeout: Optional[DurationLike] = None) -> None:
        """Wait for the client to be ready.
        
        Args:
            timeout: Maximum time to wait, no timeout if None
        """
        if not self._ctx:
            self._ctx = linksocks.NewContextWithCancel()
        timeout = _to_duration(timeout) if timeout is not None else 0
        return self._raw.WaitReady(ctx=self._ctx.Context(), timeout=timeout)

    async def async_wait_ready(self, timeout: Optional[DurationLike] = None) -> None:
        """Wait for the client to be ready asynchronously.
        
        Args:
            timeout: Maximum time to wait, no timeout if None
        """
        if not self._ctx:
            self._ctx = linksocks.NewContextWithCancel()
        timeout = _to_duration(timeout) if timeout is not None else 0
        try:
            return await asyncio.to_thread(self._raw.WaitReady, ctx=self._ctx.Context(), timeout=timeout)
        except asyncio.CancelledError:
            # Ensure the underlying Go client stops retrying/logging when the
            # awaiting task is cancelled (e.g. Ctrl+C). We cancel the context
            # we passed into Go and close the client, then re-raise.
            try:
                try:
                    self._ctx.Cancel()
                except Exception:
                    pass
                # Shield cleanup from further cancellation so it can complete
                await asyncio.shield(asyncio.to_thread(self._raw.Close))
                # Best-effort logger cleanup
                if hasattr(self, '_managed_logger') and self._managed_logger:
                    try:
                        self._managed_logger.cleanup()
                    except Exception:
                        pass
            finally:
                raise
    
    def add_connector(self, connector_token: Optional[str]) -> str:
        """Add a connector token for reverse proxy.
        
        Args:
            connector_token: Connector token string, auto-generated if not provided
            
        Returns:
            The connector token string (generated or provided)
        """
        return self._raw.AddConnector(connector_token or "")

    async def async_add_connector(self, connector_token: Optional[str]) -> str:
        """Add a connector token for reverse proxy asynchronously.
        
        Args:
            connector_token: Connector token string, auto-generated if not provided
            
        Returns:
            The connector token string (generated or provided)
        """
        return await asyncio.to_thread(self._raw.AddConnector, connector_token or "")

    @property
    def is_connected(self) -> bool:
        """Check if the client is connected to the server.
        
        Returns:
            True if connected, False otherwise
        """
        try:
            return bool(self._raw.IsConnected)
        except Exception:
            # If not exposed as field, fall back to False
            return False

    @property
    def socks_port(self) -> Optional[int]:
        """Get the SOCKS5 server port (for forward mode).
        
        Returns:
            The port number if available, None otherwise
        """
        try:
            # Exposed field in bindings
            port = getattr(self._raw, "SocksPort", None)
            return int(port) if port is not None else None
        except Exception:
            return None

    def close(self) -> None:
        """Close the client and clean up resources."""
        # Close client
        if hasattr(self, '_raw') and self._raw:
            self._raw.Close()
        # Clean up managed logger
        if hasattr(self, '_managed_logger') and self._managed_logger:
            try:
                self._managed_logger.cleanup()
            except:
                # Ignore cleanup errors
                pass
        # Close context
        if hasattr(self, '_ctx') and self._ctx:
            try:
                self._ctx.Cancel()
            except Exception:
                # Ignore errors during context close
                pass

    async def async_close(self) -> None:
        """Close the client and clean up resources asynchronously."""
        # Close client
        if hasattr(self, '_raw') and self._raw:
            await asyncio.to_thread(self._raw.Close)
        # Clean up managed logger
        if hasattr(self, '_managed_logger') and self._managed_logger:
            try:
                self._managed_logger.cleanup()
            except:
                # Ignore cleanup errors
                pass
        # Close context
        if hasattr(self, '_ctx') and self._ctx:
            try:
                self._ctx.Cancel()
            except Exception:
                # Ignore errors during context close
                pass

    # Context manager support
    def __enter__(self) -> "Client":
        """Context manager entry."""
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        """Context manager exit."""
        self.close()
        
    async def __aenter__(self) -> "Client":
        """Async context manager entry."""
        await self.async_wait_ready()
        return self
        
    async def __aexit__(self, exc_type, exc, tb) -> None:
        """Async context manager exit."""
        await self.async_close()
        
    def __del__(self):
        """Destructor - clean up resources."""
        try:
            self.close()
        except Exception:
            # Ignore errors during cleanup
            pass