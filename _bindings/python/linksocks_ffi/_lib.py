from __future__ import annotations

import json
import os
import sys
from dataclasses import dataclass
from pathlib import Path
from typing import Any, Optional

from cffi import FFI


_ffi = FFI()

_ffi.cdef(
    """
    typedef struct linksocks_buf {
        uint8_t* data;
        int64_t len;
    } linksocks_buf;

    void linksocks_free(void* p);
    void linksocks_buf_free(linksocks_buf b);

    char* linksocks_version(void);
    int64_t linksocks_seconds(void);
    char* linksocks_parse_duration(const char* s, int64_t* out);

    char* linksocks_wait_for_log_entries(int64_t timeoutMs, linksocks_buf* out);
    void linksocks_cancel_log_waiters(void);

    char* linksocks_server_new(const char* cfgJSON, uint64_t* out);
    char* linksocks_server_wait_ready(uint64_t h, int64_t timeoutNs);
    char* linksocks_server_close(uint64_t h);
    char* linksocks_server_add_forward_token(uint64_t h, const char* token);
    char* linksocks_server_add_reverse_token(uint64_t h, const char* optsJSON);
    char* linksocks_server_remove_token(uint64_t h, const char* token, int* out);
    char* linksocks_server_add_connector_token(uint64_t h, const char* connector, const char* reverseToken);

    char* linksocks_client_new(const char* token, const char* cfgJSON, uint64_t* out);
    char* linksocks_client_wait_ready(uint64_t h, int64_t timeoutNs);
    char* linksocks_client_close(uint64_t h);
    char* linksocks_client_add_connector(uint64_t h, const char* token);
    """
)


def _detect_lib_name() -> str:
    if sys.platform.startswith("linux"):
        return "liblinksocks_ffi.so"
    if sys.platform == "darwin":
        return "liblinksocks_ffi.dylib"
    if os.name == "nt":
        return "linksocks_ffi.dll"
    return "liblinksocks_ffi.so"


def _load_library() -> Any:
    override = os.environ.get("LINKSOCKS_FFI_LIB")
    if override:
        return _ffi.dlopen(override)

    libname = _detect_lib_name()

    # 1) next to this file
    here = Path(__file__).resolve().parent
    cand = here / libname
    if cand.exists():
        return _ffi.dlopen(str(cand))

    # 2) package root (wheel may place under data)
    pkg_root = here.parent
    cand2 = pkg_root / libname
    if cand2.exists():
        return _ffi.dlopen(str(cand2))

    # 3) fallback to loader search path
    return _ffi.dlopen(libname)


_lib = _load_library()


class LinkSocksError(RuntimeError):
    pass


def _raise_if_err(err_ptr) -> None:
    if err_ptr == _ffi.NULL:
        return
    try:
        msg = _ffi.string(err_ptr).decode("utf-8", errors="replace")
    finally:
        _lib.linksocks_free(err_ptr)
    raise LinkSocksError(msg)


def _take_string(ptr) -> str:
    if ptr == _ffi.NULL:
        return ""
    try:
        return _ffi.string(ptr).decode("utf-8", errors="replace")
    finally:
        _lib.linksocks_free(ptr)


def _take_buf(b) -> bytes:
    if b.data == _ffi.NULL or b.len == 0:
        return b""
    try:
        return bytes(_ffi.buffer(b.data, int(b.len)))
    finally:
        _lib.linksocks_buf_free(b)


def version() -> str:
    return _take_string(_lib.linksocks_version())


def seconds() -> int:
    return int(_lib.linksocks_seconds())


def parse_duration(s: str) -> int:
    out = _ffi.new("int64_t*")
    err = _lib.linksocks_parse_duration(s.encode("utf-8"), out)
    _raise_if_err(err)
    return int(out[0])


def wait_for_log_entries(timeout_ms: int) -> list[dict[str, Any]]:
    out = _ffi.new("linksocks_buf*")
    err = _lib.linksocks_wait_for_log_entries(int(timeout_ms), out)
    _raise_if_err(err)
    raw = _take_buf(out[0])
    if not raw:
        return []
    try:
        val = json.loads(raw.decode("utf-8", errors="replace"))
        if isinstance(val, list):
            return list(val)
        return []
    except Exception:
        return []


def cancel_log_waiters() -> None:
    _lib.linksocks_cancel_log_waiters()


@dataclass
class ReverseTokenResult:
    token: str
    port: int


class Server:
    def __init__(self, cfg: dict[str, Any]):
        out = _ffi.new("uint64_t*")
        payload = json.dumps(cfg, separators=(",", ":")).encode("utf-8")
        err = _lib.linksocks_server_new(payload, out)
        _raise_if_err(err)
        self._h = int(out[0])

    def wait_ready(self, timeout_ns: int) -> None:
        err = _lib.linksocks_server_wait_ready(self._h, int(timeout_ns))
        _raise_if_err(err)

    def close(self) -> None:
        if getattr(self, "_h", 0):
            err = _lib.linksocks_server_close(self._h)
            self._h = 0
            _raise_if_err(err)

    def add_forward_token(self, token: str) -> str:
        ptr = _lib.linksocks_server_add_forward_token(self._h, token.encode("utf-8"))
        return _take_string(ptr)

    def add_reverse_token(self, opts: dict[str, Any]) -> ReverseTokenResult:
        payload = json.dumps(opts, separators=(",", ":")).encode("utf-8")
        ptr = _lib.linksocks_server_add_reverse_token(self._h, payload)
        s = _take_string(ptr)
        obj = json.loads(s)
        return ReverseTokenResult(token=str(obj.get("Token") or obj.get("token") or ""), port=int(obj.get("Port") or obj.get("port") or 0))

    def remove_token(self, token: str) -> bool:
        out = _ffi.new("int*")
        err = _lib.linksocks_server_remove_token(self._h, token.encode("utf-8"), out)
        _raise_if_err(err)
        return bool(out[0])

    def add_connector_token(self, connector_token: str, reverse_token: str) -> str:
        ptr = _lib.linksocks_server_add_connector_token(
            self._h,
            connector_token.encode("utf-8"),
            reverse_token.encode("utf-8"),
        )
        return _take_string(ptr)


class Client:
    def __init__(self, token: str, cfg: dict[str, Any]):
        out = _ffi.new("uint64_t*")
        payload = json.dumps(cfg, separators=(",", ":")).encode("utf-8")
        err = _lib.linksocks_client_new(token.encode("utf-8"), payload, out)
        _raise_if_err(err)
        self._h = int(out[0])

    def wait_ready(self, timeout_ns: int) -> None:
        err = _lib.linksocks_client_wait_ready(self._h, int(timeout_ns))
        _raise_if_err(err)

    def close(self) -> None:
        if getattr(self, "_h", 0):
            err = _lib.linksocks_client_close(self._h)
            self._h = 0
            _raise_if_err(err)

    def add_connector(self, connector_token: str) -> str:
        ptr = _lib.linksocks_client_add_connector(self._h, connector_token.encode("utf-8"))
        return _take_string(ptr)
