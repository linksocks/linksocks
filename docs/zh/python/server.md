# Server 类

LinkSocks Python 绑定中 `Server` 类的完整参考。

## 概述

`Server` 类管理来自客户端的 WebSocket 连接并提供 SOCKS5 代理功能。它支持正向和反向代理模式，具有全面的配置选项。

```python
from linksocks import Server

server = Server(ws_port=8765)
```

## 异步和同步方法

所有方法都有同步和异步版本。异步版本以 `async_` 为前缀：

| 同步 | 异步 | 描述 |
|------|------|------|
| `add_forward_token()` | `async_add_forward_token()` | 添加正向代理令牌 |
| `add_reverse_token()` | `async_add_reverse_token()` | 添加反向代理令牌 |
| `add_connector_token()` | `async_add_connector_token()` | 添加连接者令牌 |
| `remove_token()` | `async_remove_token()` | 删除令牌 |
| `wait_ready()` | `async_wait_ready()` | 启动并等待服务器就绪 |
| `close()` | `async_close()` | 关闭服务器并清理 |

**示例：**
```python
import asyncio

async def async_server_example():
    server = Server()
    
    # 所有异步令牌操作
    token = await server.async_add_forward_token("async_token")
    result = await server.async_add_reverse_token(port=9870)
    success = await server.async_remove_token(token)
    
    # 异步启动服务器
    await server.async_wait_ready(timeout=30.0)
    print("服务器就绪！")
    
    # 清理
    await server.async_close()

asyncio.run(async_server_example())
```

## 构造函数

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

### 参数

| 参数 | 类型 | 默认值 | 描述 |
|------|------|--------|------|
| `logger` | `logging.Logger` | `None` | Python 日志记录器实例 |
| `ws_host` | `str` | `"0.0.0.0"` | WebSocket 服务器监听地址 |
| `ws_port` | `int` | `8765` | WebSocket 服务器监听端口 |
| `socks_host` | `str` | `"127.0.0.1"` | SOCKS5 服务器地址（反向模式） |
| `port_pool` | `Any` | `None` | SOCKS5 服务器的端口池 |
| `socks_wait_client` | `bool` | `True` | 启动 SOCKS5 前等待客户端连接 |
| `buffer_size` | `int` | `32768` | 数据传输的缓冲区大小 |
| `api_key` | `str` | `None` | HTTP 管理接口的 API 密钥 |
| `channel_timeout` | `DurationLike` | `30.0` | WebSocket 通道超时 |
| `connect_timeout` | `DurationLike` | `10.0` | 出站连接超时 |
| `fast_open` | `bool` | `False` | 快速打开：立即 SOCKS5 成功；节省一个 RTT。参见 /zh/guide/fast-open |
| `upstream_proxy` | `str` | `None` | 上游代理地址 |
| `upstream_username` | `str` | `None` | 上游代理用户名 |
| `upstream_password` | `str` | `None` | 上游代理密码 |

### 持续时间类型

`DurationLike` 类型接受：
- `int` 或 `float` - 秒
- `timedelta` - Python timedelta 对象
- `str` - Go 持续时间字符串（例如，"30s"、"5m"、"1h"）

## 令牌管理

### add_forward_token(token)

添加正向代理令牌。

```python
def add_forward_token(self, token: Optional[str] = None) -> str
```

**参数：**
- `token`（可选）：特定令牌字符串，如果为 `None` 则自动生成

**返回：** 令牌字符串（生成的或提供的）

**示例：**
```python
server = Server()

# 自动生成令牌
token1 = server.add_forward_token()
print(f"生成的：{token1}")

# 使用特定令牌
token2 = server.add_forward_token("my_custom_token")
print(f"自定义：{token2}")
```

### add_reverse_token(...)

添加带有 SOCKS5 服务器配置的反向代理令牌。

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

**参数：**
- `token`（可选）：令牌字符串，如果为 `None` 则自动生成
- `port`（可选）：SOCKS5 服务器端口，如果为 `None` 则自动分配
- `username`（可选）：SOCKS5 身份验证用户名
- `password`（可选）：SOCKS5 身份验证密码
- `allow_manage_connector`（可选）：允许客户端管理连接者令牌

**返回：** 带有 `token` 和 `port` 字段的 `ReverseTokenResult`

**示例：**
```python
server = Server()

# 基本反向令牌
result1 = server.add_reverse_token()
print(f"令牌：{result1.token}，端口：{result1.port}")

# 使用身份验证
result2 = server.add_reverse_token(
    token="secure_token",
    port=9870,
    username="proxy_user", 
    password="proxy_pass"
)

# 使用连接者管理
result3 = server.add_reverse_token(
    allow_manage_connector=True
)
```

### add_connector_token(connector_token, reverse_token)

为代理代理模式添加连接者令牌。

```python
def add_connector_token(
    self, 
    connector_token: Optional[str], 
    reverse_token: str
) -> str
```

**参数：**
- `connector_token`（可选）：连接者令牌，如果为 `None` 则自动生成
- `reverse_token`（必需）：关联的反向代理令牌

**返回：** 连接者令牌字符串

### remove_token(token)

从服务器中删除任何类型的令牌。

```python
def remove_token(self, token: str) -> bool
```

**参数：**
- `token`（必需）：要删除的令牌

**返回：** 如果令牌被删除返回 `True`，如果未找到返回 `False`

## 服务器生命周期

### wait_ready(timeout)

**启动并等待服务器就绪。** 此方法启动服务器并阻塞直到完全初始化并准备接受连接。

```python
def wait_ready(self, timeout: Optional[DurationLike] = None) -> None
```

**参数：**
- `timeout`（可选）：最大等待时间，如果为 `None` 则无超时

**示例：**
```python
server = Server()
server.add_forward_token("ready_token")

# 启动服务器并无限期等待
server.wait_ready()

# 启动带超时的服务器
server.wait_ready(timeout=30.0)  # 30 秒
server.wait_ready(timeout="1m")  # 1 分钟
```

### close()

关闭服务器并清理资源。

```python
def close(self) -> None
```

## 上下文管理器支持

`Server` 类支持同步和异步上下文管理器以实现自动资源管理。

```python
# 同步上下文管理器
with Server() as server:
    server.add_forward_token("context_token")
    server.wait_ready()
    print("服务器在上下文中就绪")
    # 退出上下文时服务器自动关闭

# 异步上下文管理器
async def async_context():
    async with Server() as server:
        server.add_forward_token("async_context")
        await server.async_wait_ready()
        print("服务器在异步上下文中就绪")
        # 退出上下文时服务器自动关闭

asyncio.run(async_context())
```

## 属性

### log

访问此服务器的 Python 日志记录器实例。

```python
@property
def log(self) -> logging.Logger
```

**示例：**
```python
import logging

# 自定义日志记录器
logger = logging.getLogger("my_server")
logger.setLevel(logging.DEBUG)

server = Server(logger=logger)
server.log.info("服务器已创建")  # 使用自定义日志记录器
```
