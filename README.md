# OpenCode 便携版（U 盘版） · OpenCode Portable for USB drives

把 [opencode](https://opencode.ai) 桌面版做成**插上 U 盘就能用**的便携程序：

- **单文件入口**：双击 `OpenCode-Portable.exe` 即可。没装过 → 自动下载、解包、便携化改造、启动；装过 → 直接启动。
- **零宿主残留**：所有数据（会话、凭据、缓存、日志、临时文件）全部落在 U 盘的 `data\` 里；拔盘后 Windows 上不留文件、注册表项、快捷方式或服务。
- **自动使用系统代理**：启动器与构建器的联网都自动读取系统代理（WinINET / WinHTTP / PAC / 注册表 `ProxyEnable`+`ProxyServer`），也支持镜像回退。
- **自动挑选免 key 免费模型**：每次启动都会体检当前模型，只在**完全不需要 API key** 的免费模型里选择（内置公开免 key 接口 + 本机 Ollama / LM Studio 等），并**验证工具调用能力**，失效时自动切换。
- **进度可见**：原生进度窗口实时显示"正在做什么 / 已完成百分比"，opencode 窗口出现后自动消失；失败必留证据（弹窗 + 日志 + 状态文件），不会静默闪退。

> 本仓库只包含**源码与构建脚本**。可直接使用的成品包在 [Releases](../../releases) 里下载。

---

## 快速开始

### 方式一：用发布包（推荐）

1. 到 [Releases](../../releases) 下载 `opencode-usb-portable-vX.Y.Z.zip`；
2. 解压到 U 盘（根目录或任意文件夹都行）；
3. 双击 `OpenCode-Portable.exe`，等进度条走完。

首次安装需要联网（约 120 MB 下载 + 约 500 MB 解包），建议 ≥ 2 GB 空闲空间。

### 方式二：自己构建（Linux/macOS 交叉编译，或 Windows 上编译）

```bash
git clone https://github.com/hyperfeng007/opencode-usb-portable.git
cd opencode-usb-portable
./scripts/build.sh       # 产出 releases/OpenCode-Portable.exe 与 releases/OpenCode-Portable-Builder.exe
./scripts/package.sh     # 打成一个可直接拷进 U 盘的 zip
```

需要 Go 1.24+。两个可执行文件都是**纯 Go、无 cgo**，Windows 上不需要任何运行时依赖。

---

## 目录结构

```
launcher 源码      payload/launcher/src/     Go：启动器（进度窗口、代理探测、便携环境注入、构建编排、模型体检）
                    payload/launcher/*.cmd    手动入口脚本（CRLF + 纯 ASCII，避免 cmd.exe 解析问题）
构建器源码          builder/                  Go：下载/解包/打补丁/写版本信息，含 7za 解包与 asar 重建
便携化注入脚本      payload/inject-bootstrap.js   注入进 app.asar 的引导模块（重定向所有可写路径 + 残留台账）
                    builder/bootstrap.js         同一份文件的副本（构建器内嵌，优先读取 exe 同目录的外部文件）
asar 补丁参考实现   payload/patch-asar.mjs    Node 版参考补丁器（Go 版与其输出逐字节一致）
文档                docs/使用说明.md           面向用户的中文说明书
                    docs/README-launcher.md    启动器技术说明
第三方工具          tools/7za/                7-Zip Extra 26.03（LGPL），用于解包 NSIS 安装包
构建脚本            scripts/                  build.sh / package.sh
```

---

## 它是怎么做到"便携"的

1. **启动器**定位 U 盘根目录（`OPENCODE_PORTABLE_ROOT` 或向上查找 `OpenCode-Portable.json`），把
   `userData`、`sessionData`、`Crashpad`、`TMP/TEMP`、`HOME`、`APPDATA`、`LOCALAPPDATA`、`XDG_*`
   全部指向 `data\` 下的子目录，再启动 opencode。
2. **主程序补丁**（构建时用 Go 重写 `resources\app.asar`，输出与 Node 参考实现逐字节一致）：
   - 关闭自动更新 —— 绝不会在宿主机上安装 NSIS 包；
   - `app.setPath("userData"/"sessionData")` 强制走 U 盘；
   - 不注册 `opencode://` 协议关联；
   - 注入 `out/main/opencode-portable-boot.js`：启动前重定向路径 + 维护"宿主残留台账"
     （只清理**本程序自己创建的**痕迹，绝不触碰用户原有目录）。
3. **asar 重建**会重算每个文件的 `integrity`（SHA256 / 4 MB 分块），保持与官方归档一致的结构。

## 模型是怎么选的

每次启动一次体检，全部机械可查（`data\models-report.json`）：

1. 只收集**免 key** 来源：内置公开免 key 接口（Pollinations 匿名层）、本机 Ollama / LM Studio / llama.cpp / Jan / vLLM / SGLang / GPT4All / KoboldCpp、以及 `model-policy.json` 里自定义的免 key 接口；
2. 排序：支持工具调用 > 代码模型 > 上下文更大 > 本机（无限量）> 公共接口；
3. 发真实的最小请求验证可达（走系统代理，本地地址不经过代理）；
4. **验证工具调用**：让模型真的触发一次 `tool_calls` —— opencode 靠这个读写文件和执行命令；
5. 当前模型可用则**不改配置**；失效则写入 provider 定义并切换 `model`，原配置先备份到 `data\backup\`。

详配置见 `docs/使用说明.md`「三之二」。想改用需要 key 的来源（OpenCode Zen / 智谱 / OpenRouter…）：把 `data\model-policy.json` 里的 `keylessOnly` 改成 `false`。

---

## 已知限制

- 公共免 key 接口是**共享匿名额度**，可能被限流或调整策略；启动器每次启动都会重测并自动降级/提示，但无法保证永久可用。最稳的免 key 方案是**本机 Ollama**（无限量、可离线）。
- 首次使用自定义 provider 时，opencode 需要联网从 npm 拉取 `@ai-sdk/openai-compatible`（之后缓存在 U 盘）。
- 便携模式下部分"系统级"能力（开机自启、系统托盘常驻、系统通知）可能不完整，这是便携与零残留的取舍。
- opencode 版本会更新，若某些版本的代码结构变化，`--version <已验证版本>` 可回退构建（例如 `v1.18.31`）。

## 许可

本仓库自身代码：**MIT**（见 [LICENSE](LICENSE)）；第三方组件（opencode / 7-Zip Extra / Git for Windows / models.dev 等）
的许可与再分发说明见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。使用、分发前请确认符合各组件的许可条款。

## 安全提示

- 不要把任何 API key、token、`auth.json` 提交到仓库；`.gitignore` 已排除常见的凭据与本地数据目录。
- 本程序不会把数据写到宿主机；如果你发现任何残留，欢迎提 issue 并附上 `data\launcher.log` 与 `data\build.log`。
