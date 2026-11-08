# EasybotCE 开发说明

`dev/` 包含 Go 管理服务和 Vue 3 管理台。默认使用 SQLite，也可通过配置接入 MySQL 或 PostgreSQL。普通用户的便携包使用方法见 [项目首页](../README.md)。

## 构建与本地运行

开发环境要求：Go 1.24+、Node.js 20.19+。在仓库根目录执行 `node build.mjs`，即可安装依赖、构建页面、编译程序并在 `release/` 中生成便携包。

手动调试时，从本目录 `dev/` 执行以下 PowerShell 命令：

```powershell
cd web
npm ci
npm run build
cd ..
$env:EASYBOT_CONFIG = Join-Path (Get-Location) 'appsettings.json'
go run ./cmd/easybot
```

浏览器打开 `http://127.0.0.1:5001`。Go 进程同时提供 API 和 `web/dist` 中构建好的页面；日常运行不需要启动 Vite。

需要前端热更新时，在 `dev/web/` 另开终端执行 `npm run dev`，并保持 Go 服务运行。Vite 将 `/api` 请求代理到 `http://127.0.0.1:5001`，其临时端口只用于开发。

| 服务 | 默认地址 | 状态 |
| --- | --- | --- |
| 网页管理台与管理 API | `127.0.0.1:5001` | 由 Go 进程监听 |
| Minecraft 桥接（Bridge） | `127.0.0.1:26991` | 仅保留配置约定，尚无监听、握手或协议兼容实现 |

### 可执行程序与数据位置

便携包包含可执行程序、`appsettings.json`、`web/dist`、许可证和使用说明。解压到可写目录后即可运行，默认数据保存在同目录的 `appdata/`，需保留包内目录结构。

开发构建的 `dev/bin/easybotce.exe` 默认读取 `dev/appsettings.json`；程序旁已有配置时优先读取。`go run` 使用临时可执行文件，因此应按上面的示例通过 `EASYBOT_CONFIG` 指定开发配置。

Windows 双击启动失败时，程序显示错误并等待回车关闭；从已有终端或无控制台任务运行时不暂停，返回非零退出码。

## 启动配置

配置文件使用标准 JSON，支持 UTF-8 BOM。缺失时自动生成默认文件；已有文件始终只读，CE 默认值只补充缺失的配置项。模板见 [appsettings.example.json](internal/config/appsettings.example.json)。

| 配置项 | 说明 |
| --- | --- |
| `Kestrel.Endpoints` | 配置一个 HTTP 或 HTTPS 端点；默认管理地址为 `http://127.0.0.1:5001` |
| `ServerOptions.Host`、`ServerOptions.Port` | 桥接配置，默认 `127.0.0.1:26991`；当前只读取，不建立监听 |
| `ServerOptions.HeartbeatInterval` | 为桥接运行时保留的配置 |
| `Serilog.MinimumLevel` | 支持日志级别字符串，或包含 `Default` 的对象 |
| `AllowedHosts` | 允许的请求主机名；支持 `*`、分号分隔的主机名和通配子域名 |
| `Database.Url` | CE 扩展项，用于配置 MySQL 或 PostgreSQL 连接地址 |
| `Database.SQLitePath` | CE 扩展项，默认 `appdata/easybot.db` |

JSON 中的相对文件路径基于配置文件目录解析；环境变量中的相对路径基于启动目录解析。原版配置缺少 `Database.SQLitePath` 且配置文件放在开发用 `bin/` 目录时，数据仍保存在其父目录的 `appdata/` 中。

### 环境变量

相关环境变量优先于 JSON 配置：

| 环境变量 | 用途 |
| --- | --- |
| `EASYBOT_CONFIG` | 指定配置文件 |
| `EASYBOT_ADDR` | 覆盖管理服务监听地址，例如 `127.0.0.1:5001` |
| `EASYBOT_SQLITE_PATH` | 覆盖 SQLite 数据库路径，可指向已有 CE 数据库 |
| `EASYBOT_DATABASE` | 覆盖外部数据库连接地址 |
| `EASYBOT_TLS_CERT`、`EASYBOT_TLS_KEY` | 指定 PEM 证书及私钥文件 |
| `EASYBOT_ALLOWED_HOSTS` | 覆盖允许的请求主机名 |

MySQL 连接格式为 `mysql://user:password@host:3306/database`；PostgreSQL 连接格式为 `postgres://user:password@host:5432/database?sslmode=disable`。后者示例关闭了数据库连接的 TLS，实际连接参数应与数据库部署方式一致。

### HTTPS 示例

本地与非本地监听均支持 HTTP。选择 HTTPS 时需提供有效的 PEM 证书和私钥，也可使用反向代理终止 TLS；当前不支持带密码的 PFX。通过公网传输凭据时建议使用 HTTPS。

```json
{
  "Kestrel": {
    "Endpoints": {
      "web_api": {
        "Url": "https://0.0.0.0:5001",
        "Certificate": {
          "Path": "cert.pem",
          "KeyPath": "key.pem"
        }
      }
    }
  }
}
```

示例中的证书相对路径基于配置文件目录解析。

## 登录与访问控制

认证模式由实际监听地址决定：仅绑定回环地址（如 `127.0.0.1`、`::1`）时，可在本机直接进入管理台，无需注册或登录；请求主机名、来源和连接地址仍须符合本地访问条件。绑定非本地地址时，即使从本机访问也需要登录令牌（Token）。

程序优先读取可执行文件旁的 `token.txt`，否则读取配置目录中的同名文件。该文件保存 SHA-256 原始 32 字节哈希，可直接复制原版文件，不能手写明文令牌。文件缺失时，仅在非本地模式自动生成 12 位令牌，并在控制台显示一次；后续重启继续使用原令牌，首次凭据输出不受日志级别过滤。

忘记令牌时，停止程序并删除 `token.txt`，再重启进入非本地模式。更换或删除令牌文件后，旧会话失效。旧版 CE 用户名、密码登录已由令牌登录替代，旧 `Security.ApiKey` 和 `EASYBOT_API_KEY` 不再参与认证。

非本地管理 API 使用登录后取得的会话 Cookie；Cookie 设置为 `HttpOnly`、`SameSite=Strict`，有效期为 7 天。每个远端 IP 在 5 分钟内最多尝试登录 10 次，注销和更换令牌会使对应旧会话失效。API 不在 JSON 响应中返回密码哈希或会话令牌；健康检查和静态页面仍可公开访问。

## 升级与迁移

升级前停止程序，备份并保留 `appsettings.json`、`token.txt` 与 `appdata/`，再替换程序及页面资源，避免用包内模板覆盖现有配置。

可将原版 `appsettings.json` 和 `token.txt` 复制到 CE 程序旁，已有字段及端口按原值读取，缺失的 CE 配置项使用默认值。同时运行两套程序时应避免端口冲突。复制启动配置不会导入原版数据库、适配器或插件数据，原版数据库不能直接替换 CE 数据库。

## 已实现与待验证范围

管理台和持久化 API 已覆盖适配器、服务器、玩家、账号、群组、允许与拒绝名单、命令定义、AI 服务配置、图片模板目录、JavaScript 插件目录和实例设置。配置数据通过统一记录表保存，启动时执行带版本的数据库迁移；存在配置入口不代表相应运行时已经实现。

OneBot 11 是当前唯一可运行的适配器，使用正向 WebSocket，支持 Bearer 认证、身份握手、群聊及私聊事件归一化、消息发送和群列表调用、生命周期控制、状态及能力查询、有限退避重连。程序启动时恢复已启用的配置，API 不返回适配器凭据，编辑时留空会保留已保存的令牌。不支持的平台类型会被拒绝，避免保存成可运行配置。

运行日志保留有界内存缓冲，并通过有界异步队列写入 `runtime_logs`。`/api/logs` 支持日志级别、组件、UTC 时间范围、关键词和分页筛选；`/api/logs/status` 提供保留策略和写入失败计数。日志页面可设置持久化开关、保留天数和最大条数，每小时清理一次。日志会遮蔽常见令牌、密码、数据库连接地址及验证码，默认不记录消息正文。

以下能力仍待完成或验证：

- **真实环境验证**：OneBot 11 已通过协议模拟器的认证、握手、事件接收、发送和重连测试；真实账号及平台限流行为尚未验证。
- **尚未实现**：Milky、Telegram、OpenQQ 适配器运行时，Minecraft 桥接及配套插件，消息转发和账号绑定，JavaScript 插件执行、AI 对话、浏览器图片渲染、模板市场、PlaceholderAPI 与统计求值、平台安装器及 Docker 打包。
- **参考边界**：原版 NativeAOT 可执行文件无法通过现有反编译资料可靠重建，独立的 EasyBot-Recovered 仓库仅作为管理 API、模块名称和行为的对照依据。

## 测试与协作

从 `dev/` 目录执行：

```powershell
go test ./...
go vet ./...
cd web
npm run build
```

外部数据库测试使用 `EASYBOT_TEST_DATABASE_DSN` 指定专用空测试库，再执行 `go test -count=1 -vet=off ./internal/store`。测试会创建用户和记录，不得指向生产数据库；未设置该变量时，外部数据库测试会跳过。当前 CI 验证目标为 MySQL 8.4.11、MySQL 9.6.0 和 PostgreSQL 18.6，具体版本以对应集成测试通过为准。

贡献要求见 [贡献规范](../CONTRIBUTING.md) 和 [协作约定](../AGENTS.md)。建议使用中文说明 PR 和提交；中文 CI 仅提醒，不阻断合并。AI 辅助贡献须披露工具及用途，并由贡献者人工审阅和验证，相关要求不纳入自动检测。
