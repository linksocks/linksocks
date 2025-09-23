# Go 示例

使用 LinkSocks Go 库的最小实用示例。

## 反向代理：服务器

```go
package main

import (
    "context"
    "log"
    "time"

    "github.com/linksocks/linksocks/linksocks"
)

func main() {
    server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption())

    opts := linksocks.DefaultReverseTokenOptions()
    opts.Port = 9870
    opts.Username = "user"
    opts.Password = "pass"

    res, err := server.AddReverseToken(opts)
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("反向令牌：%s，SOCKS5 端口：%d", res.Token, res.Port)

    ctx := context.Background()
    if err := server.WaitReady(ctx, 30*time.Second); err != nil {
        log.Fatal(err)
    }

    log.Println("反向服务器就绪")
    <-ctx.Done()
    server.Close()
}
```

## 反向代理：提供者客户端

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
        WithReverse(true)

    client := linksocks.NewLinkSocksClient("reverse_token", opt)
    defer client.Close()

    ctx := context.Background()
    if err := client.WaitReady(ctx, 30*time.Second); err != nil {
        log.Fatal(err)
    }

    log.Println("提供者客户端就绪")
    <-ctx.Done()
}
```

## 代理代理：服务器 + 提供者 + 连接者

### 代理服务器

```go
server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption())

opts := linksocks.DefaultReverseTokenOptions()
opts.Port = 9870
opts.AllowManageConnector = true

res, _ := server.AddReverseToken(opts)
connectorToken, _ := server.AddConnectorToken("", res.Token)

_ = connectorToken
```

### 提供者客户端

```go
opt := linksocks.DefaultClientOption().
    WithWSURL("ws://localhost:8765").
    WithReverse(true)

provider := linksocks.NewLinkSocksClient("provider_token", opt)
defer provider.Close()
_ = provider.WaitReady(context.Background(), 0)
```

### 连接者客户端

```go
opt := linksocks.DefaultClientOption().
    WithWSURL("ws://localhost:8765").
    WithSocksPort(1180)

connector := linksocks.NewLinkSocksClient("connector_token", opt)
defer connector.Close()
_ = connector.WaitReady(context.Background(), 0)
```

---

另请参见：`docs/zh/go/library.md` 获取完整的 API 和选项。
