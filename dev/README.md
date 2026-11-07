# EasyBot development rebuild

Development rebuild using Go for the HTTP/API service and Vue 3, TypeScript, Vite, and Tailwind CSS v4 for the web console. The service uses SQLite by default and accepts MySQL or PostgreSQL URLs through `EASYBOT_DATABASE`.

## Run locally

Requirements: Go 1.24+ and Node.js 20.19+.

从仓库根目录执行 `node build.mjs`，自动安装前端依赖、构建页面、编译程序并生成 `release` 下的便携压缩包。普通用户解压包后直接运行程序，无需安装开发环境。以下命令用于手动开发调试。

```powershell
cd web
npm ci
npm run build
cd ..
$env:EASYBOT_CONFIG = Join-Path (Get-Location) 'appsettings.json'
go run ./cmd/easybot
```

Open `http://127.0.0.1:5001`. The Go process serves both the API and the built UI from `web/dist`.

正常运行只使用 WebUI/API 5001 和 Bridge 26991 两个端口。前端执行 `npm run build` 后由 Go 直接提供页面，不启动 Vite。需要热更新时才在 `web` 目录另开终端执行 `npm run dev`，它将 `/api` 代理到 5001，其临时端口不是产品部署端口。Bridge 当前仅保留端口约定，尚无监听器、握手或协议兼容实现。不得把 WebUI/API 或 OneBot 适配器端口当作 Bridge 端口。

### Windows executable

发布便携包包含 `easybotce.exe`、`appsettings.json`、`web/dist`、许可证和使用说明。解压到可写目录后双击 exe，浏览器打开配置中的地址，默认 `http://127.0.0.1:5001`，保持控制台窗口打开。保留包内目录结构，数据库自动创建在程序旁的 `appdata` 中；不需要额外启动脚本。开发构建 `dev/bin/easybotce.exe` 默认读取 `dev/appsettings.json`；exe 旁已有配置时优先读取。

## appsettings.json

可直接复制原版配置到 exe 旁；读取后不会重写，CE 默认值只补充缺失的配置项。支持原版的 `Kestrel.Endpoints`（一个 HTTP/HTTPS 端点）、`ServerOptions`、`Serilog.MinimumLevel`（字符串或带 Default 的对象）和 `AllowedHosts`。Bridge 的配置会读取并显示，但当前不建立桥接监听；HeartbeatInterval 保留给桥接运行时。当前配置文件使用标准 JSON，可带 UTF-8 BOM。

CE 扩展项：`Database.Url` 设置 MySQL/PostgreSQL DSN，`Database.SQLitePath` 设置 SQLite 文件。JSON 中的相对文件路径基于配置文件目录；环境变量中的相对路径仍基于启动目录。原版配置没有 Database 扩展项时，开发用 bin 布局继续使用父目录的 appdata。登录凭据统一使用 token.txt；旧 Security.ApiKey 和 EASYBOT_API_KEY 不再参与认证。

HTTPS 配置示例（PEM 文件，相对路径基于配置文件）：

```json
{
  "Kestrel": {
    "Endpoints": {
      "web_api": {
        "Url": "https://0.0.0.0:5001",
        "Certificate": { "Path": "cert.pem", "KeyPath": "key.pem" }
      }
    }
  }
}
```

原版 token.txt 是 SHA-256 原始 32 字节文件，可直接复制，不能手写明文。CE 优先读取 exe 旁的 token.txt，否则读取配置目录中的同名文件。缺失时仅在非本地模式自动生成 12 位 Token 并在控制台显示一次。更换或删除 Token 文件后重启会清除旧会话；旧 CE 用户名/密码登录由 Token 登录替代。

复制原版启动配置不会导入原版数据库、适配器和插件数据。升级便携包时请保留自己的 appsettings.json、token.txt 与 appdata，不要用包内模板覆盖。

## Database and access

- 默认数据库由 appsettings.json 的 Database.SQLitePath 指定为 `appdata/easybot.db`。位置不依赖前端资源是否存在或启动时的工作目录。
- `EASYBOT_SQLITE_PATH` 可覆盖 JSON 中的路径，相对路径按启动目录解析。`go run` 使用临时可执行文件，因此按上面的命令指定开发配置文件；已有 CE 数据库应设置为其实际路径继续使用。
- MySQL: `EASYBOT_DATABASE=mysql://user:password@host:3306/database`
- PostgreSQL: `EASYBOT_DATABASE=postgres://user:password@host:5432/database?sslmode=disable`
- Database CI is configured for MySQL 8.4.11 (LTS), MySQL 9.6.0 (Innovation), and PostgreSQL 18.6. These versions are validation targets, not a support claim until the corresponding CI runs pass.
- Listen address: `EASYBOT_ADDR=127.0.0.1:5001`
- 本地与非本地监听均可使用 HTTP，不需要独立 API Key。非本地管理 API 通过 Token 登录后获得的会话 Cookie 访问。选择 HTTPS 时必须提供有效证书与私钥；可以使用反向代理终止 TLS。公网传输凭据建议使用 HTTPS。健康检查和静态页面仍公开。
- Windows 双击启动失败时保留独立控制台，显示错误并等待回车关闭。已有终端或无控制台任务不暂停，返回非零退出码；配置错误、数据库错误和端口占用都经过同一失败处理。
- 认证模式按实际监听地址决定：仅绑定回环地址（127.0.0.0/8、::1 等）时，本机直接进入管理台，无需注册或登录；Host、Origin 和连接来源仍需匹配本地访问。绑定非本地地址时，即使从本机访问也需要登录。
- Token 文件保存 SHA-256 哈希，不保存明文；自动生成的 Token 重启不变，首次凭据输出不受日志级别过滤。浏览器会话使用 HttpOnly、SameSite=Strict Cookie，有效期 7 天。注销和更换 Token 都会使对应旧会话失效。
- Login is rate-limited to 10 attempts per remote IP in each 5-minute window. The API never returns password hashes or session tokens in JSON responses.

## Current implementation

The dashboard and persistent management API cover adapter configurations, server/player/account/group records, allow/deny lists, command definitions, AI provider settings, image-template and JavaScript-plugin catalogs, and instance settings. CRUD data is stored in one typed record table behind versioned startup migrations; adapters are exposed at the compatibility routes `/api/adapter/*`. The first active adapter runtime is OneBot 11 over forward WebSocket, with Bearer authentication, identity handshake, group/private message event normalization, send/group-list calls, lifecycle controls, status and capabilities APIs, and bounded reconnect backoff. Enabled OneBot 11 configurations are restored at startup. Adapter credentials are omitted from adapter API responses and blank edits preserve a saved token. Runtime events are kept in a bounded in-memory tail and written through a bounded asynchronous queue to the `runtime_logs` table. The authenticated `/api/logs` endpoint supports level, component, UTC time range, keyword, and offset pagination filters; `/api/logs/status` reports the current retention policy and write failure counters. The log page controls persistent storage, retention days, and the maximum row count; cleanup runs hourly. Log messages redact common token, password, DSN, and verification-code forms; message bodies are not included by default. CRUD configuration does not imply the corresponding runtime is installed.

Real OneBot 11 account verification, Milky, Telegram and OpenQQ runtimes, game-server Bridge plugins, message forwarding and account-binding workflows, JavaScript plugin execution, AI chat execution, browser-based image rendering/template marketplace, PlaceholderAPI/statistics evaluation, and platform installers/Docker packaging remain outstanding. The API and UI expose only the implemented OneBot 11 adapter; unsupported platform types are rejected instead of being saved as runnable configurations. The OneBot protocol simulator covers authentication, identity handshake, event receipt, sending, and reconnect; no real test bot is configured, and platform rate-limit behavior has not been validated. The original NativeAOT executable cannot be reliably reconstructed from the available decompilation; the separate EasyBot-Recovered repository is used as behavioral reference for its WebUI API and module names.

## Verify

```powershell
cd web
npm run build
cd ..
go test ./...
```

To run the shared store contract against a disposable external database, set `EASYBOT_TEST_DATABASE_DSN` and run `go test ./...`. The contract creates test users and records and expects an empty database; do not point it at production data. Without the variable, the external database test is skipped.
