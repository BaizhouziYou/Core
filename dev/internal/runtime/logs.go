package runtime

import (
	"sync"
	"time"
)

type LogEntry struct {
	Time    time.Time `json:"time"`
	Level   string    `json:"level"`
	Source  string    `json:"source"`
	Message string    `json:"message"`
}

type LogBuffer struct {
	mu      sync.RWMutex
	entries []LogEntry
	limit   int
}

func NewLogBuffer(limit int) *LogBuffer {
	if limit < 1 {
		limit = 500
	}
	return &LogBuffer{limit: limit}
}

func (b *LogBuffer) Add(level, source, message string) {
	entry := LogEntry{Time: time.Now().UTC(), Level: level, Source: source, Message: message}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.entries) == b.limit {
		copy(b.entries, b.entries[1:])
		b.entries[len(b.entries)-1] = entry
		return
	}
	b.entries = append(b.entries, entry)
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
