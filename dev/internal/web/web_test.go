package web

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easybot/internal/adapter/onebot11"
	"easybot/internal/auth"
	"easybot/internal/runtime"
	"easybot/internal/store"
)

func createTestSession(t *testing.T, db *store.Store) {
	t.Helper()
	if _, err := db.CreateInitialAdmin(context.Background(), "admin", "internal-test-identity", sessionHash("test-session"), time.Now().Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
}

func TestRecordCRUDAndSession(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	createTestSession(t, db)
	logs := runtime.NewLogBuffer(20)
	handler := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), logs)

	request := httptest.NewRequest(http.MethodGet, "/api/records?kind=servers", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized, got %d", response.Code)
	}

	request = httptest.NewRequest(http.MethodPost, "/api/records", strings.NewReader(`{"kind":"servers","name":"survival","detail":"{}","enabled":true}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodGet, "/api/records?kind=servers", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "survival") {
		t.Fatalf("list returned %d: %s", response.Code, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/adapter/create", strings.NewReader(`{"adapterType":"Onebot11","displayName":"QQ Bridge","configJson":"{\"url\":\"ws://localhost:3001\"}"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusCreated || !strings.Contains(response.Body.String(), `Onebot11`) {
		t.Fatalf("adapter create returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPost, "/api/adapter/create", strings.NewReader(`{"adapterType":"Telegram","displayName":"unsupported","configJson":"{}"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotImplemented {
		t.Fatalf("unsupported adapter returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/logs?limit=10", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "POST /api/records") {
		t.Fatalf("logs returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodPut, "/api/logs/settings", strings.NewReader(`{"persistent":false,"retentionDays":45,"maxRows":2000}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
	request.Header.Set("Content-Type", "application/json")
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"retentionDays":45`) {
		t.Fatalf("log settings update returned %d: %s", response.Code, response.Body.String())
	}
	request = httptest.NewRequest(http.MethodGet, "/api/logs/status", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
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
	createTestSession(t, db)
	handler := New(db, slog.New(slog.NewTextHandler(io.Discard, nil)), runtime.NewLogBuffer(20))
	request := httptest.NewRequest(http.MethodPost, "/api/adapter/create", strings.NewReader(`{"adapterType":"Onebot11","displayName":"Mock","configJson":"{\"url\":\"ws://127.0.0.1:3001/\",\"accessToken\":\"never-return-this\"}"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
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
		request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
		response = httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "never-return-this") {
			t.Fatalf("%s leaked secret or failed: %d %s", path, response.Code, response.Body.String())
		}
	}
	request = httptest.NewRequest(http.MethodPut, "/api/adapter/"+created.ID, strings.NewReader(`{"displayName":"Mock updated","configJson":"{\"url\":\"ws://127.0.0.1:3002/\",\"accessTokenConfigured\":true}"}`))
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
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
	createTestSession(t, db)
	manager := onebot11.NewManager(context.Background(), db, nil)
	defer manager.Shutdown(context.Background())
	handler := NewWithAdapterManager(db, slog.New(slog.NewTextHandler(io.Discard, nil)), runtime.NewLogBuffer(20), manager)
	request := httptest.NewRequest(http.MethodGet, "/api/adapter/missing/status", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookie, Value: "test-session"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("missing adapter status returned %d: %s", response.Code, response.Body.String())
	}
}

func TestTokenLoginAndSessionAuthorization(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	path := filepath.Join(t.TempDir(), "token.txt")
	plain := "original-login-token"
	hash := sha256.Sum256([]byte(plain))
	if err := os.WriteFile(path, hash[:], 0600); err != nil {
		t.Fatal(err)
	}
	token, generated, err := auth.Load(path, true)
	if err != nil || generated != "" || !token.Valid(plain) {
		t.Fatalf("original token.txt compatibility: %v", err)
	}
	identity, err := db.EnsureTokenUser(context.Background(), token.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	token.UserID = identity.ID
	makeHandler := func(token *auth.Token) http.Handler {
		return NewForListener(db, slog.New(slog.NewTextHandler(io.Discard, nil)), runtime.NewLogBuffer(20), nil, "0.0.0.0:5001", token)
	}
	handler := makeHandler(token)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"token":"wrong"}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("invalid token returned %d", response.Code)
	}
	login := func(handler http.Handler, plain string) *http.Cookie {
		body, _ := json.Marshal(map[string]string{"token": plain})
		request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body)))
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK || len(response.Result().Cookies()) != 1 {
			t.Fatalf("token login: %d %s", response.Code, response.Body.String())
		}
		cookie := response.Result().Cookies()[0]
		if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || strings.Contains(response.Body.String(), cookie.Value) || strings.Contains(response.Body.String(), plain) {
			t.Fatal("invalid cookie or credential leak")
		}
		return cookie
	}
	check := func(handler http.Handler, cookie *http.Cookie, want int) {
		request := httptest.NewRequest(http.MethodGet, "/api/records?kind=servers", nil)
		if cookie != nil {
			request.AddCookie(cookie)
		}
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != want {
			t.Fatalf("management returned %d, want %d", response.Code, want)
		}
	}
	check(handler, nil, http.StatusUnauthorized)
	cookie := login(handler, plain)
	check(handler, cookie, http.StatusOK)
	if _, err := db.EnsureTokenUser(context.Background(), token.Fingerprint()); err != nil {
		t.Fatal(err)
	}
	check(makeHandler(token), cookie, http.StatusOK)
	request = httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	request.AddCookie(cookie)
	response = httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusNoContent {
		t.Fatalf("logout returned %d", response.Code)
	}
	check(handler, cookie, http.StatusUnauthorized)
	cookie = login(handler, plain)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	next, generated, err := auth.Load(path, true)
	if err != nil || len(generated) != 12 {
		t.Fatalf("token generation: %v", err)
	}
	identity, err = db.EnsureTokenUser(context.Background(), next.Fingerprint())
	if err != nil {
		t.Fatal(err)
	}
	next.UserID = identity.ID
	check(makeHandler(next), cookie, http.StatusUnauthorized)
	login(makeHandler(next), generated)
}

func TestLocalListenerAuthenticationBoundary(t *testing.T) {
	db, err := store.Open(t.TempDir() + "/easybot.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, test := range []struct {
		name, listener, remote, host, origin string
		allowed                              bool
	}{
		{name: "IPv4", listener: "127.0.0.1:5001", remote: "127.0.0.1:1234", host: "127.0.0.1:5001", allowed: true},
		{name: "IPv4 loopback range", listener: "127.0.0.2:5001", remote: "127.0.0.1:1234", host: "127.0.0.2:5001", allowed: true},
		{name: "IPv6", listener: "[::1]:5001", remote: "[::1]:1234", host: "[::1]:5001", allowed: true},
		{name: "localhost", listener: "localhost:5001", remote: "127.0.0.1:1234", host: "localhost:5001", allowed: true},
		{name: "external listener still requires login", listener: "0.0.0.0:5001", remote: "127.0.0.1:1234", host: "127.0.0.1:5001"},
		{name: "forged local host", listener: "127.0.0.1:5001", remote: "192.0.2.1:1234", host: "localhost:5001"},
		{name: "public host", listener: "127.0.0.1:5001", remote: "127.0.0.1:1234", host: "evil.example:5001"},
		{name: "public origin", listener: "127.0.0.1:5001", remote: "127.0.0.1:1234", host: "localhost:5001", origin: "https://evil.example"},
	} {
		t.Run(test.name, func(t *testing.T) {
			handler := NewForListener(db, slog.New(slog.NewTextHandler(io.Discard, nil)), runtime.NewLogBuffer(20), nil, test.listener, nil)
			for _, path := range []string{"/api/records?kind=servers", "/api/auth/session"} {
				request := httptest.NewRequest(http.MethodGet, path, nil)
				request.RemoteAddr, request.Host = test.remote, test.host
				if test.origin != "" {
					request.Header.Set("Origin", test.origin)
				}
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, request)
				if path == "/api/auth/session" {
					var session struct{ Authenticated bool }
					if err := json.Unmarshal(response.Body.Bytes(), &session); err != nil || session.Authenticated != test.allowed {
						t.Fatalf("session: %s, error %v", response.Body.String(), err)
					}
				} else if (response.Code == http.StatusOK) != test.allowed {
					t.Fatalf("management request returned %d", response.Code)
				}
			}
		})
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
