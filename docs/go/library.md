# Go Library API Reference

Complete reference for using LinkSocks as a Go library.

## Installation

```bash
go get github.com/linksocks/linksocks
```

## Import

```go
import "github.com/linksocks/linksocks/linksocks"
```

## Server API

### Creating a Server

```go
// Default server
server := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption())

// Custom configuration
opt := linksocks.DefaultServerOption().
    WithWSHost("0.0.0.0").
    WithWSPort(8765).
    WithSocksHost("127.0.0.1").
    WithBufferSize(32768).
    WithFastOpen(true)

server := linksocks.NewLinkSocksServer(opt)
```

### Server Options

| Method | Description | Default |
|--------|-------------|---------|
| `WithWSHost(host)` | WebSocket listen address | `"0.0.0.0"` |
| `WithWSPort(port)` | WebSocket listen port | `8765` |
| `WithSocksHost(host)` | SOCKS5 bind address | `"127.0.0.1"` |
| `WithPortPool(pool)` | Port pool for reverse proxies | Auto-generated |
| `WithSocksWaitClient(wait)` | Wait for clients before starting SOCKS | `true` |
| `WithBufferSize(size)` | Data transfer buffer size | `32768` |
| `WithAPI(key)` | Enable HTTP API with key | Disabled |
| `WithChannelTimeout(timeout)` | WebSocket channel timeout | `30s` |
| `WithConnectTimeout(timeout)` | Connection timeout | `10s` |
| `WithFastOpen(enable)` | Enable fast open optimization | `false` |
| `WithUpstreamProxy(url)` | Upstream proxy URL | None |
| `WithUpstreamAuth(user, pass)` | Upstream proxy auth | None |
| `WithLogger(logger)` | Custom zerolog logger | Default |

### Token Management

```go
// Forward proxy token
token, err := server.AddForwardToken("custom_token")  // or "" for auto-gen
if err != nil {
    log.Fatal(err)
}

// Reverse proxy token
opts := linksocks.DefaultReverseTokenOptions()
opts.Port = 9870
opts.Username = "user"
opts.Password = "pass"
opts.AllowManageConnector = false

result, err := server.AddReverseToken(opts)
if err != nil {
    log.Fatal(err)
}
log.Printf("Token: %s, Port: %d", result.Token, result.Port)

// Connector token (for agent proxy)
connectorToken, err := server.AddConnectorToken("connector_token", "reverse_token")
if err != nil {
    log.Fatal(err)
}

// Remove any token
removed := server.RemoveToken("token_to_remove")
```

### Server Lifecycle

```go
// Start server and wait for ready
ctx := context.Background()
if err := server.WaitReady(ctx, 30*time.Second); err != nil {
    log.Fatal(err)
}

// Graceful shutdown
server.Close()
```

### Server Status

```go
// Check client connections
clientCount := server.GetClientCount()
hasClients := server.HasClients()

// Check token-specific clients
tokenClients := server.GetTokenClientCount("specific_token")
```

## Client API

### Creating a Client

```go
// Default client
client := linksocks.NewLinkSocksClient("token", linksocks.DefaultClientOption())

// Custom configuration
opt := linksocks.DefaultClientOption().
    WithWSURL("ws://localhost:8765").
    WithReverse(false).
    WithSocksPort(9870).
    WithReconnect(true).
    WithThreads(4)

client := linksocks.NewLinkSocksClient("token", opt)
```

### Client Options

| Method | Description | Default |
|--------|-------------|---------|
| `WithWSURL(url)` | WebSocket server URL | `"ws://localhost:8765"` |
| `WithReverse(enable)` | Enable reverse proxy mode | `false` |
| `WithSocksHost(host)` | SOCKS5 listen address | `"127.0.0.1"` |
| `WithSocksPort(port)` | SOCKS5 listen port | `9870` |
| `WithSocksUsername(user)` | SOCKS5 auth username | None |
| `WithSocksPassword(pass)` | SOCKS5 auth password | None |
| `WithSocksWaitServer(wait)` | Wait for server before SOCKS | `true` |
| `WithReconnect(enable)` | Auto-reconnect on disconnect | `false` |
| `WithReconnectDelay(delay)` | Delay between reconnects | `5s` |
| `WithBufferSize(size)` | Data transfer buffer size | `32768` |
| `WithChannelTimeout(timeout)` | WebSocket channel timeout | `30s` |
| `WithConnectTimeout(timeout)` | Connection timeout | `10s` |
| `WithThreads(count)` | Concurrent WebSocket connections | `1` |
| `WithFastOpen(enable)` | Enable fast open optimization | `false` |
| `WithUpstreamProxy(url)` | Upstream proxy URL | None |
| `WithUpstreamAuth(user, pass)` | Upstream proxy auth | None |
| `WithNoEnvProxy(disable)` | Ignore proxy environment vars | `false` |
| `WithLogger(logger)` | Custom zerolog logger | Default |

### Client Lifecycle

```go
// Start client and wait for ready
ctx := context.Background()
if err := client.WaitReady(ctx, 30*time.Second); err != nil {
    log.Fatal(err)
}

// Check connection status
if client.IsConnected {
    log.Println("Client is connected")
}

// Get SOCKS port (forward mode)
if port := client.SocksPort; port > 0 {
    log.Printf("SOCKS5 server on port %d", port)
}

// Graceful shutdown
client.Close()
```

### Connector Management (Reverse Mode)

```go
// Add connector (reverse mode only)
connectorToken, err := client.AddConnector("custom_connector")  // or "" for auto-gen
if err != nil {
    log.Fatal(err)
}

// Remove connector (reverse mode only)
err = client.RemoveConnector("connector_token")
if err != nil {
    log.Fatal(err)
}

// Get partners count
partnersCount := client.GetPartnersCount()
```

## Advanced Usage

### Custom Logging

```go
import "github.com/rs/zerolog"

logger := zerolog.New(os.Stdout).With().
    Timestamp().
    Str("component", "linksocks").
    Logger()

// Server with custom logger
serverOpt := linksocks.DefaultServerOption().WithLogger(logger)
server := linksocks.NewLinkSocksServer(serverOpt)

// Client with custom logger
clientOpt := linksocks.DefaultClientOption().WithLogger(logger)
client := linksocks.NewLinkSocksClient("token", clientOpt)
```

### Context Management

```go
// Create cancellable context
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

// Use with timeout
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

// Graceful shutdown on signal
c := make(chan os.Signal, 1)
signal.Notify(c, os.Interrupt, syscall.SIGTERM)
go func() {
    <-c
    log.Println("Shutting down...")
    cancel()
}()

// Start with context
if err := server.WaitReady(ctx, 0); err != nil {
    if err == context.Canceled {
        log.Println("Shutdown requested")
    } else {
        log.Fatal(err)
    }
}
```

### Error Handling

```go
// Server error handling
server := linksocks.NewLinkSocksServer(opt)

// Add tokens with error checking
if token, err := server.AddForwardToken(""); err != nil {
    log.Printf("Failed to add token: %v", err)
} else {
    log.Printf("Token added: %s", token)
}

// Client error handling
client := linksocks.NewLinkSocksClient("token", opt)

// Connection with retry logic
for attempts := 0; attempts < 3; attempts++ {
    ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
    err := client.WaitReady(ctx, 0)
    cancel()
    
    if err == nil {
        break // Success
    }
    
    log.Printf("Connection attempt %d failed: %v", attempts+1, err)
    if attempts < 2 {
        time.Sleep(5 * time.Second)
    }
}
```

### Performance Tuning

```go
// High throughput server
serverOpt := linksocks.DefaultServerOption().
    WithBufferSize(131072).      // 128KB buffer
    WithFastOpen(true).          // Reduce RTT
    WithChannelTimeout(300*time.Second) // Long timeout

// High throughput client
clientOpt := linksocks.DefaultClientOption().
    WithBufferSize(131072).      // 128KB buffer
    WithThreads(8).              // Multiple connections
    WithFastOpen(true).          // Reduce RTT
    WithChannelTimeout(300*time.Second) // Long timeout

// Low latency configuration
lowLatencyOpt := linksocks.DefaultClientOption().
    WithBufferSize(4096).        // Small buffer
    WithFastOpen(true).          // Immediate response
    WithConnectTimeout(2*time.Second) // Quick timeout
```
