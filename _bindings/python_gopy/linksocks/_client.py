"""WebSocket SOCKS5 proxy client implementation (gopy backend)."""

from __future__ import annotations

import asyncio
import logging
from typing import Optional

from ._base import (
    _SnakePassthrough,
    _to_duration,
    _logger,
    BufferZerologLogger,
    DurationLike,
    backend,
)


class Client(_SnakePassthrough):
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
        opt = backend.DefaultClientOption()
        if logger is None:
            logger = _logger
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

        self._raw = backend.NewLinkSocksClient(token, opt)
        self._ctx = None

    @property
    def log(self) -> logging.Logger:
        return self._managed_logger.py_logger

    def wait_ready(self, timeout: Optional[DurationLike] = None) -> None:
        if not self._ctx:
            self._ctx = backend.NewContextWithCancel()
        timeout = _to_duration(timeout) if timeout is not None else 0
        return self._raw.WaitReady(ctx=self._ctx.Context(), timeout=timeout)

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

    def add_connector(self, connector_token: Optional[str]) -> str:
        return self._raw.AddConnector(connector_token or "")

    async def async_add_connector(self, connector_token: Optional[str]) -> str:
        return await asyncio.to_thread(self._raw.AddConnector, connector_token or "")

    @property
    def is_connected(self) -> bool:
        try:
            return bool(self._raw.IsConnected)
        except Exception:
            return False

    @property
    def socks_port(self) -> Optional[int]:
        try:
            port = getattr(self._raw, "SocksPort", None)
            return int(port) if port is not None else None
        except Exception:
            return None

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

    def __enter__(self) -> "Client":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    async def __aenter__(self) -> "Client":
        await self.async_wait_ready()
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        await self.async_close()

    def __del__(self):
        try:
            self.close()
        except Exception:
            pass
