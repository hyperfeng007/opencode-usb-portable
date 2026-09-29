# 第三方组件声明 / Third-party notices

本仓库自身的代码以 [MIT](LICENSE) 许可发布。仓库中或运行时涉及的第三方组件如下：

| 组件 | 许可 | 用途与说明 |
|---|---|---|
| [opencode](https://github.com/anomalyco/opencode)（anomalyco/opencode） | MIT | 本工具把官方桌面版**重新打包**成便携形态；不修改其源码，只在构建时对用户本机/本 U 盘上的 `resources/app.asar` 打补丁 |
| [7-Zip Extra 26.03](https://www.7-zip.org/) | GNU LGPL | `tools/7za/`（`7za.exe` / `7za.dll`），用于解包官方 NSIS 安装包；许可证全文见 `tools/7za/License.txt` |
| [Git for Windows](https://gitforwindows.org/) | GPLv2 | **可选**：构建时加 `--with-git` 会下载 PortableGit 到 U 盘，本仓库不分发该二进制 |
| [models.dev](https://models.dev) | 公开数据 | 启动器读取其公开模型目录（`api.json`）用于筛选模型，仅作只读查询 |
| Pollinations.AI | 其服务条款 | 免 key 公开接口，作为默认的免 key 模型来源；使用前请自行确认其条款 |

分发本工具或基于它制作的产品时，请一并满足上述组件的许可要求。

## 关于 opencode 的再分发

- 本工具**不包含** opencode 的二进制文件：成品包内只有启动器、构建器与 7-Zip Extra；opencode 由使用者首次运行时
  从官方 Release 下载（并按官方 SHA-256 校验）。
- 构建过程只在**使用者自己的机器/U 盘**上修改其已下载的副本，用于重定向可写路径（便携化），
  不改变 opencode 的功能与授权。
