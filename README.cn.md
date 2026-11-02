[![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/linksocks/linksocks/ci.yml?logo=github&label=Tests)](https://github.com/linksocks/linksocks/actions) [![Codecov](https://img.shields.io/codecov/c/github/linksocks/linksocks?logo=codecov&logoColor=white)](https://app.codecov.io/gh/linksocks/linksocks/tree/main) [![Docker Pulls](https://img.shields.io/docker/pulls/jackzzs/linksocks?logo=docker&logoColor=white)](https://hub.docker.com/r/jackzzs/linksocks)

# LinkSocks

LinkSocks 是一个基于 WebSocket 协议的跨网络跨机器 SOCKS 代理实现。

[English README / 英文文档](README.md)

## 概述

LinkSocks 允许您在 Web 应用防火墙（WAF）保护下安全地提供 SOCKS 代理服务（正向代理模式），或使没有公网 IP 的客户端连接并作为 SOCKS 代理服务器（反向代理模式）。

![架构图](https://github.com/linksocks/linksocks/raw/main/images/abstract.svg)

📖 **文档**: https://linksocks-docs.zetx.tech/

## 特性

1. 支持命令行使用、API 服务器和库集成
2. 支持正向、反向和中继代理模式
3. 反向代理支持轮询负载均衡
4. 混合本地代理端口：同一端口同时支持 SOCKS5 与 HTTP 代理
5. 本地代理身份验证（SOCKS5 用户名密码与 HTTP Basic）
6. 支持 SOCKS5 上的 IPv6
7. 支持 SOCKS5 上的 UDP

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
# 匿名临时域名（服务端 WEB_ZONE=example.com）
linksocks web -u wss://linksocks.example.com -l http://127.0.0.1:8080

# 使用账号令牌发布固定域名，并配置两个本地源站
linksocks web -u wss://linksocks.example.com -t ls_xxx \
  --domain blog.example.com \
  -l http://127.0.0.1:8080 -l http://127.0.0.1:8081
```

参数概览：`-t/--token`、`-u/--url`、`-l/--local`（可重复）、`--domain`、
`--insecure`（跳过 https 源站证书校验）、`-R/--no-reconnect`、`-d/--debug`。

`--domain` 使用的是账号的**永久域名授权**，需要在 linksocks.js 管理后台为对应账号
开启。授权后该域名不轮换也不过期，Provider 在场就一直有效；没有授权的账号请求
`--domain` 会被服务端拒绝。服务端开启强制临时域名（`forceTemporaryHost`）时，
未获授权的账号即使指定 `--domain` 也会被改写成随机域名，日志里会给出新 URL。

#### 实测示例

先在本地起一个网页服务，再通过公共中继发布，最后从公网访问：

```bash
$ python3 -m http.server 8080 --bind 127.0.0.1

$ linksocks web -u wss://l.zetx.tech -l http://127.0.0.1:8080
2026-11-02T16:41:25+08:00 INF Starting LinkSocks web provider backends=["http://127.0.0.1:8080"] domain= insecure=false server=wss://l.zetx.tech
2026-11-02T16:41:28+08:00 INF Welcome to LinkSocks.js relay server. This server is running in edge: AMS (Amsterdam, Netherlands), relay: KIX (Osaka, Japan). Your connection comes from CN (<client-ip>). After you connected, this relay group has 1 provider and 0 connectors.
2026-11-02T16:41:29+08:00 INF Web site published backends=1 expires=2026-11-02T22:41:28+08:00 host=ugxzfihx.team1.cc.cd temporary=true url=https://ugxzfihx.team1.cc.cd
2026-11-02T16:41:29+08:00 INF Temporary URL https://ugxzfihx.team1.cc.cd expires at 2026-11-02T22:41:28+08:00; it will rotate automatically
```

中继分配了 `https://ugxzfihx.team1.cc.cd`，访问该地址的请求会转发到本地源站：

```bash
$ curl -sS https://ugxzfihx.team1.cc.cd/
<!doctype html>
<html><head><title>LinkSocks demo site</title></head>
<body><h1>Hello from a local origin</h1><p id="marker">linksocks-web-demo-20261102</p><script src="/assets/app.js"></script></body></html>

$ curl -sS -o /dev/null -w '%{http_code}\n' https://ugxzfihx.team1.cc.cd/assets/app.js
200

$ curl -sS -o /dev/null -w '%{http_code}\n' https://ugxzfihx.team1.cc.cd/not-found
404
```

终端保持运行时隧道一直有效，`Ctrl+C` 结束。临时域名按服务端配置的临时网址有效期
过期（默认 6 小时），Provider 在线期间域名会自动轮换。

服务端（Cloudflare Worker、D1 数据库、管理后台与配额）说明见 linksocks.js 仓库。

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

linksocks.js 版本是一个轻量级版本，不包含 API 功能。

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
