"""
Base classes and utilities for linksocks.

This module contains shared functionality used by Server and Client classes.
"""

from __future__ import annotations

import json
import logging
import asyncio
from dataclasses import dataclass
import threading
import time
from datetime import timedelta
from typing import Any, Callable, Dict, Iterable, Optional, Tuple, Union, List

# Underlying Go bindings module (generated)
from linksockslib import linksocks # type: ignore

_logger = logging.getLogger(__name__)

# Type aliases
DurationLike = Union[int, float, timedelta, str]


def _snake_to_camel(name: str) -> str:
    """Convert snake_case to CamelCase."""
    parts = name.split("_")
    return "".join(p.capitalize() for p in parts if p)


def _camel_to_snake(name: str) -> str:
    """Convert CamelCase to snake_case."""
    out: List[str] = []
    for ch in name:
        if ch.isupper() and out:
            out.append("_")
        out.append(ch.lower())
    return "".join(out)


def _to_duration(value: Optional[DurationLike]) -> Any:
    """Convert seconds/str/timedelta to Go time.Duration via bindings.
    
    - None -> 0
    - int/float -> seconds (supports fractions)
    - timedelta -> total seconds
    - str -> parsed by Go (e.g., "1.5s", "300ms")
    """
    if value is None:
        return 0
    if isinstance(value, timedelta):
        seconds = value.total_seconds()
        return seconds * linksocks.Second()
    if isinstance(value, (int, float)):
        return value * linksocks.Second()
    if isinstance(value, str):
        try:
            return linksocks.ParseDuration(value)
        except Exception as exc:
            raise ValueError(f"Invalid duration string: {value}") from exc
    raise TypeError(f"Unsupported duration type: {type(value)!r}")


# Shared Go->Python log dispatcher
_def_level_map = {
    "trace": logging.DEBUG,
    "debug": logging.DEBUG,
    "info": logging.INFO,
    "warn": logging.WARNING,
    "warning": logging.WARNING,
    "error": logging.ERROR,
    "fatal": logging.CRITICAL,
    "panic": logging.CRITICAL,
}


def _emit_go_log(py_logger: logging.Logger, line: str) -> None:
    """Process a Go log line and emit it to the Python logger."""
    try:
        obj = json.loads(line)
    except Exception:
        py_logger.info(line)
        return
    level = str(obj.get("level", "")).lower()
    message = obj.get("message") or obj.get("msg") or ""
    extras: Dict[str, Any] = {}
    for k, v in obj.items():
        if k in ("level", "time", "message", "msg"):
            continue
        extras[k] = v
    py_logger.log(_def_level_map.get(level, logging.INFO), message, extra={"go": extras})


# Global registry for logger instances
_logger_registry: Dict[str, logging.Logger] = {}

# Event-driven log monitoring system
_log_listeners: List[Callable[[List], None]] = []
_listener_thread: Optional[threading.Thread] = None
_listener_active: bool = False


def _start_log_listener() -> None:
    """Start background thread to drain Go log buffer and forward to Python loggers."""
    global _listener_thread, _listener_active
    if _listener_active and _listener_thread and _listener_thread.is_alive():
        return
    _listener_active = True

    def _run() -> None:
        # Drain loop: wait for entries with timeout to allow graceful shutdown
        while _listener_active:
            try:
                entries = linksocks.WaitForLogEntries(2000)  # wait up to 2s
            except Exception:
                # Backoff on unexpected errors to avoid busy loop
                time.sleep(0.2)
                continue

            if not entries:
                continue

            # Iterate returned entries; handle both attr and dict styles
            for entry in entries:
                try:
                    logger_id = getattr(entry, "LoggerID", None)
                    if logger_id is None and isinstance(entry, dict):
                        logger_id = entry.get("LoggerID")

                    message = getattr(entry, "Message", None)
                    if message is None and isinstance(entry, dict):
                        message = entry.get("Message")

                    if not message:
                        continue

                    py_logger = _logger_registry.get(str(logger_id)) or _logger
                    _emit_go_log(py_logger, str(message))
                except Exception:
                    # Never let logging path crash the listener
                    continue

    _listener_thread = threading.Thread(target=_run, name="linksocks-go-log-listener", daemon=True)
    _listener_thread.start()


def _stop_log_listener() -> None:
    """Stop the background log listener thread."""
    global _listener_active
    _listener_active = False
    try:
        # Unblock WaitForLogEntries callers
        linksocks.CancelLogWaiters()
    except Exception:
        pass


class BufferZerologLogger:
    """Buffer-based logger system for Go bindings."""
    
    def __init__(self, py_logger: logging.Logger, logger_id: str):
        self.py_logger = py_logger
        self.logger_id = logger_id
        # Ensure background listener is running
        _start_log_listener()

        # Prefer Go logger with explicit ID so we can map entries back
        try:
            # Newer binding that tags entries with our provided ID
            self.go_logger = linksocks.NewLoggerWithID(self.logger_id)
        except Exception:
            # Fallback to older API; if present, still try callback path
            try:
                def log_callback(line: str) -> None:
                    _emit_go_log(py_logger, line)

                self.go_logger = linksocks.NewLogger(log_callback)
            except Exception:
                # As a last resort, create a default Go logger
                self.go_logger = linksocks.NewLoggerWithID(self.logger_id)  # may still raise; surface to caller
        _logger_registry[logger_id] = py_logger
    
    def cleanup(self):
        """Clean up logger resources."""
        if self.logger_id in _logger_registry:
            del _logger_registry[self.logger_id]


@dataclass
class ReverseTokenResult:
    """Result of adding a reverse token."""
    token: str
    port: int


class _SnakePassthrough:
    """Mixin to map snake_case attribute access to underlying CamelCase.
    
    Only used when an explicit Pythonic method/attribute is not defined.
    """

    def __getattr__(self, name: str) -> Any:
        raw = super().__getattribute__("_raw")  # type: ignore[attr-defined]
        camel = _snake_to_camel(name)
        try:
            return getattr(raw, camel)
        except AttributeError:
            raise

    def __dir__(self) -> List[str]:
        # Expose snake_case versions of underlying CamelCase for IDEs
        names = set(super().__dir__())
        try:
            raw = super().__getattribute__("_raw")  # type: ignore[attr-defined]
            for attr in dir(raw):
                if not attr or attr.startswith("_"):
                    continue
                names.add(_camel_to_snake(attr))
        except Exception:
            pass
        return sorted(names)


def set_log_level(level: Union[int, str]) -> None:
    """Set the global log level for linksocks."""
    if isinstance(level, str):
        level = getattr(logging, level.upper())
    _logger.setLevel(level)