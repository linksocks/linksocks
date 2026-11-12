# 通过 Zone 连接多台机器

Zone 是一组机器共享的虚拟 IPv4 网络。每台加入的机器都会获得一个网络地址，
应用可以通过本机 SOCKS5 代理访问其他成员；VLAN 模式下也可以直接使用成员的
虚拟地址。

Zone 默认为私有。私有 Zone 不会出现在任何列表中，只有拿到 Zone ID 的机器才能加入。
管理员可以创建公开 Zone：公开 Zone 会出现在网站的 **Public zones** 视图和
`linksocks zone list` 中，任何看到它的人都能加入，并访问成员对外开放的服务。

打开网站的 [Zones](https://l.zetx.tech/zones) 页面即可浏览公开 Zone、查看成员并复制
加入命令。浏览和加入都不需要账号；登录用户默认看到 **My zones**（自己创建的 Zone），
可切换到 **Public zones**（全部公开 Zone），没有合并列表。Zone ID 为小写 UUID。

## 连接前准备

- 使用包含 `zone` 命令的 LinkSocks CLI。此功能没有 Python 库 API，安装 Python
  绑定不会提供 `Zone` 类。
- 默认加入模式只需在本机运行 SOCKS5 代理，不需要特殊权限。可选的 `--vlan`
  模式需要一块二层 TAP 网卡：Linux 依赖 `/dev/net/tun` 与 `ip` 命令，Windows
  需要 TAP-Windows6 适配器；两者都需要 root 或管理员权限。
- Zone 网段不能与任何成员已有的局域网、VPN、容器网络或其他路由重叠。
  客户端在所有平台都会检查网卡地址，Linux 还会检查所有 IPv4 路由表，默认路由除外。
- 代理模式支持 Linux、macOS 和 Windows。

Linux 已使用真实 TAP 网卡和代理模式完成端到端验证。Windows 的 TAP 路径可编译但
尚未在 Windows 机器上验证；macOS 只支持代理模式。

代理模式传输 TCP 流和 UDP 数据报（SOCKS5 UDP ASSOCIATE），不创建网卡，
因此不支持 ICMP 等其他 IP 协议。`--vlan` 传输完整的以太网帧：ARP、ICMP/ping、
TCP、UDP、广播（UDP 广播、WOL 魔术包）、组播和原始以太网帧都能在成员之间
像在同一物理网段一样传输。VLAN 网卡 MTU 为 1500。

两种模式都不提供 IPv6、P2P 直连、DNS、局域网子网共享，也不会设置互联网默认路由。
请使用成员的虚拟 IP，代理模式下也可以使用成员名，不要依赖局域网自动发现。

## 创建网络

1. 打开 linksocks.js 网站的 `/zones` 页面并登录。
2. 点击 **Create zone**。
3. 填写 **Zone name**、**IPv4 network (CIDR)** 和 **Member limit**。
4. 选择可见性：默认的 **Private**，或管理员可选的 **Public**。
5. 点击 **Create zone**。

名称长度为 1–64 个字符。整个网段必须位于 RFC1918 私有地址范围内：
`10.0.0.0/8`、`172.16.0.0/12` 或 `192.168.0.0/16`。支持 `/16` 到 `/30`
前缀。成员上限为 2–256，且不能超过可用地址数：`/30` 可容纳 2 台，`/29`
可容纳 6 台，`/24` 可容纳 254 台。网络地址和广播地址不会分配给成员。

新建的 Zone 是私有的：不出现在任何列表中，只有你提供 ID 的机器才能加入。
只有管理员可以创建公开 Zone。公开 Zone 会出现在网站和 `linksocks zone list`
中，任何看到它的人都能加入并访问成员对外开放的服务；请谨慎分享 ID，并确认
每台成员机器的防火墙只放行需要暴露的服务。

登录用户可以创建 Zone，并删除自己创建的 Zone。管理员可以删除任意 Zone。
删除 Zone 会断开所有成员并释放全部地址，且无法撤销。

## 让每台机器加入

1. 在 `/zones` 找到目标网络，点击 **Copy command**。公开 Zone 的 ID 和命令
   默认遮盖，需要先勾选确认已检查本机防火墙；点击 **Copy command** 也会先弹出
   该警告。浏览器不允许自动复制时，命令会被选中，按 Ctrl+C 即可手动复制。
2. 在每台机器上执行复制的命令。代理模式不需要提升权限；`--vlan` 需要 root。
   保持命令运行。终端的 `Joined` 行会显示分配的虚拟 IP、VLAN 模式下的网卡
   名称和在线对端数量。
3. 用应用连接成员的虚拟 IP，代理模式下也可以使用成员名。VLAN 模式下服务
   需要监听虚拟 IP，代理模式下服务监听本机回环即可；两者都需要防火墙允许该连接。

加入命令接受小写的 Zone UUID。`--name` 可以修改成员列表中显示的机器名称，
默认使用主机名。

### 代理模式（默认）

`linksocks zone join <zone-id>` 会在本机 `127.0.0.1:6080` 启动 SOCKS5 代理，
并输出代理地址：

```text
SOCKS5 proxy listening on 127.0.0.1:6080
```

用 `--socks-port` 修改端口；`--socks-port 0` 关闭监听。应用通过虚拟 IP 或成员名
寻址，并应让代理解析名称：

```bash
curl --socks5-hostname 127.0.0.1:6080 http://10.88.0.2:8080/
curl --socks5-hostname 127.0.0.1:6080 http://machine-b:8080/
```

代理模式支持 TCP 和 UDP（SOCKS5 UDP ASSOCIATE）。连接成员虚拟 IP 的流量会到达
对方监听在 `127.0.0.1` 上的服务，因此不需要公网网卡或入站端口。由于没有网卡，
除 TCP、UDP 之外的协议不会被传输。

### VLAN 模式（`--vlan`）

使用 `--vlan` 时，客户端创建二层 TAP 网卡（默认名 `lz0`，可用 `--interface`
改名），MTU 为 1500。成员就像同一个以太网段中的主机：ARP、ICMP/ping、TCP、UDP、
UDP 广播、WOL 魔术包、组播和原始以太网帧都能在成员之间传输。应用直接连接成员的
虚拟 IP，无需配置代理。VLAN 模式下额外指定 `--socks-port` 也会启动 SOCKS5 监听。

在同一台机器加入多个 Zone 时，每个加入进程应使用不同网卡名称，且各网段不能重叠。

### Windows 和 macOS

`--vlan` 需要一块二层（以太网）虚拟网卡：Zone 协议把每台成员的网卡 MAC 当作它在
网段里的身份，并按它过滤入站帧。Linux 自带这种网卡；Windows 需要装驱动；macOS
根本不支持二层。

**Windows（TAP-Windows6）**：每台机器做三件事。

1. **安装驱动**：从 OpenVPN 的发布目录
   <https://build.openvpn.net/downloads/releases/> 下载最新的 TAP-Windows 安装包
   （文件名形如 `tap-windows-<版本>-W6.exe`，镜像见
   <https://swupdate.openvpn.net/community/releases/>），以管理员身份运行，并勾选
   **TAP Virtual Ethernet Adapter** 组件——它会同时装好驱动并创建第一块适配器。
   已经装过 OpenVPN 的机器不必再装：OpenVPN 的安装包自带同一驱动。
2. **每个 Zone 一块适配器**：安装程序创建了第一块；再要新的，用开始菜单快捷方式
   **TAP-Windows → Utilities → Add a new TAP virtual ethernet adapter**，或在管理员
   命令行运行 `C:\Program Files\TAP-Windows\bin\addtap.bat`。`Get-NetAdapter`
   可以列出已有适配器；适配器本身不需要配置 IP。想给 `--interface` 一个好记的名字，
   可用 `Rename-NetAdapter` 重命名。
3. **在管理员 PowerShell 或命令提示符里加入**（Windows 需要管理员权限，正如 Linux
   需要 root）：

```powershell
linksocks zone join <zone-id> --vlan
# 有多块适配器时，用名称指定
linksocks zone join <zone-id> --vlan --interface "Zone B"
```

客户端在联系协调服务之前就会检查权限与适配器，所以缺驱动或没有管理员权限会**立即
失败**，不会先加入再报错。随后它会在未指定 `--interface` 时使用找到的第一块
TAP-Windows6 适配器，把其 MAC 固定为本机的成员 MAC（Windows 在适配器重启后才生效，
客户端会自动重启它）、启用适配器并设置成员地址。在完成之前，Windows 会显示该适配器
"网络电缆被拔出"，这是正常的。

**macOS** 只提供三层隧道（utun），因此没有 `--vlan`：不加该参数、使用 SOCKS5
代理即可，代理能按虚拟 IP 或成员名访问成员。

**WSL2** 本身就是 Linux，`--vlan` 对 WSL 内运行的程序有效。Windows 程序仍然无法
加入该网段：请让它们使用代理，WSL2 会把监听在 `127.0.0.1` 的代理转发出来。

### 命令和参数

| 命令或参数 | 用途 |
|---|---|
| `zone list` | 列出公开 Zone 的 ID、名称、网段和成员上限 |
| `zone join <zone-id>` | 在前台加入；启动 SOCKS5 代理，或配合 `--vlan` 创建 TAP 网卡 |
| `zone leave <zone-id>` | 释放本机地址，并停止正在运行的加入进程 |
| `--url`、`-u` | 协调服务网站地址，默认为 `https://l.zetx.tech` |
| `--identity` | 本地机器密钥路径；加入和退出必须使用同一文件 |
| `--name` | 加入时显示的机器名称，默认使用主机名 |
| `--socks-port` | 本机 SOCKS5 代理端口，默认 `6080`；`0` 关闭监听；配合 `--vlan` 时只有显式指定才启动代理 |
| `--vlan` | 不使用代理模式，改为使用二层 TAP 网卡（Linux 的 `/dev/net/tun` 或 Windows 的 TAP-Windows6；需要 root 或管理员权限） |
| `--interface` | `--vlan` 使用的 TAP 网卡名称；Linux 默认 `lz0`，Windows 默认使用第一块 TAP-Windows6 适配器 |
| `--no-env-proxy`、`-E` | 忽略环境变量中的代理设置 |
| `--ech` | 连接协调服务时的 Encrypted Client Hello，默认 `auto` |
| `--debug`、`-d` | 输出调试日志；`-dd` 追加 trace 日志（流、数据报、防火墙丢弃、协调服务心跳） |

这些子命令均在 `linksocks` 下运行。`--url` 接受 HTTP(S) 和 WebSocket 地址，
加入后日志会记录：加入过程与分配到的地址、之后的对端数量变化、SOCKS5 监听，
以及 VLAN 模式下的网卡。加 `-d` 可以看到流量为何通或不通——流已建立、被拒绝
及其原因（被 Zone 防火墙拦截、对端不在线、本机服务不可达）——加 `-dd` 则记录
数据报、防火墙丢弃等逐帧细节。

连接远端协调服务时使用 HTTPS。`--name`、`--socks-port`、`--vlan` 和 `--interface`
只用于 `join`，表中其他参数为各 Zone 子命令共用。

## 管理 Zone

Zone 所有者和网站管理员可以在 `/zones` 管理成员和设置：

- **View members** 显示每台机器的名称、状态、虚拟 IP 和身份。在线机器可以
  **Kick**，保留地址的离线机器可以 **Remove**。两种操作都会立即断开正在运行的
  加入并释放地址。被移除的进程会输出 `Removed from the zone.` 后退出，不会自动
  重连；再次运行加入命令即可重新加入。
- **Settings** 可以修改 **欢迎语** 和 **防火墙规则**。欢迎语最长 512 个字符，
  成员加入时会在其终端显示。
- 每个 Zone 最多 64 条防火墙规则，按顺序匹配。每条规则包含源和目的 IPv4 网段、
  源和目的端口（any、单个端口或区间）、协议（`any`、`tcp`、`udp`、`icmp`、`arp`）
  以及允许或拒绝动作。命中第一条匹配的规则即生效；没有匹配任何规则的流量按
  Zone 的默认策略处理（allow 或 deny）。

两种模式的规则执行方式不同。代理模式下，中继和连接两端都会按规则检查每个 TCP
连接和 UDP 数据报。`--vlan` 模式下中继看不到以太网帧的内容，由接收端成员用
本机的规则副本过滤每个帧。带具体源端口的规则只对 VLAN 流量生效，因为代理模式的
流没有源端口可供匹配。

## 保留本机身份

首次加入会在本地生成 Ed25519 身份密钥，私钥始终保存在机器上。默认文件为系统用户
配置目录下的 `linksocks/zone.key`。配置目录通常为 Linux 的 `~/.config`、
macOS 的 `~/Library/Application Support` 和 Windows 的 `%AppData%`。

在 Unix 上，客户端以 `0600` 权限创建密钥文件，以 `0700` 权限创建缺失的父目录，
并拒绝使用授予用户组或其他用户任何权限的已有密钥文件。Windows 请通过文件访问控制限制权限。
不要把同一份身份密钥复制给多台机器：一个 Zone 只允许同一身份有一个在线连接。

需要在提升权限后使用固定文件位置时，请通过 `--identity` 指定绝对路径。
更换运行用户或密钥文件会改变机器身份。保留原始密钥，才能恢复保留的地址或主动释放它。

VLAN 模式下网卡的 MAC 地址由该身份派生，重连后保持不变，因此对端的 ARP 缓存仍然有效。

## 退出或恢复连接

在加入命令的终端按 **Ctrl+C**，即可退出并释放地址。也可以在另一个终端运行
`zone leave`，使用相同的 Zone ID、协调服务地址和身份文件。这会同时断开另一个
进程中运行的加入连接。以后再次加入时，地址可能改变。

网络中断或进程异常退出后，网站会将机器标记为 **Offline**，保留地址 24 小时。
离线保留地址仍占用成员名额。加入进程遇到网络故障会自动重连；保留期内用同一身份
重启，也会恢复原地址。主动退出会立即释放地址。被所有者或管理员移除的机器会停止
运行，不会自动重连。

删除 Zone 会断开所有成员并释放全部地址，且无法撤销。需要删除时，在 `/zones`
点击 **Delete zone** 并确认。Zone 被删除或成员被主动移除后，客户端会停止运行。

## 了解加密范围

每次连接都会生成新的 X25519 交换密钥，成员之间使用 XChaCha20-Poly1305 加密数据包。
重连后会更换交换密钥。协调服务提供正确的成员目录时，中继只能转发加密载荷，无法读取内容。

机器身份、对端公钥和地址分配依赖可信的协调服务。此机制不能防止恶意协调服务替换
成员目录。协调服务可以看到成员信息、虚拟源 IP 和目标 IP、数据包大小与传输时间；
VLAN 模式下还能看到所转发以太网帧的地址。

## 排查连接问题

| 提示或现象 | 检查方法 |
|---|---|
| `administrator privileges required` | `--vlan` 需要 root；使用提升权限的终端运行，并确认 `/dev/net/tun` 存在 |
| `zone --vlan currently requires Linux` | VLAN 模式仅支持 Linux；其他系统请使用代理模式 |
| `configure zone interface` | 确认系统网络命令存在，且有权执行 |
| `Note: --interface applies to --vlan mode` | 代理模式下请去掉 `--interface`；需要 TAP 网卡时加上 `--vlan` |
| `overlaps` | 选择不与提示中的网卡或路由重叠的 Zone 网段 |
| `Identity is already online` | 停止同一身份的另一个加入进程，或先用其密钥执行 `zone leave` |
| `Zone is full` | 离线地址也占名额；由对应机器主动退出，或等待 24 小时保留期结束 |
| `Zone not found` | Zone 已被删除或 ID 有误；从同一网站复制 ID 和地址 |
| `Zone service is not configured` | 服务部署者需要配置 Worker 的 `DB`、`ZONE` 绑定，参见 [Worker 部署说明](https://github.com/linksocks/linksocks.js/blob/main/README.cn.md#zone-虚拟网络) |
| `Removed from the zone.` | 被所有者或管理员移除；再次运行加入命令可以重新加入，或联系对方 |
| 通过代理连接被拒绝 | 与 Zone 所有者确认防火墙规则和默认策略 |
| 已加入，但应用无法连接 | 确认对端在线、目标是其虚拟 IP 或成员名，且 SOCKS 客户端使用远程名称解析（`--socks5-hostname`）；VLAN 模式下确认服务监听虚拟 IP，且本机防火墙放行访问 |

## Linux 实测记录

以下输出来自通过本地 Worker 连接的两个 Linux 网络命名空间，使用 `/29` 网段，
成员上限为 6。其中一次运行验证默认代理模式，另一次使用 `--vlan` 和真实 TAP 网卡，
覆盖了 ARP、ICMP、TCP、UDP、重连和退出。验证结束后已删除测试 Zone，使用时请从
网站复制你自己的加入命令。

### 使用真实 TAP 网卡的 VLAN 模式

两个加入进程分别输出：

```text
Joined Zone network test (9b89a4eb-8dff-4a3f-acab-2f192349d9a1, private) as 10.249.231.1 on lz0; 0 peer(s) online.
Joined Zone network test (9b89a4eb-8dff-4a3f-acab-2f192349d9a1, private) as 10.249.231.2 on lz0; 1 peer(s) online.
```

ARP 解析、达到网卡 MTU 的 ICMP 测试、TCP 传输和 UDP 回显均成功：

```text
PING 10.249.231.2 (10.249.231.2) 1472(1500) bytes of data.
1480 bytes from 10.249.231.2: icmp_seq=1 ttl=64 time=0.938 ms
1480 bytes from 10.249.231.2: icmp_seq=2 ttl=64 time=0.775 ms
1480 bytes from 10.249.231.2: icmp_seq=3 ttl=64 time=0.687 ms

--- 10.249.231.2 ping statistics ---
3 packets transmitted, 3 received, 0% packet loss, time 2055ms
rtt min/avg/max/mdev = 0.687/0.800/0.938/0.103 ms
TCP: 262144 bytes, SHA-256 81d0141af58569f91edf2d036b67a6b82c062d1829fd93aec50ed96b4773d225
UDP: 1200-byte echo verified.
```

第二台机器异常退出后，使用原身份重新加入，保留了原地址。ICMP 的拒绝规则一度
拦截了 ping，清除规则后恢复。随后主动退出命令停止了正在运行的加入进程，并释放了地址：

```text
Rejoin retained 10.249.231.2.
Firewall: an ICMP deny rule stopped pings in VLAN mode.
Firewall: clearing the rule restored pings.
Left zone 9b89a4eb-8dff-4a3f-acab-2f192349d9a1.
Leave disconnected the running machine and released its IPv4 lease.
Signal-driven leave removed the final membership.
PASS: real TAP Ethernet (ARP), ICMP, TCP, UDP, reconnect and leave.
```

### 代理模式

两台成员通过本地 Worker 加入同一 Zone，机器 B 在回环上提供 TCP 和 UDP 服务。
机器 A 的 SOCKS5 客户端分别通过虚拟 IP 和成员名访问成功；一条拒绝规则一度拦截了
新的 TCP 连接，清除后恢复：

```text
SOCKS5 proxy listening on 127.0.0.1:20485
TCP: curl --socks5-hostname reached machine-b by virtual IP and by name.
UDP: echoed 16 bytes through SOCKS5 UDP ASSOCIATE.
Firewall: a deny rule stopped a new TCP connection through the proxy.
Firewall: clearing the rule restored access.
PASS: proxy-mode SOCKS5 TCP and UDP between two members.
```
