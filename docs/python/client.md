# Client Class

Complete reference for the `Client` class in LinkSocks Python bindings.

## Overview

The `Client` class connects to WebSocket servers and establishes SOCKS5 proxy functionality. It supports both forward and reverse proxy modes with comprehensive configuration options.

```python
from linksocks import Client

client = Client("your_token", ws_url="ws://localhost:8765")
```

## Async and Sync Methods

All methods have both synchronous and asynchronous versions. The async versions are prefixed with `async_`:

| Synchronous | Asynchronous | Description |
|-------------|--------------|-------------|
| `wait_ready()` | `async_wait_ready()` | Start and wait for client ready |
| `close()` | `async_close()` | Close client and cleanup |
| `add_connector()` | `async_add_connector()` | Add connector token (reverse mode) |

**Example:**
```python
import asyncio

async def async_client_example():
    client = Client("token", ws_url="ws://localhost:8765", reverse=True)
    
    try:
        # Start client asynchronously
        await client.async_wait_ready(timeout=30.0)
        print("✅ Client ready!")
        
        # Add connector asynchronously (reverse mode)
        connector = await client.async_add_connector("async_connector")
        print(f"Connector added: {connector}")
        
        # Keep running
        await asyncio.sleep(3600)
        
    except asyncio.TimeoutError:
        print("❌ Connection timeout")
    finally:
        # Clean up resources
        await client.async_close()

asyncio.run(async_client_example())
```

## Constructor

### Client(token, ...)

```python
Client(
    token: str,
    *,
    logger: Optional[logging.Logger] = None,
    ws_url: Optional[str] = None,
    reverse: Optional[bool] = None,
    socks_host: Optional[str] = None,
    socks_port: Optional[int] = None,
    socks_username: Optional[str] = None,
    socks_password: Optional[str] = None,
    socks_wait_server: Optional[bool] = None,
    reconnect: Optional[bool] = None,
    reconnect_delay: Optional[DurationLike] = None,
    buffer_size: Optional[int] = None,
    channel_timeout: Optional[DurationLike] = None,
    connect_timeout: Optional[DurationLike] = None,
    threads: Optional[int] = None,
    fast_open: Optional[bool] = None,
    upstream_proxy: Optional[str] = None,
    upstream_username: Optional[str] = None,
    upstream_password: Optional[str] = None,
    no_env_proxy: Optional[bool] = None,
)
```

### Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `token` | `str` | Required | Authentication token for WebSocket connection |
| `logger` | `logging.Logger` | `None` | Python logger instance |
| `ws_url` | `str` | `"ws://localhost:8765"` | WebSocket server URL |
| `reverse` | `bool` | `False` | Enable reverse proxy mode |
| `socks_host` | `str` | `"127.0.0.1"` | SOCKS5 server address (forward mode) |
| `socks_port` | `int` | `9870` | SOCKS5 server port (forward mode) |
| `socks_username` | `str` | `None` | SOCKS5 authentication username |
| `socks_password` | `str` | `None` | SOCKS5 authentication password |
| `socks_wait_server` | `bool` | `True` | Wait for server before starting SOCKS5 |
| `reconnect` | `bool` | `True` | Auto-reconnect on disconnect |
| `reconnect_delay` | `DurationLike` | `5.0` | Delay between reconnection attempts |
| `buffer_size` | `int` | `32768` | Buffer size for data transfer |
| `channel_timeout` | `DurationLike` | `30.0` | Timeout for WebSocket channels |
| `connect_timeout` | `DurationLike` | `10.0` | Timeout for outbound connections |
| `threads` | `int` | `1` | Number of threads for processing |
| `fast_open` | `bool` | `False` | Fast Open: immediate SOCKS5 success; saves one RTT. See /guide/fast-open |
| `upstream_proxy` | `str` | `None` | Upstream proxy address |
| `upstream_username` | `str` | `None` | Username for upstream proxy |
| `upstream_password` | `str` | `None` | Password for upstream proxy |
| `no_env_proxy` | `bool` | `False` | Ignore proxy environment variables |

## Client Lifecycle

### wait_ready(timeout)

**Start and wait for the client to be ready.** This method starts the client, establishes WebSocket connection, and blocks until it's fully initialized and ready to proxy traffic.

```python
def wait_ready(self, timeout: Optional[DurationLike] = None) -> None
```

**Parameters:**
- `timeout` (optional): Maximum time to wait, no timeout if `None`

**Example:**
```python
# Create client
client = Client("token", ws_url="ws://localhost:8765")

# Start client and wait indefinitely
client.wait_ready()

# Start client with timeout
client.wait_ready(timeout=30.0)  # 30 seconds
client.wait_ready(timeout="1m")   # 1 minute
```

### close()

Close the client and clean up resources.

```python
def close(self) -> None
```

## Connector Management (Reverse Mode)

### add_connector(connector_token)

Add a connector token for reverse proxy mode.

```python
def add_connector(self, connector_token: Optional[str]) -> str
```

**Parameters:**
- `connector_token` (optional): Connector token, auto-generated if `None`

**Returns:** The connector token string

**Example:**
```python
# Reverse mode client
client = Client("reverse_token", ws_url="ws://localhost:8765", reverse=True)
client.wait_ready()

# Add connector
connector1 = client.add_connector("my_connector")
connector2 = client.add_connector(None)  # Auto-generated

print(f"Connectors: {connector1}, {connector2}")
```


## Context Manager Support

The `Client` class supports both synchronous and asynchronous context managers for automatic resource management.

```python
# Synchronous context manager
with Client("token", ws_url="ws://localhost:8765") as client:
    client.wait_ready()
    print(f"Client ready, connected: {client.is_connected}")
    # Client automatically closed when exiting

# Asynchronous context manager
async def async_context():
    async with Client("token", ws_url="ws://localhost:8765") as client:
        await client.async_wait_ready()
        print(f"SOCKS5 port: {client.socks_port}")
        # Client automatically closed when exiting

asyncio.run(async_context())
```

## Properties

### log

Access the Python logger instance for this client.

```python
@property
def log(self) -> logging.Logger
```

### is_connected

Check if the client is connected to the server.

```python
@property
def is_connected(self) -> bool
```

**Example:**
```python
client = Client("token", ws_url="ws://localhost:8765")
client.wait_ready()

if client.is_connected:
    print("✅ Client is connected")
else:
    print("❌ Client is disconnected")

client.close()
```

### socks_port

Get the SOCKS5 server port (forward mode only).

```python
@property
def socks_port(self) -> Optional[int]
```

**Example:**
```python
# Forward mode client
client = Client("token", ws_url="ws://localhost:8765", socks_port=9870)
client.wait_ready()

print(f"SOCKS5 proxy available at: 127.0.0.1:{client.socks_port}")
client.close()
```