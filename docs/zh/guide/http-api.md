# HTTP API

当使用 `--api-key` 标志启用时，LinkSocks 服务器提供用于动态令牌管理和服务器监控的 HTTP API。

## 快速开始

### 启用 API

```bash
# 启用 API 启动服务器
linksocks server --api-key your_secret_api_key
```

API 将在与 WebSocket 服务器相同的主机和端口上可用（默认：`http://localhost:8765`）。

### 基本用法

```bash
# 获取服务器状态
curl -H "X-API-Key: your_secret_api_key" \
     http://localhost:8765/api/status

# 添加正向令牌
curl -X POST \
     -H "X-API-Key: your_secret_api_key" \
     -H "Content-Type: application/json" \
     -d '{"type":"forward","token":"my_token"}' \
     http://localhost:8765/api/token

# 删除令牌
curl -X DELETE \
     -H "X-API-Key: your_secret_api_key" \
     http://localhost:8765/api/token/my_token
```

## 身份验证

所有 API 请求都需要包含您配置的 API 密钥的 `X-API-Key` 头：

```http
X-API-Key: your_secret_api_key
```

### 错误响应

如果身份验证失败，API 返回：

```json
{
  "success": false,
  "error": "invalid API key"
}
```

## 端点概述

| 方法 | 端点 | 描述 |
|------|------|------|
| GET    | `/api/status` | 获取服务器状态和令牌列表 |
| POST   | `/api/token` | 添加新令牌 |
| DELETE | `/api/token/{token}` | 通过 URL 路径删除令牌 |
| DELETE | `/api/token` | 通过请求主体删除令牌 |

## 服务器状态

### GET /api/status

返回服务器版本和所有令牌列表及其类型和活跃客户端计数。

**响应：**

```json
{
  "version": "3.0.12",
  "tokens": [
    {
      "token": "forward_token_123",
      "type": "forward", 
      "clients_count": 2
    },
    {
      "token": "reverse_token_456",
      "type": "reverse",
      "clients_count": 1,
      "port": 9870,
      "connector_tokens": ["connector_abc", "connector_def"]
    }
  ]
}
```

**令牌对象字段：**

- `token`（字符串）：身份验证令牌
- `type`（字符串）：令牌类型 - "forward" 或 "reverse"  
- `clients_count`（数字）：活跃客户端连接数
- `port`（数字）：SOCKS5 端口（仅反向令牌）
- `connector_tokens`（数组）：关联的连接者令牌（仅反向令牌）

## 令牌管理

### 添加正向令牌

**POST /api/token**

```json
{
  "type": "forward",
  "token": "my_forward_token"
}
```

**参数：**

- `type`（必需）：必须是 "forward"
- `token`（可选）：要使用的特定令牌，如果未提供则自动生成

**响应：**

```json
{
  "success": true,
  "token": "my_forward_token"
}
```

### 添加反向令牌

**POST /api/token**

```json
{
  "type": "reverse",
  "token": "my_reverse_token",
  "port": 9870,
  "username": "socks_user",
  "password": "socks_pass",
  "allow_manage_connector": true
}
```

**参数：**

- `type`（必需）：必须是 "reverse"
- `token`（可选）：要使用的特定令牌，如果未提供则自动生成
- `port`（可选）：特定的 SOCKS5 端口，如果未提供则自动分配
- `username`（可选）：SOCKS5 身份验证用户名
- `password`（可选）：SOCKS5 身份验证密码  
- `allow_manage_connector`（可选）：允许客户端管理连接者令牌（自主模式）

**响应：**

```json
{
  "success": true,
  "token": "my_reverse_token",
  "port": 9871
}
```

### 添加连接者令牌

**POST /api/token**

```json
{
  "type": "connector",
  "token": "my_connector_token",
  "reverse_token": "associated_reverse_token"
}
```

**参数：**

- `type`（必需）：必须是 "connector"
- `token`（可选）：特定的连接者令牌，如果未提供则自动生成
- `reverse_token`（必需）：关联的反向代理令牌

**响应：**

```json
{
  "success": true,
  "token": "my_connector_token"
}
```

### 删除令牌（URL 路径）

**DELETE /api/token/{token}**

通过在 URL 路径中指定来删除令牌。

**示例：**

```bash
curl -X DELETE \
     -H "X-API-Key: your_api_key" \
     http://localhost:8765/api/token/token_to_remove
```

**响应：**

```json
{
  "success": true,
  "token": "token_to_remove"
}
```

### 删除令牌（请求主体）

**DELETE /api/token**

```json
{
  "token": "token_to_remove"
}
```

**响应：**

```json
{
  "success": true,
  "token": "token_to_remove"
}
```

## 错误响应

所有端点都以此格式返回错误响应：

```json
{
  "success": false,
  "error": "error description"
}
```

**常见错误：**

- `"invalid API key"` - 身份验证失败
- `"invalid request body"` - 格式错误的 JSON 请求
- `"invalid token type"` - 不支持的令牌类型
- `"token not specified"` - 缺少必需的令牌参数
- `"reverse_token is required for connector token"` - 连接者缺少 reverse_token
