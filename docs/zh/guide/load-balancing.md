# 负载均衡

反向代理模式在连接的客户端之间分发连接。

## 工作原理
- 每连接轮询分配
- 长连接保持在同一客户端上
- 客户端断开连接时自动故障转移

## 设置
```bash
# 服务器
linksocks server -t token -r -p 9870

# 多个客户端
linksocks client -t token -r &
linksocks client -t token -r &
```
