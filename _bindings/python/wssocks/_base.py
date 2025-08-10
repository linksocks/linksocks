"""
Base classes and utilities for wssocks.

This module contains shared functionality used by Server and Client classes.
"""

from __future__ import annotations

import json
import logging
import asyncio
from dataclasses import dataclass
from datetime import timedelta
from typing import Any, Callable, Dict, Iterable, Optional, Tuple, Union, List

# Underlying Go bindings module (generated)
import wssockslib  # type: ignore

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
        return seconds * wssockslib.Second()
    if isinstance(value, (int, float)):
        return value * wssockslib.Second()
    if isinstance(value, str):
        try:
            return wssockslib.ParseDuration(value)
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
_listener_thread = None
_listener_active = False


class BufferZerologLogger:
    """Buffer-based logger system for Go bindings."""
    
    def __init__(self, py_logger: logging.Logger, logger_id: str):
        self.py_logger = py_logger
        self.logger_id = logger_id
        # Create Go logger with callback function
        def log_callback(line: str) -> None:
            _emit_go_log(py_logger, line)
        
        self.go_logger = wssockslib.NewLogger(log_callback)
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
    """Set the global log level for wssocks."""
    if isinstance(level, str):
        level = getattr(logging, level.upper())
    _logger.setLevel(level)