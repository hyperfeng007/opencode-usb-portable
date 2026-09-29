# OpenCode 便携启动器 (OpenCode-Portable.exe)

一个原生 Windows 启动程序（无控制台黑框），专门用于 **U 盘上的 opencode 便携版**。
双击它就会：自动识别 U 盘根目录 → 把全部可写数据指向 U 盘 → **自动读取系统网络代理并注入** → 启动 OpenCode 桌面版。

版本：1.0.0 ｜ 目标系统：Windows 10 / 11（64 位）｜ 单文件，无依赖，无需安装

---

## 一、快速使用

1. 把 `OpenCode-Portable.exe` 放到 U 盘根目录（与 `OpenCode-Portable.json`、`OpenCode\` 同级）。
2. 双击即可。
   - 没有任何窗口弹出是正常的（GUI 子系统程序）。
   - 想看到运行细节或排查问题，请用 `launcher\OpenCode-debug.cmd`。

U 盘目录结构（启动器期望的样子）：

```
<U盘>\
├─ OpenCode-Portable.exe      ← 本启动器
├─ OpenCode-Portable.json     ← 便携标记文件（必需）
├─ OpenCode\OpenCode.exe      ← opencode 桌面版主程序
├─ data\                      ← 全部用户数据（自动创建）
│   └─ launcher.log           ← 启动器日志
├─ runtime\node\              ← 可选：便携 Node
└─ tools\git\                 ← 可选：便携 Git
```

## 二、自动代理检测（本启动器的核心功能）

启动顺序里会依次尝试下面 5 种来源，取到第一个可用的即用：

| 顺序 | 来源 | 说明 |
|---|---|---|
| 1 | 已有环境变量 | 若系统已设置 `HTTP_PROXY` / `HTTPS_PROXY`，直接沿用，不覆盖 |
| 2 | WinINET 系统代理 | Windows「设置 → 网络和 Internet → 代理」里配置的代理 |
| 3 | WinHTTP / IE 配置 | `WinHttpGetIEProxyConfigForCurrentUser`，包含 PAC 脚本地址 |
| 4 | **注册表直读** | `HKCU\...\Internet Settings` 的 `ProxyEnable` + `ProxyServer`，Clash / v2rayN / sing-box / Proxifier 等工具写的就是这里 |
| 5 | PAC / WPAD | 若开启「自动检测设置」，会用 `WinHttpGetProxyForUrl` 向系统要一个真实可用的代理 |

检测到之后注入给 OpenCode 及其子进程（Node、Git、内嵌服务端都会自动走代理）：

```
HTTP_PROXY / http_proxy
HTTPS_PROXY / https_proxy
NO_PROXY / no_proxy     ← 自动包含 localhost,127.0.0.1,::1
```

支持的代理写法（Windows 三种格式都能解析）：

- `127.0.0.1:7897`（统一代理）
- `http=127.0.0.1:7890;https=127.0.0.1:7890`（分协议）
- `socks=127.0.0.1:1080` → 自动转成 `socks5://127.0.0.1:1080`

**关闭自动代理**：设置环境变量 `OPENCODE_PORTABLE_DISABLE_PROXY=1` 后再启动（例如你希望 OpenCode 走直连，或代理在 TUN 模式下不需要注入）。

## 三、便携化行为（拔盘无残留）

启动器会给 OpenCode 设置这些环境变量，把**所有**可写位置搬到 U 盘：

| 变量 | 指向 |
|---|---|
| `OPENCODE_PORTABLE_ROOT` | U 盘根目录 |
| `OPENCODE_PORTABLE_USERDATA` | `<U盘>\data\userdata` |
| `XDG_CONFIG_HOME` / `XDG_DATA_HOME` / `XDG_CACHE_HOME` / `XDG_STATE_HOME` | `<U盘>\data\{config,data,cache,state}` |
| `HOME` / `TMP` / `TEMP` | `<U盘>\data\{home,tmp}` |
| `APPDATA` / `LOCALAPPDATA` | `<U盘>\data\{roaming,local}` |
| `OPENCODE_DISABLE_AUTOUPDATE` | `1`（绝不在宿主机安装更新） |

配合主程序补丁（见 `patch-asar.mjs`）：不向注册表写 `opencode://` 协议关联、不启动自动更新、`userData`/`sessionData` 落在 U 盘。
另外主进程内置的「残留台账」会在每次启动和退出时检查并清理宿主机上可能出现的痕迹（`%APPDATA%\ai.opencode.desktop`、`%LOCALAPPDATA%@opencode-aidesktop-updater`、`<TEMP>\opencode` 等），且**只删除本程序自己创建的**目录。

## 四、日志与排错

- 启动器日志：`<U盘>\data\launcher.log`（超过 80 KB 自动轮转为 `launcher.log.1`）
- OpenCode 自身日志：`<U盘>\data\userdata\logs\`

日志里会明确写出：

```
launcher    : opencode-portable launcher 1.0.0
root        : E:\
proxy source: Windows system proxy settings
HTTP_PROXY  : http://127.0.0.1:7897
HTTPS_PROXY : http://127.0.0.1:7897
NO_PROXY    : localhost,127.0.0.1,::1
started     : pid 12345
```

常见情况：

| 现象 | 原因 / 处理 |
|---|---|
| 弹窗提示找不到 `OpenCode.exe` | U 盘还没完成首次构建，先在联网的 Windows 上运行一次 `Build portable version.cmd` |
| 日志显示 `none detected (direct connection)` | 系统没有开代理，或代理在 TUN/全局模式下（这时直连就是对的） |
| 代理端口填错（如 7897 写成本机不存在的端口） | 应用会连不上网；改回系统设置里正确的端口即可 |

## 五、自行编译

源码在 `launcher-source\`（Go 1.24+，纯标准库，无第三方依赖）：

```bash
# Linux / macOS 交叉编译
cd launcher-source
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "-H windowsgui -s -w" -o OpenCode-Portable.exe .

# Windows 本机编译
go build -ldflags "-H windowsgui -s -w" -o OpenCode-Portable.exe .

# 单元测试（代理解析逻辑）
go test ./...
```

ARM64 版本把 `GOARCH=amd64` 换成 `GOARCH=arm64` 即可。

## 六、已知限制

- 需要 Windows 10 1809 以上 / Windows 11（64 位）。
- 若系统代理需要**账号密码**认证，启动器只转发地址（不含凭据）；这种情况请在 `data\config\opencode.json` 或应用内单独配置。
- 启动器只负责启动与代理注入，不做 U 盘首次构建（那是 `Build portable version.cmd` 的工作）。

---

校验值（SHA-256）：

```
OpenCode-Portable.exe  f5817c9d9d6490b9e8ebabd833b21b88158476f12a60d2fd1414b243776bcf6f
```
