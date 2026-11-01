package onebot11

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	logruntime "easybot/internal/runtime"
	"github.com/gorilla/websocket"
)

type Config struct {
	WsURL             string `json:"WsUrl"`
	AccessToken       string `json:"AccessToken"`
	UseHeartBeat      bool   `json:"UseHeartBeat"`
	HeartBeatInterval int    `json:"HeartBeatInterval"`
}

type State string

const (
	StateStopped      State = "stopped"
	StateStarting     State = "starting"
	StateConnected    State = "connected"
	StateReconnecting State = "reconnecting"
	StateStopping     State = "stopping"
	StateFailed       State = "failed"
)

type Status struct {
	State          State      `json:"state"`
	ConnectedAt    *time.Time `json:"connectedAt,omitempty"`
	LastReceivedAt *time.Time `json:"lastReceivedAt,omitempty"`
	LastError      string     `json:"lastError,omitempty"`
	LastErrorCode  string     `json:"lastErrorCode,omitempty"`
	ReconnectCount uint64     `json:"reconnectCount"`
	Received       uint64     `json:"received"`
	Sent           uint64     `json:"sent"`
	SelfID         string     `json:"selfId,omitempty"`
	Nickname       string     `json:"nickname,omitempty"`
	ConnectionID   string     `json:"connectionId,omitempty"`
}

type Event struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	MessageType string          `json:"messageType,omitempty"`
	SelfID      string          `json:"selfId,omitempty"`
	PeerID      string          `json:"peerId,omitempty"`
	SenderID    string          `json:"senderId,omitempty"`
	SenderName  string          `json:"senderName,omitempty"`
	Content     string          `json:"content,omitempty"`
	OccurredAt  time.Time       `json:"occurredAt"`
	Raw         json.RawMessage `json:"raw,omitempty"`
}

type APIError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

func (e *APIError) Error() string { return e.Message }

var Capabilities = []string{"group_message_receive", "private_message_receive", "group_message_send", "private_message_send", "group_list", "heartbeat"}

func ParseConfig(raw json.RawMessage) (Config, error) {
	config := Config{}
	if len(raw) == 0 || string(raw) == "null" || string(raw) == "{}" {
		return Config{WsURL: "ws://localhost:3001/", HeartBeatInterval: 6000}, nil
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		return Config{}, &APIError{Code: "adapter_config_invalid", Message: "OneBot 11 配置不是有效 JSON", Retryable: false}
	}
	var aliases map[string]json.RawMessage
	if err := json.Unmarshal(raw, &aliases); err == nil {
		if config.WsURL == "" {
			_ = json.Unmarshal(aliases["url"], &config.WsURL)
		}
		if config.AccessToken == "" {
			_ = json.Unmarshal(aliases["accessToken"], &config.AccessToken)
		}
	}
	if config.WsURL == "" {
		config.WsURL = "ws://localhost:3001/"
	}
	if config.HeartBeatInterval == 0 {
		config.HeartBeatInterval = 6000
	}
	if err := config.Validate(); err != nil {
		return Config{}, err
	}
	return config, nil
}

func (c Config) Validate() error {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(c.WsURL))
	if err != nil || (parsed.Scheme != "ws" && parsed.Scheme != "wss") || parsed.Host == "" || parsed.User != nil {
		return &APIError{Code: "adapter_config_invalid", Message: "WsUrl 必须是有效的 ws:// 或 wss:// 地址", Retryable: false}
	}
	if len(c.AccessToken) > 4096 {
		return &APIError{Code: "adapter_config_invalid", Message: "AccessToken 长度超出限制", Retryable: false}
	}
	if c.HeartBeatInterval < 1000 || c.HeartBeatInterval > 300000 {
		return &APIError{Code: "adapter_config_invalid", Message: "HeartBeatInterval 必须在 1000 到 300000 毫秒之间", Retryable: false}
	}
	return nil
}

type Runtime struct {
	id      string
	config  Config
	onEvent func(Event)

	mu       sync.RWMutex
	status   Status
	conn     *websocket.Conn
	writeMu  sync.Mutex
	pending  map[string]chan response
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	started  bool
	sequence atomic.Uint64
}

type response struct {
	packet map[string]any
	err    error
}

func NewRuntime(id string, config Config, onEvent func(Event)) *Runtime {
	return &Runtime{id: id, config: config, onEvent: onEvent, status: Status{State: StateStopped}, pending: make(map[string]chan response)}
}

func TestConnection(ctx context.Context, config Config) (map[string]any, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	conn, responseHeader, err := dial(ctx, config)
	if err != nil {
		return nil, classifyDialError(err, responseHeader)
	}
	defer conn.Close()
	conn.SetReadDeadline(time.Now().Add(8 * time.Second))
	echo := newEcho()
	request, _ := json.Marshal(map[string]any{"action": "get_login_info", "params": map[string]any{}, "echo": echo})
	if err := conn.WriteMessage(websocket.TextMessage, request); err != nil {
		return nil, &APIError{Code: "adapter_network_error", Message: "OneBot 已连接，但无法发送身份查询", Retryable: true}
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			return nil, classifyReadError(err)
		}
		var packet map[string]any
		if json.Unmarshal(raw, &packet) != nil || fmt.Sprint(packet["echo"]) != echo {
			continue
		}
		if fmt.Sprint(packet["status"]) == "failed" {
			return nil, classifyAPIResponse(packet)
		}
		data, _ := packet["data"].(map[string]any)
		return map[string]any{"connected": true, "authenticated": true, "selfId": dataString(data, "user_id"), "nickname": dataString(data, "nickname"), "capabilities": Capabilities}, nil
	}
}

func (r *Runtime) Start(parent context.Context) error {
	r.mu.Lock()
	if r.started {
		state := r.status.State
		r.mu.Unlock()
		if state == StateConnected || state == StateStarting || state == StateReconnecting {
			return nil
		}
		return errors.New("adapter is stopping")
	}
	r.ctx, r.cancel = context.WithCancel(parent)
	r.done = make(chan struct{})
	r.started = true
	r.status = Status{State: StateStarting, ConnectionID: newEcho()}
	r.mu.Unlock()
	go r.run()
	return nil
}

func (r *Runtime) Stop(ctx context.Context) error {
	r.mu.Lock()
	if !r.started {
		r.mu.Unlock()
		return nil
	}
	r.status.State = StateStopping
	cancel, conn, done := r.cancel, r.conn, r.done
	r.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if conn != nil {
		_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
		_ = conn.Close()
	}
	select {
	case <-done:
		r.mu.Lock()
		r.started = false
		r.conn = nil
		r.status.State = StateStopped
		r.mu.Unlock()
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *Runtime) Status() Status {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.status
}

func (r *Runtime) Send(ctx context.Context, messageType, peerID, message string) (string, error) {
	if messageType != "group" && messageType != "private" {
		return "", &APIError{Code: "adapter_invalid_target", Message: "消息目标类型必须是 group 或 private", Retryable: false}
	}
	action := "send_group_msg"
	params := map[string]any{"group_id": peerID, "message": message, "auto_escape": true}
	if messageType == "private" {
		action = "send_private_msg"
		params = map[string]any{"user_id": peerID, "message": message, "auto_escape": true}
	}
	packet, err := r.call(ctx, action, params)
	if err != nil {
		return "", err
	}
	data, _ := packet["data"].(map[string]any)
	r.mu.Lock()
	r.status.Sent++
	r.mu.Unlock()
	return dataString(data, "message_id"), nil
}

func (r *Runtime) Groups(ctx context.Context) ([]map[string]any, error) {
	packet, err := r.call(ctx, "get_group_list", map[string]any{})
	if err != nil {
		return nil, err
	}
	data, _ := packet["data"].([]any)
	groups := make([]map[string]any, 0, len(data))
	for _, value := range data {
		if group, ok := value.(map[string]any); ok {
			groups = append(groups, group)
		}
	}
	return groups, nil
}

func (r *Runtime) run() {
	defer func() {
		r.mu.Lock()
		r.started = false
		r.conn = nil
		r.status.State = StateStopped
		for echo, pending := range r.pending {
			delete(r.pending, echo)
			pending <- response{err: &APIError{Code: "adapter_disconnected", Message: "OneBot 连接已关闭", Retryable: true}}
		}
		r.mu.Unlock()
		close(r.done)
	}()
	backoff := time.Second
	for {
		if r.ctx.Err() != nil {
			return
		}
		conn, responseHeader, err := dial(r.ctx, r.config)
		if err == nil {
			r.mu.Lock()
			r.conn = conn
			r.mu.Unlock()
			readErr := make(chan error, 1)
			go r.readLoop(conn, readErr)
			requestCtx, cancel := context.WithTimeout(r.ctx, 8*time.Second)
			packet, callErr := r.call(requestCtx, "get_login_info", map[string]any{})
			cancel()
			if callErr == nil {
				data, _ := packet["data"].(map[string]any)
				r.mu.Lock()
				now := time.Now().UTC()
				r.status.State = StateConnected
				r.status.ConnectedAt = &now
				r.status.LastError = ""
				r.status.LastErrorCode = ""
				r.status.SelfID = dataString(data, "user_id")
				r.status.Nickname = dataString(data, "nickname")
				r.mu.Unlock()
				backoff = time.Second
				select {
				case <-r.ctx.Done():
					return
				case err = <-readErr:
				}
				_ = conn.Close()
			} else {
				err = callErr
				_ = conn.Close()
			}
		} else {
			err = classifyDialError(err, responseHeader)
		}
		if r.ctx.Err() != nil {
			return
		}
		r.mu.Lock()
		r.conn = nil
		r.status.State = StateReconnecting
		r.status.ReconnectCount++
		r.status.LastError = safeMessage(err)
		r.status.LastErrorCode = errorCode(err)
		r.mu.Unlock()
		select {
		case <-r.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff < 30*time.Second {
			backoff *= 2
			if backoff > 30*time.Second {
				backoff = 30 * time.Second
			}
		}
	}
}

func (r *Runtime) readLoop(conn *websocket.Conn, result chan<- error) {
	for {
		if r.config.UseHeartBeat {
			_ = conn.SetReadDeadline(time.Now().Add(time.Duration(r.config.HeartBeatInterval+5000) * time.Millisecond))
		}
		_, raw, err := conn.ReadMessage()
		if err != nil {
			select {
			case result <- err:
			default:
			}
			return
		}
		var packet map[string]any
		if json.Unmarshal(raw, &packet) != nil {
			continue
		}
		if echo, ok := packet["echo"].(string); ok {
			r.mu.RLock()
			pending := r.pending[echo]
			r.mu.RUnlock()
			if pending != nil {
				if fmt.Sprint(packet["status"]) == "failed" {
					pending <- response{err: classifyAPIResponse(packet)}
				} else {
					pending <- response{packet: packet}
				}
			}
			continue
		}
		r.handleEvent(raw, packet)
	}
}

func (r *Runtime) handleEvent(raw []byte, packet map[string]any) {
	r.mu.Lock()
	now := time.Now().UTC()
	r.status.Received++
	r.status.LastReceivedAt = &now
	r.mu.Unlock()
	postType, _ := packet["post_type"].(string)
	event := Event{ID: newEcho(), Type: postType, OccurredAt: now, Raw: append(json.RawMessage(nil), raw...), SelfID: dataString(packet, "self_id")}
	if postType == "message" {
		event.MessageType = dataString(packet, "message_type")
		if event.MessageType == "group" {
			event.PeerID = dataString(packet, "group_id")
		} else {
			event.PeerID = dataString(packet, "user_id")
		}
		event.SenderID = dataString(packet, "user_id")
		sender, _ := packet["sender"].(map[string]any)
		event.SenderName = dataString(sender, "nickname")
		event.Content = dataString(packet, "raw_message")
	}
	if r.onEvent != nil {
		r.onEvent(event)
	}
}

func (r *Runtime) call(ctx context.Context, action string, params map[string]any) (map[string]any, error) {
	r.mu.RLock()
	conn := r.conn
	r.mu.RUnlock()
	if conn == nil {
		return nil, &APIError{Code: "adapter_offline", Message: "OneBot 11 连接未建立", Retryable: true}
	}
	echo := fmt.Sprintf("%s-%d", r.id, r.sequence.Add(1))
	result := make(chan response, 1)
	r.mu.Lock()
	r.pending[echo] = result
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, echo)
		r.mu.Unlock()
	}()
	data, err := json.Marshal(map[string]any{"action": action, "params": params, "echo": echo})
	if err != nil {
		return nil, err
	}
	r.writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, data)
	r.writeMu.Unlock()
	if err != nil {
		return nil, &APIError{Code: "adapter_network_error", Message: "OneBot 请求发送失败", Retryable: true}
	}
	select {
	case <-ctx.Done():
		return nil, &APIError{Code: "adapter_request_timeout", Message: "OneBot API 请求超时", Retryable: true}
	case response := <-result:
		return response.packet, response.err
	}
}

func dial(ctx context.Context, config Config) (*websocket.Conn, *http.Response, error) {
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	requestHeader := http.Header{}
	if config.AccessToken != "" {
		requestHeader.Set("Authorization", "Bearer "+config.AccessToken)
	}
	conn, response, err := dialer.DialContext(ctx, config.WsURL, requestHeader)
	if conn != nil {
		conn.SetReadLimit(1 << 20)
	}
	return conn, response, err
}

func classifyDialError(err error, response *http.Response) error {
	if response != nil && (response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden) {
		return &APIError{Code: "adapter_auth_failed", Message: "OneBot 鉴权失败，请检查 AccessToken", Retryable: false}
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr
	}
	return &APIError{Code: "adapter_network_error", Message: "无法连接 OneBot WebSocket 服务", Retryable: true}
}

func classifyReadError(err error) error {
	if timeout, ok := err.(interface{ Timeout() bool }); ok && timeout.Timeout() {
		return &APIError{Code: "adapter_request_timeout", Message: "OneBot WebSocket 请求超时", Retryable: true}
	}
	return &APIError{Code: "adapter_protocol_error", Message: "OneBot 未返回有效的身份查询响应", Retryable: true}
}

func classifyAPIResponse(packet map[string]any) error {
	message := fmt.Sprint(packet["message"])
	if message == "" || message == "<nil>" {
		message = "OneBot API 返回失败"
	}
	code, _ := packet["retcode"].(float64)
	switch int(code) {
	case 401, 403, 1403:
		return &APIError{Code: "adapter_auth_failed", Message: "OneBot 鉴权失败，请检查 AccessToken", Retryable: false}
	case 429:
		return &APIError{Code: "adapter_rate_limited", Message: "OneBot API 请求频率受限", Retryable: true}
	default:
		return &APIError{Code: "adapter_api_failed", Message: logruntime.Redact(message), Retryable: false}
	}
}

func safeMessage(err error) string {
	if err == nil {
		return "OneBot 连接已断开"
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Message
	}
	return "OneBot 连接已断开"
}

func errorCode(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Code
	}
	return "adapter_network_error"
}

func dataString(data map[string]any, key string) string {
	if data == nil || data[key] == nil {
		return ""
	}
	return fmt.Sprint(data[key])
}

func newEcho() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
