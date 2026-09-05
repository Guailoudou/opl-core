# OPL Core

OPL Core 是独立、跨平台的 OpenP2P 管理核心。它内嵌本项目需要的 OpenP2P 隧道引擎，使用本地 Web 控制台替代 WPF。

## 运行

直接运行 `opl-core`：

1. 若 Core 尚未运行，启动器会拉起后台 Core，等待管理服务就绪，然后打开当前选中网卡的 `http://<IPv4>:26780/`。首次运行使用 `127.0.0.1`，可在“设置 → Web 管理访问”选择局域网网卡，切换立即生效。
2. 若 Core 已运行，只打开现有 Web 控制台，不产生第二个后台实例。
3. 关闭浏览器不会停止 Core；Windows 可通过右下角托盘图标打开 Web 管理或退出 Core，也可以在 Web 控制台底部点击“关闭 Core”。

Windows 创建 Wintun、Linux/macOS 创建 TUN 均需要管理员/root 权限。Windows 发布目录中的 `wintun.dll` 必须与程序架构一致。

调试时可使用 `opl-core --foreground`；旧 NDJSON 标准输入输出协议保留为 `opl-core --stdio`。

## 配置与数据

默认数据目录由 Go `os.UserCacheDir` 决定，并追加 `OPL/core`。可用环境变量覆盖：

- `OPL_DATA_DIR`：Core 状态、固定设备密钥和日志目录。
- `OPL_OPENP2P_CONFIG`：OpenP2P `config.json` 路径。
- `OPL_WEB_DIR`：本地静态页面目录，默认使用可执行文件旁的 `web`。
- `OPL_WEB_ORIGIN`：允许连接本地 WebSocket 的外部静态站点 Origin，多个值用逗号分隔，例如 `https://owner.github.io`。

管理地址和客户端访问白名单保存在数据目录的 `set.json`。Core 只允许绑定当前设备真实存在的 IPv4 网卡，不使用 `0.0.0.0`；网卡失效时自动退回 `127.0.0.1`。默认白名单包含 `127.0.0.1` 和本机全部局域网 IPv4，其他客户端会在 HTTP/WebSocket 入口被拒绝；可在“设置 → Web 管理访问”增加允许访问的客户端 IPv4。

## 平台

发布 Action 与 OpenP2P 的常规可执行程序矩阵一致：

| 系统 | 架构 | TUN |
|---|---|---|
| Windows | 386、amd64、arm64 | Wintun 0.14.1 |
| Linux | 386、amd64、arm64、mips、mips64 | `/dev/net/tun` + `ip` |
| macOS | amd64、arm64 | utun + `ifconfig`/`route` |

Android 与 OpenHarmony 与 OpenP2P 原项目相同：TUN 必须由 VPNService/VpnConnection 宿主应用创建并传给 Go 移动端库，不能发布为本仓库这个后台命令行程序。对应移动宿主适配应从原项目的 `app`、`ohos` 与 `optun_android.go`、`optun_ohos.go` 同步迁移，不能用 Linux 二进制冒充。

## Web 与 GitHub Pages

静态页面全部位于 `web/`。`.github/workflows/release.yml` 在 `main` 分支自动部署该目录到 GitHub Pages，并在版本标签上发布各平台 Core 包。新仓库需要在 Settings → Pages 中把 Source 设为 GitHub Actions。

运行时控制台由 Core 从所选 IPv4 地址提供，并使用同源 WebSocket。GitHub Pages 用于静态页面发布和更新来源；浏览器对 HTTPS 页面访问局域网明文 WebSocket 有安全限制，因此实际管理入口应使用 Core 打开的本地页面。

公告、更新日志和鸣谢沿用原 WPF 数据源：`file.gldhn.top/file/json/notice.json`、`update.json`、`thank.json`。本地页面通过 Core 的只读代理读取，Pages 页面直接读取同一来源。

## 构建

必须使用 Go 1.20.14：

```text
go test -mod=readonly ./...
go build -mod=readonly ./cmd/opl-core
```

不得升级到 Go 1.21+，否则失去 Windows 7/8 兼容基线。首发仍可只发布 `windows-386`，Action 同时验证和生成其他 OpenP2P 已发布的平台制品。
