# Go CLI & Library

LinkSocks provides both a command-line tool and a Go library for building custom proxy applications.

## CLI Overview

The `linksocks` command-line tool supports server, client, provider, and connector modes. See CLI Reference for full usage.

## Quick Start

### Forward Proxy

1. **Start Server:**
   ```bash
   linksocks server -t my_token
   ```

2. **Start Client:**
   ```bash
   linksocks client -t my_token -u ws://localhost:8765 -p 9870
   ```

3. **Use Proxy:**
   ```bash
   curl --socks5 localhost:9870 http://httpbin.org/ip
   ```

### Reverse Proxy

1. **Start Server:**
   ```bash
   linksocks server -t my_token -p 9870 -r
   ```

2. **Start Client (Provider):**
   ```bash
   linksocks client -t my_token -u ws://localhost:8765 -r
   ```

3. **Use Proxy:**
   ```bash
   curl --socks5 localhost:9870 http://httpbin.org/ip
   ```

## CLI Commands

See CLI Reference for flags and examples.

## Library Quick Start

Minimal programmatic examples using the Go library.

### Server

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

    // Add a forward token (empty string auto-generates)
    token, err := srv.AddForwardToken("")
    if err != nil {
        log.Fatal(err)
    }
    log.Printf("Token: %s", token)

    // Start and wait until ready (30s timeout)
    if err := srv.WaitReady(context.Background(), 30*time.Second); err != nil {
        log.Fatal(err)
    }
    log.Println("Server ready")

    // Keep running
    select {}
}
```

### Client

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

    // Start and wait until ready (30s timeout)
    if err := cli.WaitReady(context.Background(), 30*time.Second); err != nil {
        log.Fatal(err)
    }
    log.Println("SOCKS5 ready on 127.0.0.1:9870")

    select {}
}
```

For full API and options, see Library API.

## Proxy Modes Explained

### Forward Proxy
- **Client** runs SOCKS5 server locally
- **Server** makes outbound connections
- Traffic flow: App → SOCKS5 → WebSocket → Target

### Reverse Proxy  
- **Server** runs SOCKS5 server locally
- **Client** makes outbound connections
- Traffic flow: App → SOCKS5 → WebSocket → Target

### Agent Proxy (Three-tier)
- **Server** runs SOCKS5 server
- **Provider** provides network access
- **Connector** connects to SOCKS5 server
- Traffic flow: App → SOCKS5 → WebSocket → Provider → Target

## Environment Variables

Environment variables are optional and minimal. See CLI Reference for details.

## Next Steps

- [Library API](./library.md) - Go library API reference
- [Examples](./examples.md) - Practical code examples