# Python 库

LinkSocks 提供了包装 Go 实现的 Python 绑定，提供同步和异步 API。

## 概述

Python 绑定提供两个主要类：

- `Server` - 处理 SOCKS5 代理请求的 WebSocket 服务器
- `Client` - 连接到服务器并提供 SOCKS5 功能的 WebSocket 客户端

## 快速开始

### 安装

```bash
pip install linksocks
```

### 简单正向代理

创建一个通过远程服务器路由流量的正向代理：

```python
import asyncio
from linksocks import Server, Client

async def forward_proxy_example():
    # 启动服务器
    async with Server(ws_port=8765) as server:
        # 添加用于客户端身份验证的令牌
        token = await server.async_add_forward_token()
        print(f"创建令牌：{token}")
        
        # 启动提供 SOCKS5 代理的客户端
        async with Client(token, ws_url="ws://localhost:8765", socks_port=9870) as client:
            print("✅ 正向代理就绪！")
            print(f"📡 使用 SOCKS5 代理：127.0.0.1:9870")
            print("🌐 示例：curl --socks5 127.0.0.1:9870 http://httpbin.org/ip")
            
            # 保持运行
            await asyncio.sleep(60)

# 运行示例
asyncio.run(forward_proxy_example())
```

### 简单反向代理

创建一个为远程客户端提供互联网访问的反向代理：

```python
import asyncio
from linksocks import Server, Client

async def reverse_proxy_example():
    # 启动服务器
    async with Server(ws_port=8765) as server:
        # 创建反向令牌 - 服务器提供 SOCKS5 代理
        result = await server.async_add_reverse_token()
        print(f"创建反向令牌：{result.token}")
        print(f"📡 SOCKS5 代理可用于：127.0.0.1:{result.port}")
        
        # 客户端连接并提供互联网访问
        async with Client(result.token, ws_url="ws://localhost:8765", reverse=True) as client:
            print("✅ 反向代理就绪！")
            print("🌐 示例：curl --socks5 127.0.0.1:{} http://httpbin.org/ip".format(result.port))
            
            # 保持运行
            await asyncio.sleep(60)

# 运行示例
asyncio.run(reverse_proxy_example())
```

## 下一步

- [Server API](./server.md) - 完整的 Server 类参考
- [Client API](./client.md) - 完整的 Client 类参考
