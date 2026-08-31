import asyncio
import time
import logging
import pytest
from click.testing import CliRunner

from .utils import (
    get_free_port,
    has_ipv6_support,
    async_assert_udp_connection,
    async_assert_web_connection,
)


start_time_limit = 60


def _ws_url(port: int) -> str:
    return f"ws://localhost:{port}"


# ==================== Basic ====================


def test_import_wrapper():
    from linksocks import Server, Client

    assert Server is not None and Client is not None


def test_server_connector_wait_provider_option(monkeypatch):
    import linksocks._server as server_module

    captured = {}

    class DummyOption:
        def WithLogger(self, _logger):
            captured["logger"] = True

        def WithConnectorWait(self, value):
            captured["connector_wait"] = value

    class DummyLogger:
        def __init__(self, py_logger, logger_id):
            self.py_logger = py_logger
            self.go_logger = object()

        def cleanup(self):
            return None

    class DummyRawServer:
        def Close(self):
            return None

    monkeypatch.setattr(server_module, "BufferZerologLogger", DummyLogger)
    monkeypatch.setattr(server_module.backend, "DefaultServerOption", lambda: DummyOption())
    monkeypatch.setattr(server_module.backend, "NewLinkSocksServer", lambda opt: DummyRawServer())

    srv = server_module.Server(connector_wait_provider="250ms")
    try:
        assert captured["logger"] is True
        assert captured["connector_wait"] == server_module._to_duration("250ms")
    finally:
        srv.close()


def test_ffi_server_option_connector_wait_serialization():
    from linksocks._base import _FFIServerOption, _to_duration

    opt = _FFIServerOption()
    opt.WithConnectorWait(_to_duration("250ms"))

    assert opt.to_cfg()["connector_wait_ns"] == _to_duration("250ms")


def test_cli_parse_access_rules():
    import click
    from linksocks._cli import _parse_access_rules
    from linksocks._base import AccessRule

    assert _parse_access_rules(()) is None

    rules = _parse_access_rules(("192.168.1.0/24:22", "10.0.0.0/8:8000-9000", "fe80::/10:443"))
    assert rules == [
        AccessRule(addrs=["192.168.1.0/24"], ports=[22]),
        AccessRule(addrs=["10.0.0.0/8"], ports=[(8000, 9000)]),
        AccessRule(addrs=["fe80::/10"], ports=[443]),
    ]

    with pytest.raises(click.ClickException):
        _parse_access_rules(("192.168.1.0/24",))  # missing port
    with pytest.raises(click.ClickException):
        _parse_access_rules((":22",))  # missing addr
    with pytest.raises(click.ClickException):
        _parse_access_rules(("192.168.1.0/24:",))  # empty port
    with pytest.raises(click.ClickException):
        _parse_access_rules(("192.168.1.0/24:abc",))  # bad port
    with pytest.raises(click.ClickException):
        _parse_access_rules(("192.168.1.0/24:70000",))  # out of range
    with pytest.raises(click.ClickException):
        _parse_access_rules(("192.168.1.0/24:100-22",))  # reversed range


def test_ffi_option_access_control_serialization():
    from linksocks._base import _FFIServerOption, _FFIClientOption, AccessRule

    rule = AccessRule(addrs=["192.168.1.0/24"], ports=[22, (90, 150)])
    expected = [{"addrs": ["192.168.1.0/24"], "ports": [22, [90, 150]]}]

    sopt = _FFIServerOption()
    sopt.WithEntryAccessControl([rule])
    sopt.WithDialAccessControl([rule])
    assert sopt.to_cfg()["entry_access_control"] == expected
    assert sopt.to_cfg()["dial_access_control"] == expected

    sopt.WithAPIKeys(["restricted"])
    assert sopt.to_cfg()["api_keys"] == ["restricted"]

    copt = _FFIClientOption()
    copt.WithEntryAccessControl([rule])
    copt.WithDialAccessControl([rule])
    assert copt.to_cfg()["entry_access_control"] == expected
    assert copt.to_cfg()["dial_access_control"] == expected


def test_server_entry_dial_access_control_options(monkeypatch):
    import linksocks._server as server_module
    from linksocks._base import AccessRule

    captured = {}

    class DummyOption:
        def WithLogger(self, _logger):
            captured["logger"] = True

        def WithEntryAccessControl(self, rules):
            captured["entry"] = rules

        def WithDialAccessControl(self, rules):
            captured["dial"] = rules

        def WithAPI(self, key):
            captured["api_key"] = key

        def WithAPIKeys(self, keys):
            captured["api_keys"] = keys

    class DummyLogger:
        def __init__(self, py_logger, logger_id):
            self.py_logger = py_logger
            self.go_logger = object()

        def cleanup(self):
            return None

    class DummyRawServer:
        def Close(self):
            return None

    monkeypatch.setattr(server_module, "BufferZerologLogger", DummyLogger)
    monkeypatch.setattr(server_module.backend, "DefaultServerOption", lambda: DummyOption())
    monkeypatch.setattr(server_module.backend, "NewLinkSocksServer", lambda opt: DummyRawServer())

    rules = [AccessRule(addrs=["192.168.1.0/24"], ports=[22, (90, 150)])]
    srv = server_module.Server(
        entry_access_control=rules,
        dial_access_control=rules,
        api_key="primary",
        api_keys=["restricted"],
    )
    try:
        assert captured["logger"] is True
        assert captured["entry"] == rules
        assert captured["dial"] == rules
        assert captured["api_key"] == "primary"
        assert captured["api_keys"] == ["restricted"]
    finally:
        srv.close()


def test_client_entry_access_control_options(monkeypatch):
    import linksocks._client as client_module
    from linksocks._base import AccessRule

    captured = {}

    class DummyOption:
        def WithLogger(self, _logger):
            captured["logger"] = True

        def WithEntryAccessControl(self, rules):
            captured["entry"] = rules

        def WithDialAccessControl(self, rules):
            captured["dial"] = rules

    class DummyManagedLogger:
        def __init__(self, py_logger, logger_id):
            self.py_logger = py_logger
            self.go_logger = object()

        def cleanup(self):
            return None

    class DummyRawClient:
        def Close(self):
            return None

    monkeypatch.setattr(client_module, "BufferZerologLogger", DummyManagedLogger)
    monkeypatch.setattr(client_module.backend, "DefaultClientOption", lambda: DummyOption())
    monkeypatch.setattr(client_module.backend, "NewLinkSocksClient", lambda token, opt: DummyRawClient())

    rules = AccessRule(addrs=["10.0.0.0/8"], ports=[(8000, 9000)])
    client = client_module.Client("token", entry_access_control=[rules], dial_access_control=[rules])
    try:
        assert captured["logger"] is True
        assert captured["entry"] == [rules]
        assert captured["dial"] == [rules]
    finally:
        client.close()


def test_client_remote_protocol_version(monkeypatch):
    import linksocks._client as client_module

    class DummyOption:
        def WithLogger(self, _logger):
            return None

    class DummyLogger:
        def __init__(self, py_logger, logger_id):
            self.py_logger = py_logger
            self.go_logger = object()

        def cleanup(self):
            return None

    class DummyRawClient:
        def GetRemoteProtocolVersion(self):
            return 2

        def Close(self):
            return None

    monkeypatch.setattr(client_module, "BufferZerologLogger", DummyLogger)
    monkeypatch.setattr(client_module.backend, "DefaultClientOption", lambda: DummyOption())
    monkeypatch.setattr(client_module.backend, "NewLinkSocksClient", lambda token, opt: DummyRawClient())

    client = client_module.Client("token")
    try:
        assert client.get_remote_protocol_version() == 2
    finally:
        client.close()


def test_wrapper_user_api_forwarding(monkeypatch):
    import linksocks._client as client_module
    import linksocks._server as server_module

    class DummyOption:
        def WithLogger(self, _logger):
            return None

    class DummyLogger:
        def __init__(self, py_logger, logger_id):
            self.py_logger = py_logger
            self.go_logger = object()

        def cleanup(self):
            return None

    class DummyRawClient:
        def RemoveConnector(self, token):
            self.removed = token

        def DataPath(self):
            return "data"

        def ChannelPath(self, channel_id):
            return f"channel:{channel_id}"

        def GetServerToken(self):
            return "server"

        def GetRTT(self):
            return -1

        def GetDirectRTT(self):
            return -1

        def GetPartnersCount(self):
            return 0

        def Close(self):
            return None

    class DummyRawServer:
        def AddForwardTokenWithRules(self, token, rules):
            self.forward = (token, rules)
            return token

        def AddConnectorTokenWithRules(self, connector, reverse, rules):
            self.connector = (connector, reverse, rules)
            return connector

        def GetConnectorWaitCount(self, token):
            return 0

        def GetClientCount(self):
            return 0

        def HasClients(self):
            return False

        def GetTokenClientCount(self, token):
            return 0

        def Close(self):
            return None

    monkeypatch.setattr(client_module, "BufferZerologLogger", DummyLogger)
    monkeypatch.setattr(client_module.backend, "DefaultClientOption", lambda: DummyOption())
    monkeypatch.setattr(client_module.backend, "NewLinkSocksClient", lambda token, opt: DummyRawClient())
    client = client_module.Client("token")
    try:
        client.remove_connector("connector")
        assert client.data_path() == "data"
        assert client.channel_path("channel") == "channel:channel"
        assert client.get_server_token() == "server"
        assert client.get_rtt() == -1
        assert client.get_direct_rtt() == -1
        assert client.get_partners_count() == 0
    finally:
        client.close()

    monkeypatch.setattr(server_module, "BufferZerologLogger", DummyLogger)
    monkeypatch.setattr(server_module.backend, "DefaultServerOption", lambda: DummyOption())
    monkeypatch.setattr(server_module.backend, "NewLinkSocksServer", lambda opt: DummyRawServer())
    server = server_module.Server()
    try:
        assert server.add_forward_token_with_rules("forward", []) == "forward"
        assert server.add_connector_token_with_rules("connector", "reverse", []) == "connector"
        assert server.get_connector_wait_count("reverse") == 0
        assert server.get_client_count() == 0
        assert not server.has_clients()
        assert server.get_token_client_count("forward") == 0
    finally:
        server.close()


def test_ffi_reverse_token_rules_payload():
    from linksocks._base import _FFIRawServer, _FFIReverseTokenOptions, AccessRule

    class DummyFFIServer:
        def __init__(self):
            self.payload = None

        def add_reverse_token(self, payload):
            self.payload = payload
            return type("Res", (), {"token": "tok", "port": 1080})()

    raw = object.__new__(_FFIRawServer)
    raw._srv = DummyFFIServer()

    opts = _FFIReverseTokenOptions()
    opts.Token = "tok"
    opts.Rules = [AccessRule(addrs=["192.168.1.1-255"], ports=[(90, 150)])]

    res = raw.AddReverseToken(opts)
    assert res.token == "tok"
    assert raw._srv.payload["token"] == "tok"
    assert raw._srv.payload["rules"] == [{"addrs": ["192.168.1.1-255"], "ports": [[90, 150]]}]


def test_server_add_reverse_token_rules(monkeypatch):
    import linksocks._server as server_module
    from linksocks._base import _FFIRawServer, AccessRule

    captured = {}

    class DummyOption:
        def WithLogger(self, _logger):
            return None

    class DummyLogger:
        def __init__(self, py_logger, logger_id):
            self.py_logger = py_logger
            self.go_logger = object()

        def cleanup(self):
            return None

    class DummyFFIServer:
        def add_reverse_token(self, payload):
            captured["payload"] = payload
            return type("Res", (), {"token": "tok", "port": 1080})()

    class DummyRawServer:
        def __init__(self, _opt):
            raw = object.__new__(_FFIRawServer)
            raw._srv = DummyFFIServer()
            self.raw = raw

        def AddReverseToken(self, opts):
            return self.raw.AddReverseToken(opts)

        def Close(self):
            return None

    monkeypatch.setattr(server_module, "BufferZerologLogger", DummyLogger)
    monkeypatch.setattr(server_module.backend, "DefaultServerOption", lambda: DummyOption())
    monkeypatch.setattr(server_module.backend, "NewLinkSocksServer", lambda opt: DummyRawServer(opt))

    rules = AccessRule(addrs=["192.168.1.1-255"], ports=[80])
    srv = server_module.Server()
    try:
        res = srv.add_reverse_token(token="tok", port=1080, rules=[rules])
        assert res.token == "tok"
        assert captured["payload"]["token"] == "tok"
        assert captured["payload"]["rules"] == [{"addrs": ["192.168.1.1-255"], "ports": [80]}]
    finally:
        srv.close()


def test_client_command_defaults_to_anonymous_token(monkeypatch):
    import linksocks._cli as cli_module

    captured = {}

    class DummyClient:
        def __init__(self, token, **kwargs):
            captured["token"] = token
            captured["kwargs"] = kwargs

        async def __aenter__(self):
            return self

        async def __aexit__(self, exc_type, exc, tb):
            return None

    monkeypatch.setattr(cli_module, "init_logging", lambda *args, **kwargs: None)
    monkeypatch.setattr("linksocks._client.Client", DummyClient)
    monkeypatch.setattr(asyncio, "Future", lambda: asyncio.sleep(0))

    runner = CliRunner()
    result = runner.invoke(cli_module.client, ["-u", "ws://localhost:8765"])

    assert result.exit_code == 0
    assert captured["token"] == "anonymous"


def test_client_command_surfaces_anonymous_auth_hint(monkeypatch):
    import linksocks._cli as cli_module

    class DummyClient:
        def __init__(self, token, **kwargs):
            self.token = token

        async def __aenter__(self):
            raise RuntimeError("authentication failed: please provide a token with -t or set LINKSOCKS_TOKEN")

        async def __aexit__(self, exc_type, exc, tb):
            return None

    monkeypatch.setattr(cli_module, "init_logging", lambda *args, **kwargs: None)
    monkeypatch.setattr("linksocks._client.Client", DummyClient)

    runner = CliRunner()
    result = runner.invoke(cli_module.client, ["-u", "ws://localhost:8765"])

    assert result.exit_code != 0
    assert "authentication failed: please provide a token with -t or set LINKSOCKS_TOKEN" in result.output


def test_client_command_aborts_process_on_keyboard_interrupt(monkeypatch):
    import linksocks._cli as cli_module

    called = {}

    def _raise_keyboard_interrupt(coro):
        coro.close()
        raise KeyboardInterrupt()

    def _fake_abort():
        called["aborted"] = True
        raise SystemExit(130)

    monkeypatch.setattr(cli_module.asyncio, "run", _raise_keyboard_interrupt)
    monkeypatch.setattr(cli_module, "_abort_from_sigint", _fake_abort)

    runner = CliRunner()
    result = runner.invoke(cli_module.client, ["-u", "ws://localhost:8765"])

    assert called["aborted"] is True
    assert result.exit_code == 130


def test_server_command_aborts_process_on_keyboard_interrupt(monkeypatch):
    import linksocks._cli as cli_module

    called = {}

    def _raise_keyboard_interrupt(coro):
        coro.close()
        raise KeyboardInterrupt()

    def _fake_abort():
        called["aborted"] = True
        raise SystemExit(130)

    monkeypatch.setattr(cli_module.asyncio, "run", _raise_keyboard_interrupt)
    monkeypatch.setattr(cli_module, "_abort_from_sigint", _fake_abort)

    runner = CliRunner()
    result = runner.invoke(cli_module.server, [])

    assert called["aborted"] is True
    assert result.exit_code == 130


def test_intercept_handler_renders_go_fields(monkeypatch):
    import linksocks._logging_config as logging_config

    captured = {}

    class DummyOpt:
        def log(self, level, text):
            captured["level"] = level
            captured["text"] = text

    class DummyLogger:
        def level(self, name):
            class _Level:
                def __init__(self, level_name):
                    self.name = level_name

            return _Level(name)

        def opt(self, depth, exception):
            captured["depth"] = depth
            captured["exception"] = exception
            return DummyOpt()

    monkeypatch.setattr(logging_config, "logger", DummyLogger())

    handler = logging_config.InterceptHandler()
    record = logging.LogRecord(
        name="linksocks",
        level=logging.INFO,
        pathname=__file__,
        lineno=1,
        msg="Following WebSocket redirect",
        args=(),
        exc_info=None,
    )
    record.go = {"from": "ws://old", "to": "ws://new", "status": 302}

    handler.emit(record)

    assert captured["level"] == "INFO"
    assert captured["text"] == "Following WebSocket redirect from=ws://old status=302 to=ws://new"


def test_init_logging_uses_go_style_rich_palette(monkeypatch):
    import linksocks._logging_config as logging_config

    captured = {}

    def _fake_remove():
        captured["removed"] = True

    def _fake_add(handler, format, level):
        captured["handler"] = handler
        captured["format"] = format
        captured["level"] = level

    monkeypatch.setattr(logging_config, "apply_logging_adapter", lambda level: captured.setdefault("adapter", level))
    monkeypatch.setattr(logging_config.logger, "remove", _fake_remove)
    monkeypatch.setattr(logging_config.logger, "add", _fake_add)

    logging_config.init_logging(level=logging.DEBUG)

    handler = captured["handler"]
    assert captured["adapter"] == logging.DEBUG
    assert captured["removed"] is True
    assert captured["format"] == "{message}"
    assert captured["level"] == logging.DEBUG
    assert handler.highlighter.__class__.__name__ == "LinkSocksLogHighlighter"
    assert handler.markup is False


def test_client_close_cancels_context_before_close(monkeypatch):
    import linksocks._client as client_module

    calls = []

    class DummyOption:
        def WithLogger(self, _logger):
            return None

    class DummyManagedLogger:
        def __init__(self, py_logger, logger_id):
            self.py_logger = py_logger
            self.go_logger = object()

        def cleanup(self):
            calls.append("cleanup")

    class DummyRawClient:
        def Close(self):
            calls.append("close")

    class DummyContext:
        def Cancel(self):
            calls.append("cancel")

    monkeypatch.setattr(client_module, "BufferZerologLogger", DummyManagedLogger)
    monkeypatch.setattr(client_module.backend, "DefaultClientOption", lambda: DummyOption())
    monkeypatch.setattr(client_module.backend, "NewLinkSocksClient", lambda token, opt: DummyRawClient())

    client = client_module.Client("token")
    client._ctx = DummyContext()

    client.close()

    assert calls == ["cancel", "close", "cleanup"]


def test_client_async_close_cancels_context_before_close(monkeypatch):
    import linksocks._client as client_module

    calls = []

    class DummyOption:
        def WithLogger(self, _logger):
            return None

    class DummyManagedLogger:
        def __init__(self, py_logger, logger_id):
            self.py_logger = py_logger
            self.go_logger = object()

        def cleanup(self):
            calls.append("cleanup")

    class DummyRawClient:
        def Close(self):
            calls.append("close")

    class DummyContext:
        def Cancel(self):
            calls.append("cancel")

    monkeypatch.setattr(client_module, "BufferZerologLogger", DummyManagedLogger)
    monkeypatch.setattr(client_module.backend, "DefaultClientOption", lambda: DummyOption())
    monkeypatch.setattr(client_module.backend, "NewLinkSocksClient", lambda token, opt: DummyRawClient())

    client = client_module.Client("token")
    client._ctx = DummyContext()

    asyncio.run(client.async_close())

    assert calls == ["cancel", "close", "cleanup"]


def test_forward_basic(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_basic(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Token removal ====================


def test_forward_remove_token(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_web_connection(website, socks_port)
                srv.remove_token(token)
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, socks_port, timeout=3)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_reverse_remove_token(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_web_connection(website, res.port)
                srv.remove_token(res.token)
                with pytest.raises(Exception):
                    await async_assert_web_connection(website, res.port, timeout=3)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== IPv6 ====================


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_forward_ipv6(website_v6):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_web_connection(website_v6, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_reverse_ipv6(website_v6):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_web_connection(website_v6, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== UDP ====================


def test_udp_forward_proxy(udp_server):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_udp_connection(udp_server, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_udp_reverse_proxy(udp_server):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_udp_connection(udp_server, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_forward_proxy_v6(udp_server_v6):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, no_env_proxy=True) as cli:
                await async_assert_udp_connection(udp_server_v6, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


@pytest.mark.skipif(not has_ipv6_support(), reason="IPv6 is not supported on this system")
def test_udp_reverse_proxy_v6(udp_server_v6):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli:
                await async_assert_udp_connection(udp_server_v6, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Reconnect ====================


def test_forward_reconnect(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()

        srv1 = Server(ws_port=ws_port)
        try:
            token = await srv1.async_add_forward_token()
            await srv1.async_wait_ready(timeout=start_time_limit)

            async with Client(
                token,
                ws_url=_ws_url(ws_port),
                socks_port=socks_port,
                reconnect=True,
                reconnect_delay=1,
                no_env_proxy=True,
            ) as cli:
                await async_assert_web_connection(website, socks_port)

                await srv1.async_close()
                await asyncio.sleep(1)

                srv2 = Server(ws_port=ws_port)
                try:
                    await srv2.async_add_forward_token(token)
                    await srv2.async_wait_ready(timeout=start_time_limit)

                    await cli.async_wait_ready(timeout=start_time_limit)

                    deadline = time.monotonic() + 15
                    while True:
                        try:
                            await async_assert_web_connection(website, socks_port, timeout=3)
                            break
                        except Exception:
                            if time.monotonic() >= deadline:
                                raise
                            await asyncio.sleep(0.5)
                finally:
                    await srv2.async_close()
        finally:
            try:
                await srv1.async_close()
            except Exception:
                pass

    asyncio.run(asyncio.wait_for(_main(), 60))


def test_reverse_reconnect(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            # first client
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli1:
                await async_assert_web_connection(website, res.port)

            await asyncio.sleep(2)
            # second client
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as cli2:
                await asyncio.sleep(2)
                await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 45))


# ==================== Connector ====================


def test_connector(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            # add connector token
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)

            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as rcli:
                async with Client(connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), no_env_proxy=True) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_connector_autonomy(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token(allow_manage_connector=True)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as rcli:
                # client creates connector
                token = await rcli.async_add_connector("CONNECTOR")
                assert token
                # Forward client using connector
                async with Client("CONNECTOR", ws_url=_ws_url(ws_port), socks_port=get_free_port(), no_env_proxy=True) as fcli:
                    # reverse should fail due to autonomy
                    with pytest.raises(Exception):
                        await async_assert_web_connection(website, res.port, timeout=3)
                    # forward works
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Threads ====================


def test_client_threads(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, threads=2, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Fast Open ====================


def test_fast_open_forward(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, fast_open=True, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_reverse(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_forward(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        socks_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            token = await srv.async_add_forward_token()
            async with Client(token, ws_url=_ws_url(ws_port), socks_port=socks_port, fast_open=True, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_reverse(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as cli:
                for _ in range(3):
                    await async_assert_web_connection(website, res.port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_fast_open_connector(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), fast_open=True, no_env_proxy=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


# ==================== Mixed Fast Open ====================


def test_mixed_fast_open_connector_all_fast_open(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), fast_open=True, no_env_proxy=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_server_fast_open(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port, fast_open=True) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as rcli:
                async with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), fast_open=True, no_env_proxy=True
                ) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_connector_fast_open(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, no_env_proxy=True) as rcli:
                await rcli.async_wait_ready(timeout=start_time_limit)
                with Client(
                    connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), fast_open=True, no_env_proxy=True
                ) as fcli:
                    await fcli.async_wait_ready(timeout=start_time_limit)
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))


def test_mixed_fast_open_connector_client_fast_open(website):
    from linksocks import Server, Client

    async def _main():
        ws_port = get_free_port()
        async with Server(ws_port=ws_port) as srv:
            res = srv.add_reverse_token()
            connector = "CONNECTOR"
            srv.add_connector_token(connector, res.token)
            async with Client(res.token, ws_url=_ws_url(ws_port), reverse=True, fast_open=True, no_env_proxy=True) as rcli:
                async with Client(connector, ws_url=_ws_url(ws_port), socks_port=get_free_port(), no_env_proxy=True) as fcli:
                    await async_assert_web_connection(website, res.port)
                    await async_assert_web_connection(website, fcli.socks_port)

    asyncio.run(asyncio.wait_for(_main(), 30))