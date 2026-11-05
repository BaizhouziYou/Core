package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"easybot/internal/adapter/onebot11"
	"easybot/internal/config"
	"easybot/internal/runtime"
	"easybot/internal/store"
	"easybot/internal/web"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	db, err := store.Open(os.Getenv("EASYBOT_DATABASE"))
	if err != nil {
		logger.Error("database initialization failed", "error", err)
		os.Exit(1)
	}
	defer db.Close()

	address := os.Getenv("EASYBOT_ADDR")
	if address == "" {
		address = "127.0.0.1:26990"
	}
	cert, key := os.Getenv("EASYBOT_TLS_CERT"), os.Getenv("EASYBOT_TLS_KEY")
	if err := config.ValidateListener(address, os.Getenv("EASYBOT_API_KEY"), cert, key); err != nil {
		logger.Error("listener configuration rejected", "address", address, "error", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logs := runtime.NewLogBuffer(500)
	logSettings, err := db.GetLogSettings(context.Background())
	if err != nil {
		logger.Error("log settings initialization failed", "error", err)
		os.Exit(1)
	}
	writer := runtime.NewAsyncLogWriter(db, 2048)
	writer.SetEnabled(logSettings.Persistent)
	logs.SetSink(writer.TryWrite)
	defer func() {
		flushCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		writer.Close(flushCtx)
	}()
	logs.Add("info", "runtime", "EasyBot API started")
	go maintainLogs(db, logger)
	adapters := onebot11.NewManager(ctx, db, func(id string, event onebot11.Event) {
		logs.Add("info", "adapter.onebot11", "OneBot event received: "+event.Type+" (adapter "+id+")")
	})
	if err := adapters.StartEnabled(ctx); err != nil {
		logger.Error("adapter restoration failed", "error", err)
	}
	server := &http.Server{
		Addr:              address,
		Handler:           web.NewWithAdapterManager(db, logger, logs, adapters, writer),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("EasyBot web service listening", "address", address, "database", db.Dialect())
	serveErr := make(chan error, 1)
	go func() {
		if cert != "" {
			serveErr <- server.ListenAndServeTLS(cert, key)
			return
		}
		serveErr <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		adapters.Shutdown(shutdownCtx)
		if err := server.Shutdown(shutdownCtx); err != nil {
			logger.Error("graceful shutdown failed", "error", err)
		}
		logs.Add("info", "runtime", "EasyBot API stopped")
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			logger.Error("http server stopped", "error", err)
			os.Exit(1)
		}
	}
}

func maintainLogs(db *store.Store, logger *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		settings, err := db.GetLogSettings(ctx)
		if err != nil {
			cancel()
			logger.Error("log settings read failed", "error", err)
			continue
		}
		removed, err := db.CleanupLogs(ctx, time.Now().UTC().AddDate(0, 0, -settings.RetentionDays), settings.MaxRows)
		cancel()
		if err != nil {
			logger.Error("log retention cleanup failed", "error", err)
			continue
		}
		if removed > 0 {
			logger.Info("expired runtime logs removed", "count", removed)
		}
	}
}
