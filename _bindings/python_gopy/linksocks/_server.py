"""WebSocket SOCKS5 proxy server implementation (gopy backend)."""

from __future__ import annotations

import asyncio
import logging
from typing import Any, Optional

from ._base import (
    _SnakePassthrough,
    _to_duration,
    _logger,
    BufferZerologLogger,
    ReverseTokenResult,
    DurationLike,
    backend,
)


class Server(_SnakePassthrough):
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
        opt = backend.DefaultServerOption()
        if logger is None:
            logger = _logger
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

        self._raw = backend.NewLinkSocksServer(opt)
        self._ctx = None

    @property
    def log(self) -> logging.Logger:
        return self._managed_logger.py_logger

    def add_forward_token(self, token: Optional[str] = None) -> str:
        return self._raw.AddForwardToken(token or "")

    async def async_add_forward_token(self, token: Optional[str] = None) -> str:
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
        opts = backend.DefaultReverseTokenOptions()
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
        return ReverseTokenResult(token=result.token, port=result.port)

    async def async_add_reverse_token(
        self,
        *,
        token: Optional[str] = None,
        port: Optional[int] = None,
        username: Optional[str] = None,
        password: Optional[str] = None,
        allow_manage_connector: Optional[bool] = None,
    ) -> ReverseTokenResult:
        opts = backend.DefaultReverseTokenOptions()
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
        return ReverseTokenResult(token=result.token, port=result.port)

    def add_connector_token(self, connector_token: Optional[str], reverse_token: str) -> str:
        return self._raw.AddConnectorToken(connector_token or "", reverse_token)

    async def async_add_connector_token(self, connector_token: Optional[str], reverse_token: str) -> str:
        return await asyncio.to_thread(self._raw.AddConnectorToken, connector_token or "", reverse_token)

    def remove_token(self, token: str) -> bool:
        return self._raw.RemoveToken(token)

    async def async_remove_token(self, token: str) -> bool:
        return await asyncio.to_thread(self._raw.RemoveToken, token)

    def wait_ready(self, timeout: Optional[DurationLike] = None) -> None:
        if not self._ctx:
            self._ctx = backend.NewContextWithCancel()
        timeout = _to_duration(timeout) if timeout is not None else 0
        self._raw.WaitReady(ctx=self._ctx.Context(), timeout=timeout)

    async def async_wait_ready(self, timeout: Optional[DurationLike] = None) -> None:
        if not self._ctx:
            self._ctx = backend.NewContextWithCancel()
        timeout = _to_duration(timeout) if timeout is not None else 0
        try:
            return await asyncio.to_thread(self._raw.WaitReady, ctx=self._ctx.Context(), timeout=timeout)
        except asyncio.CancelledError:
            try:
                try:
                    self._ctx.Cancel()
                except Exception:
                    pass
                await asyncio.shield(asyncio.to_thread(self._raw.Close))
                if hasattr(self, "_managed_logger") and self._managed_logger:
                    try:
                        self._managed_logger.cleanup()
                    except Exception:
                        pass
            finally:
                raise

    def close(self) -> None:
        if hasattr(self, "_raw") and self._raw:
            self._raw.Close()
        if hasattr(self, "_managed_logger") and self._managed_logger:
            try:
                self._managed_logger.cleanup()
            except Exception:
                pass
        if hasattr(self, "_ctx") and self._ctx:
            try:
                self._ctx.Cancel()
            except Exception:
                pass

    async def async_close(self) -> None:
        if hasattr(self, "_raw") and self._raw:
            await asyncio.to_thread(self._raw.Close)
        if hasattr(self, "_managed_logger") and self._managed_logger:
            try:
                self._managed_logger.cleanup()
            except Exception:
                pass
        if hasattr(self, "_ctx") and self._ctx:
            try:
                self._ctx.Cancel()
            except Exception:
                pass

    def __enter__(self) -> "Server":
        self.wait_ready()
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    async def __aenter__(self) -> "Server":
        await self.async_wait_ready()
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        await self.async_close()

    def __del__(self):
        try:
            self.close()
        except Exception:
            pass
