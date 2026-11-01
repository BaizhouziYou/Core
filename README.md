# EasyBotCE Core

EasybotCE 的主程序。当前重建项目由 Go HTTP API 和 Vue 3 管理台组成，使用 SQLite 默认存储，并提供 MySQL/PostgreSQL 存储实现与集成测试目标。

## 项目结构

- `dev/`: Go 服务、Vue 3 + TypeScript + Tailwind CSS v4 管理台和开发说明。
- `PLAN.md`: 原始重建规划。
- `REMAINING_PLAN.md`: 后续版本产品范围、验收标准和工作包。
- `.github/workflows/`: 本地构建及数据库集成测试。

当前运行时已覆盖管理员初始化/认证、管理台概览、基础配置 CRUD 和内存运行日志。OneBot 11 已选为首发社交平台；协议连接、Minecraft 网络桥接和消息同步仍在开发中。MySQL/PostgreSQL 是否正式支持以对应 CI 集成测试通过为准。

## License and attribution

This repository is licensed under [CC BY-NC 4.0 (`CC-BY-NC-4.0`)](https://creativecommons.org/licenses/by-nc/4.0/). Commercial use is not permitted by that license. When sharing or adapting this project, credit EasybotCE and link to this repository and the license, identify changes, and retain existing author, copyright, and license notices. See [`AUTHORS.md`](AUTHORS.md).

## 开发

环境要求：Go 1.24+、Node.js 20.19+。

```powershell
cd dev/web
npm ci
npm run dev
```

另开终端启动 API：

```powershell
cd dev
go run ./cmd/easybot
```

运行方式、配置项和安全说明见 [`dev/README.md`](dev/README.md)。
