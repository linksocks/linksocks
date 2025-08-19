# Go Examples

Minimal, practical examples for using the LinkSocks Go library.

## Reverse Proxy: Server

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
    log.Printf("Reverse token: %s, SOCKS5 port: %d", res.Token, res.Port)

    ctx := context.Background()
    if err := server.WaitReady(ctx, 30*time.Second); err != nil {
        log.Fatal(err)
    }

    log.Println("Reverse server ready")
    <-ctx.Done()
    server.Close()
}
```

## Reverse Proxy: Provider Client

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

    log.Println("Provider client ready")
    <-ctx.Done()
}
```

## Agent Proxy: Server + Provider + Connector

### Agent Server

```go
server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption())

opts := linksocks.DefaultReverseTokenOptions()
opts.Port = 9870
opts.AllowManageConnector = true

res, _ := server.AddReverseToken(opts)
connectorToken, _ := server.AddConnectorToken("", res.Token)

_ = connectorToken
```

### Provider Client

```go
opt := linksocks.DefaultClientOption().
    WithWSURL("ws://localhost:8765").
    WithReverse(true)

provider := linksocks.NewLinkSocksClient("provider_token", opt)
defer provider.Close()
_ = provider.WaitReady(context.Background(), 0)
```

### Connector Client

```go
opt := linksocks.DefaultClientOption().
    WithWSURL("ws://localhost:8765").
    WithSocksPort(1180)

connector := linksocks.NewLinkSocksClient("connector_token", opt)
defer connector.Close()
_ = connector.WaitReady(context.Background(), 0)
```

---

See also: `docs/go/library.md` for full API and options.