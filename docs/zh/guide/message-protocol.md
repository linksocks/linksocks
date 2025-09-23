# 消息协议

LinkSocks 在 WebSocket 上使用自定义二进制消息协议。本页详细介绍了消息传输机制和协议规范。

### 协议版本

当前协议版本：`0x01`

## 消息类型

### 身份验证消息

**AuthMessage**：客户端到服务器的初始身份验证
```
Version(1) + Type(1) + TokenLen(1) + Token(N) + Reverse(1) + Instance(16)
```

**AuthResponseMessage**：服务器对身份验证的响应
```
Version(1) + Type(1) + Success(1) + [ErrorLen(1) + Error(N) if !Success]
```

### 连接管理

**ConnectMessage**：请求建立新连接
```
Version(1) + Type(1) + Protocol(1) + ChannelID(16) + [AddrLen(1) + Addr(N) + Port(2) if TCP]
```

**ConnectResponseMessage**：对连接请求的响应
```
Version(1) + Type(1) + Success(1) + ChannelID(16) + [ErrorLen(1) + Error(N) if !Success]
```

**DisconnectMessage**：请求关闭连接
```
Version(1) + Type(1) + ChannelID(16) + [ErrorLen(1) + Error(N) if error]
```

### 数据传输

**DataMessage**：客户端和服务器之间的数据传输
```
Version(1) + Type(1) + Protocol(1) + ChannelID(16) + Compression(1) + DataLen(4) + Data(N) +
    [AddrLen(1) + Addr(N) + Port(2) if UDP]
```

## 通道管理

### 通道标识

每个代理连接都被分配一个唯一的 `ChannelID`（UUID），它有多个用途：

- 在 WebSocket 连接中链接相关消息
- 通过单个 WebSocket 启用多个代理连接的多路复用
- 在代理代理模式下促进消息路由

### 消息队列

LinkSocks 使用特定于通道的消息队列来处理异步消息传递。每个通道都有自己的消息队列，缓冲区大小为 1000 条消息。当队列满时，新消息将被丢弃以防止内存耗尽。

### 通道清理

当连接关闭时，系统会自动：
- 从活动通道映射中删除通道
- 关闭并删除关联的消息队列
- 清理任何待处理的 goroutine

## TCP 连接流程

### 正向代理模式

1. **连接请求**：客户端发送带有目标地址和端口的 ConnectMessage
2. **服务器处理**：服务器尝试建立到目标的连接
3. **响应**：服务器发送 ConnectResponseMessage 指示成功或失败
4. **数据交换**：双向 DataMessage 交换直到连接关闭
5. **清理**：任何一方关闭时发送 DisconnectMessage

### 反向代理模式

在反向代理模式下，服务器向客户端发起连接：

1. **服务器请求**：服务器发送带有目标地址和端口的 ConnectMessage
2. **客户端处理**：客户端尝试建立到目标的连接
3. **响应**：客户端发送 ConnectResponseMessage 指示成功或失败
4. **数据交换**：双向 DataMessage 交换直到连接关闭
5. **清理**：任何一方关闭时发送 DisconnectMessage

## UDP 处理

由于 UDP 的无连接性质，UDP over SOCKS5 需要特殊处理。该过程涉及 SOCKS5 UDP 关联和 WebSocket 消息交换。

### SOCKS5 UDP 关联过程

当 SOCKS5 客户端请求 UDP ASSOCIATE（cmd=0x03）时：

1. **本地 UDP 监听器创建**
   - LinkSocks 在可用端口上创建本地 UDP 监听器
   - 此监听器将从 SOCKS5 客户端接收 UDP 数据包
   - 监听器地址返回给 SOCKS5 客户端

2. **WebSocket 连接设置**
   - LinkSocks 使用 Protocol="udp" 向服务器发送 ConnectMessage
   - ChannelID 标识此 UDP 关联
   - UDP 的初始 ConnectMessage 中不包含目标地址

3. **SOCKS5 响应**
   - LinkSocks 使用本地 UDP 监听器地址响应 SOCKS5 客户端
   - 客户端将 UDP 数据包发送到此地址进行转发

### UDP 数据处理流程

#### 客户端 → 服务器方向

1. **数据包接收**
   - 本地 UDP 监听器从 SOCKS5 客户端接收数据包
   - 数据包包含带有目标地址信息的 SOCKS5 UDP 头

2. **SOCKS5 UDP 头解析**
   - 从 SOCKS5 UDP 头格式中提取目标地址和端口：
     ```
     RSV(2) + FRAG(1) + ATYP(1) + DST.ADDR(var) + DST.PORT(2) + DATA(var)
     ```

3. **WebSocket 传输**
   - 使用提取的目标地址创建 DataMessage
   - 通过 WebSocket 发送到服务器，Protocol="udp"

#### 服务器 → 客户端方向

1. **服务器接收**
   - 服务器从远程目标接收 UDP 数据包
   - 服务器知道传入数据包的源地址

2. **SOCKS5 UDP 头构造**
   - 服务器使用源地址信息构造 SOCKS5 UDP 头
   - 格式符合 SOCKS5 规范

3. **WebSocket 传输**
   - 使用 SOCKS5 UDP 头 + 数据创建 DataMessage
   - 通过 WebSocket 发送到客户端

4. **客户端转发**
   - 客户端接收 DataMessage 并转发到本地 UDP 监听器
   - SOCKS5 客户端接收具有正确源地址信息的数据包

### UDP 连接生命周期

- **关联**：持续到 SOCKS5 TCP 控制连接关闭
- **超时**：UDP 关联如果不活动可能会超时
- **清理**：关联结束时，本地 UDP 监听器关闭并释放资源

## 性能

### 数据压缩

当数据大小超过压缩阈值时（默认：512KB，可通过 `WithCompressionThreshold` 配置），DataMessage 支持条件压缩。DataMessage 中的压缩标志设置为 0x01 表示 gzip 压缩。

### 批处理

LinkSocks 为网络数据读取实现动态批处理：

**SendManager 批处理**
- 网络数据在创建 DataMessage 之前在缓冲区中批处理
- 默认批处理等待时间：最少 20ms，最多 500ms
- 自适应批处理根据连接速度调整延迟
- 延迟到期或缓冲区满时基于定时器的刷新

**批处理控制**
- 可以通过 `WithDynamicBatching(false)` 禁用动态批处理以立即发送
- 高速连接增加批处理延迟
- 低速连接减少批处理延迟
- 缓冲区大小和时间限制可通过 `WithBatchingTimeLimits` 配置

## 错误处理

### 客户端-服务器连接失败

**当令牌验证失败时**
- 服务器发送 `AuthResponseMessage{Success: false, Error: "invalid token"}`
- WebSocket 连接立即关闭
- 客户端接收 nonRetriableError 并停止重连尝试

**当 WebSocket 连接丢失时**
- 客户端检查是否启用重连且错误不是 nonRetriableError
- 在尝试重连之前等待 ReconnectDelay（默认：5s；可通过 ClientOption.WithReconnectDelay 配置）
- 现有通道保留在内存中，DataMessage 继续排队
- 成功重连后数据传输恢复

**当没有可用客户端进行负载均衡时**
- 服务器等待 10 秒寻找可用的反向代理客户端
- 使用轮询选择和 ping 活跃性检查
- 如果 ping 失败，选择下一个可用客户端
- 10 秒后没有客户端，向 SOCKS5 客户端发送错误代码 0x03

### 网络连接失败

**当目标服务器连接失败时**
- 正常模式：立即发送 `ConnectResponseMessage{Success: false, Error: details}`
- SOCKS5 客户端接收错误响应（代码 0x04）并关闭连接
- FastOpen 模式：最初假设成功，在第一次数据传输时检测失败
- FastOpen 超时：如果在 ConnectTimeout+5 秒后没有成功确认则断开通道

**当远程对等方关闭连接时**
- 发送 `DisconnectMessage{ChannelID: channelID, Error: details}`
- 清理资源：删除消息队列，从通道映射中删除，取消上下文
- 关闭 SOCKS5 客户端连接以发出终止信号

**当连接异常关闭时**
- 通道清理器每 60 秒检查一次活动
- 12 小时不活动的通道自动清理
- RST 数据包或网络超时触发立即资源清理
- 单个通道故障不影响同一 WebSocket 上的其他通道
