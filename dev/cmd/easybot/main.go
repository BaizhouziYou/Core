package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

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
	logs := runtime.NewLogBuffer(500)
	logs.Add("info", "runtime", "EasyBot API started")
	server := &http.Server{
		Addr:              address,
		Handler:           web.New(db, logger, logs),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       60 * time.Second,
	}
	logger.Info("EasyBot web service listening", "address", address, "database", db.Dialect())
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
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
