# CE 管理台

Vue 3 + TypeScript + Tailwind CSS v4，使用 Vite 构建。依赖 Node.js 20.19+。

上述依赖仅用于源码开发。普通用户下载便携包，解压后直接双击 exe 即可。开发者在仓库根目录执行 `node build.mjs` 可一次完成构建与打包。

日常运行在本目录执行 `npm ci`、`npm run build`，然后按 `dev/README.md` 的说明启动 Go 服务（`go run` 时通过 EASYBOT_CONFIG 指定开发配置），或双击已编译的 exe，访问 appsettings.json 中配置的地址，默认 `http://127.0.0.1:5001`。页面和管理 API 由同一个 Go 进程提供，无需单独启动前端服务器。

需要开发热更新时才执行 `npm run dev`，另开终端运行 Go 服务，使用 Vite 输出的临时地址；Vite 将 `/api` 代理到 5001。前端临时端口不属于产品的部署端口。

Bridge 使用独立的 26991 端口；运行时尚未实现。贡献要求见根目录 CONTRIBUTING.md，所有后续提交使用中文。
