# 身份验证

LinkSocks 支持多种身份验证方法：

1. **WebSocket 令牌** - 在正向/反向代理模式中使用
2. **连接者令牌** - 在代理代理模式中由代理消费者使用
3. **SOCKS 凭据** - 由连接到代理的程序使用

## WebSocket 令牌

在正向和反向代理模式中用于客户端-服务器身份验证。

```bash
# 服务器（如果省略则自动生成）
linksocks server -t server_token

# 客户端（必需）
linksocks client -t server_token -u ws://localhost:8765
```

## 代理代理模式令牌

在代理代理模式下，服务器充当提供者和连接者之间的中继，每种类型使用不同的令牌。

### 常规代理模式

在常规代理模式下，服务器集中管理所有令牌：

**服务器端 - 定义两个令牌：**
```bash
# 服务器设置提供者令牌和连接者令牌
linksocks server -t provider_token -c connector_token -p 9870 -r
```

**提供者端 - 使用提供者令牌：**
```bash
# 提供者使用服务器设置的提供者令牌
linksocks provider -t provider_token -u ws://localhost:8765
```

**连接者端 - 使用连接者令牌：**
```bash
# 连接者使用服务器设置的连接者令牌
linksocks connector -t connector_token -u ws://localhost:8765 -p 1180
```

### 自主模式令牌

自主模式允许提供者设置自己的连接者令牌，创建隔离的提供者-连接者对。

**服务器端 - 仅提供者令牌：**
```bash
# 服务器仅设置提供者令牌，不需要连接者令牌
linksocks server -t provider_token -r -a
```

**提供者端 - 设置自己的连接者令牌：**
```bash
# 提供者使用提供者令牌连接并定义自定义连接者令牌
linksocks provider -t provider_token -c my_custom_connector_token -u ws://localhost:8765
```

**连接者端 - 使用提供者的令牌：**
```bash
# 连接者使用提供者的自定义令牌
linksocks connector -t my_custom_connector_token -u ws://localhost:8765 -p 1180
```

### 自主模式令牌流程

1. **服务器**：仅验证提供者令牌（`-t provider_token`），不管理连接者令牌
2. **提供者**：使用提供者令牌（`-t`）进行身份验证并定义连接者令牌（`-c`）
3. **连接者**：使用提供者的自定义令牌（`-t custom_token`）
4. **隔离**：每个连接者只能访问其对应的提供者

## SOCKS5 凭据

在 SOCKS5 接口本身上进行可选的用户名/密码身份验证。

### 在哪里设置 SOCKS5 凭据

- **正向模式**：在 `client` 上设置（运行 SOCKS5 服务器的一方）
- **反向模式**：在 `server` 上设置（运行 SOCKS5 服务器的一方）
- **代理模式**：在 `connector` 上设置（运行 SOCKS5 服务器的一方）

```bash
# 正向模式 - 客户端提供 SOCKS5 服务器
linksocks client -t token -u ws://localhost:8765 -p 9870 -n user -w pass

# 反向模式 - 服务器提供 SOCKS5 服务器
linksocks server -t token -r -p 9870 -n user -w pass

# 代理模式 - 连接者提供 SOCKS5 服务器
linksocks connector -t connector_token -p 9870 -r -n user -w pass
```

## 令牌生成

对于生产使用，请使用以下命令生成强随机令牌：

**Linux/macOS：**
```bash
openssl rand -hex 16
```

**Windows PowerShell：**
```powershell
[System.Web.Security.Membership]::GeneratePassword(32, 8)
```
