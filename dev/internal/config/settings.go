package config

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

//go:embed appsettings.example.json
var defaultSettings []byte

type settingsFile struct {
	Serilog       struct{ MinimumLevel json.RawMessage }
	AllowedHosts  string
	ServerOptions struct {
		Host              string
		Port              int
		HeartbeatInterval string
	}
	Kestrel struct {
		Endpoints map[string]struct {
			URL         string
			Certificate struct{ Path, KeyPath, Password string }
		}
	}
	Database struct{ URL, SQLitePath string }
}

type Settings struct {
	Path, Address, Certificate, PrivateKey, Database, SQLitePath, AllowedHosts string
	BridgeAddress                                                              string
	LogLevel                                                                   slog.Level
}

func BaseDirectory() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(executable)
	if filepath.Base(dir) == "bin" {
		dir = filepath.Dir(dir)
	}
	return dir, nil
}

func Load() (Settings, bool, error) {
	path := os.Getenv("EASYBOT_CONFIG")
	if path == "" {
		dir, err := BaseDirectory()
		if err != nil {
			return Settings{}, false, err
		}
		path = filepath.Join(dir, "appsettings.json")
		executable, err := os.Executable()
		if err != nil {
			return Settings{}, false, err
		}
		adjacent := filepath.Join(filepath.Dir(executable), "appsettings.json")
		if _, err := os.Stat(adjacent); err == nil {
			path = adjacent
		} else if !os.IsNotExist(err) {
			return Settings{}, false, err
		}
	}
	return LoadFile(path)
}

func LoadFile(path string) (Settings, bool, error) {
	path, err := filepath.Abs(path)
	if err != nil {
		return Settings{}, false, err
	}
	created := false
	if file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600); err == nil {
		_, writeErr := file.Write(defaultSettings)
		closeErr := file.Close()
		if writeErr != nil {
			return Settings{}, false, writeErr
		}
		if closeErr != nil {
			return Settings{}, false, closeErr
		}
		created = true
	} else if !os.IsExist(err) {
		return Settings{}, false, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Settings{}, created, err
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	var overrides struct {
		Kestrel  struct{ Endpoints json.RawMessage }
		Database struct{ SQLitePath *string }
	}
	if err := json.Unmarshal(data, &overrides); err != nil {
		return Settings{}, created, fmt.Errorf("appsettings.json 格式错误: %w", err)
	}
	var file settingsFile
	if err := json.Unmarshal(defaultSettings, &file); err != nil {
		return Settings{}, created, err
	}
	// 旧配置没有 CE 扩展项时使用默认值；已有文件始终只读，不重写。
	// Endpoints 必须整体替换，不能与默认 web_api 合并出第二个监听器。
	if len(overrides.Kestrel.Endpoints) != 0 {
		file.Kestrel.Endpoints = nil
	}
	if err := json.Unmarshal(data, &file); err != nil {
		return Settings{}, created, fmt.Errorf("appsettings.json 格式错误: %w", err)
	}
	if len(file.Kestrel.Endpoints) != 1 {
		return Settings{}, created, fmt.Errorf("Kestrel.Endpoints 必须配置一个 WebUI 端点")
	}
	settings := Settings{Path: path, AllowedHosts: file.AllowedHosts}
	base := filepath.Dir(path)
	resolve := func(value string) string {
		if value == "" || filepath.IsAbs(value) {
			return value
		}
		return filepath.Join(base, value)
	}
	var https bool
	for _, endpoint := range file.Kestrel.Endpoints {
		parsed, err := url.Parse(endpoint.URL)
		if err != nil || parsed.Hostname() == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.User != nil || (parsed.Path != "" && parsed.Path != "/") || parsed.RawQuery != "" || parsed.Fragment != "" {
			return Settings{}, created, fmt.Errorf("Kestrel 端点 Url 必须是 http:// 或 https:// 监听地址")
		}
		host, port := parsed.Hostname(), parsed.Port()
		if host == "*" || host == "+" {
			host = "0.0.0.0"
		}
		if port == "" {
			if parsed.Scheme == "https" {
				port = "443"
			} else {
				port = "80"
			}
		}
		settings.Address = net.JoinHostPort(host, port)
		settings.Certificate, settings.PrivateKey = resolve(endpoint.Certificate.Path), resolve(endpoint.Certificate.KeyPath)
		if endpoint.Certificate.Password != "" {
			return Settings{}, created, fmt.Errorf("证书请使用 PEM Path 和 KeyPath；不支持带密码的 PFX")
		}
		https = parsed.Scheme == "https"
	}
	if file.ServerOptions.Port < 1 || file.ServerOptions.Port > 65535 || file.ServerOptions.Host == "" {
		return Settings{}, created, fmt.Errorf("ServerOptions 必须配置有效的 Host 和 1 到 65535 的 Port")
	}
	settings.BridgeAddress = net.JoinHostPort(file.ServerOptions.Host, fmt.Sprint(file.ServerOptions.Port))
	settings.Database, settings.SQLitePath = file.Database.URL, resolve(file.Database.SQLitePath)
	// 将旧配置复制到开发用 bin 旁边时，保持原有 CE 数据目录。
	if overrides.Database.SQLitePath == nil && filepath.Base(base) == "bin" {
		settings.SQLitePath = filepath.Join(filepath.Dir(base), "appdata", "easybot.db")
	}
	for name, target := range map[string]*string{
		"EASYBOT_ADDR": &settings.Address, "EASYBOT_DATABASE": &settings.Database,
		"EASYBOT_SQLITE_PATH": &settings.SQLitePath,
		"EASYBOT_TLS_CERT":    &settings.Certificate, "EASYBOT_TLS_KEY": &settings.PrivateKey,
		"EASYBOT_ALLOWED_HOSTS": &settings.AllowedHosts,
	} {
		if value, ok := os.LookupEnv(name); ok {
			*target = value
		}
	}
	if settings.Address == "" {
		settings.Address = "127.0.0.1:5001"
	}
	if settings.SQLitePath == "" {
		settings.SQLitePath = resolve("appdata/easybot.db")
	}
	if https && settings.Certificate == "" {
		return Settings{}, created, fmt.Errorf("HTTPS 端点需要 Certificate.Path 和 Certificate.KeyPath，或对应 TLS 环境变量")
	}
	var minimum string
	if err := json.Unmarshal(file.Serilog.MinimumLevel, &minimum); err != nil {
		var levels struct{ Default string }
		if err := json.Unmarshal(file.Serilog.MinimumLevel, &levels); err != nil {
			return Settings{}, created, fmt.Errorf("Serilog.MinimumLevel 格式错误")
		}
		minimum = levels.Default
	}
	switch strings.ToLower(minimum) {
	case "verbose", "debug":
		settings.LogLevel = slog.LevelDebug
	case "", "information", "info":
		settings.LogLevel = slog.LevelInfo
	case "warning", "warn":
		settings.LogLevel = slog.LevelWarn
	case "error":
		settings.LogLevel = slog.LevelError
	case "fatal":
		settings.LogLevel = slog.LevelError + 4
	default:
		return Settings{}, created, fmt.Errorf("不支持的 Serilog 日志级别: %s", minimum)
	}
	return settings, created, nil
}

func AllowedHost(host, patterns string) bool {
	if parsed, _, err := net.SplitHostPort(host); err == nil {
		host = parsed
	}
	host = strings.ToLower(strings.TrimSuffix(strings.Trim(host, "[]"), "."))
	if patterns == "" {
		return true
	}
	for _, pattern := range strings.Split(strings.ToLower(patterns), ";") {
		pattern = strings.Trim(strings.TrimSpace(pattern), "[]")
		if pattern == "*" || host == pattern || (strings.HasPrefix(pattern, "*.") && strings.HasSuffix(host, pattern[1:])) {
			return true
		}
	}
	return false
}
