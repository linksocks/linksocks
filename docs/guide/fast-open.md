# Fast Open

Fast Open is a performance optimization feature that significantly reduces connection latency by streamlining the SOCKS5 handshake process. It is particularly suitable for applications that make **large numbers of requests**, such as web browsing.

## How It Works

Fast Open optimizes the CONNECT handshake by allowing the SOCKS5 side to immediately reply with "success", enabling applications to send data immediately without waiting for the full handshake to complete.

::: tip Performance Benefit
Fast Open saves **one full round-trip time (RTT)** during connection establishment, significantly improving perceived performance for applications making many connections.
:::

## Configuration

Fast Open can be enabled independently on different components:

### Server Side (reverse proxy mode)
```bash
# Enable Fast Open on server
linksocks server -t token --fast-open
```

### Client Side (forward proxy mode)
```bash
# Enable Fast Open on client
linksocks client -t token -u ws://server:8765 --fast-open
```

## Timeout Settings and Connection Failure Handling

The default timeout for Fast Open is **ConnectTimeout (10 seconds) + 5 seconds guard time**. This means the proxy will wait up to 15 seconds for the actual connection to be established after sending the immediate success response.

If no response is received from the server after 15 seconds, or if the server responds that the connection cannot be established, the connection will be closed. This ensures that failed connections don't leave hanging sockets.

## Potential Issues and Limitations

Fast Open may cause problems in certain scenarios:

- **Application assumes immediate connectivity**: Some applications may assume that a successful SOCKS5 CONNECT response means the connection is immediately ready for data transfer
- **Error handling complexity**: Applications may receive data before the actual connection fails, leading to confusing error states
- **Network instability**: In unstable network environments, the delayed connection establishment may cause more timeouts

::: warning When to Disable Fast Open
If you experience connection issues, application errors, or unexpected timeouts, try disabling Fast Open by removing the `--fast-open` flag. Fast Open works best with stable networks and applications that can handle asynchronous connection establishment.
:::