# Go CLI 和库

LinkSocks 提供命令行工具和 Go 库，用于构建自定义代理应用程序。

## CLI 概述

`linksocks` 命令行工具支持服务器、客户端、提供者和连接者模式。完整用法请参见 CLI 参考。

## 快速开始

### 正向代理

1. **启动服务器：**
   ```bash
   linksocks server -t my_token
   ```

2. **启动客户端：**
   ```bash
   linksocks client -t my_token -u ws://localhost:8765 -p 9870
   ```

3. **使用代理：**
   ```bash
   curl --socks5 localhost:9870 http://httpbin.org/ip
   ```

### 反向代理

1. **启动服务器：**
   ```bash
   linksocks server -t my_token -p 9870 -r
   ```

2. **启动客户端（提供者）：**
   ```bash
   linksocks client -t my_token -u ws://localhost:8765 -r
   ```

3. **使用代理：**
   ```bash
   curl --socks5 localhost:9870 http://httpbin.org/ip
   ```

## CLI 命令

标志和示例请参见 CLI 参考。

## 库快速开始

使用 Go 库的最小编程示例。

### 服务器

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/linksocks/linksocks/linksocks"
)

func main() {
    srv := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption())

    // 添加正向令牌（空字符串自动生成）
    token, err := srv.AddForwardToken("")
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("令牌：%s", token)

    // 启动并等待就绪（30 秒超时）
    if err := srv.WaitReady(context.Background(), 30*time.Second); err != nil {
        log.Fatal(err)
    }
    log.Println("服务器就绪")

    // 保持运行
    select {}
}
```

### 客户端

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/linksocks/linksocks/linksocks"
)

func main() {
    opt := linksocks.DefaultClientOption().
        WithWSURL("ws://localhost:8765").
        WithSocksPort(9870)

    cli := linksocks.NewLinkSocksClient("your_token", opt)
    defer cli.Close()

    // 启动并等待就绪（30 秒超时）
    if err := cli.WaitReady(context.Background(), 30*time.Second); err != nil {
        log.Fatal(err)
    }
    log.Println("SOCKS5 在 127.0.0.1:9870 就绪")

    select {}
}
```

完整的 API 和选项，请参见库 API。

## 代理模式说明

### 正向代理
- **客户端**在本地运行 SOCKS5 服务器
- **服务器**建立出站连接
- 流量流向：应用 → SOCKS5 → WebSocket → 目标

### 反向代理  
- **服务器**在本地运行 SOCKS5 服务器
- **客户端**建立出站连接
- 流量流向：应用 → SOCKS5 → WebSocket → 目标

### 代理代理（三层）
- **服务器**运行 SOCKS5 服务器
- **提供者**提供网络访问
- **连接者**连接到 SOCKS5 服务器
- 流量流向：应用 → SOCKS5 → WebSocket → 提供者 → 目标

## 环境变量

环境变量是可选的且最少的。详细信息请参见 CLI 参考。

## 下一步

- [库 API](./library.md) - Go 库 API 参考
- [示例](./examples.md) - 实用代码示例
