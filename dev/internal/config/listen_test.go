package config

import (
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestSettingsFileCompatibilityAndOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "appsettings.json")
	settings, created, err := LoadFile(path)
	if err != nil || !created || settings.Address != "127.0.0.1:5001" || settings.BridgeAddress != "127.0.0.1:26991" {
		t.Fatalf("default settings: %#v, created %v, error %v", settings, created, err)
	}
	legacy := []byte("\xef\xbb\xbf" + `{"Serilog":{"MinimumLevel":{"Default":"Warning"}},"AllowedHosts":"localhost;*.example.test","ServerOptions":{"Host":"127.0.0.1","Port":26990,"HeartbeatInterval":"0.00:02:00"},"Kestrel":{"Endpoints":{"web_api":{"Url":"http://localhost:5000"}}}}`)
	if err := os.WriteFile(path, legacy, 0600); err != nil {
		t.Fatal(err)
	}
	settings, created, err = LoadFile(path)
	if err != nil || created || settings.Address != "localhost:5000" || settings.BridgeAddress != "127.0.0.1:26990" || settings.LogLevel != slog.LevelWarn {
		t.Fatalf("legacy settings: %#v, created %v, error %v", settings, created, err)
	}
	if settings.SQLitePath != filepath.Join(filepath.Dir(path), "appdata", "easybot.db") {
		t.Fatalf("database path: %s", settings.SQLitePath)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(legacy) {
		t.Fatal("existing configuration was rewritten")
	}
	t.Setenv("EASYBOT_ADDR", "127.0.0.2:5003")
	t.Setenv("EASYBOT_SQLITE_PATH", "custom.db")
	settings, _, err = LoadFile(path)
	if err != nil || settings.Address != "127.0.0.2:5003" || settings.SQLitePath != "custom.db" {
		t.Fatalf("environment overrides: %#v, %v", settings, err)
	}
}

func TestSettingsRejectInvalidEndpoints(t *testing.T) {
	for _, content := range []string{
		`{"Kestrel":{"Endpoints":{}}}`,
		`{"Kestrel":{"Endpoints":{"a":{"Url":"http://localhost:5001"},"b":{"Url":"http://localhost:5002"}}}}`,
		`{"Kestrel":{"Endpoints":{"web_api":{"Url":"file:///tmp/server"}}}}`,
		`{"Kestrel":{"Endpoints":{"web_api":{"Url":"https://localhost:5001"}}}}`,
		`{"ServerOptions":{"Port":70000}}`,
	} {
		path := filepath.Join(t.TempDir(), "appsettings.json")
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := LoadFile(path); err == nil {
			t.Fatalf("accepted invalid configuration: %s", content)
		}
	}
}

func TestAllowedHost(t *testing.T) {
	for _, test := range []struct {
		host, patterns string
		allowed        bool
	}{
		{"localhost:5001", "localhost;127.0.0.1", true},
		{"[::1]:5001", "[::1]", true},
		{"app.example.test:5001", "*.example.test", true},
		{"example.test.evil:5001", "*.example.test", false},
		{"evil.test:5001", "localhost", false},
	} {
		if actual := AllowedHost(test.host, test.patterns); actual != test.allowed {
			t.Errorf("AllowedHost(%q, %q) = %v", test.host, test.patterns, actual)
		}
	}
}

func TestValidateListener(t *testing.T) {
	tests := []struct {
		name    string
		address string
		cert    string
		tlsKey  string
		wantErr bool
	}{
		{name: "default loopback", address: "127.0.0.1:5001"},
		{name: "loopback TLS pair", address: "[::1]:5001", cert: "cert.pem", tlsKey: "key.pem"},
		{name: "external HTTP without API key or TLS", address: "0.0.0.0:5001"},
		{name: "LAN HTTP", address: "192.0.2.1:5001"},
		{name: "external HTTPS", address: "0.0.0.0:5001", cert: "cert.pem", tlsKey: "key.pem"},
		{name: "partial TLS pair", address: "127.0.0.1:5001", cert: "cert.pem", wantErr: true},
		{name: "invalid address", address: "localhost", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateListener(tt.address, tt.cert, tt.tlsKey)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateListener() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}
