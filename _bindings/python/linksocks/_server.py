"""
WebSocket SOCKS5 proxy server implementation.

This module provides the Server class for running a SOCKS5 proxy server
that communicates with clients over WebSocket connections.
"""

from __future__ import annotations

import asyncio
import logging
from typing import Any, Optional

# Underlying Go bindings module (generated)
from linksockslib import linksocks  # type: ignore

from ._base import (
    _SnakePassthrough,
    _to_duration,
    _logger,
    BufferZerologLogger,
    ReverseTokenResult,
    DurationLike,
)


class Server(_SnakePassthrough):
    """WebSocket SOCKS5 proxy server.
    
    The Server class manages WebSocket connections from clients and provides
    SOCKS5 proxy functionality. It supports both forward and reverse proxy modes.
    
    In forward mode, clients connect to the server and the server makes outbound
    connections on behalf of the clients.
    
    In reverse mode, the server runs a local SOCKS5 server and forwards connections
    through WebSocket to clients, which then connect to targets directly.
    """

    def __init__(
        self,
        *,
        logger: Optional[logging.Logger] = None,
        ws_host: Optional[str] = None,
        ws_port: Optional[int] = None,
        socks_host: Optional[str] = None,
        port_pool: Optional[Any] = None,
        socks_wait_client: Optional[bool] = None,
        buffer_size: Optional[int] = None,
        api_key: Optional[str] = None,
        channel_timeout: Optional[DurationLike] = None,
        connect_timeout: Optional[DurationLike] = None,
        fast_open: Optional[bool] = None,
        upstream_proxy: Optional[str] = None,
        upstream_username: Optional[str] = None,
        upstream_password: Optional[str] = None,
    ) -> None:
        """Initialize the WebSocket SOCKS5 proxy server.
        
        Args:
            logger: Python logger instance for this server
            ws_host: WebSocket server listen address
            ws_port: WebSocket server listen port
            socks_host: SOCKS5 server listen address (for reverse mode)
            port_pool: Pool of ports for SOCKS5 servers
            socks_wait_client: Whether to wait for client connections before starting SOCKS5
            buffer_size: Buffer size for data transfer
            api_key: API key for HTTP management interface
            channel_timeout: Timeout for WebSocket channels
            connect_timeout: Timeout for outbound connections
            fast_open: Assume connection success and allow data transfer immediately
            upstream_proxy: Upstream proxy address for chaining
            upstream_username: Username for upstream proxy authentication
            upstream_password: Password for upstream proxy authentication
        """
        opt = linksocks.DefaultServerOption()
        if logger is None:
            logger = _logger
        # Use buffer-based logger system
        self._managed_logger = BufferZerologLogger(logger, f"server_{id(self)}")
        opt.WithLogger(self._managed_logger.go_logger)
        if ws_host is not None:
            opt.WithWSHost(ws_host)
        if ws_port is not None:
            opt.WithWSPort(ws_port)
        if socks_host is not None:
            opt.WithSocksHost(socks_host)
        if port_pool is not None:
            opt.WithPortPool(port_pool)
        if socks_wait_client is not None:
            opt.WithSocksWaitClient(bool(socks_wait_client))
        if buffer_size is not None:
            opt.WithBufferSize(int(buffer_size))
        if api_key is not None:
            opt.WithAPI(api_key)
        if channel_timeout is not None:
            opt.WithChannelTimeout(_to_duration(channel_timeout))
        if connect_timeout is not None:
            opt.WithConnectTimeout(_to_duration(connect_timeout))
        if fast_open is not None:
            opt.WithFastOpen(bool(fast_open))
        if upstream_proxy is not None:
            opt.WithUpstreamProxy(upstream_proxy)
        if upstream_username or upstream_password:
            opt.WithUpstreamAuth(upstream_username or "", upstream_password or "")

        self._raw = linksocks.NewLinkSocksServer(opt)
        self._ctx = None

    @property
    def log(self) -> logging.Logger:
        """Access the Python logger for this server instance."""
        return self._managed_logger.py_logger

    def add_forward_token(self, token: Optional[str] = None) -> str:
        """Add a forward proxy token.
        
        Args:
            token: Token string, auto-generated if not provided
            
        Returns:
            The token string (generated or provided)
        """
        return self._raw.AddForwardToken(token or "")

    async def async_add_forward_token(self, token: Optional[str] = None) -> str:
        """Add a forward proxy token asynchronously.
        
        Args:
            token: Token string, auto-generated if not provided
            
        Returns:
            The token string (generated or provided)
        """
        return await asyncio.to_thread(self._raw.AddForwardToken, token or "")

    def add_reverse_token(
        self,
        *,
        token: Optional[str] = None,
        port: Optional[int] = None,
        username: Optional[str] = None,
        password: Optional[str] = None,
        allow_manage_connector: Optional[bool] = None,
    ) -> ReverseTokenResult:
        """Add a reverse proxy token.
        
        Args:
            token: Token string, auto-generated if not provided
            port: SOCKS5 server port, auto-assigned if not provided
            username: SOCKS5 authentication username
            password: SOCKS5 authentication password
            allow_manage_connector: Whether to allow clients to manage connector tokens
            
        Returns:
            Result containing the token and assigned port
        """
        opts = linksocks.DefaultReverseTokenOptions()
        if token:
            opts.Token = token
        if port is not None:
            opts.Port = int(port)
        if username is not None:
            opts.Username = username
        if password is not None:
            opts.Password = password
        if allow_manage_connector is not None:
            opts.AllowManageConnector = bool(allow_manage_connector)
        result = self._raw.AddReverseToken(opts)
        return ReverseTokenResult(token=result.Token, port=result.Port)

    async def async_add_reverse_token(
        self,
        *,
        token: Optional[str] = None,
        port: Optional[int] = None,
        username: Optional[str] = None,
        password: Optional[str] = None,
        allow_manage_connector: Optional[bool] = None,
    ) -> ReverseTokenResult:
        """Add a reverse proxy token asynchronously.
        
        Args:
            token: Token string, auto-generated if not provided
            port: SOCKS5 server port, auto-assigned if not provided
            username: SOCKS5 authentication username
            password: SOCKS5 authentication password
            allow_manage_connector: Whether to allow clients to manage connector tokens
            
        Returns:
            Result containing the token and assigned port
        """
        opts = linksocks.DefaultReverseTokenOptions()
        if token:
            opts.Token = token
        if port is not None:
            opts.Port = int(port)
        if username is not None:
            opts.Username = username
        if password is not None:
            opts.Password = password
        if allow_manage_connector is not None:
            opts.AllowManageConnector = bool(allow_manage_connector)
        result = await asyncio.to_thread(self._raw.AddReverseToken, opts)
        return ReverseTokenResult(token=result.Token, port=result.Port)

    def add_connector_token(self, connector_token: Optional[str], reverse_token: str) -> str:
        """Add a connector token for reverse proxy.
        
        Args:
            connector_token: Connector token string, auto-generated if not provided
            reverse_token: Associated reverse proxy token
            
        Returns:
            The connector token string (generated or provided)
        """
        return self._raw.AddConnectorToken(connector_token or "", reverse_token)

    async def async_add_connector_token(self, connector_token: Optional[str], reverse_token: str) -> str:
        """Add a connector token for reverse proxy asynchronously.
        
        Args:
            connector_token: Connector token string, auto-generated if not provided
            reverse_token: Associated reverse proxy token
            
        Returns:
            The connector token string (generated or provided)
        """
        return await asyncio.to_thread(self._raw.AddConnectorToken, connector_token or "", reverse_token)

    def remove_token(self, token: str) -> bool:
        """Remove a token from the server.
        
        Args:
            token: Token to remove
            
        Returns:
            True if token was removed, False if not found
        """
        return self._raw.RemoveToken(token)

    async def async_remove_token(self, token: str) -> bool:
        """Remove a token from the server asynchronously.
        
        Args:
            token: Token to remove
            
        Returns:
            True if token was removed, False if not found
        """
        return await asyncio.to_thread(self._raw.RemoveToken, token)

    def wait_ready(self, timeout: Optional[DurationLike] = None) -> None:
        """Wait for the server to be ready.
        
        Args:
            timeout: Maximum time to wait, no timeout if None
        """
        if not self._ctx:
            self._ctx = linksocks.NewContextWithCancel()
        timeout = _to_duration(timeout) if timeout is not None else 0
        self._raw.WaitReady(ctx=self._ctx.Context(), timeout=timeout)

    async def async_wait_ready(self, timeout: Optional[DurationLike] = None) -> None:
        """Wait for the server to be ready asynchronously.
        
        Args:
            timeout: Maximum time to wait, no timeout if None
        """
        if not self._ctx:
            self._ctx = linksocks.NewContextWithCancel()
        timeout = _to_duration(timeout) if timeout is not None else 0
        try:
            return await asyncio.to_thread(self._raw.WaitReady, ctx=self._ctx.Context(), timeout=timeout)
        except asyncio.CancelledError:
            # Ensure the underlying Go server stops when startup wait is cancelled
            try:
                try:
                    self._ctx.Cancel()
                except Exception:
                    pass
                await asyncio.shield(asyncio.to_thread(self._raw.Close))
                if hasattr(self, '_managed_logger') and self._managed_logger:
                    try:
                        self._managed_logger.cleanup()
                    except Exception:
                        pass
            finally:
                raise

    def close(self) -> None:
        """Close the server and clean up resources."""
        # Close server
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
        """Close the server and clean up resources asynchronously."""
        # Close server
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
    
    def __enter__(self) -> "Server":
        """Context manager entry."""
        self.wait_ready()
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        """Context manager exit."""
        self.close()
        
    async def __aenter__(self) -> "Server":
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