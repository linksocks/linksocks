[![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/linksocks/linksocks/ci.yml?logo=github&label=Tests)](https://github.com/linksocks/linksocks/actions) [![Codecov](https://img.shields.io/codecov/c/github/linksocks/linksocks?logo=codecov&logoColor=white)](https://app.codecov.io/gh/linksocks/linksocks/tree/main) [![Docker Pulls](https://img.shields.io/docker/pulls/jackzzs/linksocks?logo=docker&logoColor=white)](https://hub.docker.com/r/jackzzs/linksocks)

# LinkSocks

LinkSocks 是一个基于 WebSocket 协议的跨网络跨机器 SOCKS 代理实现。

[English README / 英文文档](README.md)

## 概述

LinkSocks 允许您在 Web 应用防火墙（WAF）保护下安全地提供 SOCKS 代理服务（正向代理模式），或使没有公网 IP 的客户端连接并作为 SOCKS 代理服务器（反向代理模式）。

代理通道端到端加密（协议代 2）：provider 与 connector 协商 Noise XX 会话，目标地址、载荷和对端裁决都在密文内，中继只按 channel ID 转发帧；Zone 数据包在成员之间加密；网站发布沿用旧协议代，因为中继需要读取 HTTP 才能提供服务域名。

![架构图](https://github.com/linksocks/linksocks/raw/main/images/abstract.svg)

📖 **文档**: https://linksocks-docs.zetx.tech/

**网站**：[l.zetx.tech](https://l.zetx.tech) ·
[公开 Zone](https://l.zetx.tech/zones)

## 特性

1. 代理通道端到端加密（协议代 2）：provider 与 connector 运行 Noise XX 握手，目标地址与载荷都在密文内，中继只按 channel ID 转发
2. 支持命令行使用、API 服务器和库集成
3. 支持正向、反向和中继代理模式
4. 反向代理支持轮询负载均衡
5. 混合本地代理端口：同一端口同时支持 SOCKS5 与 HTTP 代理
6. 本地代理身份验证（SOCKS5 用户名密码与 HTTP Basic）
7. 支持 SOCKS5 上的 IPv6
8. 支持 SOCKS5 上的 UDP
9. Zone 虚拟 IPv4 网络：默认在本机运行 SOCKS5 代理，Linux 下可用 `--vlan` 创建二层 TAP 网卡
10. 默认启用 Encrypted Client Hello：连接中继时查找中继域名的 ECH 配置，TLS ClientHello 中不再暴露目标主机名

## 潜在应用场景

1. 分布式 HTTP 后端
2. 使用客户端代理绕过验证码
3. 通过 CDN 网络实现安全的内网穿透

作为一个好的案例, 请参考 [LinkTerm](https://github.com/linksocks/linkterm) 和 [Cloudflyer](https://github.com/cloudflyer-project/cloudflyer) 项目.

## 快速开始

### 正向代理

```bash
# 服务端：在 8765 端口启动 WebSocket 服务
linksocks server -t example_token

# 客户端：连接到服务端并在 9870 端口提供 SOCKS5 代理
linksocks client -t example_token -u ws://localhost:8765 -p 9870

# 测试代理
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

同一本地端口也接受 HTTP 代理客户端：

```bash
curl -x http://127.0.0.1:9870 http://httpbin.org/ip
curl -x http://127.0.0.1:9870 https://httpbin.org/ip
```

### 反向代理

```bash
# 服务端：在 9870 端口启动 SOCKS5 代理服务
linksocks server -t example_token -r -p 9870

# 客户端：作为网络提供方连接
linksocks client -t example_token -u ws://localhost:8765 -r

# 测试代理
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

### 中继代理

```bash
# 服务端：使用提供方和连接器令牌启动服务
linksocks server -t provider_token -c connector_token -p 9870 -r

# 提供方：作为网络提供方连接
linksocks provider -t provider_token -u ws://localhost:8765

# 连接器：连接并使用代理
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180

# 测试代理
curl --socks5 127.0.0.1:1180 http://httpbin.org/ip
```

### 中继代理（自助连接者管理）

```bash
# 服务端：以自助连接者管理方式启动中继
linksocks server -t provider_token -r -a

# 提供方：provider 设置自己的 connector token
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765

# 连接器：使用 provider 登记的 connector token 访问该 provider
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

### 网站反向代理

`linksocks web` 可以把本地 HTTP(S) 网站发布到
[linksocks.js](https://github.com/linksocks/linksocks.js) Worker。HTTP 请求复用同一条
WebSocket 隧道，源站始终位于 NAT 之后，无需公网 IP 或入站端口。

```bash
# 匿名临时域名（公共中继）
linksocks web -u wss://l.zetx.tech -l http://127.0.0.1:8080

# 使用账号令牌发布自定义域名前缀，并配置两个本地源站
linksocks web -u wss://l.zetx.tech -t ls_xxx \
  --domain-prefix blog \
  -l http://127.0.0.1:8080 -l http://127.0.0.1:8081
```

参数概览：`-t/--token`、`-u/--url`、`-l/--local`（可重复）、`-p/--domain-prefix`、
`--insecure`（跳过 https 源站证书校验）、`-R/--no-reconnect`、`-d/--debug`。

`--domain-prefix blog` 会把站点发布到 `blog.<WEB_ZONE>`：域名由中继选择，Provider
无法自行指定域名。永久域名授权、轮换规则和强制临时域名模式见
[网站发布指南](https://linksocks.github.io/zh/guide/quick-start)，指南里也有公共中继的实测过程。

服务端（Cloudflare Worker、D1 数据库、管理后台与配额）说明见 linksocks.js 仓库。

### Zone 虚拟网络

Zone 把多台机器连成一个虚拟 IPv4 网络：每台机器获得自己的地址，成员之间可以互相
访问 TCP 和 UDP 服务，流量端到端加密。先在网站的 `/zones` 页面创建 Zone，再在每台
机器上加入；加入无需账号：

```bash
# 机器 A
linksocks zone join <zone-id>

# 机器 B
linksocks zone join <zone-id>
```

每台机器会打印分配到的地址，并在本机 `127.0.0.1:1080` 启动 SOCKS5 代理；之后可以
用虚拟地址或成员名访问其他成员：

```bash
curl --socks5-hostname 127.0.0.1:1080 http://machine-b:8080
```

`zone join` 默认使用公共协调服务 `https://l.zetx.tech`，可用 `-u` 指定其他地址。
Linux 下 `--vlan`（需要 root）会改用二层 TAP 网卡加入，额外支持 ARP、ICMP、广播、
组播和 WOL。`linksocks zone leave` 立即释放地址，`linksocks zone list` 列出公开
Zone。

新建 Zone 默认为私有，只有拿到 Zone ID 的机器才能加入。所有者和管理员可以在同一个
`/zones` 页面管理成员、防火墙规则、欢迎语和踢人。平台要求、身份密钥等细节和真实
Linux 实测见 [Zone 使用指南](https://linksocks.github.io/zh/guide/zones)。Zone 没有
Python 库 API。

自行部署需要 Worker 的 `DB` 绑定、指向 SQLite 存储 `Zone` Durable Object 的 `ZONE`
绑定，以及 D1 迁移 `0005_zones.sql` 与 `0006_zone_visibility.sql`。详见
[Worker 部署说明](https://github.com/linksocks/linksocks.js/blob/main/README.cn.md#zone-虚拟网络)。

## 安装

### Golang 版本
```bash
go install github.com/linksocks/linksocks/cmd/linksocks@latest
```

或从[发布页面](https://github.com/linksocks/linksocks/releases)下载预编译二进制文件。

### Docker
```bash
docker run --rm -it jackzzs/linksocks --help
```

### Python 版本
```bash
pip install linksocks
```

> Python 版本是 Golang 实现的封装。详见：[Python 绑定](https://linksocks-docs.zetx.tech/python/)

## Cloudflare Worker

LinkSocks 服务端可以部署在 Cloudflare Worker 上，详见：[linksocks/linksocks.js](https://github.com/linksocks/linksocks.js)

[![部署到 Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/linksocks/linksocks.js)

Worker 提供账号、网站发布和 Zone 管理页面，其管理接口与下文 Go 服务端的令牌 API 不同。

## API 服务

## API 服务端

LinkSocks 服务端提供用于动态令牌管理的 HTTP API：

```bash
# 启用 API 功能启动服务端
linksocks server --api-key your_api_key
```

API Key 仅用于认证，不携带任何访问规则。访问控制通过令牌级别（建令牌时指定）或服务器级别（entry/dial access control）配置。

详细的 API 使用说明和示例，请参见：[HTTP API](https://linksocks-docs.zetx.tech/guide/http-api)

## 许可证

LinkSocks 在 MIT 许可证下开源。
