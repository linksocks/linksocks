# Web 类

把本地 HTTP(S) 网站发布到 [linksocks.js](https://github.com/linksocks/linksocks.js)
中继。请求复用同一条 WebSocket 隧道，源站可以位于 NAT 之后，无需公网 IP 或入站端口。

## 概述

```python
from linksocks import Web

with Web(["http://127.0.0.1:8080"], url="wss://l.zetx.tech") as site:
    host = site.wait_ready(timeout=30)
    print(f"已发布：https://{host}")
```

中继会在其 `WEB_ZONE` 下分配临时域名，也可以用 `domain` 指定固定域名。
传入多个 backend 时按轮询方式转发。固定域名需要中继方授权，未授权的账号会被拒绝。

## 构造函数

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

| 参数 | 类型 | 默认值 | 说明 |
|------|------|--------|------|
| `backends` | `list[str]` | 必填 | 本地 HTTP(S) 源站地址 |
| `url` | `str` | `"ws://localhost:8765"` | linksocks.js 服务地址 |
| `token` | `str` | `""` | 控制台 Provider token |
| `domain` | `str \| None` | `None` | 中继 `WEB_ZONE` 下的固定域名 |
| `insecure` | `bool` | `False` | 跳过 HTTPS 源站的证书校验 |
| `no_reconnect` | `bool` | `False` | 中继断开后停止运行 |
| `reconnect_delay` | `DurationLike` | `None` | 重连间隔 |
| `logger` | `logging.Logger` | `None` | 接收中继日志的 Python logger |

## 方法

### wait_ready(timeout=None)

阻塞直到站点注册完成，返回分配到的域名。

```python
site.wait_ready()
site.wait_ready(timeout=30)
site.wait_ready(timeout="1m")
```

### async_wait_ready(timeout=None)

`wait_ready` 的异步版本。

### close()

停止 Provider 并释放中继注册。

### async_close()

`close` 的异步版本。

## 属性

### host

当前远端域名；临时网址轮换后该值会更新。

```python
print(site.host)
```

## 异步示例

```python
import asyncio
from linksocks import Web


async def main():
    async with Web(
        ["http://127.0.0.1:8080", "http://127.0.0.1:8081"],
        url="wss://l.zetx.tech",
        domain="blog.example.com",
    ) as site:
        print(f"已发布：https://{site.host}")
        await asyncio.Future()


asyncio.run(main())
```

`async with Web(...)` 会在进入代码块前等待注册完成。同步的 `with Web(...)`
不会等待，需要在块内调用 `wait_ready()` 获取域名。

## 命令行

安装后的 `linksocks` 命令提供同样的能力：

```bash
# 临时域名
linksocks web -u wss://l.zetx.tech -l http://127.0.0.1:8080

# 控制台 token、固定域名和两个源站
linksocks web -u wss://l.zetx.tech -t ls_xxx \
  --domain blog.example.com \
  -l http://127.0.0.1:8080 -l http://127.0.0.1:8081
```

通过公共中继实测：

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

终端保持运行时隧道一直有效，`Ctrl+C` 结束。临时域名按配置的有效期过期（默认 6 小时），
Provider 在线期间会自动轮换。
