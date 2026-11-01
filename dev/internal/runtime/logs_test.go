package runtime

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestLogBufferKeepsRecentEntriesNewestFirst(t *testing.T) {
	logs := NewLogBuffer(2)
	logs.Add("info", "test", "one")
	logs.Add("warn", "test", "two")
	logs.Add("error", "test", "three")
	items := logs.Recent(2)
	if len(items) != 2 || items[0].Message != "three" || items[1].Message != "two" {
		t.Fatalf("unexpected recent logs: %#v", items)
	}
}

func TestRedactSensitiveValues(t *testing.T) {
	got := Redact("Authorization: Bearer abc.def password=hunter2 dsn=postgres://user:pass@host/db challenge: 123456")
	for _, secret := range []string{"abc.def", "hunter2", "user:pass", "123456"} {
		if strings.Contains(got, secret) {
			t.Fatalf("sensitive value %q was not redacted: %s", secret, got)
		}
	}
}

type blockingLogWriter struct {
	release chan struct{}
	started chan struct{}
}

func (w blockingLogWriter) WriteLog(context.Context, LogEntry) error {
	select {
	case <-w.started:
	default:
		close(w.started)
	}
	<-w.release
	return nil
}

func TestAsyncLogWriterQueueIsBoundedAndNonBlocking(t *testing.T) {
	backend := blockingLogWriter{release: make(chan struct{}), started: make(chan struct{})}
	writer := NewAsyncLogWriter(backend, 1)
	writer.TryWrite(LogEntry{ID: "first"})
	<-backend.started
	writer.TryWrite(LogEntry{ID: "queued"})
	writer.TryWrite(LogEntry{ID: "overflow"})
	if writer.Dropped() != 1 {
		t.Fatalf("expected one dropped write, got %d", writer.Dropped())
	}
	close(backend.release)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	writer.Close(ctx)
}
