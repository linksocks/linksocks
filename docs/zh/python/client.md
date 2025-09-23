# Client 类

LinkSocks Python 绑定中 `Client` 类的完整参考。

## 概述

`Client` 类连接到 WebSocket 服务器并建立 SOCKS5 代理功能。它支持正向和反向代理模式，具有全面的配置选项。

```python
from linksocks import Client

client = Client("your_token", ws_url="ws://localhost:8765")
```

## 异步和同步方法

所有方法都有同步和异步版本。异步版本以 `async_` 为前缀：

| 同步 | 异步 | 描述 |
|------|------|------|
| `wait_ready()` | `async_wait_ready()` | 启动并等待客户端就绪 |
| `close()` | `async_close()` | 关闭客户端并清理 |
| `add_connector()` | `async_add_connector()` | 添加连接者令牌（反向模式） |

**示例：**
```python
import asyncio

async def async_client_example():
    client = Client("token", ws_url="ws://localhost:8765", reverse=True)
    
    try:
        # 异步启动客户端
        await client.async_wait_ready(timeout=30.0)
        print("✅ 客户端就绪！")
        
        # 异步添加连接者（反向模式）
        connector = await client.async_add_connector("async_connector")
        print(f"连接者已添加：{connector}")
        
        # 保持运行
        await asyncio.sleep(3600)
        
    except asyncio.TimeoutError:
        print("❌ 连接超时")
    finally:
        # 清理资源
        await client.async_close()

asyncio.run(async_client_example())
```

## 构造函数

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

### 参数

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `token` | `str` | 必需 | WebSocket 连接的身份验证令牌 |
| `logger` | `logging.Logger` | `None` | Python 日志记录器实例 |
| `ws_url` | `str` | `"ws://localhost:8765"` | WebSocket 服务器 URL |
| `reverse` | `bool` | `False` | 启用反向代理模式 |
| `socks_host` | `str` | `"127.0.0.1"` | SOCKS5 服务器地址（正向模式） |
| `socks_port` | `int` | `9870` | SOCKS5 服务器端口（正向模式） |
| `socks_username` | `str` | `None` | SOCKS5 身份验证用户名 |
| `socks_password` | `str` | `None` | SOCKS5 身份验证密码 |
| `socks_wait_server` | `bool` | `True` | 启动 SOCKS5 前等待服务器 |
| `reconnect` | `bool` | `True` | 断开连接时自动重连 |
| `reconnect_delay` | `DurationLike` | `5.0` | 重连尝试间的延迟 |
| `buffer_size` | `int` | `32768` | 数据传输的缓冲区大小 |
| `channel_timeout` | `DurationLike` | `30.0` | WebSocket 通道超时 |
| `connect_timeout` | `DurationLike` | `10.0` | 出站连接超时 |
| `threads` | `int` | `1` | 处理的线程数 |
| `fast_open` | `bool` | `False` | 快速打开：立即 SOCKS5 成功；节省一个 RTT。参见 /zh/guide/fast-open |
| `upstream_proxy` | `str` | `None` | 上游代理地址 |
| `upstream_username` | `str` | `None` | 上游代理用户名 |
| `upstream_password` | `str` | `None` | 上游代理密码 |
| `no_env_proxy` | `bool` | `False` | 忽略代理环境变量 |

## 客户端生命周期

### wait_ready(timeout)

**启动并等待客户端就绪。** 此方法启动客户端，建立 WebSocket 连接，并阻塞直到完全初始化并准备代理流量。

```python
def wait_ready(self, timeout: Optional[DurationLike] = None) -> None
```

**参数：**
- `timeout`（可选）：最大等待时间，如果为 `None` 则无超时

**示例：**
```python
# 创建客户端
client = Client("token", ws_url="ws://localhost:8765")

# 启动客户端并无限期等待
client.wait_ready()

# 启动带超时的客户端
client.wait_ready(timeout=30.0)  # 30 秒
client.wait_ready(timeout="1m")   # 1 分钟
```

### close()

关闭客户端并清理资源。

```python
def close(self) -> None
```

## 连接者管理（反向模式）

### add_connector(connector_token)

为反向代理模式添加连接者令牌。

```python
def add_connector(self, connector_token: Optional[str]) -> str
```

**参数：**
- `connector_token`（可选）：连接者令牌，如果为 `None` 则自动生成

**返回：** 连接者令牌字符串

**示例：**
```python
# 反向模式客户端
client = Client("reverse_token", ws_url="ws://localhost:8765", reverse=True)
client.wait_ready()

# 添加连接者
connector1 = client.add_connector("my_connector")
connector2 = client.add_connector(None)  # 自动生成

print(f"连接者：{connector1}，{connector2}")
```

## 上下文管理器支持

`Client` 类支持同步和异步上下文管理器以实现自动资源管理。

```python
# 同步上下文管理器
with Client("token", ws_url="ws://localhost:8765") as client:
    client.wait_ready()
    print(f"客户端就绪，已连接：{client.is_connected}")
    # 退出时客户端自动关闭

# 异步上下文管理器
async def async_context():
    async with Client("token", ws_url="ws://localhost:8765") as client:
        await client.async_wait_ready()
        print(f"SOCKS5 端口：{client.socks_port}")
        # 退出时客户端自动关闭

asyncio.run(async_context())
```

## 属性

### log

访问此客户端的 Python 日志记录器实例。

```python
@property
def log(self) -> logging.Logger
```

### is_connected

检查客户端是否连接到服务器。

```python
@property
def is_connected(self) -> bool
```

**示例：**
```python
client = Client("token", ws_url="ws://localhost:8765")
client.wait_ready()

if client.is_connected:
    print("✅ 客户端已连接")
else:
    print("❌ 客户端已断开连接")

client.close()
```

### socks_port

获取 SOCKS5 服务器端口（仅正向模式）。

```python
@property
def socks_port(self) -> Optional[int]
```

**示例：**
```python
# 正向模式客户端
client = Client("token", ws_url="ws://localhost:8765", socks_port=9870)
client.wait_ready()

print(f"SOCKS5 代理可用于：127.0.0.1:{client.socks_port}")
client.close()
```
