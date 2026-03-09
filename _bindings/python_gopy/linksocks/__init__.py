"""linksocks: SOCKS5 over WebSocket proxy library (gopy backend)."""

__version__ = "1.8.1"

from ._server import Server
from ._client import Client
from ._base import ReverseTokenResult, set_log_level

__all__ = ["Server", "Client", "ReverseTokenResult", "set_log_level"]
