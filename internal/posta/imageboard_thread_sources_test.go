package posta

import (
	"posta/internal/jx"
	"testing"
)

func TestApprovedImageboardThreadsAreScopedToTopicAndExplicitDecision(t *testing.T) {
	a := &App{}
	a.catalog = &Catalog{app: a, idx: buildTopicIndex(nil)}
	d := &SourceDB{Votes: []jx.M{
		{"scope": "thread", "vote": "approve", "topic": "мемы", "platform": PlatformDvach, "board": "b", "thread_id": "1", "source_id": "dvach:b:1"},
		{"scope": "thread", "vote": "approve", "topic": "фитнес", "platform": PlatformDvach, "board": "b", "thread_id": "2", "source_id": "dvach:b:2"},
		{"vote": "approve", "topic": "мемы", "platform": PlatformDvach, "board": "b", "thread_id": "3", "source_id": "dvach:b:3"},
		{"scope": "thread", "vote": "reject", "topic": "мемы", "platform": PlatformDvach, "board": "b", "thread_id": "4", "source_id": "dvach:b:4"},
	}}
	got := a.approvedImageboardThreads(d, "мемы")
	if len(got) != 1 || jx.Str(got[0], "thread_id") != "1" {
		t.Fatalf("wrong approved threads: %#v", got)
	}
	got = a.approvedImageboardThreads(d, "фитнес")
	if len(got) != 1 || jx.Str(got[0], "thread_id") != "2" {
		t.Fatalf("wrong fitness threads: %#v", got)
	}
}
