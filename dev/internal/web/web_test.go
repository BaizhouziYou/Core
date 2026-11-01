package web

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"easybot/internal/adapter/onebot11"
	"easybot/internal/runtime"
	"easybot/internal/store"
)

func TestRecordCRUDAndApiKey(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("EASYBOT_API_KEY", "test-key")
	logs := runtime.NewLogBuffer(20)
	handler := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), logs)

	request := httptest.NewRequest(http.MethodGet, "/api/records?kind=servers", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/records", strings.NewReader(`{"kind":"servers","name":"survival","detail":"{}","enabled":true}`))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/records?kind=servers", nil)
	request.Header.Set("Authorization", "Bearer test-key")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "survival") {
		t.Fatalf("list returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/adapter/create", strings.NewReader(`{"adapterType":"Onebot11","displayName":"QQ Bridge","configJson":"{\"url\":\"ws://localhost:3001\"}"}`))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `Onebot11`) {
		t.Fatalf("adapter create returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/logs?limit=10", nil)
	request.Header.Set("Authorization", "Bearer test-key")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "POST /api/records") {
		t.Fatalf("logs returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPut, "/api/logs/settings", strings.NewReader(`{"persistent":false,"retentionDays":45,"maxRows":2000}`))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"retentionDays":45`) {
		t.Fatalf("log settings update returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/logs/status", nil)
	request.Header.Set("Authorization", "Bearer test-key")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"persistent":false`) {
		t.Fatalf("log status returned %d: %s", response.Code, response.Body.String())
	}
}

func TestAdapterCredentialsAreRedactedAndPreservedOnUpdate(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("EASYBOT_API_KEY", "test-key")
	handler := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), runtime.NewLogBuffer(20))
	request := httptest.NewRequest(http.MethodPost, "/api/adapter/create", strings.NewReader(`{"adapterType":"Onebot11","displayName":"Mock","configJson":"{\"url\":\"ws://127.0.0.1:3001/\",\"accessToken\":\"never-return-this\"}"}`))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), "never-return-this") {
		t.Fatalf("create leaked secret or failed: %d %s", response.Code, response.Body.String())
	}
	var created store.Record
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/api/adapter/list", "/api/records?kind=adapters"} {
		request = httptest.NewRequest(http.MethodGet, path, nil)
		request.Header.Set("Authorization", "Bearer test-key")
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "never-return-this") {
			t.Fatalf("%s leaked secret or failed: %d %s", path, response.Code, response.Body.String())
		}
	}
	request = httptest.NewRequest(http.MethodPut, "/api/adapter/"+created.ID, strings.NewReader(`{"displayName":"Mock updated","configJson":"{\"url\":\"ws://127.0.0.1:3002/\",\"accessTokenConfigured\":true}"}`))
	request.Header.Set("Authorization", "Bearer test-key")
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "never-return-this") {
		t.Fatalf("update failed or leaked secret: %d %s", response.Code, response.Body.String())
	}
	items, err := db.List(context.Background(), "adapters")
	if err != nil || len(items) != 1 {
		t.Fatalf("adapter record missing: %v", err)
	}
	var detail struct {
		Config json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal([]byte(items[0].Detail), &detail); err != nil {
		t.Fatal(err)
	}
	config, err := onebot11.ParseConfig(detail.Config)
	if err != nil || config.AccessToken != "never-return-this" || config.WsURL != "ws://127.0.0.1:3002/" {
		t.Fatalf("update failed to preserve the token: %#v %v", config, err)
	}
}

func TestAdapterAPIReportsStoppedAndRejectsUnknownID(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("EASYBOT_API_KEY", "test-key")
	manager := onebot11.NewManager(context.Background(), db, nil)
	defer manager.Shutdown(context.Background())
	handler := NewWithAdapterManager(db, slog.New(slog.NewTextHandler(io.Discard, nil)), runtime.NewLogBuffer(20), manager)
	request := httptest.NewRequest(http.MethodGet, "/api/adapter/missing/status", nil)
	request.Header.Set("Authorization", "Bearer test-key")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing adapter status returned %d: %s", response.Code, response.Body.String())
	}
}

func TestInitialSetupLoginAndSessionAuthorization(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	t.Setenv("EASYBOT_API_KEY", "")
	handler := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), runtime.NewLogBuffer(20))
	request := httptest.NewRequest(http.MethodGet, "/api/setup/status", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"initialized":false`) {
		t.Fatalf("setup status returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/records?kind=servers", nil)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated management request returned %d", response.Code)
	}

	body := `{"username":"admin","password":"correct-horse-battery"}`
	request = httptest.NewRequest(http.MethodPost, "/api/setup/admin", strings.NewReader(body))
	request.RemoteAddr = "192.0.2.4:1234"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("remote initialization returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/setup/admin", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "console.example.com"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forwarded public-host initialization returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/setup/admin", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1:5173"
	request.Header.Set("Origin", "https://console.example.com")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusForbidden {
		t.Fatalf("forwarded public-origin initialization returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/setup/admin", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1:5173"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("initialization returned %d: %s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].Name != sessionCookie || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteStrictMode {
		t.Fatalf("expected session cookie attributes, got %#v", cookies)
	}
	if strings.Contains(response.Body.String(), cookies[0].Value) {
		t.Fatal("session token leaked in response body")
	}

	request = httptest.NewRequest(http.MethodGet, "/api/records?kind=servers", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("authenticated management request returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/setup/admin", strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Host = "127.0.0.1:5173"
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusConflict {
		t.Fatalf("second initialization returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"admin","password":"correct-horse-battery"}`))
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || len(response.Result().Cookies()) != 1 {
		t.Fatalf("login returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/records?kind=servers", nil)
	request.AddCookie(cookies[0])
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked session returned %d", response.Code)
	}
}

func TestLoginRateLimit(t *testing.T) {
	s := &server{loginAttempts: make(map[string]loginAttempt)}
	for i := 0; i < 10; i++ {
		if !s.allowLogin("127.0.0.1:1234") {
			t.Fatalf("attempt %d was unexpectedly rejected", i+1)
		}
	}
	if s.allowLogin("127.0.0.1:1234") {
		t.Fatal("11th attempt should be rate limited")
	}
}
