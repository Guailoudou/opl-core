# OPL Core 独立版本更新机制设计方案

> 状态：待确认，本文只定义方案，不实现更新代码。  
> 起始版本：`2.0.0`  
> 适用仓库：未来从当前 `opl-core/` 独立发布的新仓库。

## 1. 目标

OPL Core 脱离原 WPF 项目后，使用自己的版本号、更新清单、发布包和 GitHub Action。更新系统需要满足：

- Core 从 `2.0.0` 开始独立版本管理，不再读取原 WPF 的 `update.json`。
- 独立更新清单固定为仓库内的 `web/update.json`，随 `web/` 一起部署到 GitHub Pages。
- Web 控制台只负责展示与发起操作，下载、校验、替换、回滚全部由本地 Core 执行。
- 支持 Windows、Linux、macOS 的已发布架构；首发重点保障 Windows 386。
- 更新不能依赖 WPF、.NET 或平台安装器。
- 下载文件必须经过签名、目标平台、长度和 SHA-256 校验。
- Windows 正在运行的 EXE 无法原地覆盖，必须通过临时更新进程完成替换。
- 更新失败必须能自动恢复旧版本，并留下完整日志。

## 2. 版本规则

采用 SemVer：`主版本.次版本.修订版本`。

- 首个独立正式版：`2.0.0`。
- 修复且协议兼容：例如 `2.0.1`。
- 新增兼容功能：例如 `2.1.0`。
- 出现不兼容的本地 API、配置或组网协议：例如 `3.0.0`。
- Core 内部版本、Release 标签和 `web/update.json` 的 `version` 必须完全一致。
- Git 标签固定为 `v2.0.0` 形式；JSON 内不带 `v`。

首版只设计 `stable` 单通道，不增加 beta、灰度比例和差分更新。确有测试通道需求时，再增加独立的 `web/update-beta.json`，不在同一个清单里堆条件。

## 3. 文件位置

```text
opl-core/
├─ web/
│  ├─ index.html
│  ├─ app.js
│  ├─ style.css
│  ├─ update.json          # 独立更新清单
│  └─ update.json.sig      # 对 update.json 原始字节的 Ed25519 签名
├─ .github/workflows/
│  └─ release.yml
└─ cmd/opl-core/
```

GitHub Pages 直接发布 `web/`。Core 默认读取 Pages 上的 `update.json` 和同目录的 `update.json.sig`，不再读取 `file.gldhn.top` 上原 WPF 的更新清单。公告、鸣谢等内容暂时仍可沿用原数据源，它们与 Core 更新信任链互不影响。

## 4. `web/update.json` 格式

建议首版格式如下：

```json
{
  "schemaVersion": 1,
  "product": "opl-core",
  "channel": "stable",
  "version": "2.0.0",
  "publishedAt": "2026-08-26T12:00:00Z",
  "minimumVersion": "2.0.0",
  "releaseNotes": "首个独立 Core 版本",
  "releasePage": "https://github.com/Guailoudou/opl-core/releases/tag/v2.0.0",
  "artifacts": {
    "windows-386": {
      "url": "https://github.com/Guailoudou/opl-core/releases/download/v2.0.0/opl-core-windows-386.zip",
      "sha256": "64位小写十六进制",
      "size": 12345678,
      "format": "zip"
    },
    "windows-amd64": {
      "url": "https://github.com/Guailoudou/opl-core/releases/download/v2.0.0/opl-core-windows-amd64.zip",
      "sha256": "64位小写十六进制",
      "size": 12345678,
      "format": "zip"
    },
    "linux-amd64": {
      "url": "https://github.com/Guailoudou/opl-core/releases/download/v2.0.0/opl-core-linux-amd64.tar.gz",
      "sha256": "64位小写十六进制",
      "size": 12345678,
      "format": "tar.gz"
    }
  }
}
```

正式清单包含 Action 实际发布的全部目标：

- `windows-386`、`windows-amd64`、`windows-arm64`
- `linux-386`、`linux-amd64`、`linux-arm64`、`linux-mips`、`linux-mips64`
- `darwin-amd64`、`darwin-arm64`

`minimumVersion` 表示允许直接升级到该版本的最低 Core 版本。首版先设为 `2.0.0`；以后只有配置或更新协议无法跨版本迁移时才提高。

## 5. 清单签名

仅依赖 HTTPS 和 SHA-256 不够：如果 Pages 或仓库发布权限被盗，攻击者可以同时替换文件和哈希。因此采用 Ed25519：

1. 生成一次更新签名密钥对。
2. 私钥只保存为新仓库的 GitHub Actions Secret，例如 `OPL_UPDATE_SIGNING_KEY`，不得提交到仓库或打入发布包。
3. 公钥固定编译进 Core。
4. Action 生成最终 `update.json` 后，对文件的原始字节签名，输出 Base64 编码的 `web/update.json.sig`。
5. Core 先下载两个文件并验证签名，通过后才解析 JSON。
6. Core 再按清单校验目标平台、下载长度和 SHA-256。

签名对象是 `update.json` 的原始文件字节，因此不需要自行实现 JSON 字段排序或“规范化 JSON”。任何字节变化都必须重新签名。

## 6. Core 更新状态机

```text
idle
  → checking
  → available / up_to_date
  → downloading
  → verifying
  → ready
  → stopping_network
  → applying
  → restarting
  → healthy

任一步失败 → failed
替换后健康检查失败 → rolling_back → rolled_back / rollback_failed
```

WebSocket 新增方法建议：

- `update.getState`：取得当前版本、可用版本、进度和错误。
- `update.check`：立即检查更新。
- `update.download`：下载并校验，但不重启。
- `update.install`：停止当前网络、安装并重启。
- `update.cancel`：只允许取消检查或下载，不在文件替换阶段强制中断。

WebSocket 推送事件：

- `update.changed`

进度数据至少包含 `state`、`currentVersion`、`availableVersion`、`downloadedBytes`、`totalBytes`、`message`。

## 7. 检查与下载

1. Core 启动后延迟 10 秒检查一次，避免阻塞本地管理页面启动。
2. 此后每 6 小时检查一次；Web 页面可手动立即检查。
3. 默认只提示，不自动安装。用户在 Web 控制台确认后才开始更新。
4. 使用系统临时目录下的独立 staging 目录下载，禁止直接写入程序目录。
5. HTTP 客户端设置连接和总下载超时，限制清单大小、签名大小和发布包最大尺寸。
6. 只接受 HTTPS；重定向后的最终 URL 仍必须是 HTTPS。
7. 下载完成后依次验证文件长度、SHA-256、压缩包结构和目标平台。
8. 解压时拒绝绝对路径、`..`、符号链接和任何逃逸 staging 目录的路径。

更新包只允许包含：

- 对应平台的 `opl-core` 可执行文件；
- `web/`；
- Windows 对应架构的 `wintun.dll`；
- README、组件清单和许可证文件。

## 8. 安装与回滚

### 8.1 通用流程

1. Core 将已校验的新包解压到 staging。
2. 再次核对包内文件类型与必要文件。
3. 记录更新前运行状态：普通隧道是否运行、是否为房主、是否正在加入网络。
4. 通知 Web 即将断开，停止组网、OpenP2P 和 TUN，完整落盘状态与日志。
5. 把当前可执行文件复制到临时目录，并以 `--apply-update` 模式启动这个临时副本。
6. 主 Core 退出。
7. 临时更新进程等待原 PID 退出，执行原子替换，并重新启动新 Core 的 `--daemon` 模式。
8. 更新进程轮询新版 `/health`，健康信息必须包含产品名、API 版本和新版 Core 版本。
9. 新 Core 健康后删除旧版本备份与 staging；失败则恢复备份并启动旧 Core。

同一套临时更新进程用于 Windows、Linux 和 macOS，避免维护两套替换协议。Windows 必须从临时目录运行更新副本，否则运行中的 EXE 会锁定自身文件。

### 8.2 文件替换

- 当前版本先重命名为 `.old`，新文件先写为 `.new` 并同步到磁盘，再重命名为正式名称。
- `web/` 使用新目录整体替换，旧目录保留到健康检查完成。
- Windows 同时替换对应架构的 `wintun.dll`。
- 配置、房间密钥、设备密钥、邀请码、成员租约和日志位于用户数据目录，不放在程序目录，更新不得覆盖。

### 8.3 健康检查与回滚

- 启动新版后最多等待 30 秒。
- `/health` 必须返回 `product=opl-core`、正确的 `apiVersion` 和目标 `coreVersion`。
- 超时、进程提前退出、版本不符均视为失败。
- 回滚恢复可执行文件、`web/` 和 Wintun，再启动旧版。
- 只保留一个上一版本备份；更新成功 24 小时后或下一次成功更新时清理。
- 若回滚也失败，更新进程保留 staging、备份和日志，并显示可人工恢复的明确路径。

## 9. 运行状态恢复

更新前把“期望运行模式”写入一次性恢复文件：

```json
{
  "schemaVersion": 1,
  "mode": "room-host",
  "createdAt": "2026-08-26T12:00:00Z"
}
```

新版 Core 健康后读取并删除该文件：

- `ordinary`：恢复普通 OpenP2P 隧道。
- `room-host`：恢复房主监听。
- `room-client`：使用已保存的固定设备密钥和成员密钥继续重连房主。
- `idle`：不自动启动网络。

恢复失败不触发版本回滚，因为程序本身已经健康；错误展示在 Web 的组网/隧道状态中并继续按现有重连规则处理。

## 10. GitHub Action 发布顺序

版本发布只允许从 `v*` 标签触发：

1. 校验标签、源码版本和 `web/update.json` 目标版本一致。
2. 使用 Go 1.20.14 测试。
3. 构建全部目标并生成包。
4. 计算每个包的大小和 SHA-256。
5. 把真实 Release URL、大小和哈希写入 `web/update.json`。
6. 使用 Actions Secret 中的 Ed25519 私钥生成 `web/update.json.sig`。
7. 创建 GitHub Release 并上传包。
8. 部署包含最终 `update.json` 和签名的 `web/` 到 GitHub Pages。
9. 发布后从公开 URL 回读清单、签名和一个首发 Windows 386 包，完成签名与哈希冒烟验证。

如果任一步失败，不更新 Pages 清单，避免页面指向未完整发布的版本。

## 11. Web 控制台

关于页面新增：

- 当前 Core 版本；
- 最新稳定版本；
- 检查更新时间；
- 更新说明；
- “下载更新”“安装并重启”按钮；
- 下载/校验/安装/回滚进度；
- 失败原因和更新日志路径。

安装按钮在执行后立即变为“正在停止网络 / 正在安装”，禁止重复点击。浏览器断开不影响更新进程；重新打开页面后通过 `update.getState` 恢复显示。

## 12. 日志

更新日志独立写入用户数据目录：

```text
OPL/core/logs/update.log
```

至少记录：当前版本、目标版本、清单 URL、签名验证结果、目标文件哈希、原 PID、每次重命名、重启命令、健康检查结果和回滚结果。不得记录房间密钥、设备私钥、成员 PSK 或完整邀请码。

## 13. 不在首版实现

- 二进制差分更新；
- 多更新通道和灰度比例；
- 静默自动安装；
- 平台安装器或系统服务自身升级；
- 从 `1.x` WPF 包直接升级到 `2.x` Core 包。

这些能力都不影响当前完整包更新协议，出现明确需求后再增加。

## 14. 需要确认

实施前需要确认以下 4 项：

1. **已确认**：新仓库为 `Guailoudou/opl-core`；默认更新清单地址为 `https://guailoudou.github.io/opl-core/update.json`，Release 地址为 `https://github.com/Guailoudou/opl-core/releases`。
2. **已确认**：默认自动检查并提示，但必须由用户在 Web 控制台手动点击安装，不进行静默安装。
3. **已确认**：生成独立的 Ed25519 更新签名密钥；私钥只存入新仓库 GitHub Actions Secret，公钥编译进 Core，并且不与任何组网密钥共用。
4. **已确认**：更新成功并重启 Core 后，按第 9 节自动恢复普通隧道、房主监听或加入网络重连状态；恢复网络失败不回滚已健康运行的新版本。

确认后实施顺序为：先冻结 JSON schema 和签名测试向量，再实现 Core 下载/校验，随后实现临时更新进程与回滚，最后接入 Web 页面和新仓库 Action。
