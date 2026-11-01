package web

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"easybot/internal/runtime"
	"easybot/internal/store"
)

type server struct {
	store         *store.Store
	logger        *slog.Logger
	logs          *runtime.LogBuffer
	loginMu       sync.Mutex
	loginAttempts map[string]loginAttempt
}

type loginAttempt struct {
	start time.Time
	count int
}

const sessionCookie = "easybot_session"
const sessionLifetime = 12 * time.Hour

func New(db *store.Store, logger *slog.Logger, logs *runtime.LogBuffer) http.Handler {
	s := &server{store: db, logger: logger, logs: logs, loginAttempts: make(map[string]loginAttempt)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/app/info", s.info)
	mux.HandleFunc("GET /api/setup/status", s.setupStatus)
	mux.HandleFunc("POST /api/setup/admin", s.setupAdmin)
	mux.HandleFunc("GET /api/auth/session", s.authSession)
	mux.HandleFunc("POST /api/auth/login", s.login)
	mux.HandleFunc("POST /api/auth/logout", s.logout)
	mux.HandleFunc("GET /api/dashboard/data", s.dashboard)
	mux.HandleFunc("GET /api/records", s.listRecords)
	mux.HandleFunc("POST /api/records", s.createRecord)
	mux.HandleFunc("PUT /api/records/{kind}/{id}", s.updateRecord)
	mux.HandleFunc("DELETE /api/records/{kind}/{id}", s.deleteRecord)
	mux.HandleFunc("GET /api/adapter/types", s.adapterTypes)
	mux.HandleFunc("GET /api/adapter/list", s.adapters)
	mux.HandleFunc("POST /api/adapter/create", s.createAdapter)
	mux.HandleFunc("PUT /api/adapter/{id}", s.updateAdapter)
	mux.HandleFunc("POST /api/adapter/{id}/start", s.adapterStart)
	mux.HandleFunc("POST /api/adapter/{id}/stop", s.adapterStop)
	mux.HandleFunc("GET /api/logs", s.recentLogs)
	mux.Handle("/", http.FileServer(http.Dir("web/dist")))
	return s.observe(s.security(s.cors(mux)))
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": "EasyBot", "status": "running", "version": "0.1.0-dev", "database": s.store.Dialect()})
}

func (s *server) info(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]any{"app": map[string]string{"version": "0.1.0-dev", "type": "development"}, "database": s.store.Dialect(), "address": os.Getenv("EASYBOT_ADDR")}})
}

func (s *server) dashboard(w http.ResponseWriter, r *http.Request) {
	counts := make(map[string]int)
	for _, kind := range []string{"adapters", "servers", "players", "social-accounts", "whitelist", "blacklist", "commands"} {
		count, err := s.store.Count(r.Context(), kind)
		if err != nil {
			http.Error(w, "database query failed", http.StatusInternalServerError)
			return
		}
		counts[kind] = count
	}
	writeJSON(w, http.StatusOK, map[string]any{"counts": counts, "recent": s.logs.Recent(8), "uptime": time.Since(startedAt).Round(time.Second).String()})
}

func (s *server) recentLogs(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit == 0 {
		limit = 100
	}
	if limit < 1 || limit > 500 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "limit must be between 1 and 500"})
		return
	}
	writeJSON(w, http.StatusOK, s.logs.Recent(limit))
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (w *statusRecorder) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusRecorder) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return w.ResponseWriter.Write(data)
}

func (s *server) observe(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w}
		started := time.Now()
		next.ServeHTTP(recorder, r)
		status := recorder.status
		if status == 0 {
			status = http.StatusOK
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && (r.Method != http.MethodGet || status >= 400) {
			level := "info"
			if status >= 500 {
				level = "error"
			} else if status >= 400 {
				level = "warn"
			}
			message := fmt.Sprintf("%s %s (%d) in %s", r.Method, r.URL.Path, status, time.Since(started).Round(time.Millisecond))
			s.logs.Add(level, "api", message)
			s.logger.Log(r.Context(), levelFor(level), message, "status", status, "method", r.Method, "path", r.URL.Path)
		}
	})
}

func levelFor(level string) slog.Level {
	switch level {
	case "error":
		return slog.LevelError
	case "warn":
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
}

var startedAt = time.Now()

func (s *server) listRecords(w http.ResponseWriter, r *http.Request) {
	kind := r.URL.Query().Get("kind")
	if !store.Kinds[kind] {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "unknown record kind"})
		return
	}
	items, err := s.store.List(r.Context(), kind)
	if err != nil {
		http.Error(w, "database query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *server) createRecord(w http.ResponseWriter, r *http.Request) {
	var item store.Record
	if !decode(w, r, &item) {
		return
	}
	item, err := s.store.Save(r.Context(), item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *server) updateRecord(w http.ResponseWriter, r *http.Request) {
	var item store.Record
	if !decode(w, r, &item) {
		return
	}
	item.ID, item.Kind = r.PathValue("id"), r.PathValue("kind")
	item, err := s.store.Save(r.Context(), item)
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) deleteRecord(w http.ResponseWriter, r *http.Request) {
	err := s.store.Delete(r.Context(), r.PathValue("kind"), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) adapterTypes(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, []map[string]string{
		{"adapterType": "Onebot11", "displayName": "OneBot 11", "platformName": "onebot11"},
		{"adapterType": "Milky", "displayName": "Milky", "platformName": "milky"},
		{"adapterType": "Telegram", "displayName": "Telegram", "platformName": "telegram"},
		{"adapterType": "OpenQQ", "displayName": "OpenQQ", "platformName": "openqq"},
	})
}

func (s *server) adapters(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.List(r.Context(), "adapters")
	if err != nil {
		http.Error(w, "database query failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, items)
}

func (s *server) createAdapter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AdapterType string `json:"adapterType"`
		DisplayName string `json:"displayName"`
		ConfigJSON  string `json:"configJson"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.AdapterType != "Onebot11" && body.AdapterType != "Milky" && body.AdapterType != "Telegram" && body.AdapterType != "OpenQQ" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "unsupported adapter type"})
		return
	}
	if body.DisplayName == "" {
		body.DisplayName = body.AdapterType
	}
	if body.ConfigJSON == "" {
		body.ConfigJSON = "{}"
	}
	var config json.RawMessage
	if !json.Valid([]byte(body.ConfigJSON)) || json.Unmarshal([]byte(body.ConfigJSON), &config) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "adapter config must be valid JSON"})
		return
	}
	detail, _ := json.Marshal(map[string]any{"adapterType": body.AdapterType, "config": config})
	item, err := s.store.Save(r.Context(), store.Record{Kind: "adapters", Name: body.DisplayName, Detail: string(detail), Enabled: true})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, item)
}

func (s *server) updateAdapter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string `json:"displayName"`
		ConfigJSON  string `json:"configJson"`
	}
	if !decode(w, r, &body) {
		return
	}
	item, err := s.store.Save(r.Context(), store.Record{ID: r.PathValue("id"), Kind: "adapters", Name: body.DisplayName, Detail: body.ConfigJSON, Enabled: true})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, item)
}

func (s *server) adapterStart(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"message": "Adapter protocol runtimes are not implemented in this build."})
}

func (s *server) adapterStop(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotImplemented, map[string]string{"message": "No adapter runtime is active."})
}

func (s *server) security(next http.Handler) http.Handler {
	key := os.Getenv("EASYBOT_API_KEY")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodOptions {
			switch r.URL.Path {
			case "/api/health", "/api/setup/status", "/api/setup/admin", "/api/auth/session", "/api/auth/login":
			default:
				if key != "" && secureEqual(r.Header.Get("Authorization"), "Bearer "+key) {
					break
				}
				cookie, err := r.Cookie(sessionCookie)
				if err != nil {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
					return
				}
				if _, ok, err := s.store.SessionAdmin(r.Context(), sessionHash(cookie.Value)); err != nil || !ok {
					writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "authentication required"})
					return
				}
			}
		}
		next.ServeHTTP(w, r)
	})
}

func (s *server) setupStatus(w http.ResponseWriter, r *http.Request) {
	complete, err := s.store.SetupComplete(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "setup status unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"initialized": complete})
}

func (s *server) setupAdmin(w http.ResponseWriter, r *http.Request) {
	if !requestIsLocal(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"message": "initial setup is only available from localhost"})
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	body.Username = strings.ToLower(strings.TrimSpace(body.Username))
	if !validUsername(body.Username) || len(body.Password) < 12 || len(body.Password) > 72 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "username must be 3-32 characters; password must be 12-72 bytes"})
		return
	}
	complete, err := s.store.SetupComplete(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "setup status unavailable"})
		return
	}
	if complete {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "initial setup is already complete"})
		return
	}
	token, err := newSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "could not create session"})
		return
	}
	expires := time.Now().UTC().Add(sessionLifetime)
	user, err := s.store.CreateInitialAdmin(r.Context(), body.Username, body.Password, sessionHash(token), expires)
	if err != nil {
		if strings.Contains(err.Error(), "already complete") {
			writeJSON(w, http.StatusConflict, map[string]string{"message": "initial setup is already complete"})
			return
		}
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "could not initialize administrator"})
		return
	}
	setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusCreated, map[string]string{"id": user.ID, "username": user.Username})
}

func (s *server) authSession(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(sessionCookie)
	if err == nil {
		if user, ok, lookupErr := s.store.SessionAdmin(r.Context(), sessionHash(cookie.Value)); lookupErr == nil && ok {
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "username": user.Username})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(r.RemoteAddr) {
		w.Header().Set("Retry-After", "300")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"message": "too many login attempts; try again later"})
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	complete, err := s.store.SetupComplete(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "authentication unavailable"})
		return
	}
	if !complete {
		writeJSON(w, http.StatusConflict, map[string]string{"message": "initial setup is required"})
		return
	}
	user, ok, err := s.store.AuthenticateAdmin(r.Context(), strings.ToLower(strings.TrimSpace(body.Username)), body.Password)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "authentication unavailable"})
		return
	}
	if !ok {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "invalid username or password"})
		return
	}
	token, err := newSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "could not create session"})
		return
	}
	expires := time.Now().UTC().Add(sessionLifetime)
	if err := s.store.CreateSession(r.Context(), user.ID, sessionHash(token), expires); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "could not create session"})
		return
	}
	setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusOK, map[string]string{"id": user.ID, "username": user.Username})
}

func (s *server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if err := s.store.RevokeSession(r.Context(), sessionHash(cookie.Value)); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "could not end session"})
			return
		}
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

func (s *server) allowLogin(remote string) bool {
	key := remote
	if host, _, err := net.SplitHostPort(remote); err == nil {
		key = host
	}
	now := time.Now()
	s.loginMu.Lock()
	defer s.loginMu.Unlock()
	for address, current := range s.loginAttempts {
		if now.Sub(current.start) >= 5*time.Minute {
			delete(s.loginAttempts, address)
		}
	}
	if len(s.loginAttempts) >= 4096 {
		if _, exists := s.loginAttempts[key]; !exists {
			return false
		}
	}
	attempt := s.loginAttempts[key]
	if now.Sub(attempt.start) >= 5*time.Minute {
		attempt = loginAttempt{start: now}
	}
	if attempt.count >= 10 {
		return false
	}
	attempt.count++
	s.loginAttempts[key] = attempt
	return true
}

func validUsername(username string) bool {
	if len(username) < 3 || len(username) > 32 {
		return false
	}
	for _, char := range username {
		if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '_' && char != '-' && char != '.' {
			return false
		}
	}
	return true
}

func remoteIsLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func requestIsLocal(r *http.Request) bool {
	if !remoteIsLoopback(r.RemoteAddr) || !hostIsLoopback(r.Host) {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		parsed, err := url.Parse(origin)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || !hostIsLoopback(parsed.Host) {
			return false
		}
	}
	return true
}

func hostIsLoopback(value string) bool {
	host := value
	if parsedHost, _, err := net.SplitHostPort(value); err == nil {
		host = parsedHost
	}
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		host = strings.Trim(host, "[]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func newSessionToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func sessionHash(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}

func setSessionCookie(w http.ResponseWriter, r *http.Request, token string, expires time.Time) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: token, Path: "/", Expires: expires, MaxAge: int(time.Until(expires).Seconds()), HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode})
}

func secureEqual(actual, expected string) bool {
	return len(actual) == len(expected) && subtle.ConstantTimeCompare([]byte(actual), []byte(expected)) == 1
}

func (s *server) cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "http://127.0.0.1:") {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func decode(w http.ResponseWriter, r *http.Request, target any) bool {
	defer r.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := decoder.Decode(target); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "invalid JSON body"})
		return false
	}
	return true
}

func writeStoreError(w http.ResponseWriter, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"message": "record not found"})
		return
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"message": err.Error()})
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
