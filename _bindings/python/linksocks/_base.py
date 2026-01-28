"""Base classes and utilities for linksocks.

This module contains shared functionality used by Server and Client classes.
"""

from __future__ import annotations

import json
import logging
import threading
import time
from dataclasses import dataclass
from datetime import timedelta
from typing import Any, Callable, Dict, List, Optional, Union

_BACKEND: str

try:
    from linksocks_ffi import parse_duration as _ffi_parse_duration
    from linksocks_ffi import seconds as _ffi_seconds
    from linksocks_ffi import cancel_log_waiters as _ffi_cancel_log_waiters
    from linksocks_ffi import wait_for_log_entries as _ffi_wait_for_log_entries

    _BACKEND = "ffi"
except Exception:
    _ffi_parse_duration = None
    _ffi_seconds = None
    _BACKEND = "gopy"

    from linksockslib import linksocks as _gopy_linksocks  # type: ignore

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
    
    Returns an int since Go's time.Duration is int64.
    """
    if value is None:
        return 0
    if isinstance(value, timedelta):
        seconds = value.total_seconds()
        if _BACKEND == "ffi":
            return int(seconds * int(_ffi_seconds()))  # type: ignore[misc]
        return int(seconds * _gopy_linksocks.Second())
    if isinstance(value, (int, float)):
        if _BACKEND == "ffi":
            return int(value * int(_ffi_seconds()))  # type: ignore[misc]
        return int(value * _gopy_linksocks.Second())
    if isinstance(value, str):
        try:
            if _BACKEND == "ffi":
                return int(_ffi_parse_duration(value))  # type: ignore[misc]
            return int(_gopy_linksocks.ParseDuration(value))
        except Exception as exc:
            raise ValueError(f"Invalid duration string: {value}") from exc
    raise TypeError(f"Unsupported duration type: {type(value)!r}")


class _NoopLogger:
    def __call__(self, *args: Any, **kwargs: Any) -> None:
        return None


class _DummyManagedLogger:
    def __init__(self, py_logger: logging.Logger, logger_id: str):
        self.py_logger = py_logger
        self.logger_id = logger_id
        self.go_logger = _NoopLogger()

    def cleanup(self) -> None:
        return None


def _json_key(snake: str) -> str:
    return snake


class _FFIReverseTokenOptions:
    def __init__(self) -> None:
        self.Token = ""
        self.Port: Optional[int] = None
        self.Username = ""
        self.Password = ""
        self.AllowManageConnector: Optional[bool] = None


class _FFIServerOption:
    def __init__(self) -> None:
        self._cfg: Dict[str, Any] = {}

    def WithLogger(self, logger: Any) -> None:
        logger_id: Optional[str] = None
        if isinstance(logger, str):
            logger_id = logger
        else:
            logger_id = getattr(logger, "logger_id", None)
        if logger_id:
            self._cfg[_json_key("logger_id")] = str(logger_id)

    def WithWSHost(self, v: str) -> None:
        self._cfg[_json_key("ws_host")] = v

    def WithWSPort(self, v: int) -> None:
        self._cfg[_json_key("ws_port")] = int(v)

    def WithSocksHost(self, v: str) -> None:
        self._cfg[_json_key("socks_host")] = v

    def WithSocksWaitClient(self, v: bool) -> None:
        self._cfg[_json_key("socks_wait_client")] = bool(v)

    def WithPortPool(self, _v: Any) -> None:
        raise NotImplementedError("port_pool is not supported by the cffi backend")

    def WithBufferSize(self, v: int) -> None:
        self._cfg[_json_key("buffer_size")] = int(v)

    def WithAPI(self, v: str) -> None:
        self._cfg[_json_key("api_key")] = v

    def WithChannelTimeout(self, v: int) -> None:
        self._cfg[_json_key("channel_timeout_ns")] = int(v)

    def WithConnectTimeout(self, v: int) -> None:
        self._cfg[_json_key("connect_timeout_ns")] = int(v)

    def WithFastOpen(self, v: bool) -> None:
        self._cfg[_json_key("fast_open")] = bool(v)

    def WithUpstreamProxy(self, v: str) -> None:
        self._cfg[_json_key("upstream_proxy")] = v

    def WithUpstreamAuth(self, username: str, password: str) -> None:
        self._cfg[_json_key("upstream_username")] = username
        self._cfg[_json_key("upstream_password")] = password

    def to_cfg(self) -> Dict[str, Any]:
        return dict(self._cfg)


class _FFIClientOption:
    def __init__(self) -> None:
        self._cfg: Dict[str, Any] = {}

    def WithLogger(self, logger: Any) -> None:
        logger_id: Optional[str] = None
        if isinstance(logger, str):
            logger_id = logger
        else:
            logger_id = getattr(logger, "logger_id", None)
        if logger_id:
            self._cfg[_json_key("logger_id")] = str(logger_id)

    def WithWSURL(self, v: str) -> None:
        self._cfg[_json_key("ws_url")] = v

    def WithReverse(self, v: bool) -> None:
        self._cfg[_json_key("reverse")] = bool(v)

    def WithSocksHost(self, v: str) -> None:
        self._cfg[_json_key("socks_host")] = v

    def WithSocksPort(self, v: int) -> None:
        self._cfg[_json_key("socks_port")] = int(v)

    def WithSocksUsername(self, v: str) -> None:
        self._cfg[_json_key("socks_username")] = v

    def WithSocksPassword(self, v: str) -> None:
        self._cfg[_json_key("socks_password")] = v

    def WithSocksWaitServer(self, v: bool) -> None:
        self._cfg[_json_key("socks_wait_server")] = bool(v)

    def WithReconnect(self, v: bool) -> None:
        self._cfg[_json_key("reconnect")] = bool(v)

    def WithReconnectDelay(self, v: int) -> None:
        self._cfg[_json_key("reconnect_delay_ns")] = int(v)

    def WithBufferSize(self, v: int) -> None:
        self._cfg[_json_key("buffer_size")] = int(v)

    def WithChannelTimeout(self, v: int) -> None:
        self._cfg[_json_key("channel_timeout_ns")] = int(v)

    def WithConnectTimeout(self, v: int) -> None:
        self._cfg[_json_key("connect_timeout_ns")] = int(v)

    def WithThreads(self, v: int) -> None:
        self._cfg[_json_key("threads")] = int(v)

    def WithFastOpen(self, v: bool) -> None:
        self._cfg[_json_key("fast_open")] = bool(v)

    def WithUpstreamProxy(self, v: str) -> None:
        self._cfg[_json_key("upstream_proxy")] = v

    def WithUpstreamAuth(self, username: str, password: str) -> None:
        self._cfg[_json_key("upstream_username")] = username
        self._cfg[_json_key("upstream_password")] = password

    def WithNoEnvProxy(self, v: bool) -> None:
        self._cfg[_json_key("no_env_proxy")] = bool(v)

    def to_cfg(self) -> Dict[str, Any]:
        return dict(self._cfg)


class _FFIRawServer:
    def __init__(self, cfg: Dict[str, Any]):
        from linksocks_ffi import Server as FFIServer

        self._srv = FFIServer(cfg)

    def WaitReady(self, *, ctx: Any = None, timeout: int = 0) -> None:
        self._srv.wait_ready(int(timeout))

    def Close(self) -> None:
        self._srv.close()

    def AddForwardToken(self, token: str = "") -> str:
        return self._srv.add_forward_token(token or "")

    def AddReverseToken(self, opts: Any) -> Any:
        payload: Dict[str, Any] = {}
        token = getattr(opts, "Token", "")
        if token:
            payload["token"] = token
        port = getattr(opts, "Port", None)
        if port is not None:
            payload["port"] = int(port)
        username = getattr(opts, "Username", "")
        if username:
            payload["username"] = username
        password = getattr(opts, "Password", "")
        if password:
            payload["password"] = password
        allow = getattr(opts, "AllowManageConnector", None)
        if allow is not None:
            payload["allow_manage_connector"] = bool(allow)
        return self._srv.add_reverse_token(payload)

    def RemoveToken(self, token: str) -> bool:
        return bool(self._srv.remove_token(token))

    def AddConnectorToken(self, connector: str, reverse_token: str) -> str:
        return self._srv.add_connector_token(connector, reverse_token)


class _FFIRawClient:
    def __init__(self, token: str, cfg: Dict[str, Any]):
        from linksocks_ffi import Client as FFIClient

        self.SocksPort = cfg.get("socks_port")
        self._cli = FFIClient(token, cfg)

    def WaitReady(self, *, ctx: Any = None, timeout: int = 0) -> None:
        self._cli.wait_ready(int(timeout))

    def Close(self) -> None:
        self._cli.close()

    def AddConnector(self, token: str) -> str:
        return self._cli.add_connector(token)


class _FFIContextWithCancel:
    def __init__(self) -> None:
        self._cancelled = False

    def Context(self) -> None:
        return None

    def Cancel(self) -> None:
        self._cancelled = True


class _FFIBackend:
    def DefaultServerOption(self) -> Any:
        return _FFIServerOption()

    def DefaultClientOption(self) -> Any:
        return _FFIClientOption()

    def DefaultReverseTokenOptions(self) -> Any:
        return _FFIReverseTokenOptions()

    def NewLinkSocksServer(self, opt: Any) -> Any:
        return _FFIRawServer(opt.to_cfg())

    def NewLinkSocksClient(self, token: str, opt: Any) -> Any:
        return _FFIRawClient(token, opt.to_cfg())

    def NewContextWithCancel(self) -> Any:
        return _FFIContextWithCancel()

    def NewLoggerWithID(self, _logger_id: str) -> Any:
        return str(_logger_id)

    def NewLogger(self, _cb: Any) -> Any:
        return f"logger_{id(_cb)}"

    def WaitForLogEntries(self, ms: int) -> list[Any]:
        try:
            entries = _ffi_wait_for_log_entries(int(ms))  # type: ignore[misc]
            if not entries:
                return []
            out: List[Any] = []
            for e in entries:
                if isinstance(e, dict):
                    out.append({
                        "LoggerID": e.get("LoggerID") or e.get("logger_id") or "",
                        "Message": e.get("Message") or e.get("message") or "",
                        "Time": e.get("Time") or e.get("time") or 0,
                    })
            return out
        except Exception:
            time.sleep(max(int(ms), 1) / 1000.0)
            return []

    def CancelLogWaiters(self) -> None:
        try:
            _ffi_cancel_log_waiters()  # type: ignore[misc]
        except Exception:
            return None

    def Second(self) -> int:
        return int(_ffi_seconds())  # type: ignore[misc]

    def ParseDuration(self, s: str) -> int:
        return int(_ffi_parse_duration(s))  # type: ignore[misc]


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

    def Second(self) -> int:
        return int(_gopy_linksocks.Second())

    def ParseDuration(self, s: str) -> int:
        return int(_gopy_linksocks.ParseDuration(s))


backend = _FFIBackend() if _BACKEND == "ffi" else _GopyBackend()


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
            entries = backend.WaitForLogEntries(2000)

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
        backend.CancelLogWaiters()
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
            self.go_logger = backend.NewLoggerWithID(self.logger_id)
        except Exception:
            # Fallback to older API; if present, still try callback path
            try:
                def log_callback(line: str) -> None:
                    _emit_go_log(py_logger, line)

                self.go_logger = backend.NewLogger(log_callback)
            except Exception:
                # As a last resort, create a default Go logger
                self.go_logger = backend.NewLoggerWithID(self.logger_id)  # may still raise; surface to caller
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