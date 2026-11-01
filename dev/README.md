# EasyBot development rebuild

Development rebuild using Go for the HTTP/API service and Vue 3, TypeScript, Vite, and Tailwind CSS v4 for the web console. The service uses SQLite by default and accepts MySQL or PostgreSQL URLs through `EASYBOT_DATABASE`.

## Run locally

Requirements: Go 1.24+ and Node.js 20.19+.

```powershell
cd web
npm install
npm run dev
```

In another terminal:

```powershell
$env:PATH = "C:\Program Files\Go\bin;$env:PATH"
go run ./cmd/easybot
```

The API listens on `http://127.0.0.1:26990`; Vite serves the UI on its displayed local port and proxies `/api` requests to the Go service. The production UI is served by the Go process from `web/dist` after `npm run build`.

## Database and access

- Default: `EASYBOT_SQLITE_PATH=appdata/easybot.db`
- MySQL: `EASYBOT_DATABASE=mysql://user:password@host:3306/database`
- PostgreSQL: `EASYBOT_DATABASE=postgres://user:password@host:5432/database?sslmode=disable`
- Database CI is configured for MySQL 8.4.11 (LTS), MySQL 9.6.0 (Innovation), and PostgreSQL 18.6. These versions are validation targets, not a support claim until the corresponding CI runs pass.
- Listen address: `EASYBOT_ADDR=127.0.0.1:26990`
- Loopback binds may omit a key. Non-loopback binds require `EASYBOT_API_KEY`, `EASYBOT_TLS_CERT`, and `EASYBOT_TLS_KEY`; use a reverse proxy that forwards to loopback if TLS terminates elsewhere. Health checks and static UI files remain public.
- On a fresh database, open the console from the same machine and create the initial administrator. The one-time setup endpoint only accepts loopback connections. Passwords are bcrypt-hashed; browser sessions use HttpOnly, SameSite=Strict cookies and expire after 12 hours. Management APIs require a valid session or the explicitly configured `EASYBOT_API_KEY` bearer token.
- Login is rate-limited to 10 attempts per remote IP in each 5-minute window. The API never returns password hashes or session tokens in JSON responses.

## Current implementation

The dashboard and persistent management API cover adapter configurations, server/player/account/group records, allow/deny lists, command definitions, AI provider settings, image-template and JavaScript-plugin catalogs, and instance settings. CRUD data is stored in one typed record table behind versioned startup migrations; adapters are exposed at the compatibility routes `/api/adapter/*`. Runtime events are kept in a bounded in-memory tail and written through a bounded asynchronous queue to the `runtime_logs` table. The authenticated `/api/logs` endpoint supports level, component, UTC time range, keyword, and offset pagination filters; `/api/logs/status` reports the current retention policy and write failure counters. The log page controls persistent storage, retention days, and the maximum row count; cleanup runs hourly. Log messages redact common token, password, DSN, and verification-code forms; message bodies are not included by default. CRUD configuration does not imply the corresponding runtime is installed.

Protocol connections (OneBot 11, Milky, Telegram, OpenQQ), game-server Bridge plugins, message forwarding and account-binding workflows, JavaScript plugin execution, AI chat execution, browser-based image rendering/template marketplace, PlaceholderAPI/statistics evaluation, and platform installers/Docker packaging are not implemented yet. Adapter start intentionally returns HTTP 501. The original NativeAOT executable cannot be reliably reconstructed from the available decompilation; `../decompiled` is used as behavioral reference for its WebUI API and module names.

## Verify

```powershell
cd web
npm run build
cd ..
go test ./...
```

To run the shared store contract against a disposable external database, set `EASYBOT_TEST_DATABASE_DSN` and run `go test ./...`. The contract creates test users and records and expects an empty database; do not point it at production data. Without the variable, the external database test is skipped.
