# 命令行选项

建议按这个顺序阅读和配置：

1. 先选模式。
2. 先填关键参数。
3. 只在需要时再加其他参数。

## 模式速览

| 模式 | 命令 | 用途 |
|------|------|------|
| 正向代理 | `linksocks server` + `linksocks client` | SOCKS5 监听在客户端 |
| 反向代理 | `linksocks server -r` + `linksocks client -r` | SOCKS5 监听在服务端 |
| 代理模式 | `linksocks server -r -c ...` + `linksocks provider` + `linksocks connector` | 分离 provider 和 connector 权限 |
| 自主模式 | `linksocks server -r -a` + `linksocks provider -c ...` + `linksocks connector` | provider 自行管理 connector token |
| 直连传输 | 客户端加 `--direct-*`，服务端加 `--direct-enable` | 在可用时优先尝试点对点传输 |

## 服务端命令

`server` 用于启动 WebSocket 中继服务。加上 `-r` 后，SOCKS5 会暴露在服务端。

### 关键参数

| 参数 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--token` | `-t` | 未提供时自动生成（由服务端管理的模式） | 主认证令牌，也支持 `LINKSOCKS_TOKEN` |
| `--ws-host` | `-H` | `0.0.0.0` | WebSocket 监听地址，也支持 `LINKSOCKS_WEBSOCKET_HOST` |
| `--ws-port` | `-P` | `8765` | WebSocket 监听端口，也支持 `LINKSOCKS_WEBSOCKET_PORT` |
| `--reverse` | `-r` | `false` | 从正向中继切换到反向/代理模式 |
| `--socks-host` | `-s` | `127.0.0.1` | 反向模式下的 SOCKS5 监听地址 |
| `--socks-port` | `-p` | `9870` | 反向模式下的 SOCKS5 监听端口 |

### 其他参数

| 参数 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--connector-token` | `-c` | 未提供时自动生成 | 代理模式使用的 connector token，也支持 `LINKSOCKS_CONNECTOR_TOKEN` |
| `--connector-autonomy` | `-a` | `false` | 允许 provider 自己管理 connector token，也支持 `LINKSOCKS_CONNECTOR_AUTONOMY` |
| `--socks-username` | `-n` | | 反向模式下的 SOCKS5 用户名，也支持 `LINKSOCKS_SOCKS_USERNAME` |
| `--socks-password` | `-w` | | 反向模式下的 SOCKS5 密码，也支持 `LINKSOCKS_SOCKS_PASSWORD` |
| `--socks-nowait` | `-i` | `false` | 不等 provider 就立即启动 SOCKS5 |

| `--api-key` | `-k` | | 启用 HTTP API，也支持 `LINKSOCKS_API_KEY` |
| `--buffer-size` | `-b` | `1048576` | 数据传输缓冲区大小 |
| `--upstream-proxy` | `-x` | | 服务端出站连接使用的上游代理，也支持 `LINKSOCKS_UPSTREAM_PROXY` |
| `--fast-open` | `-f` | `false` | 在远端完全确认前就允许开始传输数据，也支持 `LINKSOCKS_FASTOPEN` |
| `--connector-wait-provider` | | `5s` | connector 等待 provider 重连的时间 |
| `--direct-enable` | | `false` | 为兼容客户端开启直连协商 |
| `--direct-rendezvous-udp` | | `false` | 开启服务端 UDP rendezvous。要求服务端能监听真实 UDP 端口，Cloudflare Workers 不支持。 |
| `--direct-rendezvous-host` | | `ws-host` | rendezvous 的 UDP 地址 |
| `--direct-rendezvous-port` | | `ws-port` | rendezvous 的 UDP 端口 |
| `--debug` | `-d` | | 调试日志，使用 `-dd` 可输出 trace |

## 客户端、Provider、Connector 命令

`client` 是通用命令：

- `linksocks client` = 正向代理客户端
- `linksocks client -r` = 反向模式 provider
- `linksocks provider` = `linksocks client -r` 的快捷命令
- `linksocks connector` = 常用于 connector token 的客户端别名

### 关键参数

| 参数 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--token` | `-t` | | 认证令牌，也支持 `LINKSOCKS_TOKEN` |
| `--url` | `-u` | `ws://localhost:8765` | WebSocket 服务端地址，也支持 `LINKSOCKS_URL` |
| `--reverse` | `-r` | `false` | 将 `client` 切换为反向 provider |
| `--socks-host` | `-s` | `127.0.0.1` | 正向或 connector 模式下的本地 SOCKS5 地址，也支持 `LINKSOCKS_SOCKS_HOST` |
| `--socks-port` | `-p` | `9870` | 正向或 connector 模式下的本地 SOCKS5 端口，也支持 `LINKSOCKS_SOCKS_PORT` |

### 其他参数

| 参数 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--connector-token` | `-c` | | 代理/自主模式使用的 connector token，也支持 `LINKSOCKS_CONNECTOR_TOKEN` |
| `--socks-username` | `-n` | | 本地 SOCKS5 用户名，也支持 `LINKSOCKS_SOCKS_USERNAME` |
| `--socks-password` | `-w` | | 本地 SOCKS5 密码，也支持 `LINKSOCKS_SOCKS_PASSWORD` |
| `--socks-no-wait` | `-i` | `false` | 立即启动本地 SOCKS5 |
| `--no-reconnect` | `-R` | `false` | 与服务端断开后直接退出 |

| `--threads` | `-T` | `1` | 数据传输线程数 |
| `--upstream-proxy` | `-x` | | 连接 WebSocket 服务端时使用的上游代理，也支持 `LINKSOCKS_UPSTREAM_PROXY` |
| `--no-env-proxy` | `-E` | `false` | 忽略环境变量中的代理配置 |
| `--fast-open` | `-f` | `false` | 在远端完全确认前就允许开始传输数据，也支持 `LINKSOCKS_FASTOPEN` |
| `--direct-mode` | | `auto` | `relay-only`、`auto` 或 `direct-only` |
| `--direct-discovery` | | `stun` | 直连候选地址发现方式 |
| `--direct-host-candidates` | | `auto` | 主机地址候选公布策略 |
| `--stun-server` | | 内置地址池 | 额外 STUN 服务器，可重复指定 |
| `--direct-only-action` | | `exit` | `direct-only` 失败时的处理方式 |
| `--direct-upnp` | | `false` | 为直连启用 UPnP 映射 |
| `--direct-upnp-lease` | | `30m` | UPnP 租约时长 |
| `--direct-upnp-keep` | | `false` | 退出时保留 UPnP 映射 |
| `--direct-upnp-external-port` | | `0` | 显式指定 UPnP 外部端口 |
| `--debug` | `-d` | | 调试日志，使用 `-dd` 可输出 trace |

## 模式示例

### 1. 正向代理

```bash
linksocks server -t my_token
linksocks client -t my_token -u ws://localhost:8765 -p 9870
```

### 2. 反向代理

```bash
linksocks server -t my_token -r -p 9870
linksocks client -t my_token -u ws://localhost:8765 -r
```

### 3. 代理模式

```bash
linksocks server -t provider_token -c connector_token -r -p 9870
linksocks provider -t provider_token -u ws://localhost:8765
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

### 4. 自主模式

```bash
linksocks server -t provider_token -r -a
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

### 5. 直连传输

```bash
linksocks server -t my_token --direct-enable
linksocks client -t my_token -u ws://localhost:8765 --direct-mode auto
```

## 环境变量

下面这些参数也可以通过环境变量提供：

| 环境变量 | 对应参数 |
|----------|----------|
| `LINKSOCKS_MODE` | 根命令模式别名（`server`、`client`、`provider`、`connector`） |
| `LINKSOCKS_URL` | `--url` |
| `LINKSOCKS_WEBSOCKET_HOST` | `--ws-host` |
| `LINKSOCKS_WEBSOCKET_PORT` | `--ws-port` |
| `LINKSOCKS_SOCKS_HOST` | `--socks-host` |
| `LINKSOCKS_SOCKS_PORT` | `--socks-port` |
| `LINKSOCKS_TOKEN` | `--token` |
| `LINKSOCKS_CONNECTOR_TOKEN` | `--connector-token` |
| `LINKSOCKS_SOCKS_USERNAME` | `--socks-username` |
| `LINKSOCKS_SOCKS_PASSWORD` | `--socks-password` |
| `LINKSOCKS_API_KEY` | `--api-key` |
| `LINKSOCKS_CONNECTOR_AUTONOMY` | `--connector-autonomy` |
| `LINKSOCKS_UPSTREAM_PROXY` | `--upstream-proxy` |
| `LINKSOCKS_FASTOPEN` | `--fast-open` |

## 上游代理格式

`--upstream-proxy` 同时支持 SOCKS5 和 HTTP 代理 URL：

```text
socks5://[username[:password]@]host[:port]
http://[username[:password]@]host[:port]
```

示例：

- `socks5://proxy.example.com:1080`
- `socks5://user:pass@proxy.example.com:1080`
- `http://user:pass@proxy.example.com:8080`
