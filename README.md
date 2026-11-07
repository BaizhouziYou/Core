# EasyBotCE Core

EasybotCE 的主程序。当前重建项目由 Go HTTP API 和 Vue 3 管理台组成，使用 SQLite 默认存储，并提供 MySQL/PostgreSQL 存储实现与集成测试目标。

## 项目结构

- `dev/`: Go 服务、Vue 3 + TypeScript + Tailwind CSS v4 管理台和开发说明。
- `PLAN.md`: 原始重建规划。
- `REMAINING_PLAN.md`: 后续版本产品范围、验收标准和工作包。
- `.github/workflows/`: 本地构建及数据库集成测试。

当前运行时已覆盖管理员初始化/认证、管理台概览、配置 CRUD、持久化运行日志及 OneBot 11 正向 WebSocket 连接；OneBot 模拟器验证已完成，真实账号仍待验收。Minecraft 网络桥接和消息同步尚未实现。MySQL/PostgreSQL 是否正式支持以对应 CI 集成测试通过为准。

## License and attribution

This repository is licensed under [CC BY-NC 4.0 (`CC-BY-NC-4.0`)](https://creativecommons.org/licenses/by-nc/4.0/). Commercial use is not permitted by that license. When sharing or adapting this project, credit EasybotCE and link to this repository and the license, identify changes, and retain existing author, copyright, and license notices. See [`AUTHORS.md`](AUTHORS.md).

## 运行

Windows 用户下载便携压缩包，解压到可写目录后双击 `easybotce.exe`，浏览器打开 `http://127.0.0.1:5001`。默认本机回环监听直接进入管理台，无需注册或登录。包内已包含页面资源，默认使用 SQLite，无需安装 Go、Node.js 或数据库服务器，也无需启动脚本。运行期间保持程序窗口打开。

非本地监听使用原版式 Token 登录，不需要用户名或独立 API Key。首次自动生成 12 位登录 Token，仅在控制台显示一次；`token.txt` 保存原版格式的 SHA-256 哈希，后续沿用。非本地地址可直接使用 HTTP，HTTPS 可选；公网传输 Token 时建议使用 HTTPS。

保留包内的 `web` 目录。首次启动自动创建 `appdata` 数据目录；升级前停止程序、备份并保留 `appdata`，再替换程序和页面资源。已有数据库可通过 `EASYBOT_SQLITE_PATH` 指定。

Windows 双击启动失败时，窗口会保留并显示原因，按回车后关闭。从已有终端或自动化环境运行时直接返回错误码。

## 启动配置与原版迁移

便携包自带 `appsettings.json`，缺失时首次启动自动生成。修改后重启生效，程序不会重写已有配置。沿用原版字段：

- `Kestrel.Endpoints.web_api.Url`：WebUI 地址，CE 默认 `http://127.0.0.1:5001`。
- `ServerOptions.Host`、`ServerOptions.Port`：Bridge 地址，CE 默认 `127.0.0.1:26991`；运行时尚未实现。
- `Serilog.MinimumLevel.Default`：控制台日志级别。
- `AllowedHosts`：允许的 HTTP Host，支持 `*`、分号分隔的主机名及 `*.example.com`。

迁移启动配置时，将原版 `appsettings.json` 直接复制到 CE 的 exe 旁边即可，现有字段及端口按原值读取。已有原版 `token.txt` 也可一并复制，继续使用同一个登录 Token。你本机原版使用的 5000/26990 会保留；同时运行原版时请改成不同端口。CE 新增配置缺失时使用默认值，原版数据库需要单独迁移，不能直接替换 CE 数据库。

忘记 Token 时，先停止程序并删除 `token.txt`，非本地模式重启后会生成新的 Token，并使旧登录会话失效。升级请保留自己的 `appsettings.json`、`token.txt` 和 `appdata`。旧 CE 用户名/密码登录由 Token 登录替代，业务数据保留。

`EASYBOT_CONFIG` 可指定其他配置文件，原有环境变量优先于 JSON 配置。开发构建默认生成 `dev/appsettings.json`；exe 旁已有配置时优先读取它。配置示例见 [appsettings.example.json](dev/internal/config/appsettings.example.json)。

## 开发

环境要求：Go 1.24+、Node.js 20.19+。

运行端口：`127.0.0.1:5001` 同时提供 WebUI 和管理 API；`26991` 用于 Bridge 与插件/模组通信（运行时尚未实现，当前不监听）。构建后的程序不需要独立前端服务器。WebUI 地址可通过 `EASYBOT_ADDR` 覆盖。

贡献者与开发代理须遵守 [贡献规范](CONTRIBUTING.md) 和 [协作约定](AGENTS.md)：所有后续 commit 使用中文。

在仓库根目录执行一条命令，完成依赖下载、前端构建、Go 编译和便携包打包：

```powershell
node build.mjs
```

Windows 输出 `release/easybotce-windows-amd64.zip`，Linux 输出对应平台的 `.tar.gz`（程序名为 `easybotce`）；CI 生成的便携包可从运行记录的 Artifacts 下载。本地编译程序同时保存在 `dev/bin`。

浏览器打开 `http://127.0.0.1:5001`。可选热更新开发方式见开发说明，日常运行无需启动 Vite。

运行方式、配置项和安全说明见 [`dev/README.md`](dev/README.md)。
