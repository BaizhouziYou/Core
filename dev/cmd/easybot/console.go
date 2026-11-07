package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
)

type consoleHandler struct {
	minimum slog.Level
	mu      *sync.Mutex
	attrs   []slog.Attr
	group   string
}

var useConsoleColors = os.Getenv("NO_COLOR") == "" && enableConsoleColors()

func consoleLogger(levels ...slog.Level) *slog.Logger {
	minimum := slog.LevelInfo
	if len(levels) != 0 {
		minimum = levels[0]
	}
	return slog.New(&consoleHandler{mu: &sync.Mutex{}, minimum: minimum})
}

func (h *consoleHandler) Enabled(_ context.Context, level slog.Level) bool {
	return level >= h.minimum
}

func (h *consoleHandler) Handle(_ context.Context, record slog.Record) error {
	var line strings.Builder
	level, color := "INFO", "97"
	switch {
	case record.Level >= slog.LevelError+4:
		level, color = "FATL", "97;41"
	case record.Level >= slog.LevelError:
		level, color = "EROR", "97;41"
	case record.Level >= slog.LevelWarn:
		level, color = "WARN", "93"
	case record.Level < slog.LevelInfo:
		level, color = "DBUG", "37"
	}
	fmt.Fprint(&line, consoleColor("[", "90"), consoleColor(record.Time.Format("15:04:05"), "37"), consoleColor("][", "90"), consoleColor(level, color), consoleColor("]: ", "90"), consoleColor(record.Message, "97"))
	appendAttr := func(attr slog.Attr) bool {
		// 原版模板仅显示消息和异常，不重复追加结构化属性。
		if attr.Key == "error" {
			fmt.Fprint(&line, "\n", consoleColor(attr.Value.Resolve().String(), "97;41"))
		}
		return true
	}
	for _, attr := range h.attrs {
		appendAttr(attr)
	}
	record.Attrs(appendAttr)
	h.mu.Lock()
	defer h.mu.Unlock()
	_, err := fmt.Fprintln(os.Stdout, line.String())
	return err
}

func (h *consoleHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := *h
	next.attrs = append(append([]slog.Attr(nil), h.attrs...), attrs...)
	return &next
}

func (h *consoleHandler) WithGroup(name string) slog.Handler {
	next := *h
	if name != "" {
		next.group += name + "."
	}
	return &next
}

func consoleColor(text, color string) string {
	if !useConsoleColors {
		return text
	}
	return "\x1b[" + color + "m" + text + "\x1b[0m"
}

func printBanner(logger *slog.Logger) {
	// 使用与原版 FigletText 相同的字形，保留 CE 的产品名称。
	logo := "  _____                         _               _      ____   _____ \n" +
		" | ____|   __ _   ___   _   _  | |__     ___   | |_   / ___| | ____|\n" +
		" |  _|    / _` | / __| | | | | | '_ \\   / _ \\  | __| | |     |  _|  \n" +
		" | |___  | (_| | \\__ \\ | |_| | | |_) | | (_) | | |_  | |___  | |___ \n" +
		" |_____|  \\__,_| |___/  \\__, | |_.__/   \\___/   \\__|  \\____| |_____|\n" +
		"                        |___/                                       "
	fmt.Fprintln(os.Stdout, consoleColor(logo, "96"))
	logger.Info("EasybotCE v0.1.0-dev")
}

func printStartup(logger *slog.Logger, address, boundAddress string, tls, localOnly bool, token string) {
	host, _, _ := net.SplitHostPort(address)
	_, port, _ := net.SplitHostPort(boundAddress)
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	scheme := "http"
	if tls {
		scheme = "https"
	}
	logger.Info("[WebUI] 管理地址: " + scheme + "://" + net.JoinHostPort(host, port))
	logger.Info("[WebUI] 监听地址: " + boundAddress)
	if localOnly {
		logger.Info("[WebUI] 本机免登录，直接进入管理台")
	} else {
		logger.Info("[WebUI] 已启用 Token 登录")
		if token != "" {
			// 首次凭据必须可见，不受用户配置的日志级别过滤。
			credentials := consoleLogger()
			credentials.Info("[WebUI] 登录 Token: " + token)
			credentials.Warn("[WebUI] 登录 Token 仅显示一次，请妥善保存")
		} else {
			logger.Info("[WebUI] 使用 token.txt 对应的登录 Token")
		}
	}
	logger.Info("[WebUI] 服务已启动，按 Ctrl+C 停止")
}
