package onebot11

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestConfigValidationAndLegacyFields(t *testing.T) {
	config, err := ParseConfig(json.RawMessage(`{"url":"ws://127.0.0.1:3001/","accessToken":"secret"}`))
	if err != nil || config.WsURL != "ws://127.0.0.1:3001/" || config.AccessToken != "secret" {
		t.Fatalf("legacy config parsed as %#v, error %v", config, err)
	}
	if _, err := ParseConfig(json.RawMessage(`{"WsUrl":"https://example.test/ws"}`)); err == nil {
		t.Fatal("expected non-websocket URL to fail validation")
	}
}

func TestOneBotAPIErrorClassification(t *testing.T) {
	if err := classifyAPIResponse(map[string]any{"retcode": float64(1403), "message": "credential rejected"}); errorCode(err) != "adapter_auth_failed" {
		t.Fatalf("expected auth failure, got %v", err)
	}
	if err := classifyAPIResponse(map[string]any{"retcode": float64(429), "message": "too many requests"}); errorCode(err) != "adapter_rate_limited" {
		t.Fatalf("expected rate limit, got %v", err)
	}
}

func TestTestConnectionReceivesIdentityAndUsesBearerToken(t *testing.T) {
	var auth string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		var request map[string]any
		if err := conn.ReadJSON(&request); err != nil {
			return
		}
		if request["action"] != "get_login_info" {
			t.Errorf("unexpected first action: %#v", request)
		}
		_ = conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": request["echo"], "data": map[string]any{"user_id": 4242, "nickname": "mock bot"}})
	}))
	defer server.Close()
	config := Config{WsURL: "ws" + strings.TrimPrefix(server.URL, "http"), AccessToken: "mock-token", HeartBeatInterval: 6000}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := TestConnection(ctx, config)
	if err != nil {
		t.Fatal(err)
	}
	if result["selfId"] != "4242" || result["nickname"] != "mock bot" || auth != "Bearer mock-token" {
		t.Fatalf("unexpected connection result %#v, auth %q", result, auth)
	}
}

func TestRuntimeReceiveSendAndStop(t *testing.T) {
	events := make(chan Event, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			var request map[string]any
			if err := conn.ReadJSON(&request); err != nil {
				return
			}
			action := request["action"]
			response := map[string]any{"status": "ok", "retcode": 0, "echo": request["echo"]}
			switch action {
			case "get_login_info":
				response["data"] = map[string]any{"user_id": 7, "nickname": "runtime bot"}
			case "send_group_msg":
				response["data"] = map[string]any{"message_id": 88}
			}
			if conn.WriteJSON(response) != nil {
				return
			}
			if action == "get_login_info" {
				_ = conn.WriteJSON(map[string]any{"post_type": "message", "message_type": "group", "self_id": 7, "group_id": 99, "user_id": 5, "raw_message": "hello", "sender": map[string]any{"nickname": "tester"}})
			}
		}
	}))
	defer server.Close()
	runtime := NewRuntime("adapter-1", Config{WsURL: "ws" + strings.TrimPrefix(server.URL, "http"), HeartBeatInterval: 6000}, func(event Event) { events <- event })
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := waitForStatus(ctx, runtime, StateConnected); err != nil {
		t.Fatal(err)
	}
	select {
	case event := <-events:
		if event.Type != "message" || event.MessageType != "group" || event.PeerID != "99" || event.Content != "hello" {
			t.Fatalf("unexpected normalized event %#v", event)
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for OneBot event")
	}
	messageID, err := runtime.Send(ctx, "group", "99", "outbound")
	if err != nil || messageID != "88" {
		t.Fatalf("send returned id=%q, error=%v", messageID, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := runtime.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
	if status := runtime.Status(); status.State != StateStopped || status.Sent != 1 || status.Received == 0 {
		t.Fatalf("unexpected stopped status %#v", status)
	}
}

func TestRuntimeBackoffReconnects(t *testing.T) {
	var connections sync.WaitGroup
	var count int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		count++
		current := count
		mu.Unlock()
		connections.Add(1)
		defer connections.Done()
		defer conn.Close()
		var request map[string]any
		if conn.ReadJSON(&request) != nil {
			return
		}
		_ = conn.WriteJSON(map[string]any{"status": "ok", "retcode": 0, "echo": request["echo"], "data": map[string]any{"user_id": 7, "nickname": "bot"}})
		if current == 1 {
			time.Sleep(50 * time.Millisecond)
			return
		}
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	runtime := NewRuntime("adapter-reconnect", Config{WsURL: "ws" + strings.TrimPrefix(server.URL, "http"), HeartBeatInterval: 6000}, nil)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	for {
		status := runtime.Status()
		mu.Lock()
		accepted := count
		mu.Unlock()
		if status.State == StateConnected && status.ReconnectCount > 0 && accepted >= 2 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatalf("runtime did not reconnect: status=%#v connections=%d", status, accepted)
		case <-time.After(20 * time.Millisecond):
		}
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
	defer stopCancel()
	if err := runtime.Stop(stopCtx); err != nil {
		t.Fatal(err)
	}
}

func newOneBotMock(t *testing.T, handle func(*websocket.Conn)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer mock-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		conn, err := (&websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}).Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		handle(conn)
	}))
}

func waitForStatus(ctx context.Context, runtime *Runtime, target State) error {
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if runtime.Status().State == target {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
