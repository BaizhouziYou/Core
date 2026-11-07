package web

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"easybot/internal/adapter/onebot11"
	"easybot/internal/auth"
	"easybot/internal/config"
	"easybot/internal/runtime"
	"easybot/internal/store"
)

type server struct {
	loginToken     *auth.Token
	localOnly      bool
	store          *store.Store
	logger         *slog.Logger
	logs           *runtime.LogBuffer
	logWriter      *runtime.AsyncLogWriter
	adapterManager *onebot11.Manager
	loginMu        sync.Mutex
	loginAttempts  map[string]loginAttempt
}

type loginAttempt struct {
	start time.Time
	count int
}

const sessionCookie = "easybot_session"
const sessionLifetime = 7 * 24 * time.Hour

func New(db *store.Store, logger *slog.Logger, logs *runtime.LogBuffer, writers ...*runtime.AsyncLogWriter) http.Handler {
	return NewWithAdapterManager(db, logger, logs, nil, writers...)
}

func NewWithAdapterManager(db *store.Store, logger *slog.Logger, logs *runtime.LogBuffer, adapters *onebot11.Manager, writers ...*runtime.AsyncLogWriter) http.Handler {
	return NewForListener(db, logger, logs, adapters, "", nil, writers...)
}

func NewForListener(db *store.Store, logger *slog.Logger, logs *runtime.LogBuffer, adapters *onebot11.Manager, address string, token *auth.Token, writers ...*runtime.AsyncLogWriter) http.Handler {
	s := &server{store: db, logger: logger, logs: logs, adapterManager: adapters, loginAttempts: make(map[string]loginAttempt)}
	s.localOnly = config.IsLoopbackListener(address)
	s.loginToken = token
	if len(writers) > 0 {
		s.logWriter = writers[0]
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/health", s.health)
	mux.HandleFunc("GET /api/app/info", s.info)
	mux.HandleFunc("GET /api/setup/status", s.setupStatus)
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
	mux.HandleFunc("POST /api/adapter/{id}/test", s.adapterTest)
	mux.HandleFunc("POST /api/adapter/{id}/restart", s.adapterRestart)
	mux.HandleFunc("GET /api/adapter/{id}/status", s.adapterStatus)
	mux.HandleFunc("GET /api/adapter/{id}/capabilities", s.adapterCapabilities)
	mux.HandleFunc("POST /api/adapter/{id}/send", s.adapterSend)
	mux.HandleFunc("GET /api/adapter/{id}/groups", s.adapterGroups)
	mux.HandleFunc("GET /api/logs", s.recentLogs)
	mux.HandleFunc("GET /api/logs/status", s.logStatus)
	mux.HandleFunc("PUT /api/logs/settings", s.updateLogSettings)
	mux.Handle("/", http.FileServer(http.Dir(staticDirectory())))
	handler := s.security(s.cors(mux))
	allowedHosts := os.Getenv("EASYBOT_ALLOWED_HOSTS")
	return s.observe(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !config.AllowedHost(r.Host, allowedHosts) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "host not allowed"})
			return
		}
		handler.ServeHTTP(w, r)
	}))
}

func staticDirectory() string {
	if executable, err := os.Executable(); err == nil {
		dir := filepath.Dir(executable)
		for _, base := range []string{dir, filepath.Dir(dir)} {
			candidate := filepath.Join(base, "web", "dist")
			if info, err := os.Stat(filepath.Join(candidate, "index.html")); err == nil && !info.IsDir() {
				return candidate
			}
		}
	}
	// go run 的临时可执行文件不与前端资源放在一起。
	return "web/dist"
}

func (s *server) health(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"name": "EasybotCE", "status": "running", "version": "0.1.0-dev", "database": s.store.Dialect()})
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
	offset := 0
	var err error
	if raw := r.URL.Query().Get("offset"); raw != "" {
		offset, err = strconv.Atoi(raw)
	}
	if err != nil || offset < 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "offset must be a non-negative integer"})
		return
	}
	filter := store.LogFilter{Level: r.URL.Query().Get("level"), Component: r.URL.Query().Get("component"), RequestID: r.URL.Query().Get("requestId"), Query: runtime.NormalizeLogFilterText(r.URL.Query().Get("q")), Limit: limit, Offset: offset}
	if raw := r.URL.Query().Get("from"); raw != "" {
		value, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "from must be an RFC3339 timestamp"})
			return
		}
		filter.From = &value
	}
	if raw := r.URL.Query().Get("to"); raw != "" {
		value, parseErr := time.Parse(time.RFC3339, raw)
		if parseErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"message": "to must be an RFC3339 timestamp"})
			return
		}
		filter.To = &value
	}
	items, total, err := s.store.ListLogs(r.Context(), filter)
	if err != nil {
		http.Error(w, "log query failed", http.StatusInternalServerError)
		return
	}
	if offset == 0 {
		seen := make(map[string]bool, len(items))
		for _, item := range items {
			seen[item.ID] = true
		}
		for _, item := range s.logs.Recent(500) {
			if seen[item.ID] || (filter.Level != "" && item.Level != filter.Level) || (filter.Component != "" && item.Source != filter.Component) || (filter.RequestID != "" && item.RequestID != filter.RequestID) || (filter.Query != "" && !strings.Contains(strings.ToLower(item.Message), strings.ToLower(filter.Query))) || (filter.From != nil && item.Time.Before(*filter.From)) || (filter.To != nil && !item.Time.Before(*filter.To)) {
				continue
			}
			items = append(items, item)
			seen[item.ID] = true
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Time.Equal(items[j].Time) {
				return items[i].ID > items[j].ID
			}
			return items[i].Time.After(items[j].Time)
		})
		if len(items) > limit {
			items = items[:limit]
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "limit": limit, "offset": offset})
}

func (s *server) logStatus(w http.ResponseWriter, r *http.Request) {
	settings, err := s.store.GetLogSettings(r.Context())
	if err != nil {
		http.Error(w, "log settings unavailable", http.StatusInternalServerError)
		return
	}
	status := map[string]any{"persistent": settings.Persistent, "retentionDays": settings.RetentionDays, "maxRows": settings.MaxRows, "updatedAt": settings.UpdatedAt, "droppedWrites": uint64(0), "failedWrites": uint64(0)}
	if s.logWriter != nil {
		status["droppedWrites"] = s.logWriter.Dropped()
		status["failedWrites"] = s.logWriter.Failed()
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *server) updateLogSettings(w http.ResponseWriter, r *http.Request) {
	var settings store.LogSettings
	if !decode(w, r, &settings) {
		return
	}
	if settings.RetentionDays < 1 || settings.RetentionDays > 3650 || settings.MaxRows < 1000 || settings.MaxRows > 10000000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "retentionDays must be 1-3650 and maxRows must be 1000-10000000"})
		return
	}
	if err := s.store.SaveLogSettings(r.Context(), settings); err != nil {
		http.Error(w, "log settings update failed", http.StatusInternalServerError)
		return
	}
	if s.logWriter != nil {
		s.logWriter.SetEnabled(settings.Persistent)
	}
	s.logs.Add("info", "settings", "Runtime log retention settings updated")
	updated, err := s.store.GetLogSettings(r.Context())
	if err != nil {
		http.Error(w, "log settings unavailable", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, updated)
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
			s.logs.AddWithRequestID(level, "api", message, r.Header.Get("X-Request-ID"))
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
	if kind == "adapters" {
		for i := range items {
			items[i] = publicAdapterRecord(items[i])
		}
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
	if item.Kind == "adapters" {
		item = publicAdapterRecord(item)
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
	if item.Kind == "adapters" {
		item = publicAdapterRecord(item)
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
	})
}

func (s *server) adapters(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.List(r.Context(), "adapters")
	if err != nil {
		http.Error(w, "database query failed", http.StatusInternalServerError)
		return
	}
	for i := range items {
		items[i] = publicAdapterRecord(items[i])
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
	if body.AdapterType != "Onebot11" {
		writeJSON(w, http.StatusNotImplemented, map[string]string{"message": "当前主程序仅支持 OneBot 11 适配器"})
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
	if body.AdapterType == "Onebot11" {
		if _, err := onebot11.ParseConfig(config); err != nil {
			writeAdapterError(w, err)
			return
		}
	}
	detail, _ := json.Marshal(map[string]any{"adapterType": body.AdapterType, "config": config})
	item, err := s.store.Save(r.Context(), store.Record{Kind: "adapters", Name: body.DisplayName, Detail: string(detail), Enabled: true})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, publicAdapterRecord(item))
}

func (s *server) updateAdapter(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DisplayName string `json:"displayName"`
		ConfigJSON  string `json:"configJson"`
	}
	if !decode(w, r, &body) {
		return
	}
	old, err := s.adapterRecord(r.Context(), r.PathValue("id"))
	if err != nil {
		writeStoreError(w, err)
		return
	}
	var oldDetail struct {
		AdapterType string          `json:"adapterType"`
		Config      json.RawMessage `json:"config"`
	}
	if err := json.Unmarshal([]byte(old.Detail), &oldDetail); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "stored adapter config is invalid"})
		return
	}
	var config json.RawMessage
	if !json.Valid([]byte(body.ConfigJSON)) || json.Unmarshal([]byte(body.ConfigJSON), &config) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "adapter config must be valid JSON"})
		return
	}
	config = preserveAccessToken(oldDetail.Config, config)
	if oldDetail.AdapterType == "Onebot11" {
		if _, err := onebot11.ParseConfig(config); err != nil {
			writeAdapterError(w, err)
			return
		}
	}
	detail, _ := json.Marshal(map[string]any{"adapterType": oldDetail.AdapterType, "config": config})
	item, err := s.store.Save(r.Context(), store.Record{ID: old.ID, Kind: "adapters", Name: body.DisplayName, Detail: string(detail), Enabled: old.Enabled})
	if err != nil {
		writeStoreError(w, err)
		return
	}
	if s.adapterManager != nil {
		if status, statusErr := s.adapterManager.Status(item.ID); statusErr == nil && status.State != onebot11.StateStopped {
			if restartErr := s.adapterManager.Restart(r.Context(), item.ID); restartErr != nil {
				s.logger.Warn("适配器 "+item.ID+" 更新配置后重启失败", "error", restartErr)
			}
		}
	}
	writeJSON(w, http.StatusOK, publicAdapterRecord(item))
}

func (s *server) adapterStart(w http.ResponseWriter, r *http.Request) {
	if s.adapterManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Adapter runtime is unavailable."})
		return
	}
	if err := s.adapterManager.Start(r.Context(), r.PathValue("id")); err != nil {
		writeAdapterError(w, err)
		return
	}
	status, _ := s.adapterManager.Status(r.PathValue("id"))
	writeJSON(w, http.StatusAccepted, status)
}

func (s *server) adapterStop(w http.ResponseWriter, r *http.Request) {
	if s.adapterManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Adapter runtime is unavailable."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	if err := s.adapterManager.Stop(ctx, r.PathValue("id")); err != nil {
		writeAdapterError(w, err)
		return
	}
	status, _ := s.adapterManager.Status(r.PathValue("id"))
	writeJSON(w, http.StatusOK, status)
}

func (s *server) adapterRestart(w http.ResponseWriter, r *http.Request) {
	if s.adapterManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Adapter runtime is unavailable."})
		return
	}
	if err := s.adapterManager.Restart(r.Context(), r.PathValue("id")); err != nil {
		writeAdapterError(w, err)
		return
	}
	status, _ := s.adapterManager.Status(r.PathValue("id"))
	writeJSON(w, http.StatusAccepted, status)
}

func (s *server) adapterTest(w http.ResponseWriter, r *http.Request) {
	if s.adapterManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Adapter runtime is unavailable."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	result, err := s.adapterManager.Test(ctx, r.PathValue("id"))
	if err != nil {
		writeAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *server) adapterStatus(w http.ResponseWriter, r *http.Request) {
	if s.adapterManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Adapter runtime is unavailable."})
		return
	}
	status, err := s.adapterManager.Status(r.PathValue("id"))
	if err != nil {
		writeAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (s *server) adapterCapabilities(w http.ResponseWriter, r *http.Request) {
	if s.adapterManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Adapter runtime is unavailable."})
		return
	}
	capabilities, err := s.adapterManager.Capabilities(r.Context(), r.PathValue("id"))
	if err != nil {
		writeAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, capabilities)
}

func (s *server) adapterSend(w http.ResponseWriter, r *http.Request) {
	if s.adapterManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Adapter runtime is unavailable."})
		return
	}
	var body struct {
		MessageType string `json:"messageType"`
		PeerID      string `json:"peerId"`
		Message     string `json:"message"`
	}
	if !decode(w, r, &body) {
		return
	}
	if body.PeerID == "" || strings.TrimSpace(body.Message) == "" || len(body.Message) > 4000 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"message": "peerId and a message of 1-4000 bytes are required"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	messageID, err := s.adapterManager.Send(ctx, r.PathValue("id"), body.MessageType, body.PeerID, body.Message)
	if err != nil {
		writeAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"messageId": messageID})
}

func (s *server) adapterGroups(w http.ResponseWriter, r *http.Request) {
	if s.adapterManager == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"message": "Adapter runtime is unavailable."})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	groups, err := s.adapterManager.Groups(ctx, r.PathValue("id"))
	if err != nil {
		writeAdapterError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, groups)
}

func (s *server) adapterRecord(ctx context.Context, id string) (store.Record, error) {
	items, err := s.store.List(ctx, "adapters")
	if err != nil {
		return store.Record{}, err
	}
	for _, item := range items {
		if item.ID == id {
			return item, nil
		}
	}
	return store.Record{}, sql.ErrNoRows
}

func publicAdapterRecord(item store.Record) store.Record {
	var detail struct {
		AdapterType string          `json:"adapterType"`
		Config      json.RawMessage `json:"config"`
	}
	if json.Unmarshal([]byte(item.Detail), &detail) != nil {
		item.Detail = `{"adapterType":"unknown","config":{},"configError":true}`
		return item
	}
	var config map[string]json.RawMessage
	if json.Unmarshal(detail.Config, &config) != nil {
		publicDetail, _ := json.Marshal(map[string]any{"adapterType": detail.AdapterType, "config": map[string]any{}, "configError": true})
		item.Detail = string(publicDetail)
		return item
	}
	var token string
	_ = json.Unmarshal(config["AccessToken"], &token)
	if token == "" {
		_ = json.Unmarshal(config["accessToken"], &token)
	}
	if token != "" {
		config["AccessToken"] = json.RawMessage(`""`)
		delete(config, "accessToken")
		config["accessTokenConfigured"] = json.RawMessage(`true`)
	}
	publicConfig, _ := json.Marshal(config)
	publicDetail, _ := json.Marshal(map[string]any{"adapterType": detail.AdapterType, "config": json.RawMessage(publicConfig)})
	item.Detail = string(publicDetail)
	return item
}

func preserveAccessToken(oldConfig, newConfig json.RawMessage) json.RawMessage {
	var oldValues, newValues map[string]json.RawMessage
	if json.Unmarshal(oldConfig, &oldValues) != nil || json.Unmarshal(newConfig, &newValues) != nil {
		return newConfig
	}
	var oldToken, newToken string
	_ = json.Unmarshal(oldValues["AccessToken"], &oldToken)
	if oldToken == "" {
		_ = json.Unmarshal(oldValues["accessToken"], &oldToken)
	}
	_ = json.Unmarshal(newValues["AccessToken"], &newToken)
	if newToken == "" {
		_ = json.Unmarshal(newValues["accessToken"], &newToken)
	}
	var configured bool
	_ = json.Unmarshal(newValues["accessTokenConfigured"], &configured)
	if oldToken != "" && (newToken == "" || newToken == "********" || configured) {
		newValues["AccessToken"] = oldValues["AccessToken"]
		if len(newValues["AccessToken"]) == 0 {
			newValues["AccessToken"] = oldValues["accessToken"]
		}
		delete(newValues, "accessToken")
	}
	delete(newValues, "accessTokenConfigured")
	result, err := json.Marshal(newValues)
	if err != nil {
		return newConfig
	}
	return result
}

func writeAdapterError(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	var apiErr *onebot11.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case "adapter_config_invalid", "adapter_invalid_target":
			status = http.StatusBadRequest
		case "adapter_auth_failed":
			status = http.StatusUnauthorized
		case "adapter_rate_limited":
			status = http.StatusTooManyRequests
		case "adapter_offline":
			status = http.StatusServiceUnavailable
		case "adapter_type_unsupported":
			status = http.StatusNotImplemented
		}
	} else if errors.Is(err, onebot11.ErrAdapterNotFound) || errors.Is(err, sql.ErrNoRows) {
		status = http.StatusNotFound
	}
	code := "adapter_error"
	if errors.As(err, &apiErr) {
		code = apiErr.Code
	}
	writeJSON(w, status, map[string]any{"message": err.Error(), "code": code})
}

func (s *server) security(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.localOnly && requestIsLocal(r) {
			next.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/") && r.Method != http.MethodOptions {
			switch r.URL.Path {
			case "/api/health", "/api/setup/status", "/api/auth/session", "/api/auth/login":
			default:
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
	if s.localOnly && requestIsLocal(r) {
		writeJSON(w, http.StatusOK, map[string]bool{"initialized": true})
		return
	}
	complete, err := s.store.SetupComplete(r.Context())
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "setup status unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"initialized": complete})
}

func (s *server) authSession(w http.ResponseWriter, r *http.Request) {
	if s.localOnly && requestIsLocal(r) {
		writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "authRequired": false})
		return
	}
	cookie, err := r.Cookie(sessionCookie)
	if err == nil {
		if _, ok, lookupErr := s.store.SessionAdmin(r.Context(), sessionHash(cookie.Value)); lookupErr == nil && ok {
			writeJSON(w, http.StatusOK, map[string]any{"authenticated": true, "authRequired": true})
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": false, "authRequired": true})
}

func (s *server) login(w http.ResponseWriter, r *http.Request) {
	if !s.allowLogin(r.RemoteAddr) {
		w.Header().Set("Retry-After", "300")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"message": "尝试次数过多，请稍后重试"})
		return
	}
	var body struct {
		Token string `json:"token"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.loginToken.Valid(body.Token) {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "登录 Token 无效"})
		return
	}
	token, err := newSessionToken()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "无法创建登录会话"})
		return
	}
	expires := time.Now().UTC().Add(sessionLifetime)
	if err := s.store.CreateSession(r.Context(), s.loginToken.UserID, sessionHash(token), expires); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"message": "无法保存登录会话"})
		return
	}
	setSessionCookie(w, r, token, expires)
	writeJSON(w, http.StatusOK, map[string]bool{"authenticated": true})
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
	if strings.EqualFold(strings.TrimSuffix(host, "."), "localhost") {
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
