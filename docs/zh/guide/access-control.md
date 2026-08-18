# 访问控制

访问控制以类似防火墙的规则限制隧道可访问的目标,包含两种独立规则:**入口访问控制**(在本地代理端口解析 SOCKS5/HTTP 请求时执行)与**拨号访问控制**(在实际发起连接、提供出口的 provider 拨号前执行)。这样,共享的 token 只能访问你明确允许的子网与端口。

## 角色

| 角色 | 命令 | 行为 |
|------|------|------|
| Provider(出口端) | `linksocks provider` / `client -r` | 实际连接目标地址(提供出站网络) |
| Connector(使用端) | `linksocks connector` / `client` | 运行应用使用的本地 SOCKS5/HTTP 代理端口 |
| Server(中继) | `linksocks server` | 在两端之间转发消息 |

存在**两种相互独立的访问控制**,各自在各自的一侧配置与执行:

1. **入口访问控制**:作用于本地代理端口解析 SOCKS5/HTTP 请求时。
2. **拨号访问控制**:作用于实际执行连接的一侧,在拨号前检查。

两者分别配置、互不共享规则。任一层拦截都会向应用返回失败。

## 规则

访问控制是一组**相互独立的允许条目**。每条条目将地址区与端口区配对。目标必须命中**至少一条条目**,且在该条目内同时满足地址与端口条件:

```text
规则 A: 192.168.1.0/24  +  端口 22
规则 B: 10.0.0.0/8      +  端口 8000-9000
```

以上规则下,`192.168.1.5:22` 被允许(规则 A),`10.1.2.3:8500` 被允许(规则 B),但 `192.168.1.5:80` 被拒绝(规则 A 端口不匹配、规则 B 地址不匹配)。

地址区为空表示匹配任意地址;端口区为空表示匹配任意端口。未配置任何规则 → 全部允许(默认行为)。

## 地址格式

每条条目可包含以下一种或多种:

| 格式 | 示例 | 含义 |
|------|------|------|
| CIDR | `192.168.0.0/16` | 子网 |
| 裸 IP | `192.168.1.5` | 单台主机(IPv4 为 `/32`,IPv6 为 `/128`) |
| 末段范围 | `192.168.1.1-255` | `192.168.1.1` 至 `192.168.1.255` |
| 完整 IP 范围 | `192.168.1.1-192.168.2.255` | 两端之间的任意 IP(含两端) |

## 端口格式

端口为**数字**。单个端口用数字表示;范围用 `[start, end]` 数组表示:

```json
22
[90, 150]
```

## 匹配语义

- 域名目标会先经 DNS 解析,解析出的 IP 必须落在条目地址区内;无法解析的域名会被**拒绝**。
- 同一条目内地址与端口必须同时满足。

## 配置方式

### 入口访问控制(本地代理端口)

作用于本地代理端口解析 SOCKS5/HTTP 请求时。

**服务器侧** —— reverse 模式下服务器暴露的端口(服务器级默认与按 token 覆盖):

```go
srv := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
    WithSocksWaitClient(false).
    WithEntryAccessControl(ac))

srv.AddReverseToken(&linksocks.ReverseTokenOptions{
    Token:         "my_token",
    AccessControl: ac, // 按 token 覆盖
})
```

**HTTP API** —— 创建 reverse token 时携带 `rules` 字段(作用于服务器本地代理端口):

```bash
curl -X POST \
     -H "X-API-Key: your_secret_api_key" \
     -H "Content-Type: application/json" \
     -d '{
       "type": "reverse",
       "token": "my_token",
       "rules": [
         {
           "addrs": ["192.168.1.0/24", "192.168.1.1-255"],
           "ports": [22, [90, 150]]
         },
         {
           "addrs": ["10.0.0.5"],
           "ports": [443]
         }
       ]
     }' \
     http://localhost:8765/api/token
```

非法的地址或端口范围会被拒绝并返回错误。

**客户端侧** —— 客户端开启的本地 SOCKS5/HTTP 端口(forward / connector 模式):

```go
client := linksocks.NewLinkSocksClient(token, linksocks.DefaultClientOption().
    WithWSURL("ws://your-server:8765").
    WithSocksPort(9870).
    WithEntryAccessControl(ac))
```

### 拨号访问控制(provider 出口)

作用于实际执行连接的一侧,在拨号前检查。目标不匹配任何规则时,连接被拒绝,SOCKS5 客户端收到 `0x04`(主机不可达)。

**客户端侧** —— provider 进程(`linksocks provider` / `client -r`):

```go
ac, err := linksocks.NewAccessControl([]linksocks.AccessRule{
    {
        Addrs: []string{"192.168.1.0/24", "10.0.0.5"},
        Ports: []linksocks.PortSpec{linksocks.SinglePort(22), linksocks.PortRange(90, 150)},
    },
})
if err != nil {
    log.Fatal(err)
}

client := linksocks.NewLinkSocksClient(token, linksocks.DefaultClientOption().
    WithWSURL("ws://your-server:8765").
    WithReverse(true).
    WithSocksWaitServer(true).
    WithDialAccessControl(ac))
```

**服务器侧** —— 服务器自身执行拨号时(forward 代理模式):

```go
srv := linksocks.NewLinkSocksServer(linksocks.DefaultServerOption().
    WithDialAccessControl(ac))
```

## 按 token 的规则

规则也可以绑定到单个 token。token 级规则对该 token **覆盖**对应的服务器级规则;存在时只检查 token 规则。

**Forward token** —— 服务器替该 token 拨号前执行:

```go
token, err := srv.AddForwardTokenWithRules("my_token", []linksocks.AccessRule{
    {Addrs: []string{"192.168.1.0/24"}, Ports: []linksocks.PortSpec{linksocks.SinglePort(22)}},
})
```

**Connector token** —— 服务器把该 connector 的请求转发给 reverse provider 前执行:

```go
token, err := srv.AddConnectorTokenWithRules("my_connector", "my_reverse_token", []linksocks.AccessRule{
    {Addrs: []string{"10.0.0.0/8"}, Ports: []linksocks.PortSpec{linksocks.PortRange(8000, 9000)}},
})
```

**HTTP API** —— 两种 token 类型都支持可选的 `rules` 字段:

```bash
curl -X POST \
     -H "X-API-Key: your_secret_api_key" \
     -H "Content-Type: application/json" \
     -d '{
       "type": "forward",
       "token": "my_token",
       "rules": [
         {"addrs": ["192.168.1.0/24"], "ports": [22]}
       ]
     }' \
     http://localhost:8765/api/token

curl -X POST \
     -H "X-API-Key: your_secret_api_key" \
     -H "Content-Type: application/json" \
     -d '{
       "type": "connector",
       "token": "my_connector",
       "reverse_token": "my_reverse_token",
       "rules": [
         {"addrs": ["10.0.0.0/8"], "ports": [[8000, 9000]]}
       ]
     }' \
     http://localhost:8765/api/token
```

## 命令行

Go 与 Python(`linksocks` pip 包)两种命令行均提供单个可重复的 `--access-rule` 参数,格式为 `地址区:端口区`:地址区为 CIDR、裸 IP 或地址范围;端口区为单个端口或 `起-止` 范围。施加于哪一侧由命令角色自动决定:

| 命令 | 角色 | `--access-rule` 施加位置 |
|------|------|--------------------------|
| `linksocks client`(forward) | connector | 入口(本地 SOCKS5/HTTP 端口) |
| `linksocks connector` | connector | 入口(本地 SOCKS5/HTTP 端口) |
| `linksocks client -r` / `linksocks provider` | provider | 拨号(出站连接前) |
| `linksocks server`(forward 模式) | 拨号的服务器 | 拨号(服务器级) |
| `linksocks server -r`(reverse 模式) | 中继服务器 | 按 token 入口(作用于 reverse token) |

```bash
linksocks server -u ws://localhost:9870 -r -p 1080 \
    --access-rule 192.168.1.0/24:22 \
    --access-rule 192.168.1.1-192.168.2.255:22-100 \
    --access-rule 10.0.0.5:443
```

当服务器以 **API 服务器模式** 运行(带 `--api-key`)时,`--access-rule` 会被拒绝:服务器级全局规则必须通过 HTTP API 施加。此时存在两级规则:

- **服务器级全局规则** —— 通过 `GET/PUT /api/config/access` 设置服务器级入口/出站规则(等价于 `WithEntryAccessControl` / `WithDialAccessControl`)。
- **按 token / 按 key 的规则** —— 通过 `POST /api/token` 的 `rules` 字段,或使用携带默认规则的附加 API key(`ServerOption.WithAPIKeys`)创建令牌。

详见[HTTP API 访问控制](/zh/guide/http-api#服务器级访问控制)。

## 被拒绝时的行为

| 位置 | 协议 | 响应 |
|------|------|------|
| 拨号侧(provider 出口 / forward 服务器) | TCP / UDP 连接 | SOCKS5 `0x04`(主机不可达) |
| forward token 规则(服务器拨号) | TCP / UDP 连接 | SOCKS5 `0x04`(主机不可达) |
| connector token 规则(服务器转发) | TCP / UDP 连接 | SOCKS5 `0x04`(主机不可达) |
| 本地代理入口 | SOCKS5 CONNECT | `0x02`(规则不允许该连接) |
| 本地代理入口 | SOCKS5 UDP 数据包 | 数据包被静默丢弃 |
| 本地代理入口 | HTTP CONNECT | `403 Forbidden` |
| 本地代理入口 | HTTP 绝对请求 | `403 Forbidden` |

所有被拦截的尝试都会记录日志,包含目标地址与端口。

## 示例:仅允许 SSH 的隧道

隧道只允许访问一台机器的 SSH(端口 22)与另一网段的 Web(端口 80/443),在 provider 拨号侧执行:

```go
ac, _ := linksocks.NewAccessControl([]linksocks.AccessRule{
    {Addrs: []string{"10.0.0.5"}, Ports: []linksocks.PortSpec{linksocks.SinglePort(22)}},
    {Addrs: []string{"10.0.1.0/24"}, Ports: []linksocks.PortSpec{linksocks.SinglePort(80), linksocks.SinglePort(443)}},
})
client := linksocks.NewLinkSocksClient(token, linksocks.DefaultClientOption().
    WithWSURL("ws://your-server:8765").
    WithReverse(true).
    WithSocksWaitServer(true).
    WithDialAccessControl(ac))
```
