# EasybotCE 网页管理台

管理台使用 Vue 3、TypeScript 和 Tailwind CSS v4，由 Vite 构建。开发环境要求 Node.js 20.19+。

普通用户直接运行便携包中的程序即可，无需安装前端依赖或额外启动网页服务。下载与运行方式见 [项目首页](../../README.md)。

## 构建页面

在本目录 `dev/web/` 执行：

```powershell
npm ci
npm run build
```

页面输出到 `dist/`。Go 进程同时提供管理 API 和构建后的页面，默认地址为 `http://127.0.0.1:5001`。启动 Go 服务的命令及 `EASYBOT_CONFIG` 配置见 [开发说明](../README.md)。

需要完整便携包时，在仓库根目录执行 `node build.mjs`，脚本会完成依赖安装、前端构建、Go 编译及打包。

## 热更新开发

保持 Go 服务运行，在本目录另开终端执行：

```powershell
npm run dev
```

打开 Vite 输出的开发地址。`/api` 请求会代理到 `http://127.0.0.1:5001`；这个临时端口仅用于开发，不属于产品部署端口。

Minecraft 桥接（Bridge）预留 `26991` 端口，运行时尚未实现，当前不监听。管理台页面与 OneBot 适配器均不能替代桥接运行时。

## 贡献要求

请阅读 [贡献规范](../../CONTRIBUTING.md) 和 [协作约定](../../AGENTS.md)。建议使用中文说明 PR 和提交，中文 CI 仅提醒，不阻断合并；AI 辅助贡献须披露工具及用途，由贡献者人工审阅和验证，相关要求不纳入自动检测。
