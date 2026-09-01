# OPL Core 功能文档

> 适用代码：`opl-core` 当前工作区实现  
> Core API：`1`  
> Core 版本：`0.1.0-dev`  
> 首发制品：`opl-core-windows-386.exe`  
> 文档日期：2026-08-25

## 1. 模块定位

OPL Core 是 OPL 的网络状态所有者。WPF 只负责界面、用户输入和进程托管，以下能力统一由 Core 管理：

- 内嵌 OpenP2P 引擎的启动、停止、重连和隧道状态；
- 房间创建、固定房间恢复、暂停加入、邀请码轮换和成员管理；
- TCP 自定义配对协议、成员认证、虚拟 IP 租约和设备状态同步；
- Windows 上的 wireguard-go、Wintun、Peer 热更新和流量统计；
- 游戏 IPv4 UDP 有限广播、虚拟子网广播及组播发现中继；
- 配置、非秘密状态、DPAPI 秘密、日志和发布组件完整性校验；
- 通过标准输入/输出向 WPF 提供 NDJSON IPC。

Core 不是通用 CLI，也不提供 HTTP、WebSocket 或守护进程接口。正常情况下由 WPF 启动和关闭。

## 2. 制品与运行要求

### 2.1 发布目录

Core 在正式包中的最小运行布局如下：

```text
OPL_WpfApp.exe
App.config
updata.exe                         # 已安装版本中存在
bin/
  opl-core-windows-386.exe
  manifest.json
  wintun.dll
  licenses/
    openp2p-LICENSE.txt
    go-ethereum-LGPL-3.0.txt
    wintun-LICENSE.txt
    wireguard-go-LICENSE.txt
    go-third-party-notices.txt
```

`openp2p.exe` 不再作为独立进程发布；所需 OpenP2P 模块已经迁入 Core。发布包也不得包含用户 `config.json`、密钥文件或旧版 `tunnel.dll`、`wireguard.dll`。

### 2.2 固定依赖基线

| 组件 | 当前基线 |
|---|---|
| Go | 1.20.14 |
| 目标系统/架构 | `windows/386`，`CGO_ENABLED=0` |
| 内嵌 OpenP2P | 3.25.11，提交 `232fb31200c4d9de6ce7ea8ca54d28d7a504f893` |
| wireguard-go | 提交 `9eb3221f1de589e5dd6a1721fdd7dc0fde0eb10b` |
| Wintun | 0.14.1 x86 |

Core 启动时会校验同目录 `wintun.dll` 的固定 SHA-256。WPF 启动 Core 前还会用 `manifest.json` 校验 Core EXE 自身的 SHA-256。

### 2.3 权限和平台

- WPF 会请求管理员权限；创建 TUN、设置接口地址、路由和 IPv4 forwarding 需要管理员权限。
- Windows 后端已实现；其他平台当前只保留接口边界，WireGuard 管理返回“不支持”。
- 单一 x86 Core 面向 Windows 7 SP1、8/8.1、10、11；按项目最新决定，不执行 Windows 多版本实机测试，因此不能把当前制品描述为已通过多系统验证。

## 3. 进程入口和环境变量

Core 无命令行参数。入口读取以下环境变量：

| 变量 | 必填 | 说明 |
|---|---:|---|
| `OPL_HOST_UID` | 否 | 房主/本机 OpenP2P UID；空值时尝试读取配置中的 `Network.Node` |
| `OPL_DATA_DIR` | 否 | Core 状态、秘密和日志目录；WPF 指向 `%LOCALAPPDATA%\OPL\core` |
| `OPL_OPENP2P_CONFIG` | 否 | OpenP2P 配置路径；默认是 `<DataDir>\config.json` |

若 `OPL_DATA_DIR` 为空，Core 使用 `os.UserCacheDir()/OPL/core`。WPF 会显式传入统一的数据目录和配置路径，避免生成两套配置。

进程通道约定：

- `stdin`：WPF 发往 Core 的 UTF-8 无 BOM NDJSON；
- `stdout`：只允许输出协议 NDJSON；
- `stderr`：诊断信息，WPF 将其写入界面日志；
- OpenP2P 运行日志：`<DataDir>\log\openp2p.log`。

## 4. 总体架构与互斥模式

```text
WPF
 └─ NDJSON/stdin+stdout
    OPL Core
     ├─ API 调度与生命周期
     ├─ OpenP2P 配置仓库
     ├─ 内嵌 OpenP2P 引擎
     ├─ 房间/租约/配对 TCP 服务
     ├─ wireguard-go + Wintun
     ├─ 游戏发现中继
     └─ 状态、秘密和日志存储
```

Core 同一时间只允许一种网络模式：

| 模式 | 含义 | 可同时运行普通隧道 |
|---|---|---:|
| `idle` | 未运行网络 | — |
| `ordinary` | 普通 OpenP2P TCP/UDP 隧道 | 是，属于本模式 |
| `host` | 创建/启动 OPL2 房间 | 否 |
| `client` | 加入 OPL2 房间 | 否 |

创建房间、加入房间和普通 OpenP2P 启动互斥。活动房间中不能修改 OpenP2P 网络账号；隧道列表变化会原子保存并重启内嵌引擎。

## 5. 数据文件

| 文件 | 内容 | 是否秘密 | 生命周期 |
|---|---|---:|---|
| `config.json` | OpenP2P 网络配置和用户隧道 | 含 Token | 长期 |
| `room.json` | 固定房间 ID、端口、成员、租约、封禁列表 | 否 | 固定房间 |
| `room.json.bak` | 上一个有效房间状态 | 否 | 自动备份 |
| `room.secret` | 固定房间密钥和成员 PSK | 是，DPAPI | 固定房间 |
| `host.secret` | 固定房主 X25519 私钥 | 是，DPAPI | 长期 |
| `client-<hostUID>.secret` | 固定客户端 X25519 私钥 | 是，DPAPI | 长期 |
| `client-<hostUID>.membership.secret` | 成员重连 PSK | 是，DPAPI | 长期 |
| `log/openp2p.log` | 内嵌 OpenP2P 日志 | 不应含密钥 | 运行日志 |

配置和房间状态使用同目录临时文件后原子重命名。`room.json` 保存前会把上一个有效版本写入 `.bak`，主文件损坏时读取备份。Windows 秘密使用当前用户 DPAPI，复制到其他用户或计算机后通常无法解密。

## 6. OpenP2P 配置模型

```json
{
  "Network": {
    "Token": 123456,
    "Node": "0123456789abcdef",
    "User": "gldoffice",
    "ShareBandwidth": 10,
    "ServerHost": "api.openp2p.cn",
    "ServerPort": 27183,
    "PublicIPPort": 0
  },
  "Apps": [],
  "LogLevel": 1
}
```

### 6.1 `Network`

| 字段 | 约束/说明 |
|---|---|
| `Token` | 非零无符号 64 位整数；读取时兼容 JSON 数字或数字字符串，写出为数字 |
| `Node` | 非空 OpenP2P UID |
| `User` | OpenP2P 用户名 |
| `ShareBandwidth` | 不得为负数 |
| `ServerHost` | 非空控制服务器主机名 |
| `ServerPort` | `1..65535` |
| `PublicIPPort` | `0..65535`；0 时内嵌引擎按 UID 生成端口 |

### 6.2 `Apps`

| 字段 | 说明 |
|---|---|
| `AppName` | 非空显示名；`_opl2_` 前缀保留给 Core 内部隧道 |
| `Protocol` | `tcp` 或 `udp` |
| `UnderlayProtocol` | OpenP2P 下层协议选择，可空 |
| `PunchPriority` | 打洞优先级 |
| `Whitelist` | 白名单配置 |
| `SrcPort` | 本地监听端口，`1..65535` |
| `PeerNode` | 对端 OpenP2P UID |
| `DstPort` | 对端目标端口，`1..65535` |
| `DstHost` | 对端目标地址，非空 |
| `PeerUser`、`RelayNode` | 可选对端用户/中继节点 |
| `ForceRelay` | 是否强制中继 |
| `Enabled` | 只能为 `0` 或 `1` |

同一配置中，不允许两个已启用隧道占用相同的 `Protocol/SrcPort`。公共 IPC 永远过滤 `_opl2_` 内部条目，用户也不能通过 `config.replace` 或 `tunnel.replace` 写入这类条目。

### 6.3 内部隧道

客户端加入房间时，Core 自动创建并持续保留两条内部 OpenP2P 隧道：

- TCP：本地临时端口 → 房主配对端口；
- UDP：本地临时端口 → 房主 WireGuard 端口。

两条隧道都指向 `127.0.0.1`，名称使用 `_opl2_pair_...` 和 `_opl2_wg_...`。离开房间或 Core 关闭时清理，用户配置读取时不可见。

## 7. IPC 协议

### 7.1 编码与边界

- 每行一个完整 JSON 对象，以 `\n` 结束；
- UTF-8，无 BOM，不转义中文；
- 单行最大 64 KiB；
- 请求必须包含非零 `id` 和非空 `method`；
- 未知 JSON 字段、尾随 JSON、错误参数类型均返回 `INVALID_REQUEST`；
- 请求按读取顺序同步调度，事件写出通过互斥锁与响应串行化。

请求：

```json
{"id":1,"method":"core.getState"}
```

成功响应：

```json
{"id":1,"result":{"state":"idle"}}
```

失败响应：

```json
{"id":1,"error":{"code":"INVALID_STATE","message":"network is active"}}
```

### 7.2 启动握手

Core 启动后首先输出：

```json
{
  "event":"hello",
  "apiVersion":1,
  "coreVersion":"0.1.0-dev",
  "platform":"windows-386",
  "capabilities":[
    "openp2p",
    "wireguard-hot-update",
    "invite-v2",
    "pairing-v2",
    "pairing-tcp-sync-v1",
    "room-v1",
    "fixed-secret",
    "lease-v1",
    "discovery-relay-v1"
  ]
}
```

WPF 要求 5 秒内收到 `apiVersion: 1`，否则终止 Core。平台值由实际 `GOOS-GOARCH` 生成。

## 8. IPC 命令参考

无参数命令应省略 `params`，也可传 `null` 或空对象；包含未知字段的空对象不被接受。

| 方法 | `params` | 返回 `result` | 关键前置条件 |
|---|---|---|---|
| `core.getState` | 无 | 完整 Core 状态 | 无 |
| `config.get` | 无 | 公共 OpenP2P 配置 | 配置文件有效 |
| `config.replace` | `{config}` | 保存后的公共配置 | 活动房间中不可修改 `Network` |
| `tunnel.list` | 无 | 用户隧道数组 | 配置文件有效 |
| `tunnel.replace` | `{apps:[...]}` | 新用户隧道数组 | 不得含内部条目 |
| `openp2p.start` | 无 | `{"state":"starting"}` | 当前无房间/客户端/普通会话 |
| `openp2p.stop` | 无 | `{"state":"stopped"}` | 当前无房间/客户端会话 |
| `room.create` | 无 | `{"invite", "room"}` | 当前无活动网络 |
| `room.start` | 无 | 房间快照 | 已保存有效固定房间 |
| `room.stop` | 无 | 停止后的房间快照 | 幂等 |
| `room.getInvite` | 无 | `{"invite"}` | 房间正在运行 |
| `room.rotateKey` | 无 | `{"invite"}` | 房间正在运行 |
| `room.setJoinEnabled` | `{enabled:boolean}` | 房间快照 | 房间正在运行 |
| `room.removeMember` | `{publicKey}` | 房间快照 | 成员存在且房间运行 |
| `room.renameMember` | `{publicKey,name}` | 房间快照 | 成员存在且名称有效 |
| `room.join` | `{invite,name}` | 加入结果 | 无其他活动网络；可恢复错误持续重试 |
| `room.leave` | 无 | 房间服务快照 | 幂等 |
| `core.shutdown` | 无 | `{"state":"stopped"}` | 成功后进程退出 |

### 8.1 创建房间

```json
{
  "id":2,
  "method":"room.create"
}
```

首次调用时，Core 自动选择可用 TCP 配对端口和 UDP WireGuard 端口，固定房间密钥及房主设备密钥，并持久化邀请码。后续调用优先恢复已保存房间和原端口，因此邀请码不会变化；只有调用 `room.rotateKey` 主动废除当前邀请码时才会生成新邀请码。

### 8.2 启动已保存房间

`room.start` 是兼容性接口；界面统一调用 `room.create`，由 Core 判断恢复已有房间或首次创建。恢复时读取 `room.json`/备份、DPAPI 房间秘密和固定房主密钥，恢复成员租约与 Peer，然后重新监听。

### 8.3 加入房间

```json
{
  "id":3,
  "method":"room.join",
  "params":{
    "invite":"OPL2....",
    "name":"玩家一"
  }
}
```

返回示例：

```json
{
  "hostUid":"0123456789abcdef",
  "assignedIP":"10.0.23.2",
  "prefixLength":24,
  "wireGuardPort":49152,
  "discoveryRelayPort":25675
}
```

客户端设备密钥和成员 PSK 始终持久化，后续可自动重连。加入没有总超时；房主暂时离线、配对隧道尚未建立等可恢复错误会持续重试，并通过 `joinState`/`joinError` 更新界面。邀请码无效、认证失败或房主明确拒绝等不可恢复错误会立即返回。

### 8.4 成员移除

`publicKey` 使用标准 Base64 编码的 32 字节 X25519 公钥。移除操作同时删除 WireGuard Peer、租约、统计和成员 PSK，并把公钥加入封禁集合；固定房间会持久化该集合。当前 IPC 没有单独的解除封禁命令。

## 9. 状态和事件

### 9.1 Core 状态

`core.getState` 返回：

```json
{
  "state":"online",
  "room":{},
  "joined":true,
  "joinState":"connected",
  "joinError":"",
  "tunnels":[],
  "openP2PRunning":false,
  "openP2PState":"online",
  "tunnelStates":[],
  "devices":[],
  "discovery":{"accepted":0,"forwarded":0,"dropped":0}
}
```

生命周期 `state` 可能值：

| 值 | 含义 |
|---|---|
| `idle` | 空闲/已完全停止 |
| `starting_openp2p` | 内嵌 OpenP2P 正在启动 |
| `starting_wireguard` | OpenP2P 已启动，正在创建虚拟网卡 |
| `listening` | 房主网络与配对端口正在监听 |
| `joining` | 客户端正在配对或恢复控制连接 |
| `online` | 普通 OpenP2P 或客户端已在线 |
| `stopping` | 正在关闭资源 |
| `faulted` | 启动或运行失败 |

`openP2PState` 独立表示内嵌引擎：`stopped`、`starting`、`online`、`faulted`。

`joinState` 独立表示客户端与房主的连接：`idle`、`connecting`、`retrying`、`connected`、`failed`；`joinError` 保存最近一次失败原因。界面左下角服务灯只使用 `openP2PState`，组网状态灯使用 `joinState`。

### 9.2 房间快照

房间快照包含：

- `running`、`fixedRoomKey`、`fixedHostKey`、`joinEnabled`；
- `hostUid`、`pairPort`、`wireGuardPort`、`discoveryRelayPort`；
- 房主虚拟 IP `hostIP`，固定为 `10.0.23.1`；
- `members`：UID、公钥、IP、名称、是否启用、状态、租约修订、握手时间和上下行字节。

成员状态常见值：`pairing`、`configured`、`online`、`offline`。WireGuard 最近握手不超过 3 分钟时标记为 `online`；已有配置但握手较旧时为 `configured`。

### 9.3 设备同步

客户端每 5 秒通过配对 TCP 控制连接：

1. 汇总本机 TUN Peer 的 `rxBytes`、`txBytes`；
2. 上报本机 OpenP2P UID、虚拟 IP 对应关系和流量；
3. 接收房间中当前已连接设备快照；
4. 更新 `core.getState.devices`；
5. 控制连接断开后等待 5 秒并使用成员 PSK 自动重连。

房主也会从 WireGuard UAPI 读取每个 Peer 的握手和流量。`rxBytes`/`txBytes` 均为累计字节数，不是瞬时速率。

### 9.4 事件

房间快照发生变化时，Core 最多约 500 ms 后输出：

```json
{"event":"room.changed","room":{...}}
```

设备快照和普通生命周期没有独立推送事件，WPF通过 `core.getState` 定时获取。

## 10. OPL2 邀请码

格式固定为：

```text
OPL2.<Base64URL无填充的27字节Payload>
```

27 字节 Payload 编码为 36 个 Base64URL 字符，加上 `OPL2.` 前缀后邀请码总长固定为 41 个字符。Payload：

| 偏移 | 长度 | 内容 |
|---:|---:|---|
| 0 | 1 | 标志位；bit 0 表示固定房间 |
| 1 | 8 | 房主 UID，十六进制解码后的 8 字节 |
| 9 | 2 | TCP 配对端口，大端序 |
| 11 | 12 | 房间认证密钥 |
| 23 | 4 | 前 23 字节 CRC32 |

邀请码不包含 WireGuard/设备私钥。它是可重复使用、可转发的加入凭据，在房间允许加入且密钥未轮换时有效；程序重启后是否仍有效取决于房间是否选择固定密钥。CRC32 只用于发现输入损坏，安全认证由后续 HMAC 协议完成。

`room.rotateKey` 会立即替换新成员使用的 12 字节房间密钥并返回新邀请码。已配对成员仍可使用独立成员 PSK 重连，因此轮换邀请码不会强制踢出已有成员。

## 11. TCP 配对和控制协议

### 11.1 监听边界

- 房主配对服务只监听 `127.0.0.1:<pairPort>`；
- 远端通过 OpenP2P TCP 内部隧道访问，不直接暴露系统公网监听；
- 最大 16 个并发连接；
- 新连接令牌桶：每秒 5 个，突发 10 个；
- 单阶段空闲超时 10 秒，总配对超时 30 秒；
- 单帧最大 16 KiB。

### 11.2 帧格式

```text
uint32_be length   # 包含 frameType，不包含自身
uint8     frameType
byte[]    payload
```

| 类型 | 值 | 方向 |
|---|---:|---|
| `ServerChallenge` | 1 | 房主 → 客户端 |
| `ClientRequest` | 2 | 客户端 → 房主 |
| `PairResult` | 3 | 房主 → 客户端 |
| `PairAck` | 4 | 客户端 → 房主 |
| `StatsReport` | 5 | 客户端 → 房主，持续 |
| `DeviceSnapshot` | 6 | 房主 → 客户端，持续 |

配对顺序：`ServerChallenge → ClientRequest → PairResult → PairAck`。首次加入以邀请码中的房间密钥进行 HMAC-SHA256；已知成员可改用 32 字节成员 PSK。双方随机数、双方公钥、设备名、租约修订和返回参数都进入认证转录，防止篡改和简单重放。

失败状态：认证失败、暂停加入、房间已满、内部错误、成员已禁用。新成员暂停时被拒绝，已知成员使用成员 PSK仍可恢复连接。

## 12. 虚拟地址和 WireGuard

### 12.1 地址规划

| 用途 | 地址 |
|---|---|
| 子网 | `10.0.23.0/24` |
| 房主 | `10.0.23.1` |
| 成员池 | `10.0.23.2`–`10.0.23.254` |
| 子网广播 | `10.0.23.255` |

租约以设备公钥为主键，已有设备重连复用原 IP；新设备分配最小可用地址。每个新租约拥有单调递增的 revision。未完成 `PairAck` 的新租约会回滚，避免失败连接耗尽地址池。最大成员数为 253。

### 12.2 WireGuard 行为

- 接口名固定为 `OPL`，MTU 使用发现包上限 1200；
- 房主监听用户选择的 UDP 端口，客户端通过 OpenP2P UDP 内部隧道连接房主；
- 每个成员使用 X25519 公钥和独立 PSK；
- 房主 Peer 的 AllowedIPs 为成员 `/32`；客户端房主 Peer 的 AllowedIPs 为 `10.0.23.0/24`；
- PersistentKeepalive 为 25 秒；
- 成员加入/移除使用 wireguard-go UAPI 热更新，不重建整个接口；
- 房主接口启用 IPv4 forwarding，关闭时恢复禁用；
- 关闭时删除组播和有限广播活动路由并关闭 TUN。

## 13. 游戏广播/组播发现

支持的包：

- IPv4 UDP `255.255.255.255` 有限广播；
- IPv4 UDP `10.0.23.255` 虚拟子网广播；
- IPv4 UDP `224.0.0.0/4` 组播；
- 最大完整 IP 包 1200 字节，不支持分片包和 IPv6。

TUN 包装器捕获上述发现包，普通单播仍交给 WireGuard。房主在 `10.0.23.1:25675`（默认端口）运行 UDP 中继：

- 成员包先校验源 IP 必须等于已连接成员的虚拟 IP；
- 房主包标记为 host-origin；
- 包注入房主本地 TUN，并扇出到除发送者外的在线成员；
- 单播响应走普通 WireGuard 路由，不再次进入发现中继；
- 重放窗口默认 1024 项、5 秒；
- 单成员限制 128 包/秒、256 KiB/秒；房间总限制 2048 包/秒。

`core.getState.discovery` 返回累计 `accepted`、`forwarded`、`dropped`。关闭房间时 Core 在 stderr 输出一次汇总，不记录包内容。

## 14. 隧道连接状态

`core.getState.tunnelStates` 直接来自内嵌 OpenP2P 每个 App 的实际 Tunnel：

```json
{
  "protocol":"tcp",
  "srcPort":25565,
  "connected":false,
  "error":"peer offline"
}
```

只要任一底层 Tunnel 正在运行，`connected=true` 并清空旧错误。失败时保留最近错误，WPF 映射为“对端不在线”“网络异常”“对端版本不兼容”“中继节点不可用”等提示。控制服务器在线不等于所有业务隧道已连接。

## 15. 错误码

| 错误码 | 常见原因 |
|---|---|
| `INVALID_REQUEST` | JSON/字段/参数无效，或房间输入非法 |
| `METHOD_NOT_FOUND` | 未知方法 |
| `INVALID_INVITE` | 邀请码长度、Base64、标志、CRC、端口或密钥无效 |
| `UNSUPPORTED_PROTOCOL` | 邀请码前缀/版本不受支持 |
| `AUTH_FAILED` | 房间密钥或成员 PSK认证失败 |
| `JOIN_DISABLED` | 房主暂停新成员加入 |
| `ROOM_FULL` | `10.0.23.2..254` 已无可用地址 |
| `MEMBER_DISABLED` | 设备公钥已被移除/禁用 |
| `MEMBER_NOT_FOUND` | 重命名或移除的公钥不存在 |
| `PAIR_PORT_IN_USE` | 房主 TCP 配对端口无法监听 |
| `OPENP2P_NOT_READY` | 内嵌引擎、内部隧道或网络连接未就绪 |
| `WIREGUARD_FAILED` | Wintun、wireguard-go、接口或路由启动失败 |
| `WIREGUARD_UPDATE_FAILED` | Peer 热更新或移除失败 |
| `SECRET_STORE_FAILED` | DPAPI 或秘密文件读写失败 |
| `CONFIG_WRITE_FAILED` | OpenP2P/房间配置无效或原子写入失败 |
| `INVALID_STATE` | 模式冲突、房间未运行/已运行、文件缺失等 |
| `TIMEOUT` | 30 秒加入超时或上下文取消 |
| `INTERNAL_ERROR` | 未分类内部错误 |

调用方应优先根据稳定 `code` 处理，`message` 仅用于诊断和展示。

## 16. 生命周期与清理

`core.shutdown` 或 stdin 关闭时依次：

1. 离开客户端会话并关闭控制连接；
2. 停止房主配对监听；
3. 清除内部 TCP/UDP App；
4. 关闭发现中继、WireGuard/Wintun 和活动路由；
5. 停止内嵌 OpenP2P；
6. 返回最终响应并退出。

普通 `openp2p.stop` 只在底层引擎确实停止后返回成功。WPF 在等待期间禁用按钮并显示“关闭中”，成功后才切回“启动”。若 Core 无响应，WPF 关闭流程会在短超时后终止子进程。

## 17. 日志与安全边界

- stdout 严格保留给 IPC；OpenP2P 日志写文件，Core/WireGuard 诊断写 stderr；
- Token、房间密钥、设备私钥、成员 PSK、完整邀请码和原始控制帧不得写日志；
- 下载包、Core EXE、Wintun 和更新器都使用 SHA-256 校验；
- 配置接口拒绝伪造 `_opl2_` 内部隧道；
- 配对仅经 loopback 监听和 OpenP2P 隧道到达；
- 配对认证使用 HMAC-SHA256，秘密比较使用常量时间比较；
- TCP 控制帧、设备数、名称、UID、地址和 UDP 中继包都有长度/格式上限；
- `config.json` 含 OpenP2P Token，不得随日志或发布包导出。

## 18. 常见故障定位

| 现象 | 优先检查 |
|---|---|
| Core 启动即退出 | `bin/manifest.json`、Core SHA-256、`wintun.dll` 哈希、stderr |
| `CONFIG_WRITE_FAILED` | `%LOCALAPPDATA%\OPL\core` 权限、`config.json` 字段和端口重复 |
| `PAIR_PORT_IN_USE` | 恢复房间时，已保存的 TCP 配对端口是否被其他程序占用 |
| `OPENP2P_NOT_READY` | OpenP2P Token/UID/服务器、控制网络、防火墙和 `openp2p.log` |
| `WIREGUARD_FAILED` | 是否管理员运行、Wintun 完整性、`netsh` 输出、残留 OPL 接口 |
| 一直显示未连接 | 查看 `tunnelStates.error`，不要只看 `openP2PState` |
| 成员无法重新加入 | 是否暂停加入、设备是否已被移除、固定设备密钥是否仍可解密；查看 `joinError` |
| 游戏看不到房间 | 包是否为 IPv4 UDP 广播/组播、是否超过 1200 字节、发现计数是否增长 |
| 设备列表不更新 | TCP 控制连接是否存活；同步周期为 5 秒，断线重连也至少等待一个周期 |

## 19. 开发入口

主要目录：

| 路径 | 职责 |
|---|---|
| `cmd/opl-core` | 进程入口 |
| `internal/api` | NDJSON API、命令调度、错误码 |
| `internal/core` | 模式编排和总状态 |
| `internal/room` | 房间与成员生命周期 |
| `internal/pairing` | TCP 配对、成员控制与设备同步 |
| `internal/invite` | OPL2 邀请码 |
| `internal/lease` | `10.0.23.0/24` 租约 |
| `internal/wg` | wireguard-go/Wintun 管理 |
| `internal/discovery` | 游戏广播/组播发现中继 |
| `internal/openp2p` | 公共配置与内部 App 隔离 |
| `internal/openp2pengine` | 迁入的 OpenP2P 运行模块 |
| `internal/state` | 非秘密固定房间状态 |
| `internal/secret` | DPAPI 秘密存储 |

构建、版本变更和发布步骤见同目录的《更新推送指南》。
