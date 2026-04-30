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
2. 支持正向、反向和代理模式
3. 反向代理支持轮询负载均衡
4. 支持 SOCKS 代理身份验证
5. 支持 SOCKS5 上的 IPv6
6. 支持 SOCKS5 上的 UDP

## 潜在应用场景

1. 分布式 HTTP 后端
2. 使用客户端代理绕过验证码
3. 通过 CDN 网络实现安全的内网穿透

作为一个好的案例, 请参考 [LinkTerm](https://github.com/linksocks/linkterm) 和 [Cloudflyer](https://github.com/cloudflyer-project/cloudflyer) 项目.

## 快速开始

Docker Compose 示例（Provider / Connector，默认 `l.zetx.tech`）：https://linksocks-docs.zetx.tech/zh/guide/quick-start#docker-compose

### 正向代理

```bash
# 服务端：在 8765 端口启动 WebSocket 服务
linksocks server -t example_token

# 客户端：连接到服务端并在 9870 端口提供 SOCKS5 代理
linksocks client -t example_token -u ws://localhost:8765 -p 9870

# 测试代理
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
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

### 代理模式

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

### 自主模式

```bash
# 服务端：以自主模式启动服务
linksocks server -t provider_token -r -a

# 提供方：提供方设置自己的连接器令牌
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765

# 连接器：使用特定的连接器令牌访问此提供方
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

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

LinkSocks 服务端提供用于动态令牌管理的 HTTP API：

```bash
# 启用 API 功能启动服务端
linksocks server --api-key your_api_key
```

详细的 API 使用说明和示例，请参见：[HTTP API](https://linksocks-docs.zetx.tech/guide/http-api)

## 许可证

LinkSocks 在 MIT 许可证下开源。
