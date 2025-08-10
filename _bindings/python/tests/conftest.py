import asyncio
import threading
import time
import pytest
import socket
import sys
import os

from .utils import *

SKIP_DEFAULT_FLAGS = ["integration_tests"]


def pytest_addoption(parser):
    for flag in SKIP_DEFAULT_FLAGS:
        parser.addoption(
            "--{}".format(flag.replace("_", "-")),
            action="store_true",
            default=False,
            help="run {} tests".format(flag),
        )


def pytest_configure(config):
    for flag in SKIP_DEFAULT_FLAGS:
        config.addinivalue_line("markers", flag)


def pytest_collection_modifyitems(config, items):
    for flag in SKIP_DEFAULT_FLAGS:
        if config.getoption("--{}".format(flag.replace("_", "-"))):
            return

        skip_mark = pytest.mark.skip(reason="need --{} option to run".format(flag))
        for item in items:
            if flag in item.keywords:
                item.add_marker(skip_mark)


@pytest.fixture(scope="session", name="udp_server")
def local_udp_echo_server():
    """Create a local udp echo server"""
    udp_port = get_free_port()

    async def echo_server():
        class AsyncUDPServer(asyncio.DatagramProtocol):
            def connection_made(self, transport):
                self.transport = transport

            def datagram_received(self, data, addr):
                self.transport.sendto(data, addr)

        return await asyncio.get_event_loop().create_datagram_endpoint(
            AsyncUDPServer, local_addr=("127.0.0.1", udp_port)
        )

    def run_server():
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        transport, _ = loop.run_until_complete(echo_server())
        try:
            loop.run_forever()
        finally:
            transport.close()
            loop.close()

    server_thread = threading.Thread(target=run_server)
    server_thread.daemon = True
    server_thread.start()

    time.sleep(1)

    yield f"127.0.0.1:{udp_port}"


@pytest.fixture(scope="session", name="udp_server_v6")
def local_udp_echo_server_v6():
    """Create a local ipv6 udp echo server"""
    udp_port = get_free_port(ipv6=True)

    async def echo_server():
        class AsyncUDPServer(asyncio.DatagramProtocol):
            def connection_made(self, transport):
                self.transport = transport

            def datagram_received(self, data, addr):
                self.transport.sendto(data, addr)

        return await asyncio.get_event_loop().create_datagram_endpoint(
            AsyncUDPServer, local_addr=("::1", udp_port), family=socket.AF_INET6
        )

    def run_server():
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        transport, _ = loop.run_until_complete(echo_server())
        try:
            loop.run_forever()
        finally:
            transport.close()
            loop.close()

    server_thread = threading.Thread(target=run_server)
    server_thread.daemon = True
    server_thread.start()

    time.sleep(1)

    yield f"[::1]:{udp_port}"


@pytest.fixture(scope="session", name="udp_server_domain")
def local_udp_echo_server_domain():
    """Create a local udp echo server accessible via localhost domain"""
    udp_port = get_free_port()

    async def echo_server():
        class AsyncUDPServer(asyncio.DatagramProtocol):
            def connection_made(self, transport):
                self.transport = transport

            def datagram_received(self, data, addr):
                self.transport.sendto(data, addr)

        return await asyncio.get_event_loop().create_datagram_endpoint(
            AsyncUDPServer, local_addr=("127.0.0.1", udp_port)
        )

    def run_server():
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        transport, _ = loop.run_until_complete(echo_server())
        try:
            loop.run_forever()
        finally:
            transport.close()
            loop.close()

    server_thread = threading.Thread(target=run_server)
    server_thread.daemon = True
    server_thread.start()

    time.sleep(1)

    yield f"localhost:{udp_port}"


@pytest.fixture(scope="session", name="website")
def local_http_server():
    """Create a local ipv4 http server"""
    http_port = get_free_port()

    async def handle_request(reader, writer):
        try:
            request = await reader.read(1024)
            request_str = request.decode()

            # Check if request is empty
            if not request_str:
                response = "HTTP/1.1 400 Bad Request\r\n\r\n"
            else:
                # Split request into lines and check format
                request_lines = request_str.split("\n")
                if not request_lines:
                    response = "HTTP/1.1 400 Bad Request\r\n\r\n"
                else:
                    try:
                        method, path, _ = request_lines[0].split(" ")
                        if path == "/generate_204":
                            response = "HTTP/1.1 204 No Content\r\n\r\n"
                        else:
                            response = "HTTP/1.1 404 Not Found\r\n\r\n"
                    except ValueError:
                        # Invalid request line format
                        response = "HTTP/1.1 400 Bad Request\r\n\r\n"
        except Exception as e:
            # Handle any other unexpected errors
            response = "HTTP/1.1 500 Internal Server Error\r\n\r\n"
        finally:
            try:
                writer.write(response.encode())
                await writer.drain()
                writer.close()
                await writer.wait_closed()
            except Exception:
                # Ensure writer is closed even if writing fails
                if not writer.is_closing():
                    writer.close()

    def run_server():
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        server = loop.run_until_complete(asyncio.start_server(handle_request, "127.0.0.1", http_port))
        loop.run_forever()

    server_thread = threading.Thread(target=run_server)
    server_thread.daemon = True
    server_thread.start()

    time.sleep(1)

    yield f"http://127.0.0.1:{http_port}/generate_204"


@pytest.fixture(scope="session", name="website_v6")
def local_http_server_v6():
    """Create a local ipv6 http server"""
    http_port = get_free_port(ipv6=True)

    async def handle_request(reader, writer):
        request = await reader.read(1024)

        request_line = request.decode().split("\n")[0]
        method, path, _ = request_line.split(" ")

        if path == "/generate_204":
            response = "HTTP/1.1 204 No Content\r\n\r\n"
        else:
            response = "HTTP/1.1 404 Not Found\r\n\r\n"

        writer.write(response.encode())
        await writer.drain()
        writer.close()
        await writer.wait_closed()

    def run_server():
        loop = asyncio.new_event_loop()
        asyncio.set_event_loop(loop)
        server = loop.run_until_complete(
            asyncio.start_server(handle_request, "::1", http_port, family=socket.AF_INET6)
        )
        loop.run_forever()

    server_thread = threading.Thread(target=run_server)
    server_thread.daemon = True
    server_thread.start()

    time.sleep(1)

    yield f"http://[::1]:{http_port}/generate_204"
