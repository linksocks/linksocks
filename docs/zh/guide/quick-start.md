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

## 正向代理

在正向代理模式下，服务器提供网络访问，客户端运行 SOCKS5 接口。

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

**测试代理：**
```bash
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

## 反向代理

在反向代理模式下，服务器运行 SOCKS5 接口，客户端提供网络访问。

**服务端：**
```bash
# 在端口 9870 启动 SOCKS5 代理服务器
linksocks server -t example_token -r -p 9870
```

**客户端：**
```bash
# 作为网络提供者连接
linksocks client -t example_token -u ws://localhost:8765 -r
```

**测试代理：**
```bash
curl --socks5 127.0.0.1:9870 http://httpbin.org/ip
```

## 代理代理模式

在代理代理模式下，服务器充当两种类型客户端之间的中继：提供者（共享网络访问）和连接者（使用代理）。每种类型使用不同的令牌进行受控访问。

**服务端：**
```bash
# 使用提供者和连接者令牌启动服务器
linksocks server -t provider_token -c connector_token -p 9870 -r
```

**提供者端：**
```bash
# 作为网络提供者连接
linksocks provider -t provider_token -u ws://localhost:8765
```

**连接者端：**
```bash
# 连接使用代理
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

**测试代理：**
```bash
curl --socks5 127.0.0.1:1180 http://httpbin.org/ip
```

## 自主模式

自主模式是一种特殊类型的代理代理，具有以下特征：

1. 服务器的 SOCKS 代理不会开始监听
2. 提供者可以指定自己的连接者令牌
3. 负载均衡被禁用 - 每个连接者的请求只路由到对应的提供者

**服务端：**
```bash
# 在自主模式下启动服务器
linksocks server -t provider_token -r -a
```

**提供者端：**
```bash
# 提供者设置自己的连接者令牌
linksocks provider -t provider_token -c my_connector_token -u ws://localhost:8765
```

**连接者端：**
```bash
# 使用特定的连接者令牌访问此提供者
linksocks connector -t my_connector_token -u ws://localhost:8765 -p 1180
```

### 使用我们的公共服务器

您可以使用我们在 `linksocks.zetx.tech` 的公共 LinkSocks 服务器进行内网穿透：

**步骤 1：在机器 A 上（您要访问的网络内部）**
```bash
linksocks provider -t any_token -u wss://linksocks.zetx.tech -c your_token
```

**步骤 2：在机器 B 上（您要访问网络的地方）**
```bash
linksocks connector -t your_token -u wss://linksocks.zetx.tech -p 1080
```

**测试连接：**
```bash
curl --socks5 127.0.0.1:1080 http://httpbin.org/ip
```

## 在 Cloudflare Workers 上部署服务器

在 Cloudflare Workers 上部署 LinkSocks 服务器实现无服务器运行：

[![部署到 Cloudflare](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/linksocks/linksocks.js)

服务器将在自主模式下启动。部署后，使用以下方式连接：

```bash
linksocks client -t your_token -u wss://your-worker.your-subdomain.workers.dev -p 9870
```

## API 服务器

LinkSocks 服务器提供 HTTP API 用于动态令牌管理，允许您添加/删除令牌并监控连接，无需重启服务器。

```bash
# 启动启用 API 的服务器
linksocks server --api-key your_api_key
```

详细的 API 使用方法和示例，参见：[HTTP API](/zh/guide/http-api)

## 常用选项

### 身份验证
```bash
# 使用 SOCKS 身份验证的服务器
linksocks server -t token -r -p 9870 -n username -w password

# 使用 SOCKS 身份验证的客户端
linksocks client -t token -u ws://localhost:8765 -n username -w password
```

### 调试模式
```bash
# 启用调试日志
linksocks server -t token -d
linksocks client -t token -u ws://localhost:8765 -d
```

### 自定义地址
```bash
# 服务器监听所有接口
linksocks server -t token -H 0.0.0.0 -P 8765

# 客户端自定义 SOCKS 地址
linksocks client -t token -u ws://localhost:8765 -h 0.0.0.0 -p 1080
```

## 下一步

- 了解[命令行选项](/zh/guide/cli-options)进行高级配置
- 理解[身份验证](/zh/guide/authentication)和安全选项
- 探索[Python 库](/zh/python/)进行集成
- 查看[HTTP API](/zh/guide/http-api)进行动态管理
