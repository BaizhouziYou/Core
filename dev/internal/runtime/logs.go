package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type LogEntry struct {
	ID        string    `json:"id"`
	Time      time.Time `json:"time"`
	Level     string    `json:"level"`
	Source    string    `json:"source"`
	Message   string    `json:"message"`
	RequestID string    `json:"requestId,omitempty"`
}

type LogBuffer struct {
	mu      sync.RWMutex
	entries []LogEntry
	limit   int
	sink    func(LogEntry)
}

func NewLogBuffer(limit int) *LogBuffer {
	if limit < 1 {
		limit = 500
	}
	return &LogBuffer{limit: limit}
}

func newID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(value[:])
}

func (b *LogBuffer) Add(level, source, message string) {
	b.AddWithRequestID(level, source, message, "")
}

func (b *LogBuffer) AddWithRequestID(level, source, message, requestID string) {
	entry := LogEntry{ID: newID(), Time: time.Now().UTC(), Level: level, Source: source, Message: Redact(message)}
	if validRequestID(requestID) {
		entry.RequestID = requestID
	}
	b.mu.Lock()
	sink := b.sink
	if len(b.entries) == b.limit {
		copy(b.entries, b.entries[1:])
		b.entries[len(b.entries)-1] = entry
	} else {
		b.entries = append(b.entries, entry)
	}
	b.mu.Unlock()
	if sink != nil {
		sink(entry)
	}
}

func validRequestID(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for _, char := range value {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("._-", char)) {
			return false
		}
	}
	return true
}

func (b *LogBuffer) SetSink(sink func(LogEntry)) {
	b.mu.Lock()
	b.sink = sink
	b.mu.Unlock()
}

func (b *LogBuffer) Recent(limit int) []LogEntry {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if limit < 1 || limit > len(b.entries) {
		limit = len(b.entries)
	}
	result := make([]LogEntry, 0, limit)
	for i := len(b.entries) - 1; i >= 0 && len(result) < limit; i-- {
		result = append(result, b.entries[i])
	}
	return result
}

var (
	secretAssignment = regexp.MustCompile(`(?i)(token|password|passwd|secret|api[_-]?key|authorization|dsn|challenge|verification[_-]?code)(\s*[=:]\s*)([^\s&,;]+)`)
	bearerToken      = regexp.MustCompile(`(?i)bearer\s+[a-z0-9._~+/-]+=*`)
	urlCredentials   = regexp.MustCompile(`(?i)(://[^:/\s]+:)[^@/\s]+@`)
)

func Redact(value string) string {
	value = bearerToken.ReplaceAllString(value, "Bearer [REDACTED]")
	value = urlCredentials.ReplaceAllString(value, "$1[REDACTED]@")
	return secretAssignment.ReplaceAllStringFunc(value, func(match string) string {
		parts := secretAssignment.FindStringSubmatch(match)
		if len(parts) != 4 {
			return "[REDACTED]"
		}
		return parts[1] + parts[2] + "[REDACTED]"
	})
}

type LogWriter interface {
	WriteLog(context.Context, LogEntry) error
}

type AsyncLogWriter struct {
	queue   chan LogEntry
	writer  LogWriter
	dropped atomic.Uint64
	failed  atomic.Uint64
	enabled atomic.Bool
	done    chan struct{}
}

func NewAsyncLogWriter(writer LogWriter, capacity int) *AsyncLogWriter {
	if capacity < 1 {
		capacity = 1024
	}
	a := &AsyncLogWriter{queue: make(chan LogEntry, capacity), writer: writer, done: make(chan struct{})}
	a.enabled.Store(true)
	go a.run()
	return a
}

func (a *AsyncLogWriter) TryWrite(entry LogEntry) {
	if !a.enabled.Load() {
		return
	}
	select {
	case a.queue <- entry:
	default:
		a.dropped.Add(1)
	}
}

func (a *AsyncLogWriter) Dropped() uint64         { return a.dropped.Load() }
func (a *AsyncLogWriter) Failed() uint64          { return a.failed.Load() }
func (a *AsyncLogWriter) SetEnabled(enabled bool) { a.enabled.Store(enabled) }

func (a *AsyncLogWriter) Close(ctx context.Context) {
	close(a.queue)
	select {
	case <-a.done:
	case <-ctx.Done():
	}
}

func (a *AsyncLogWriter) run() {
	defer close(a.done)
	for entry := range a.queue {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		if err := a.writer.WriteLog(ctx, entry); err != nil {
			a.failed.Add(1)
		}
		cancel()
	}
}

func NormalizeLogFilterText(value string) string { return strings.TrimSpace(value) }
