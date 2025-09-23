---
layout: home

hero:
  name: "LinkSocks"
  text: "基于 WebSocket 的 SOCKS5 代理"
  image:
    src: /hero.png
    alt: LinkSocks
  tagline: "零配置内网穿透工具"
  actions:
    - theme: brand
      text: 开始使用
      link: /zh/guide/
    - theme: alt
      text: GitHub
      link: https://github.com/linksocks/linksocks

features:
  - icon: 🌐
    title: 零配置
    details: 专为非特定的动态客户端设计；客户端可以随时加入/离开
  - icon: ☁️
    title: 无服务器架构
    details: 中继服务器可以部署在 Cloudflare Workers 上。快速且全球化。
  - icon: ⚖️
    title: 负载均衡
    details: 动态增加或减少客户端作为后端，实现负载均衡
  - icon: 🌍
    title: IPv6 + UDP 支持
    details: 完整的 SOCKS5 协议支持，包括 IPv6 和 UDP over SOCKS5
  - icon: 🐍
    title: Python 绑定
    details: Python API，便于集成到现有应用程序中
  - icon: 📱
    title: 多平台
    details: 提供 Go 二进制文件和 Docker 镜像，支持跨平台
---

## 快速开始

```bash
go install github.com/linksocks/linksocks/cmd/linksocks@latest
```

或者从 [发布页面](https://github.com/linksocks/linksocks/releases) 下载预构建的二进制文件。

### 正向代理

```bash
# 服务端（WebSocket 端口 8765，作为网络提供者）
linksocks server -t example_token

# 客户端（SOCKS5 端口 9870）
linksocks client -t example_token -u http://localhost:8765 -p 9870
```

### 反向代理

```bash
# 服务端（WebSocket 端口 8765，SOCKS 端口 9870）
linksocks server -t example_token -p 9870 -r

# 客户端（作为网络提供者）
linksocks client -t example_token -u http://localhost:8765 -r
```
