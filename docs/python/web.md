# Web Class

Publish a local HTTP(S) site through a [linksocks.js](https://github.com/linksocks/linksocks.js)
relay. Requests reuse the WebSocket tunnel, so the origin stays behind NAT with
no public IP or inbound port.

## Overview

```python
from linksocks import Web

with Web(["http://127.0.0.1:8080"], url="wss://l.zetx.tech") as site:
    host = site.wait_ready(timeout=30)
    print(f"Published at https://{host}")
```

The relay returns a temporary hostname under its `WEB_ZONE`, or the hostname
passed in `domain`. Several backends are served round-robin. A fixed `domain`
requires the relay to allow it: an account without the hostname grant is
refused.

## Constructor

```python
Web(
    backends: list[str],
    *,
    url: str = "ws://localhost:8765",
    token: str = "",
    domain: Optional[str] = None,
    insecure: bool = False,
    no_reconnect: bool = False,
    reconnect_delay: Optional[DurationLike] = None,
    logger: Optional[logging.Logger] = None,
)
```

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `backends` | `list[str]` | Required | Local HTTP(S) origin URLs |
| `url` | `str` | `"ws://localhost:8765"` | linksocks.js server URL |
| `token` | `str` | `""` | Dashboard provider token |
| `domain` | `str \| None` | `None` | Fixed hostname under the relay's `WEB_ZONE` |
| `insecure` | `bool` | `False` | Skip TLS verification for HTTPS backends |
| `no_reconnect` | `bool` | `False` | Stop when the relay disconnects |
| `reconnect_delay` | `DurationLike` | `None` | Delay between reconnect attempts |
| `logger` | `logging.Logger` | `None` | Python logger for relay messages |

## Methods

### wait_ready(timeout=None)

Block until the site is registered and return the assigned hostname.

```python
site.wait_ready()
site.wait_ready(timeout=30)
site.wait_ready(timeout="1m")
```

### async_wait_ready(timeout=None)

Async variant of `wait_ready`.

### close()

Stop the provider and release the relay registration.

### async_close()

Async variant of `close`.

## Properties

### host

The current remote hostname. It changes when a temporary URL rotates.

```python
print(site.host)
```

## Async Example

```python
import asyncio
from linksocks import Web


async def main():
    async with Web(
        ["http://127.0.0.1:8080", "http://127.0.0.1:8081"],
        url="wss://l.zetx.tech",
        domain="blog.example.com",
    ) as site:
        print(f"Published at https://{site.host}")
        await asyncio.Future()


asyncio.run(main())
```

`async with Web(...)` waits for registration before entering the block. The
synchronous `with Web(...)` returns immediately; call `wait_ready()` inside the
block when you need the hostname.

## CLI

The installed `linksocks` command exposes the same provider:

```bash
# Temporary hostname
linksocks web -u wss://l.zetx.tech -l http://127.0.0.1:8080

# Dashboard token, fixed hostname and two origins
linksocks web -u wss://l.zetx.tech -t ls_xxx \
  --domain blog.example.com \
  -l http://127.0.0.1:8080 -l http://127.0.0.1:8081
```

A real run against the public relay:

```bash
$ python3 -m http.server 8080 --bind 127.0.0.1

$ linksocks web -u wss://l.zetx.tech -l http://127.0.0.1:8080
[11/02 16:42] INFO     Welcome to LinkSocks.js relay server. This server is running in edge: AMS
                       (Amsterdam, Netherlands), relay: HKG (Hong Kong). Your connection comes from CN
                       (<client-ip>). After you connected, this relay group has 1 provider and 0
                       connectors.
Web site published: https://gden3eme.team1.cc.cd
              INFO     Web site published backends=1 expires=2026-11-02T22:42:28+08:00
                       host=gden3eme.team1.cc.cd temporary=True url=https://gden3eme.team1.cc.cd
              INFO     Temporary URL https://gden3eme.team1.cc.cd expires at 2026-11-02T22:42:28+08:00; it
                       will rotate automatically

$ curl -sS https://gden3eme.team1.cc.cd/
<!doctype html>
<html><head><title>LinkSocks demo site</title></head>
<body><h1>Hello from a local origin</h1><p id="marker">linksocks-web-demo-20261102</p><script src="/assets/app.js"></script></body></html>

$ curl -sS -o /dev/null -w '%{http_code}\n' https://gden3eme.team1.cc.cd/assets/app.js
200

$ curl -sS -o /dev/null -w '%{http_code}\n' https://gden3eme.team1.cc.cd/not-found
404
```

Stop with `Ctrl+C`. Temporary URLs expire after the configured lifetime (6 hours
by default) and rotate automatically while the provider stays connected.
