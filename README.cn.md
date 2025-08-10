[![GitHub Actions Workflow Status](https://img.shields.io/github/actions/workflow/status/zetxtech/linksocks/ci.yml?logo=github&label=Tests)](https://github.com/zetxtech/linksocks/actions) [![Codecov](https://img.shields.io/codecov/c/github/zetxtech/linksocks?logo=codecov&logoColor=white)](https://app.codecov.io/gh/zetxtech/linksocks/tree/main) [![Docker Pulls](https://img.shields.io/docker/pulls/jackzzs/linksocks?logo=docker&logoColor=white)](https://hub.docker.com/r/jackzzs/linksocks)

# LinkSocks

LinkSocks 是一个基于 WebSocket 协议的跨网络跨机器 SOCKS 代理实现。

[English README](README.md)

## 概述

LinkSocks 允许您在 Web 应用防火墙（WAF）保护下安全地提供 SOCKS 代理服务（正向代理模式），或使没有公网 IP 的客户端连接并作为 SOCKS 代理服务器（反向代理模式）。

![架构图](https://github.com/zetxtech/linksocks/raw/main/images/abstract.svg)

如需 Python 版本，请查看 [zetxtech/pylinksocks](https://github.com/zetxtech/pylinksocks)。但请注意，Python 版本的性能通常低于 Go 语言实现版本。

## 特性

1. 支持命令行使用、API 服务器和库集成
2. 支持正向、反向和代理模式
3. 反向代理支持轮询负载均衡
4. 支持 SOCKS 代理身份验证
5. 支持 SOCKS5 上的 IPv6
6. 支持 SOCKS5 上的 UDP

## 潜在应用场景

1. 分布式 HTTP 后端
2. 使用客户端代理绕过验证码
3. 通过 CDN 网络实现安全的内网穿透

## 使用方法

### 命令行工具

正向代理模式：

```bash
# 服务端（WebSockets 监听 8765 端口，作为网络提供方）
linksocks server -t example_token

# 客户端（SOCKS5 监听 9870 端口）
linksocks client -t example_token -u http://localhost:8765 -p 9870
```

反向代理模式（使用 `-r` 参数）：

```bash
# 服务端（WebSockets 监听 8765 端口，SOCKS 监听 9870 端口）
linksocks server -t example_token -p 9870 -r

# 客户端（作为网络提供方）
linksocks client -t example_token -u http://localhost:8765 -r
```

代理模式（使用 `-c` 参数指定连接器令牌）：

```bash
# 服务端（WebSockets 监听 8765 端口，SOCKS 监听 9870 端口）
linksocks server -t example_token -c example_connector_token -p 9870 -r

# 客户端（作为网络提供方）
linksocks provider -t example_token -u http://localhost:8765

# 连接器（SOCKS5 监听 1180 端口）
linksocks connector -t example_connector_token -u http://localhost:8765 -p 1180
```

您也可以使用我们的公共演示服务器：

```bash
# 客户端（作为网络提供方）
linksocks provider -t any_token -u https://linksocks.zetx.tech -c any_connector_token

# 连接器（SOCKS5 监听 1180 端口）
linksocks connector -t any_connector_token -u https://linksocks.zetx.tech -p 1180
```

自主代理模式（使用 `-a` 参数）：

```bash
# 服务端（WebSocket 监听 8765 端口，自主模式）
linksocks server -r -t example_token -a

# 客户端（作为网络提供方，启动时设置连接器令牌）
linksocks provider -t example_token -c example_connector_token
```

在自主模式下：
1. 服务端的 SOCKS 代理不会启动监听
2. 反向客户端可以指定自己的连接器令牌
3. 负载均衡被禁用 - 每个连接器的请求只会路由到其对应的反向客户端

## 安装

安装 LinkSocks：

```bash
go install github.com/zetxtech/linksocks/cmd/linksocks@latest
```

您也可以从[发布页面](https://github.com/zetxtech/linksocks/releases)下载适合您系统架构的预编译二进制文件。

LinkSocks 也提供 Docker 镜像：

```bash
docker run --rm -it jackzzs/linksocks --help
```

## Cloudflare Worker

LinkSocks 服务端可以部署在 Cloudflare Worker 上，详见：[zetxtech/linksocks.js](https://github.com/zetxtech/linksocks.js)

[![部署到 Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/zetxtech/linksocks.js)

linksocks.js 版本是一个轻量级版本，不包含 API 功能。

## API 服务

使用 `--api-key` 参数启用时，LinkSocks 服务端提供 HTTP API：

```bash
# 启用 API 功能启动服务端
linksocks server --api-key your_api_key
```

### API 接口

所有 API 请求需要在请求头中包含 `X-API-Key` 字段及您配置的 API 密钥。

#### 获取服务器状态

```
GET /api/status
```

返回服务器版本以及所有令牌的类型和活跃客户端数量列表。

#### 添加正向令牌

```
POST /api/token
Content-Type: application/json

{
    "type": "forward",
    "token": "new_token"  // 可选：若不提供则自动生成
}
```

添加新的正向代理令牌。

#### 添加反向令牌

```
POST /api/token
Content-Type: application/json

{
    "type": "reverse",
    "token": "new_token",  // 可选：若不提供则自动生成
    "port": 9870,          // 可选：若不提供则自动分配
    "username": "user",    // 可选：SOCKS 身份验证
    "password": "pass"     // 可选：SOCKS 身份验证
}
```

添加带有指定 SOCKS 设置的新反向代理令牌。

#### 删除令牌

```
DELETE /api/token/{token}
```

或

```
DELETE /api/token

Content-Type: application/json

{
    "token": "token_to_delete"
}
```

删除指定的令牌。

## 许可证

LinkSocks 在 MIT 许可证下开源。
