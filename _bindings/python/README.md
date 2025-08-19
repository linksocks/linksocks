# LinkSocks Python Bindings

[![PyPI version](https://badge.fury.io/py/linksocks.svg)](https://badge.fury.io/py/linksocks)
[![Python versions](https://img.shields.io/pypi/pyversions/linksocks.svg)](https://pypi.org/project/linksocks/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

Python bindings for LinkSocks - a SOCKS5 over WebSocket proxy tool.

## Overview

LinkSocks is a SOCKS proxy implementation over WebSocket protocol that allows you to securely expose SOCKS proxy services under Web Application Firewall (WAF) protection. This package provides Python bindings for the Go implementation.

### Key Features

- 🔄 **Forward & Reverse Proxy**: Support both forward and reverse SOCKS5 proxy modes
- 🌐 **WebSocket Transport**: Works under WAF protection using standard WebSocket connections
- ⚖️ **Load Balancing**: Round-robin load balancing for reverse proxy with multiple clients
- 🔐 **Authentication**: SOCKS5 proxy authentication and secure token-based WebSocket authentication
- 🌍 **Protocol Support**: Full IPv6 over SOCKS5 and UDP over SOCKS5 support
- 🐍 **Pythonic API**: Both synchronous and asynchronous APIs with context manager support

## Installation

### Using pip (Recommended)

```bash
pip install linksocks
```

### Development Installation

```bash
git clone https://github.com/linksocks/linksocks.git
cd linksocks/_bindings/python
pip install -e .
```

### Requirements

- Python 3.8 or later
- Go 1.19 or later (for building from source)

## Quick Start

### Forward Proxy Example

```python
import asyncio
from linksocks import Server, Client

async def main():
    # Create server and client with async context managers
    async with Server(ws_port=8765) as server:
        # Add forward token asynchronously
        token = await server.async_add_forward_token()
        
        async with Client(token, ws_url="ws://localhost:8765", socks_port=9870, no_env_proxy=True) as client:
            print("✅ Forward proxy ready!")
            print("🌐 SOCKS5 proxy: 127.0.0.1:9870")
            print("🔧 Test: curl --socks5 127.0.0.1:9870 http://httpbin.org/ip")
            
            # Keep running
            await asyncio.sleep(3600)

if __name__ == "__main__":
    asyncio.run(main())
```

### Reverse Proxy Example

```python
import asyncio
from linksocks import Server, Client

async def main():
    # Create server and client with async context managers
    async with Server(ws_port=8765) as server:
        # Add reverse token (port is auto-allocated)
        result = server.add_reverse_token()
        print(f"🔑 Reverse token: {result.token}")
        print(f"🌐 SOCKS5 proxy will be available on: 127.0.0.1:{result.port}")
        
        # Create client in reverse mode
        async with Client(result.token, ws_url="ws://localhost:8765", reverse=True, no_env_proxy=True) as client:
            print("✅ Reverse proxy ready!")
            print(f"🔧 Test: curl --socks5 127.0.0.1:{result.port} http://httpbin.org/ip")
            
            # Keep running
            await asyncio.sleep(3600)

if __name__ == "__main__":
    asyncio.run(main())
```

## API Reference

### Server Class

The `Server` class manages WebSocket connections and provides SOCKS5 proxy functionality.

```python
from linksocks import Server

# Create server with options
server = Server(
    ws_host="0.0.0.0",           # WebSocket listen address
    ws_port=8765,                # WebSocket listen port  
    socks_host="127.0.0.1",      # SOCKS5 listen address (reverse mode)
    buffer_size=32768,           # Buffer size for data transfer
    api_key="your_api_key",      # Enable HTTP API
    channel_timeout=30.0,        # WebSocket channel timeout (seconds)
    connect_timeout=10.0,        # Connection timeout (seconds)
    fast_open=False,             # Enable fast open optimization
    upstream_proxy="socks5://proxy:1080",  # Upstream proxy
    upstream_username="user",    # Upstream proxy username
    upstream_password="pass"     # Upstream proxy password
)
```

#### Token Management

```python
# Add forward proxy token (synchronous)
token = server.add_forward_token("custom_token")  # or auto-generate with None

# Add forward proxy token (asynchronous - recommended)
token = await server.async_add_forward_token("custom_token")

# Add reverse proxy token
result = server.add_reverse_token(
    token="custom_token",        # optional, auto-generated if None
    port=9870,                   # optional, auto-allocated if None
    username="socks_user",       # optional, SOCKS5 auth username
    password="socks_pass",       # optional, SOCKS5 auth password
    allow_manage_connector=True  # optional, allow client connector management
)
print(f"Token: {result.token}, Port: {result.port}")

# Add connector token  
connector_token = server.add_connector_token("connector_token", "reverse_token")

# Remove any token
success = server.remove_token("token_to_remove")
```

#### Running the Server

```python
# Asynchronous (recommended) - automatically waits for ready
async with server:
    print("Server is ready!")  # No need for async_wait_ready()
    # Server runs while in context

# Synchronous - requires manual wait
server.wait_ready()  # Blocks until ready
# Server runs in background
server.close()  # Clean shutdown

# Manual wait with timeout (only needed for synchronous usage)
server.wait_ready(timeout=30.0)  # 30 second timeout
await server.async_wait_ready(timeout="30s")  # Go duration string (rarely needed)
```

### Client Class

The `Client` class connects to WebSocket servers and provides SOCKS5 functionality.

```python
from linksocks import Client

# Create client with options
client = Client(
    token="your_token",          # Authentication token (required)
    ws_url="ws://localhost:8765", # WebSocket server URL
    reverse=False,               # Enable reverse proxy mode
    socks_host="127.0.0.1",      # SOCKS5 listen address (forward mode)
    socks_port=9870,             # SOCKS5 listen port (forward mode)
    socks_username="user",       # SOCKS5 auth username
    socks_password="pass",       # SOCKS5 auth password
    socks_wait_server=True,      # Wait for server before starting SOCKS5
    reconnect=True,              # Auto-reconnect on disconnect
    reconnect_delay=5.0,         # Reconnect delay (seconds)
    buffer_size=32768,           # Buffer size for data transfer
    channel_timeout=30.0,        # WebSocket channel timeout
    connect_timeout=10.0,        # Connection timeout
    threads=4,                   # Number of processing threads
    fast_open=False,             # Enable fast open optimization
    upstream_proxy="socks5://proxy:1080",  # Upstream proxy
    upstream_username="proxy_user",        # Upstream proxy username
    upstream_password="proxy_pass",        # Upstream proxy password
    no_env_proxy=False           # Ignore proxy environment variables
)
```

#### Running the Client

```python
# Asynchronous (recommended) - automatically waits for ready
async with client:
    print(f"Client ready! SOCKS5 port: {client.socks_port}")
    print(f"Connected: {client.is_connected}")
    # Client runs while in context

# Synchronous - requires manual wait
client.wait_ready()
print(f"Connected: {client.is_connected}")
client.close()  # Clean shutdown
```

#### Connector Management (Reverse Mode)

```python
# Add connector token (reverse mode only)
connector_token = client.add_connector("my_connector")  # or auto-generate
connector_token = await client.async_add_connector(None)  # async version
```

### Logging

```python
import logging
from linksocks import set_log_level

# Set global log level
set_log_level(logging.DEBUG)
set_log_level("INFO")  # String format

# Use custom logger
logger = logging.getLogger("my_app")
logger.setLevel(logging.DEBUG)
handler = logging.StreamHandler()
handler.setFormatter(logging.Formatter('%(asctime)s - %(name)s - %(levelname)s - %(message)s'))
logger.addHandler(handler)

server = Server(logger=logger)
client = Client("token", logger=logger)
```

## Advanced Examples

### Agent Proxy with Connector Management

```python
import asyncio
from linksocks import Server, Client

async def agent_proxy():
    # Server with connector autonomy enabled
    async with Server(ws_port=8765) as server:
        result = server.add_reverse_token(allow_manage_connector=True)
        print(f"🔑 Provider token: {result.token}")
        print(f"🌐 SOCKS5 proxy will be available on: 127.0.0.1:{result.port}")
        
        # Provider client (provides network access)
        async with Client(result.token, ws_url="ws://localhost:8765", reverse=True, no_env_proxy=True) as provider:
            print("✅ Agent proxy server and provider ready!")
            
            # Provider can manage its own connectors
            connector_token = await provider.async_add_connector("my_connector")
            print(f"🔑 Connector token: {connector_token}")
            
            # Now external connectors can use this token
            print(f"🔧 Start connector: linksocks connector -t {connector_token} -u ws://localhost:8765 -p 1180")
            
            await asyncio.sleep(3600)

asyncio.run(agent_proxy())
```

### Error Handling and Monitoring

```python
import asyncio
import logging
from linksocks import Client

async def robust_client():
    logger = logging.getLogger("robust_client")
    
    client = Client(
        "your_token",
        ws_url="ws://server:8765",
        reconnect=True,
        reconnect_delay=5.0,
        no_env_proxy=True,
        logger=logger
    )
    
    try:
        async with client:
            logger.info("✅ Client connected successfully")
            
            # Monitor connection status
            while True:
                if not client.is_connected:
                    logger.warning("⚠️  Connection lost, reconnecting...")
                    
                await asyncio.sleep(5)
                    
    except asyncio.TimeoutError:
        logger.error("❌ Connection timeout after 30 seconds")
    except Exception as e:
        logger.error(f"❌ Client error: {e}")
    finally:
        logger.info("🔄 Client shutting down")

asyncio.run(robust_client())
```

### HTTP API Integration

```python
import asyncio
import aiohttp
from linksocks import Server

async def api_server_example():
    # Start server with API enabled
    server = Server(ws_port=8765, api_key="secret_api_key")
    
    async with server:
        print("✅ Server with API ready!")
        
        # Use HTTP API to manage tokens
        async with aiohttp.ClientSession() as session:
            headers = {"X-API-Key": "secret_api_key"}
            
            # Add forward token via API
            async with session.post(
                "http://localhost:8765/api/token",
                headers=headers,
                json={"type": "forward", "token": "api_token"}
            ) as resp:
                result = await resp.json()
                print(f"📝 Added token via API: {result}")
            
            # Get server status
            async with session.get(
                "http://localhost:8765/api/status",
                headers=headers
            ) as resp:
                status = await resp.json()
                print(f"📊 Server status: {status}")
        
        await asyncio.sleep(3600)

asyncio.run(api_server_example())
```

## Type Hints

The package includes comprehensive type hints for better IDE support:

```python
from typing import Optional
from linksocks import Server, Client, ReverseTokenResult

def create_proxy_pair(token: str, port: Optional[int] = None) -> tuple[Server, Client]:
    server = Server(ws_port=8765)
    server.add_forward_token(token)
    
    client = Client(token, ws_url="ws://localhost:8765", socks_port=port or 9870, no_env_proxy=True)
    return server, client

# ReverseTokenResult is a dataclass
server = Server(ws_port=8765)
result: ReverseTokenResult = server.add_reverse_token()
print(f"Token: {result.token}, Port: {result.port}")
```

## Comparison with CLI Tool

| Feature | Python Bindings | Go CLI Tool |
|---------|-----------------|-------------|
| **Integration** | Library for Python apps | Standalone binary |
| **API Style** | Object-oriented, async/sync | Command-line flags |
| **Use Cases** | Embedded in applications | Quick setup, scripting |
| **Performance** | Same (uses Go backend) | Same |
| **Features** | Full feature parity | Full feature parity |

## Troubleshooting

### Common Issues

1. **Import Error**: Make sure Go 1.19+ is installed when building from source
2. **Connection Refused**: Check that server is running and ports are correct
3. **Authentication Failed**: Verify tokens match between server and client
4. **Port Already in Use**: Choose different ports or check for existing processes

### Debug Logging

```python
import logging
from linksocks import set_log_level

# Enable debug logging
set_log_level(logging.DEBUG)

# Or use environment variable
import os
os.environ["LINKSOCKS_LOG_LEVEL"] = "DEBUG"
```

### Performance Tuning

```python
# Increase buffer size for high-throughput scenarios
server = Server(buffer_size=65536)
client = Client("token", buffer_size=65536, threads=8)

# Enable fast open for lower latency
server = Server(fast_open=True)
client = Client("token", fast_open=True)
```

## Contributing

We welcome contributions! Please see the main [LinkSocks repository](https://github.com/linksocks/linksocks) for contribution guidelines.

## License

This project is licensed under the MIT License.

## Links

- 📚 [Full Documentation](https://linksocks.github.io/linksocks/)
- 🐛 [Issue Tracker](https://github.com/linksocks/linksocks/issues)
- 💬 [Discussions](https://github.com/linksocks/linksocks/discussions)
- 📦 [PyPI Package](https://pypi.org/project/linksocks/)
- 🐳 [Docker Hub](https://hub.docker.com/r/jackzzs/linksocks)
