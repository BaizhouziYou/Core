package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"easybot/internal/adapter/onebot11"
	"easybot/internal/auth"
	"easybot/internal/config"
	"easybot/internal/runtime"
	"easybot/internal/store"
	"easybot/internal/web"
)

func main() {
	code := run()
	if code != 0 {
		waitAfterFailure()
		os.Exit(code)
	}
}

func run() (code int) {
	defer func() {
		if recovered := recover(); recovered != nil {
			consoleLogger().Error("程序异常退出", "error", fmt.Errorf("%v", recovered))
			code = 1
		}
	}()
	logger := consoleLogger()
	started := time.Now()
	settings, created, err := config.Load()
	if err != nil {
		consoleLogger().Error("启动配置读取失败", "error", err)
		return 1
	}
	logger = consoleLogger(settings.LogLevel)
	printBanner(logger)
	if created {
		logger.Info("[配置] 检测到首次启动，已生成默认配置文件: " + settings.Path)
	} else {
		logger.Info("[配置] 已加载配置文件: " + settings.Path)
	}
	for name, value := range map[string]string{
		"EASYBOT_ADDR":          settings.Address,
		"EASYBOT_SQLITE_PATH":   settings.SQLitePath,
		"EASYBOT_ALLOWED_HOSTS": settings.AllowedHosts,
	} {
		if err := os.Setenv(name, value); err != nil {
			consoleLogger().Error("配置应用失败", "error", err)
			return 1
		}
	}
	logger.Info("[数据库] 正在初始化数据库...")
	db, err := store.Open(settings.Database)
	if err != nil {
		consoleLogger().Error("数据库初始化失败", "error", err)
		return 1
	}
	defer db.Close()
	logger.Info("[数据库] 初始化完成，已连接 " + db.Dialect())

	address := settings.Address
	cert, key := settings.Certificate, settings.PrivateKey
	if err := config.ValidateListener(address, cert, key); err != nil {
		consoleLogger().Error("监听配置无效: "+address, "error", err)
		return 1
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		consoleLogger().Error("服务监听失败", "address", address, "error", err)
		return 1
	}
	defer listener.Close()
	localOnly := config.IsLoopbackListener(listener.Addr().String())
	logger.Info("[认证] 正在初始化访问控制...")
	tokenPath := filepath.Join(filepath.Dir(settings.Path), "token.txt")
	if executable, err := os.Executable(); err == nil {
		adjacent := filepath.Join(filepath.Dir(executable), "token.txt")
		if _, err := os.Stat(adjacent); err == nil {
			tokenPath = adjacent
		}
	}
	loginToken, initialToken, err := auth.Load(tokenPath, !localOnly)
	if err != nil {
		consoleLogger().Error("登录 Token 读取失败", "error", err)
		return 1
	}
	if loginToken != nil {
		identity, err := db.EnsureTokenUser(context.Background(), loginToken.Fingerprint())
		if err != nil {
			consoleLogger().Error("登录会话初始化失败", "error", err)
			return 1
		}
		loginToken.UserID = identity.ID
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	logger.Info("[认证] 访问控制初始化完成")
	defer stop()
	logger.Info("[日志] 正在初始化运行日志...")
	logs := runtime.NewLogBuffer(500)
	logSettings, err := db.GetLogSettings(context.Background())
	if err != nil {
		consoleLogger().Error("日志设置初始化失败", "error", err)
		return 1
	}
	writer := runtime.NewAsyncLogWriter(db, 2048)
	writer.SetEnabled(logSettings.Persistent)
	logs.SetSink(writer.TryWrite)
	if logSettings.Persistent {
		logger.Info(fmt.Sprintf("[日志] 持久化已启用，保留 %d 天，最多 %d 条", logSettings.RetentionDays, logSettings.MaxRows))
	} else {
		logger.Info("[日志] 持久化已关闭，使用内存日志")
	}
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		writer.Close(flushCtx)
	}()
	logs.Add("info", "runtime", "EasybotCE 正在初始化运行模块")
	go maintainLogs(db, logger)
	logger.Info("[StartupAdapterService] 已启用适配器掉线自动重连")
	logger.Info("[AdapterManager] 正在初始化适配器管理器...")
	adapterConfigs, listErr := db.List(ctx, "adapters")
	if listErr != nil {
		logger.Error("[AdapterManager] 读取适配器配置失败", "error", listErr)
	} else {
		logger.Info(fmt.Sprintf("[AdapterManager] 发现 %d 个适配器配置", len(adapterConfigs)))
	}
	adapters := onebot11.NewManager(ctx, db, func(id string, event onebot11.Event) {
		logs.Add("info", "adapter.onebot11", "OneBot event received: "+event.Type+" (adapter "+id+")")
	})
	if err := adapters.StartEnabled(ctx); err != nil {
		logger.Error("[AdapterManager] 适配器初始化失败", "error", err)
	} else {
		for _, item := range adapterConfigs {
			if !item.Enabled {
				logger.Info(fmt.Sprintf("[AdapterManager] 适配器 %q 未启用，跳过加载", item.Name))
				continue
			}
			state, err := adapters.Status(item.ID)
			if err != nil {
				logger.Warn(fmt.Sprintf("[AdapterManager] 适配器 %q 状态读取失败", item.Name), "error", err)
			} else if state.State == onebot11.StateFailed {
				logger.Warn(fmt.Sprintf("[AdapterManager] 适配器 %q 加载失败: %s", item.Name, runtime.Redact(state.LastError)))
			} else if state.State == onebot11.StateStopped {
				logger.Warn(fmt.Sprintf("[AdapterManager] 适配器 %q 未加载，当前仅支持 OneBot 11", item.Name))
			} else {
				logger.Info(fmt.Sprintf("[AdapterManager] 已加载适配器 %q，连接状态以运行时结果为准", item.Name))
			}
		}
		logger.Info(fmt.Sprintf("[AdapterManager] 初始化完成，已加载 %d 个适配器实例", adapters.RuntimeCount()))
	}
	logger.Warn("[桥接] 模块尚未实现，未监听配置地址 " + settings.BridgeAddress)
	logger.Info("[插件] 脚本执行模块尚未实现，跳过加载")
	logger.Info("[图片模板] 浏览器渲染模块尚未实现，跳过加载")
	logger.Info("[AI] 对话执行模块尚未实现，跳过加载")
	logger.Info("[WebUI] 正在初始化管理服务...")
	server := &http.Server{
		Addr:              address,
		Handler:           web.NewForListener(db, logger, logs, adapters, listener.Addr().String(), loginToken, writer),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("[WebUI] 管理路由初始化完成")
	printStartup(logger, address, listener.Addr().String(), cert != "", localOnly, initialToken)
	logger.Info(fmt.Sprintf("[启动] 初始化完成，耗时 %d ms", time.Since(started).Milliseconds()))
	logs.Add("info", "runtime", "EasybotCE 管理服务初始化完成")
	serveErr := make(chan error, 1)
	go func() {
		if cert != "" {
			serveErr <- server.ServeTLS(listener, cert, key)
			return
		}
		serveErr <- server.Serve(listener)
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		adapters.Shutdown(shutdownCtx)
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("服务关闭失败", "error", err)
		}
		logs.Add("info", "runtime", "EasyBot API stopped")
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			consoleLogger().Error("管理服务已停止", "error", err)
			return 1
		}
	}
	return 0
}

func maintainLogs(db *store.Store, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		settings, err := db.GetLogSettings(ctx)
		if err != nil {
			cancel()
			logger.Error("日志设置读取失败", "error", err)
			continue
		}
		removed, err := db.CleanupLogs(ctx, time.Now().UTC().AddDate(0, 0, -settings.RetentionDays), settings.MaxRows)
		cancel()
		if err != nil {
			logger.Error("历史日志清理失败", "error", err)
			continue
		}
		if removed > 0 {
			logger.Info(fmt.Sprintf("已清理 %d 条过期日志", removed))
		}
	}
}
