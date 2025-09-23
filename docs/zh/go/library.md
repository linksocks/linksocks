# Go 库 API 参考

使用 LinkSocks 作为 Go 库的完整参考。

## 安装

```bash
go get github.com/linksocks/linksocks
```

## 导入

```go
import "github.com/linksocks/linksocks/linksocks"
```

## 服务器 API

### 创建服务器

```go
// 默认服务器
server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption())

// 自定义配置
opt := linksocks.DefaultServerOption().
    WithWSHost("0.0.0.0").
    WithWSPort(8765).
    WithSocksHost("127.0.0.1").
    WithBufferSize(32768).
    WithFastOpen(true)

server := linksocks.NewLinkSocksServer(opt)
```

### 服务器选项

| 方法 | 描述 | 默认值 |
|------|------|--------|
| `WithWSHost(host)` | WebSocket 监听地址 | `"0.0.0.0"` |
| `WithWSPort(port)` | WebSocket 监听端口 | `8765` |
| `WithSocksHost(host)` | SOCKS5 绑定地址 | `"127.0.0.1"` |
| `WithPortPool(pool)` | 反向代理的端口池 | 自动生成 |
| `WithSocksWaitClient(wait)` | 启动 SOCKS 前等待客户端 | `true` |
| `WithBufferSize(size)` | 数据传输缓冲区大小 | `32768` |
| `WithAPI(key)` | 使用密钥启用 HTTP API | 禁用 |
| `WithChannelTimeout(timeout)` | WebSocket 通道超时 | `30s` |
| `WithConnectTimeout(timeout)` | 连接超时 | `10s` |
| `WithFastOpen(enable)` | 启用快速打开优化 | `false` |
| `WithUpstreamProxy(url)` | 上游代理 URL | 无 |
| `WithUpstreamAuth(user, pass)` | 上游代理认证 | 无 |
| `WithLogger(logger)` | 自定义 zerolog 记录器 | 默认 |

### 令牌管理

```go
// 正向代理令牌
token, err := server.AddForwardToken("custom_token")  // 或 "" 自动生成
if err != nil {
    log.Fatal(err)
}

// 反向代理令牌
opts := linksocks.DefaultReverseTokenOptions()
opts.Port = 9870
opts.Username = "user"
opts.Password = "pass"
opts.AllowManageConnector = false

result, err := server.AddReverseToken(opts)
if err != nil {
    log.Fatal(err)
}
log.Printf("令牌：%s，端口：%d", result.Token, result.Port)

// 连接者令牌（用于代理代理）
connectorToken, err := server.AddConnectorToken("connector_token", "reverse_token")
if err != nil {
    log.Fatal(err)
}

// 删除任何令牌
removed := server.RemoveToken("token_to_remove")
```

### 服务器生命周期

```go
// 启动服务器并等待就绪
ctx := context.Background()
if err := server.WaitReady(ctx, 30*time.Second); err != nil {
    log.Fatal(err)
}

// 优雅关闭
server.Close()
```

### 服务器状态

```go
// 检查客户端连接
clientCount := server.GetClientCount()
hasClients := server.HasClients()

// 检查特定令牌的客户端
tokenClients := server.GetTokenClientCount("specific_token")
```

## 客户端 API

### 创建客户端

```go
// 默认客户端
client := linksocks.NewLinkSocksClient("token", linksocks.DefaultClientOption())

// 自定义配置
opt := linksocks.DefaultClientOption().
    WithWSURL("ws://localhost:8765").
    WithReverse(false).
    WithSocksPort(9870).
    WithReconnect(true).
    WithThreads(4)

client := linksocks.NewLinkSocksClient("token", opt)
```

### 客户端选项

| 方法 | 描述 | 默认值 |
|------|------|--------|
| `WithWSURL(url)` | WebSocket 服务器 URL | `"ws://localhost:8765"` |
| `WithReverse(enable)` | 启用反向代理模式 | `false` |
| `WithSocksHost(host)` | SOCKS5 监听地址 | `"127.0.0.1"` |
| `WithSocksPort(port)` | SOCKS5 监听端口 | `9870` |
| `WithSocksUsername(user)` | SOCKS5 认证用户名 | 无 |
| `WithSocksPassword(pass)` | SOCKS5 认证密码 | 无 |
| `WithSocksWaitServer(wait)` | SOCKS 前等待服务器 | `true` |
| `WithReconnect(enable)` | 断开连接时自动重连 | `false` |
| `WithReconnectDelay(delay)` | 重连间的延迟 | `5s` |
| `WithBufferSize(size)` | 数据传输缓冲区大小 | `32768` |
| `WithChannelTimeout(timeout)` | WebSocket 通道超时 | `30s` |
| `WithConnectTimeout(timeout)` | 连接超时 | `10s` |
| `WithThreads(count)` | 并发 WebSocket 连接 | `1` |
| `WithFastOpen(enable)` | 启用快速打开优化 | `false` |
| `WithUpstreamProxy(url)` | 上游代理 URL | 无 |
| `WithUpstreamAuth(user, pass)` | 上游代理认证 | 无 |
| `WithNoEnvProxy(disable)` | 忽略代理环境变量 | `false` |
| `WithLogger(logger)` | 自定义 zerolog 记录器 | 默认 |

### 客户端生命周期

```go
// 启动客户端并等待就绪
ctx := context.Background()
if err := client.WaitReady(ctx, 30*time.Second); err != nil {
    log.Fatal(err)
}

// 检查连接状态
if client.IsConnected {
    log.Println("客户端已连接")
}

// 获取 SOCKS 端口（正向模式）
if port := client.SocksPort; port > 0 {
    log.Printf("SOCKS5 服务器在端口 %d", port)
}

// 优雅关闭
client.Close()
```

### 连接者管理（反向模式）

```go
// 添加连接者（仅反向模式）
connectorToken, err := client.AddConnector("custom_connector")  // 或 "" 自动生成
if err != nil {
    log.Fatal(err)
}

// 删除连接者（仅反向模式）
err = client.RemoveConnector("connector_token")
if err != nil {
    log.Fatal(err)
}

// 获取伙伴计数
partnersCount := client.GetPartnersCount()
```

## 高级用法

### 自定义日志

```go
import "github.com/rs/zerolog"

logger := zerolog.New(os.Stdout).With().
    Timestamp().
    Str("component", "linksocks").
    Logger()

// 带自定义记录器的服务器
serverOpt := linksocks.DefaultServerOption().WithLogger(logger)
server := linksocks.NewLinkSocksServer(serverOpt)

// 带自定义记录器的客户端
clientOpt := linksocks.DefaultClientOption().WithLogger(logger)
client := linksocks.NewLinkSocksClient("token", clientOpt)
```

### 上下文管理

```go
// 创建可取消上下文
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// 使用超时
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

// 信号优雅关闭
c := make(chan os.Signal, 1)
signal.Notify(c, os.Interrupt, syscall.SIGTERM)
go func() {
    <-c
    log.Println("正在关闭...")
    cancel()
}()

// 使用上下文启动
if err := server.WaitReady(ctx, 0); err != nil {
    if err == context.Canceled {
        log.Println("请求关闭")
    } else {
        log.Fatal(err)
    }
}
```

### 错误处理

```go
// 服务器错误处理
server := linksocks.NewLinkSocksServer(opt)

// 添加令牌并检查错误
if token, err := server.AddForwardToken(""); err != nil {
    log.Printf("添加令牌失败：%v", err)
} else {
    log.Printf("令牌已添加：%s", token)
}

// 客户端错误处理
client := linksocks.NewLinkSocksClient("token", opt)

// 带重试逻辑的连接
for attempts := 0; attempts < 3; attempts++ {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    err := client.WaitReady(ctx, 0)
    cancel()
    
    if err == nil {
        break // 成功
    }
    
    log.Printf("连接尝试 %d 失败：%v", attempts+1, err)
    if attempts < 2 {
        time.Sleep(5 * time.Second)
    }
}
```

### 性能调优

```go
// 高吞吐量服务器
serverOpt := linksocks.DefaultServerOption().
    WithBufferSize(131072).      // 128KB 缓冲区
    WithFastOpen(true).          // 减少 RTT
    WithChannelTimeout(300*time.Second) // 长超时

// 高吞吐量客户端
clientOpt := linksocks.DefaultClientOption().
    WithBufferSize(131072).      // 128KB 缓冲区
    WithThreads(8).              // 多个连接
    WithFastOpen(true).          // 减少 RTT
    WithChannelTimeout(300*time.Second) // 长超时

// 低延迟配置
lowLatencyOpt := linksocks.DefaultClientOption().
    WithBufferSize(4096).        // 小缓冲区
    WithFastOpen(true).          // 立即响应
    WithConnectTimeout(2*time.Second) // 快速超时
```
