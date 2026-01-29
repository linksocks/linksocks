"""Base classes and utilities for linksocks (gopy backend)."""

from __future__ import annotations

import json
import logging
import threading
import time
from dataclasses import dataclass
from datetime import timedelta
from typing import Any, Callable, Dict, List, Optional, Union

from linksockslib import linksocks as _gopy_linksocks


_logger = logging.getLogger(__name__)

DurationLike = Union[int, float, timedelta, str]


def _snake_to_camel(name: str) -> str:
    parts = name.split("_")
    return "".join(p.capitalize() for p in parts if p)


def _camel_to_snake(name: str) -> str:
    out: List[str] = []
    for ch in name:
        if ch.isupper() and out:
            out.append("_")
        out.append(ch.lower())
    return "".join(out)


def _to_duration(value: Optional[DurationLike]) -> Any:
    if value is None:
        return 0
    if isinstance(value, timedelta):
        seconds = value.total_seconds()
        return int(seconds * _gopy_linksocks.Second())
    if isinstance(value, (int, float)):
        return int(value * _gopy_linksocks.Second())
    if isinstance(value, str):
        try:
            return int(_gopy_linksocks.ParseDuration(value))
        except Exception as exc:
            raise ValueError(f"Invalid duration string: {value}") from exc
    raise TypeError(f"Unsupported duration type: {type(value)!r}")


@dataclass
class ReverseTokenResult:
    token: str
    port: int


class _SnakePassthrough:
    def __getattr__(self, name: str) -> Any:
        raw = super().__getattribute__("_raw")  # type: ignore[attr-defined]
        camel = _snake_to_camel(name)
        return getattr(raw, camel)

    def __dir__(self) -> List[str]:
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


class _GopyBackend:
    def DefaultServerOption(self) -> Any:
        return _gopy_linksocks.DefaultServerOption()

    def DefaultClientOption(self) -> Any:
        return _gopy_linksocks.DefaultClientOption()

    def DefaultReverseTokenOptions(self) -> Any:
        return _gopy_linksocks.DefaultReverseTokenOptions()

    def NewLinkSocksServer(self, opt: Any) -> Any:
        return _gopy_linksocks.NewLinkSocksServer(opt)

    def NewLinkSocksClient(self, token: str, opt: Any) -> Any:
        return _gopy_linksocks.NewLinkSocksClient(token, opt)

    def NewContextWithCancel(self) -> Any:
        return _gopy_linksocks.NewContextWithCancel()

    def NewLoggerWithID(self, logger_id: str) -> Any:
        return _gopy_linksocks.NewLoggerWithID(logger_id)

    def NewLogger(self, cb: Any) -> Any:
        return _gopy_linksocks.NewLogger(cb)

    def WaitForLogEntries(self, ms: int) -> list[Any]:
        return _gopy_linksocks.WaitForLogEntries(ms)

    def CancelLogWaiters(self) -> None:
        return _gopy_linksocks.CancelLogWaiters()


backend = _GopyBackend()


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


_logger_registry: Dict[str, logging.Logger] = {}

_log_listeners: List[Callable[[List], None]] = []
_listener_thread: Optional[threading.Thread] = None
_listener_active: bool = False


def _start_log_listener() -> None:
    global _listener_thread, _listener_active
    if _listener_active and _listener_thread and _listener_thread.is_alive():
        return
    _listener_active = True

    def _run() -> None:
        while _listener_active:
            entries = backend.WaitForLogEntries(2000)
            if not entries:
                continue
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
                    continue

    _listener_thread = threading.Thread(target=_run, name="linksocks-go-log-listener", daemon=True)
    _listener_thread.start()


def _stop_log_listener() -> None:
    global _listener_active
    _listener_active = False
    try:
        backend.CancelLogWaiters()
    except Exception:
        pass


class BufferZerologLogger:
    def __init__(self, py_logger: logging.Logger, logger_id: str):
        self.py_logger = py_logger
        self.logger_id = logger_id
        _start_log_listener()

        try:
            self.go_logger = backend.NewLoggerWithID(self.logger_id)
        except Exception:
            def log_callback(line: str) -> None:
                _emit_go_log(py_logger, line)

            self.go_logger = backend.NewLogger(log_callback)

        _logger_registry[logger_id] = py_logger

    def cleanup(self) -> None:
        if self.logger_id in _logger_registry:
            del _logger_registry[self.logger_id]


def set_log_level(level: Union[int, str]) -> None:
    if isinstance(level, str):
        level = getattr(logging, level.upper())
    _logger.setLevel(level)
