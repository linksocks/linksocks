"""Web site provider for publishing a local HTTP(S) backend."""

from __future__ import annotations

import asyncio
import logging
from typing import Any, List, Optional

from linksocks_ffi import Web as _FFIWeb

from ._base import (
    BufferZerologLogger,
    DurationLike,
    _logger,
    _to_duration,
)


class Web:
    """Publish a local HTTP(S) site through a LinkSocks.js web relay.

    The relay returns a temporary hostname under its ``WEB_ZONE`` (or the
    provider's custom ``domain``) and forwards requests to ``backends``.
    """

    def __init__(
        self,
        backends: List[str],
        *,
        url: str = "ws://localhost:8765",
        token: str = "",
        domain: Optional[str] = None,
        insecure: bool = False,
        no_reconnect: bool = False,
        reconnect_delay: Optional[DurationLike] = None,
        logger: Optional[logging.Logger] = None,
    ) -> None:
        if not backends:
            raise ValueError("at least one backend is required")
        if logger is None:
            logger = _logger

        self._managed_logger = BufferZerologLogger(logger, f"web_{id(self)}")
        cfg: dict[str, Any] = {
            "url": url,
            "token": token,
            "host": domain or "",
            "backends": list(backends),
            "insecure": bool(insecure),
            "no_reconnect": bool(no_reconnect),
            "reconnect_delay_ns": _to_duration(reconnect_delay),
            "logger_id": self._managed_logger.logger_id,
        }
        self._host = ""
        self._raw = _FFIWeb(cfg)

    @property
    def log(self) -> logging.Logger:
        """Access the Python logger for this web provider."""
        return self._managed_logger.py_logger

    @property
    def host(self) -> str:
        """The current remote hostname, updated when a temporary URL rotates."""
        if self._raw:
            self._host = self._raw.host()
        return self._host

    def wait_ready(self, timeout: Optional[DurationLike] = None) -> str:
        """Block until the site is registered and return its hostname."""
        timeout_ms = 0
        if timeout is not None:
            timeout_ms = int(_to_duration(timeout) / 1_000_000)
        if not self._raw:
            raise RuntimeError("web provider is closed")
        self._host = self._raw.wait_registered(timeout_ms)
        return self._host

    async def async_wait_ready(self, timeout: Optional[DurationLike] = None) -> str:
        """Asynchronously wait for registration and return the hostname."""
        return await asyncio.to_thread(self.wait_ready, timeout)

    def close(self) -> None:
        """Stop the provider and release its relay registration."""
        if getattr(self, "_raw", None):
            self._raw.close()
            self._raw = None
        if getattr(self, "_managed_logger", None):
            try:
                self._managed_logger.cleanup()
            except Exception:
                pass

    async def async_close(self) -> None:
        """Stop the provider asynchronously."""
        await asyncio.to_thread(self.close)

    def __enter__(self) -> "Web":
        return self

    def __exit__(self, exc_type, exc, tb) -> None:
        self.close()

    async def __aenter__(self) -> "Web":
        await self.async_wait_ready()
        return self

    async def __aexit__(self, exc_type, exc, tb) -> None:
        await self.async_close()

    def __del__(self) -> None:
        try:
            self.close()
        except Exception:
            pass
