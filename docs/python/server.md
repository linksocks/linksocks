# Server Class

Complete reference for the `Server` class in LinkSocks Python bindings.

## Overview

The `Server` class manages WebSocket connections from clients and provides SOCKS5 proxy functionality. It supports both forward and reverse proxy modes with comprehensive configuration options.

```python
from linksocks import Server

server = Server(ws_port=8765)
```

## Async and Sync Methods

All methods have both synchronous and asynchronous versions. The async versions are prefixed with `async_`:

| Synchronous | Asynchronous | Description |
|-------------|--------------|-------------|
| `add_forward_token()` | `async_add_forward_token()` | Add forward proxy token |
| `add_reverse_token()` | `async_add_reverse_token()` | Add reverse proxy token |
| `add_connector_token()` | `async_add_connector_token()` | Add connector token |
| `remove_token()` | `async_remove_token()` | Remove token |
| `wait_ready()` | `async_wait_ready()` | Start and wait for server ready |
| `close()` | `async_close()` | Close server and cleanup |

**Example:**
```python
import asyncio

async def async_server_example():
    server = Server()
    
    # All async token operations
    token = await server.async_add_forward_token("async_token")
    result = await server.async_add_reverse_token(port=9870)
    success = await server.async_remove_token(token)
    
    # Start server asynchronously
    await server.async_wait_ready(timeout=30.0)
    print("Server ready!")
    
    # Cleanup
    await server.async_close()

asyncio.run(async_server_example())
```

## Constructor

### Server(...)

```python
Server(
    *,
    logger: Optional[logging.Logger] = None,
    ws_host: Optional[str] = None,
    ws_port: Optional[int] = None,
    socks_host: Optional[str] = None,
    port_pool: Optional[Any] = None,
    socks_wait_client: Optional[bool] = None,
    buffer_size: Optional[int] = None,
    api_key: Optional[str] = None,
    channel_timeout: Optional[DurationLike] = None,
    connect_timeout: Optional[DurationLike] = None,
    fast_open: Optional[bool] = None,
    upstream_proxy: Optional[str] = None,
    upstream_username: Optional[str] = None,
    upstream_password: Optional[str] = None,
)
```

### Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `logger` | `logging.Logger` | `None` | Python logger instance |
| `ws_host` | `str` | `"0.0.0.0"` | WebSocket server listen address |
| `ws_port` | `int` | `8765` | WebSocket server listen port |
| `socks_host` | `str` | `"127.0.0.1"` | SOCKS5 server address (reverse mode) |
| `port_pool` | `Any` | `None` | Pool of ports for SOCKS5 servers |
| `socks_wait_client` | `bool` | `True` | Wait for client connections before starting SOCKS5 |
| `buffer_size` | `int` | `32768` | Buffer size for data transfer |
| `api_key` | `str` | `None` | API key for HTTP management interface |
| `channel_timeout` | `DurationLike` | `30.0` | Timeout for WebSocket channels |
| `connect_timeout` | `DurationLike` | `10.0` | Timeout for outbound connections |
| `fast_open` | `bool` | `False` | Fast Open: immediate SOCKS5 success; saves one RTT. See /guide/fast-open |
| `upstream_proxy` | `str` | `None` | Upstream proxy address |
| `upstream_username` | `str` | `None` | Username for upstream proxy |
| `upstream_password` | `str` | `None` | Password for upstream proxy |

### Duration Types

The `DurationLike` type accepts:
- `int` or `float` - seconds
- `timedelta` - Python timedelta object
- `str` - Go duration string (e.g., "30s", "5m", "1h")

## Token Management

### add_forward_token(token)

Add a forward proxy token.

```python
def add_forward_token(self, token: Optional[str] = None) -> str
```

**Parameters:**
- `token` (optional): Specific token string, auto-generated if `None`

**Returns:** The token string (generated or provided)

**Example:**
```python
server = Server()

# Auto-generate token
token1 = server.add_forward_token()
print(f"Generated: {token1}")

# Use specific token
token2 = server.add_forward_token("my_custom_token")
print(f"Custom: {token2}")
```

### add_reverse_token(...)

Add a reverse proxy token with SOCKS5 server configuration.

```python
def add_reverse_token(
    self,
    *,
    token: Optional[str] = None,
    port: Optional[int] = None,
    username: Optional[str] = None,
    password: Optional[str] = None,
    allow_manage_connector: Optional[bool] = None,
) -> ReverseTokenResult
```

**Parameters:**
- `token` (optional): Token string, auto-generated if `None`
- `port` (optional): SOCKS5 server port, auto-assigned if `None`
- `username` (optional): SOCKS5 authentication username
- `password` (optional): SOCKS5 authentication password
- `allow_manage_connector` (optional): Allow clients to manage connector tokens

**Returns:** `ReverseTokenResult` with `token` and `port` fields

**Example:**
```python
server = Server()

# Basic reverse token
result1 = server.add_reverse_token()
print(f"Token: {result1.token}, Port: {result1.port}")

# With authentication
result2 = server.add_reverse_token(
    token="secure_token",
    port=9870,
    username="proxy_user", 
    password="proxy_pass"
)

# With connector management
result3 = server.add_reverse_token(
    allow_manage_connector=True
)
```

### add_connector_token(connector_token, reverse_token)

Add a connector token for agent proxy mode.

```python
def add_connector_token(
    self, 
    connector_token: Optional[str], 
    reverse_token: str
) -> str
```

**Parameters:**
- `connector_token` (optional): Connector token, auto-generated if `None`
- `reverse_token` (required): Associated reverse proxy token

**Returns:** The connector token string

### remove_token(token)

Remove any type of token from the server.

```python
def remove_token(self, token: str) -> bool
```

**Parameters:**
- `token` (required): Token to remove

**Returns:** `True` if token was removed, `False` if not found

## Server Lifecycle

### wait_ready(timeout)

**Start and wait for the server to be ready.** This method starts the server and blocks until it's fully initialized and ready to accept connections.

```python
def wait_ready(self, timeout: Optional[DurationLike] = None) -> None
```

**Parameters:**
- `timeout` (optional): Maximum time to wait, no timeout if `None`

**Example:**
```python
server = Server()
server.add_forward_token("ready_token")

# Start server and wait indefinitely
server.wait_ready()

# Start server with timeout
server.wait_ready(timeout=30.0)  # 30 seconds
server.wait_ready(timeout="1m")  # 1 minute
```

### close()

Close the server and clean up resources.

```python
def close(self) -> None
```

## Context Manager Support

The `Server` class supports both synchronous and asynchronous context managers for automatic resource management.

```python
# Synchronous context manager
with Server() as server:
    server.add_forward_token("context_token")
    server.wait_ready()
    print("Server ready in context")
    # Server automatically closed when exiting context

# Asynchronous context manager
async def async_context():
    async with Server() as server:
        server.add_forward_token("async_context")
        await server.async_wait_ready()
        print("Server ready in async context")
        # Server automatically closed when exiting context

asyncio.run(async_context())
```

## Properties

### log

Access the Python logger instance for this server.

```python
@property
def log(self) -> logging.Logger
```

**Example:**
```python
import logging

# Custom logger
logger = logging.getLogger("my_server")
logger.setLevel(logging.DEBUG)

server = Server(logger=logger)
server.log.info("Server created")  # Uses custom logger
```