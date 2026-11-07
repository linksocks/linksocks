# Python Library

LinkSocks provides Python bindings that wrap the Go implementation, offering both synchronous and asynchronous APIs.

## Overview

The Python bindings provide three main classes:

- `Server` - WebSocket server that handles SOCKS5 proxy requests
- `Client` - WebSocket client that connects to servers and provides SOCKS5 functionality
- `Web` - HTTP(S) site provider for a linksocks.js relay

To connect machines with virtual IPv4 addresses, use the standalone
[`linksocks zone` CLI](/guide/zones). Zones do not have a Python library API or a
`Zone` class. Zone creation and deletion take place on the linksocks.js website.

## Quick Start

### Installation

```bash
pip install linksocks
```

### Simple Forward Proxy

Create a forward proxy that routes traffic through a remote server:

```python
import asyncio
from linksocks import Server, Client

async def forward_proxy_example():
    # Start server
    async with Server(ws_port=8765) as server:
        # Add token for client authentication
        token = await server.async_add_forward_token()
        print(f"Created token: {token}")
        
        # Start client that provides SOCKS5 proxy
        async with Client(token, ws_url="ws://localhost:8765", socks_port=9870) as client:
            print("✅ Forward proxy ready!")
            print(f"📡 Use SOCKS5 proxy: 127.0.0.1:9870")
            print("🌐 Example: curl --socks5 127.0.0.1:9870 http://httpbin.org/ip")
            
            # Keep running
            await asyncio.sleep(60)

# Run the example
asyncio.run(forward_proxy_example())
```

### Simple Reverse Proxy

Create a reverse proxy that provides internet access to a remote client:

```python
import asyncio
from linksocks import Server, Client

async def reverse_proxy_example():
    # Start server
    async with Server(ws_port=8765) as server:
        # Create reverse token - server provides SOCKS5 proxy
        result = await server.async_add_reverse_token()
        print(f"Created reverse token: {result.token}")
        print(f"📡 SOCKS5 proxy available at: 127.0.0.1:{result.port}")
        
        # Client connects and provides internet access
        async with Client(result.token, ws_url="ws://localhost:8765", reverse=True) as client:
            print("✅ Reverse proxy ready!")
            print("🌐 Example: curl --socks5 127.0.0.1:{} http://httpbin.org/ip".format(result.port))
            
            # Keep running
            await asyncio.sleep(60)

# Run the example
asyncio.run(reverse_proxy_example())
```

### Publish a Web Site

Publish a local HTTP site through a linksocks.js relay:

```python
from linksocks import Web

with Web(["http://127.0.0.1:8080"], url="wss://l.zetx.tech") as site:
    host = site.wait_ready(timeout=30)
    print(f"Published at https://{host}")
    input("Press Enter to stop\n")
```

## Next Steps

- [Server API](./server.md) - Complete Server class reference
- [Client API](./client.md) - Complete Client class reference
- [Web API](./web.md) - Publish HTTP(S) sites through a relay
