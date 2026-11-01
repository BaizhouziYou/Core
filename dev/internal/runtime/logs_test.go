package runtime

import "testing"

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
