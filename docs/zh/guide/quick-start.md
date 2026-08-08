# 快速开始

## 安装

### Golang 版本
```bash
go install github.com/linksocks/linksocks/cmd/linksocks@latest
```

或者从[发布页面](https://github.com/linksocks/linksocks/releases)下载预构建的二进制文件。

### Docker
```bash
docker run --rm -it jackzzs/linksocks --help
```

### Python 版本
```bash
pip install linksocks
```

::: info
Python 版本是 Golang 实现的包装器。参见：[Python 绑定](/zh/python/)
:::

## 选哪种模式？

| 模式 | 适合场景 | SOCKS5 在哪 | 谁出网 |
|------|----------|-------------|--------|
| **正向代理** | 把服务端所在网络共享给客户端 | 客户端本机 | 服务端 |
| **反向代理** | 把客户端（内网）网络共享到服务端 | 服务端 | 客户端（provider） |
| **中继代理** | 服务端只做中转；出网与 SOCKS5 分属两端 | connector 本机 | provider |
| **中继代理（自助连接者管理）** | 公共中继 / 无服务器中继；每个 provider 自己发 connector token | connector 本机 | 对应 provider |

日常「内网穿透、两端都主动连中继」优先用 **中继代理（自助连接者管理）**，也可直接用公共服务器 `l.zetx.tech`。

## 正向代理

服务端提供网络访问，客户端在本机开 SOCKS5。

**服务端：**
```bash
# 在端口 8765 启动 WebSocket 服务器
linksocks server -t example_token
```

**客户端：**
```bash
# 连接到服务器并在端口 9870 提供 SOCKS5 代理
linksocks client -t example_token -u ws://localhost:8765 -p 9870
```

**测试：**
```bash
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

## 反向代理

服务端开 SOCKS5，客户端作为网络提供者接入。

**服务端：**
```bash
# 在端口 9870 启动 SOCKS5 代理
linksocks server -t example_token -r -p 9870
```

**客户端（provider）：**
```bash
# 作为网络提供者连接
linksocks client -t example_token -u ws://localhost:8765 -r
# 或：linksocks provider -t example_token -u ws://localhost:8765
```

**测试：**
```bash
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

## 中继代理

服务端只做中继，不直接出网。用两套令牌区分角色：

- **provider token**（`-t`）：谁可以共享网络
- **connector token**（`-c`）：谁可以使用代理

**服务端：**
```bash
linksocks server -t provider_token -c connector_token -p 9870 -r
```

**Provider（在要共享的网络内）：**
```bash
linksocks provider -t provider_token -u ws://localhost:8765
```

**Connector（需要 SOCKS5 的机器）：**
```bash
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

**测试：**
```bash
curl --socks5 127.0.0.1:1180 http://httpbin.org/ip
```

## 中继代理（自助连接者管理）

中继代理的变体，适合公共中继或 Cloudflare Workers 等场景：

1. 服务端通常不监听 SOCKS5
2. 每个 provider 自行指定 connector token（`-c`）
3. 不做跨 provider 负载均衡：一个 connector 只连到登记了该 token 的 provider

**服务端：**
```bash
linksocks server -t provider_token -r -a
```

**Provider：**
```bash
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765
```

**Connector：**
```bash
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

### 使用公共服务器

公共中继 `l.zetx.tech` 即按此模式运行，无需自建 server：

**步骤 1：机器 A（要访问的网络内部）**
```bash
linksocks provider -t any_token -u wss://l.zetx.tech -c your_token
```

**步骤 2：机器 B（需要代理的地方）**
```bash
linksocks connector -t your_token -u wss://l.zetx.tech -p 1080
```

**测试：**
```bash
curl --socks5 127.0.0.1:1080 http://httpbin.org/ip
```

::: warning
请使用足够复杂的 connector token。任何持有该 token 的人都可以使用你的 provider 网络。
:::

生成强 token 示例：

```bash
openssl rand -hex 16
```

## 在 Cloudflare Workers 上部署服务器

无服务器中继可部署到 Cloudflare Workers：

[![部署到 Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/linksocks/linksocks.js)

部署后的 Worker 以 **中继代理（自助连接者管理）** 方式运行。连接示例：

```bash
# Provider
linksocks provider -t any_token -c your_token -u wss://your-worker.your-subdomain.workers.dev

# Connector
linksocks connector -t your_token -u wss://your-worker.your-subdomain.workers.dev -p 9870
```

## P2P 直连（QUIC）

在反向代理、中继代理及其变体中，默认开启 P2P 直连（`--direct-mode auto`，候选发现 `--direct-discovery stun`）。当 provider 与 connector 之间能建立 UDP 直连时，数据不再经服务器中转，而通过加密 QUIC 直传，以降低延迟、提高吞吐。

未指定 STUN 服务器时，程序会并发探测内置公共 STUN 池，并选用响应最快的节点。可用 `--stun-server` 自定义，或用 `--direct-mode` / `--direct-discovery` 覆盖默认行为。

**Linux 性能提示：** 大流量 QUIC 直连时若出现 `failed to sufficiently increase receive buffer size`，多为系统默认 UDP 缓冲区偏小。程序仍可工作；若追求最佳性能（约 7MB 理想缓冲），可在运行前调整：

```bash
sudo sysctl -w net.core.rmem_max=2500000
sudo sysctl -w net.core.wmem_max=2500000
```

## HTTP API

服务端可启用 HTTP API，用于动态增删令牌、查看连接，无需重启：

```bash
linksocks server --api-key your_api_key
```

详见：[HTTP API](/zh/guide/http-api)

## 常用选项

### SOCKS 身份验证
```bash
linksocks server -t token -r -p 9870 -n username -w password
linksocks client -t token -u ws://localhost:8765 -n username -w password
```

### 调试日志
```bash
linksocks server -t token -d
linksocks client -t token -u ws://localhost:8765 -d
```

### 自定义监听地址
```bash
# 服务端 WebSocket 监听所有接口
linksocks server -t token -H 0.0.0.0 -P 8765

# 客户端自定义 SOCKS 地址
linksocks client -t token -u ws://localhost:8765 -h 0.0.0.0 -p 1080
```

## 下一步

- [命令行选项](/zh/guide/cli-options)：完整参数与模式对照
- [身份验证](/zh/guide/authentication)：令牌与 SOCKS 凭据
- [Python 库](/zh/python/)：程序内集成
- [HTTP API](/zh/guide/http-api)：动态管理

## Docker Compose

在两台机器上分别运行 provider 与 connector，默认都连公共中继 `l.zetx.tech`。

::: warning
请使用足够复杂的 connector token。任何持有该 token 的人都可以连接并使用你的 provider。
:::

### Provider 侧

在 provider 机器上创建 `compose.yaml`：

```yaml
services:
  linksocks-provider:
    image: jackzzs/linksocks:latest
    environment:
      LINKSOCKS_MODE: provider
      LINKSOCKS_URL: l.zetx.tech
      LINKSOCKS_CONNECTOR_TOKEN: your_connector_token
    restart: unless-stopped
```

启动：

```bash
docker compose up -d
docker compose logs -f linksocks-provider
```

### Connector 侧

在 connector 机器上创建 `compose.yaml`：

```yaml
services:
  linksocks-connector:
    image: jackzzs/linksocks:latest
    environment:
      LINKSOCKS_MODE: connector
      LINKSOCKS_URL: l.zetx.tech
      LINKSOCKS_TOKEN: your_connector_token
    ports:
      - "127.0.0.1:1080:1080"
    restart: unless-stopped
```

启动并测试：

```bash
docker compose up -d
curl --socks5 127.0.0.1:1080 http://httpbin.org/ip
```

停止：

```bash
docker compose down
```

所有参数均可通过环境变量传入 — 参见[环境变量](/zh/guide/cli-options#环境变量)。
