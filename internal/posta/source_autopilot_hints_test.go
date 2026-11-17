package posta

import (
	"fmt"
	"testing"
)

func TestAppendAutopilotSourceHint(t *testing.T) {
	hints := appendAutopilotSourceHint(nil, "telegram:marketing_channel")
	hints = appendAutopilotSourceHint(hints, "telegram:marketing_channel")
	if len(hints) != 1 {
		t.Fatalf("duplicated accepted source: %v", hints)
	}
	hints = appendAutopilotSourceHint(hints, "")
	if len(hints) != 1 {
		t.Fatalf("empty source was queued: %v", hints)
	}
	for i := 0; i < autopilotSourceHintMax+10; i++ {
		hints = appendAutopilotSourceHint(hints, fmt.Sprintf("rss:https://example.org/%d", i))
	}
	if len(hints) != autopilotSourceHintMax {
		t.Fatalf("queue length %d, want %d", len(hints), autopilotSourceHintMax)
	}
	if hints[len(hints)-1] == "" {
		t.Fatal("last accepted source dropped")
	}
}

func TestAutopilotSourceHintKey(t *testing.T) {
	key := autopilotSourceHintKey("SMM и блогинг")
	if key == "" || key != autopilotSourceHintKey("SMM и блогинг") {
		t.Fatalf("unstable hint key: %q", key)
	}
}
