# 命令行选项

LinkSocks 是一个基于 WebSocket 协议的多功能 SOCKS 代理实现，支持正向和反向代理配置。本指南提供了如何通过命令行使用 LinkSocks 的详细说明。

## 基本命令

LinkSocks 提供两个主要命令：`server` 和 `client`。

### Server

启动 LinkSocks 服务器，监听传入的 WebSocket 连接并管理 SOCKS 代理服务。

### Client

启动 LinkSocks 客户端，连接到服务器并为本地机器提供 SOCKS 代理功能。

## 服务器选项

### 基本选项

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--ws-host` | `-H` | `0.0.0.0` | WebSocket 服务器监听地址 |
| `--ws-port` | `-P` | `8765` | WebSocket 服务器监听端口 |
| `--token` | `-t` | | 认证令牌（如果为空则自动生成） |
| `--reverse` | `-r` | `false` | 启用反向代理模式 |

### SOCKS5 选项（反向模式）

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--socks-host` | `-s` | `127.0.0.1` | SOCKS5 服务器监听地址 |
| `--socks-port` | `-p` | `9870` | SOCKS5 服务器监听端口 |
| `--socks-username` | `-n` | | SOCKS5 认证用户名 |
| `--socks-password` | `-w` | | SOCKS5 认证密码 |
| `--socks-nowait` | `-i` | `false` | 立即启动 SOCKS 服务器 |

### 代理代理选项

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--connector-token` | `-c` | | 代理代理的连接者令牌 |
| `--connector-autonomy` | `-a` | `false` | 允许客户端管理连接者令牌 |

### 性能选项

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--buffer-size` | `-b` | `32768` | 数据传输的缓冲区大小 |
| `--fast-open` | `-f` | `false` | 启用快速打开优化（参见：[快速打开模式](/zh/guide/fast-open)） |
| `--upstream-proxy` | `-x` | | 上游 SOCKS5 代理 URL |

### 管理选项

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--api-key` | `-k` | | 使用指定密钥启用 HTTP API |
| `--debug` | `-d` | | 调试日志（使用 -dd 进行跟踪） |

## 客户端参数

### 基本选项

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--token` | `-t` | | 认证令牌（必需） |
| `--url` | `-u` | `ws://localhost:8765` | WebSocket 服务器 URL |
| `--reverse` | `-r` | `false` | 启用反向代理模式 |
| `--connector-token` | `-c` | | 代理代理的连接者令牌 |

### SOCKS5 选项（正向模式）

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--socks-host` | `-s` | `127.0.0.1` | SOCKS5 服务器监听地址 |
| `--socks-port` | `-p` | `9870` | SOCKS5 服务器监听端口 |
| `--socks-username` | `-n` | | SOCKS5 认证用户名 |
| `--socks-password` | `-w` | | SOCKS5 认证密码 |
| `--socks-no-wait` | `-i` | `false` | 立即启动 SOCKS 服务器 |

### 连接选项

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--no-reconnect` | `-R` | `false` | 服务器断开连接时停止 |
| `--threads` | `-T` | `1` | WebSocket 连接数 |
| `--fast-open` | `-f` | `false` | 启用快速打开优化（参见：[快速打开](/zh/guide/fast-open)） |
| `--upstream-proxy` | `-x` | | 上游 SOCKS5 代理 URL |
| `--no-env-proxy` | `-E` | `false` | 忽略代理环境变量 |

### 调试选项

| 参数 | 简写 | 默认值 | 描述 |
|-----|------|--------|------|
| `--debug` | `-d` | | 调试日志（使用 -dd 进行跟踪） |

## 上游代理格式

`--upstream-proxy` 参数接受 SOCKS5 URL：

```
socks5://[username[:password]@]host[:port]
```

**示例：**
- `socks5://proxy.example.com:1080`
- `socks5://user:pass@proxy.example.com:1080`
- `socks5://proxy.example.com`（默认端口 1080）

## 常用参数组合

### 正向代理

```bash
# 基本正向代理
linksocks server -t my_token
linksocks client -t my_token -u ws://localhost:8765 -p 9870

# 使用认证
linksocks client -t my_token -u ws://localhost:8765 -n user -w pass

# 使用上游代理
linksocks client -t my_token -u ws://localhost:8765 -x socks5://upstream:1080
```

### 反向代理

```bash
# 基本反向代理
linksocks server -t my_token -r -p 9870
linksocks client -t my_token -u ws://localhost:8765 -r

# 使用 SOCKS 认证
linksocks server -t my_token -r -p 9870 -n user -w pass
linksocks client -t my_token -u ws://localhost:8765 -r
```

### 代理代理

```bash
# 带连接者令牌的服务器
linksocks server -t server_token -c connector_token -r -p 9870

# 提供者客户端
linksocks provider -t server_token -u ws://localhost:8765

# 连接者客户端
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

### 自主模式

```bash
# 启用自主的服务器
linksocks server -t server_token -r -a

# 带自定义连接者的提供者
linksocks provider -t server_token -c my_connector -u ws://localhost:8765
```

## 性能调优

### 缓冲区大小

为高吞吐量场景增加缓冲区大小：
```bash
linksocks server -b 65536
linksocks client -t token -b 65536
```

### 线程

为并发处理使用多个线程：
```bash
linksocks client -t token -T 8
```

### 快速打开

启用快速打开以降低延迟（节省一个 RTT）：
```bash
linksocks server -f
linksocks client -t token -f
```
