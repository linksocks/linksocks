# 命令行选项

本页按「先选模式，再填必要参数，其余按需追加」的顺序组织。大多数场景只需关键参数；其他参数用于认证、性能、直连等进阶配置。

## 模式一览

| 模式 | 命令组合 | 说明 |
|------|----------|------|
| 正向代理 | `linksocks server` + `linksocks client` | 服务端出网；混合本地代理（SOCKS5 + HTTP）开在客户端本机 |
| 反向代理 | `linksocks server -r` + `linksocks client -r` | 客户端出网；混合本地代理（SOCKS5 + HTTP）开在服务端 |
| 中继代理 | `linksocks server -r -c ...` + `linksocks provider` + `linksocks connector` | 服务端只做中继；出网与本地代理分别由 provider / connector 承担 |
| 中继代理（自助连接者管理） | `linksocks server -r -a` + `linksocks provider -c ...` + `linksocks connector` | 中继代理的变体：由 provider 自行登记 connector token，服务端不再统一下发 |
| 直连传输 | 客户端 `--direct-*`，服务端 `--direct-enable` | 在中继协商成功后，尽量走点对点传输，降低延迟 |

### 角色说明

| 角色 | 职责 |
|------|------|
| **server** | WebSocket 中继。可选在本机监听混合本地代理（反向代理时） |
| **client** | 通用客户端。默认做正向代理的本地代理端；加 `-r` 后等价于 provider |
| **provider** | `client -r` 的快捷命令：连上中继后，用本机网络替对方出站 |
| **connector** | 用 connector token 连上中继，在本机开混合本地代理，流量经对应 provider 出站 |

## 服务端（`server`）

启动 WebSocket 中继。加 `-r` 后进入反向 / 中继类模式（本地代理可开在服务端，或交给 connector）。

本地监听端口为混合端口：同一端口同时接受 SOCKS5 与 HTTP 代理客户端。HTTP 支持 `CONNECT`（HTTPS 推荐）以及绝对形式 HTTP 请求；UDP 仍仅通过 SOCKS5。配置 `--socks-username` / `--socks-password` 时，SOCKS5 用户名密码与 HTTP `Proxy-Authorization: Basic` 共用同一套凭证。

### 关键参数

| 参数 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--token` | `-t` | 省略时由服务端自动生成（服务端管理令牌的模式） | 主认证令牌。也可用环境变量 `LINKSOCKS_TOKEN` |
| `--ws-host` | `-H` | `0.0.0.0` | WebSocket 监听地址。也可用 `LINKSOCKS_WEBSOCKET_HOST` |
| `--ws-port` | `-P` | `8765` | WebSocket 监听端口。也可用 `LINKSOCKS_WEBSOCKET_PORT` |
| `--reverse` | `-r` | `false` | 切换到反向 / 中继类模式（否则为正向中继） |
| `--socks-host` | `-s` | `127.0.0.1` | 反向模式下混合本地代理监听地址 |
| `--socks-port` | `-p` | `9870` | 反向模式下混合本地代理监听端口 |

### 其他参数

| 参数 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--connector-token` | `-c` | 省略时自动生成 | 中继代理下 connector 使用的令牌。也可用 `LINKSOCKS_CONNECTOR_TOKEN` |
| `--connector-autonomy` | `-a` | `false` | 开启自助连接者管理：由 provider 自行登记 connector token。也可用 `LINKSOCKS_CONNECTOR_AUTONOMY` |
| `--socks-username` | `-n` | | 反向模式下本地代理用户名（SOCKS5 与 HTTP Basic）。也可用 `LINKSOCKS_SOCKS_USERNAME` |
| `--socks-password` | `-w` | | 反向模式下本地代理密码（SOCKS5 与 HTTP Basic）。也可用 `LINKSOCKS_SOCKS_PASSWORD` |
| `--socks-nowait` | `-i` | `false` | 不等待 provider 就绪，立即启动混合本地代理 |
| `--api-key` | `-k` | | 启用 HTTP API。也可用 `LINKSOCKS_API_KEY` |
| `--buffer-size` | `-b` | `1048576` | 数据传输缓冲区大小（字节） |
| `--upstream-proxy` | `-x` | | 服务端出站时使用的上游代理。也可用 `LINKSOCKS_UPSTREAM_PROXY` |
| `--fast-open` | `-f` | `false` | 远端尚未完全确认前即允许开始传数据。也可用 `LINKSOCKS_FASTOPEN` |
| `--connector-wait-provider` | | `5s` | connector 等待 provider 重连的最长时间 |
| `--direct-enable` | | `false` | 为兼容客户端开启直连协商 |
| `--direct-rendezvous-udp` | | `false` | 开启服务端 UDP rendezvous。需真实 UDP 监听；Cloudflare Workers 不支持 |
| `--direct-rendezvous-host` | | 与 `ws-host` 相同 | rendezvous 的 UDP 地址 |
| `--direct-rendezvous-port` | | 与 `ws-port` 相同 | rendezvous 的 UDP 端口 |
| `--debug` | `-d` | | 调试日志；`-dd` 输出 trace |

## 客户端 / Provider / Connector

三者共用同一套客户端逻辑，差别主要在默认角色与常用参数：

| 命令 | 等价关系 | 典型用途 |
|------|----------|----------|
| `linksocks client` | 正向代理客户端 | 本机开混合本地代理，经 server 出网 |
| `linksocks client -r` | 反向 / 中继中的 provider | 本机网络对外提供出站 |
| `linksocks provider` | `client -r` 的快捷命令 | 同上 |
| `linksocks connector` | 面向 connector token 的客户端别名 | 本机开混合本地代理，经指定 provider 出网 |

### 关键参数

| 参数 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--token` | `-t` | | 认证令牌。也可用 `LINKSOCKS_TOKEN` |
| `--url` | `-u` | `ws://localhost:8765` | WebSocket 服务端地址。也可用 `LINKSOCKS_URL` |
| `--reverse` | `-r` | `false` | 将 `client` 切换为 provider（反向 / 中继出站端） |
| `--socks-host` | `-s` | `127.0.0.1` | 正向或 connector 模式下本地混合代理地址。也可用 `LINKSOCKS_SOCKS_HOST` |
| `--socks-port` | `-p` | `9870` | 正向或 connector 模式下本地混合代理端口。也可用 `LINKSOCKS_SOCKS_PORT` |

### 其他参数

| 参数 | 简写 | 默认值 | 说明 |
|------|------|--------|------|
| `--connector-token` | `-c` | | 中继代理 / 自助连接者管理下，provider 登记的 connector token。也可用 `LINKSOCKS_CONNECTOR_TOKEN` |
| `--socks-username` | `-n` | | 本地代理用户名（SOCKS5 与 HTTP Basic）。也可用 `LINKSOCKS_SOCKS_USERNAME` |
| `--socks-password` | `-w` | | 本地代理密码（SOCKS5 与 HTTP Basic）。也可用 `LINKSOCKS_SOCKS_PASSWORD` |
| `--socks-no-wait` | `-i` | `false` | 立即启动混合本地代理，不等待远端就绪 |
| `--no-reconnect` | `-R` | `false` | 与服务端断开后直接退出（默认会重连） |
| `--threads` | `-T` | `1` | 数据传输线程数 |
| `--upstream-proxy` | `-x` | | 连接 WebSocket 服务端时使用的上游代理。也可用 `LINKSOCKS_UPSTREAM_PROXY` |
| `--no-env-proxy` | `-E` | `false` | 忽略环境变量中的代理配置 |
| `--fast-open` | `-f` | `false` | 远端尚未完全确认前即允许开始传数据。也可用 `LINKSOCKS_FASTOPEN` |
| `--direct-mode` | | `auto` | 直连策略：`relay-only`、`auto` 或 `direct-only` |
| `--direct-discovery` | | `stun` | 直连候选地址发现方式 |
| `--direct-host-candidates` | | `auto` | 主机地址候选公布策略 |
| `--stun-server` | | 内置地址池 | 额外 STUN 服务器，可重复指定 |
| `--direct-only-action` | | `exit` | `direct-only` 失败时的处理方式 |
| `--direct-upnp` | | `false` | 为直连启用 UPnP 端口映射 |
| `--direct-upnp-lease` | | `30m` | UPnP 租约时长 |
| `--direct-upnp-keep` | | `false` | 退出时保留 UPnP 映射 |
| `--direct-upnp-external-port` | | `0` | 显式指定 UPnP 外部端口（`0` 表示自动） |
| `--debug` | `-d` | | 调试日志；`-dd` 输出 trace |

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
# 或：linksocks provider -t my_token -u ws://localhost:8765
```

### 3. 中继代理

服务端统一管理 provider token 与 connector token；混合本地代理开在 connector 本机。

```bash
linksocks server -t provider_token -c connector_token -r -p 9870
linksocks provider -t provider_token -u ws://localhost:8765
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

### 4. 中继代理（自助连接者管理）

服务端只校验 provider；每个 provider 用 `-c` 登记自己的 connector token。连接者只连到对应 provider，不做跨 provider 负载均衡。

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

下列参数也可通过环境变量传入（Docker / Compose 常用）：

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

## 上游代理 URL 格式

`--upstream-proxy` 支持 SOCKS5 与 HTTP：

```text
socks5://[username[:password]@]host[:port]
http://[username[:password]@]host[:port]
```

示例：

- `socks5://proxy.example.com:1080`
- `socks5://user:pass@proxy.example.com:1080`
- `http://user:pass@proxy.example.com:8080`
