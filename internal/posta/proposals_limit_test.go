package posta

import (
	"fmt"
	"testing"

	"posta/internal/jx"
)

func TestPruneSelectedHistoryKeepsLatestAndRatings(t *testing.T) {
	h := EmptyHistory()
	for i := 0; i < 103; i++ {
		h["liked"] = append(h["liked"], jx.M{"image": fmt.Sprintf("image-%d", i), "topic": "cats", "timestamp": i, "text": "post text", "image_local": fmt.Sprintf("/media/%d", i), "source_key": "rss:source", "tags": []string{"cat"}})
	}
	h["liked"] = append(h["liked"], jx.M{"image": "other", "topic": "dogs", "timestamp": 0})
	h["undecided"] = append(h["undecided"], jx.M{"image": "maybe", "topic": "cats"})
	removed := pruneSelectedHistory(h, "cats", func(a, b string) bool { return a == b }, 103)
	if len(removed) != 3 {
		t.Fatalf("removed %d posts, want 3", len(removed))
	}
	for i := 0; i < 3; i++ {
		r := h["liked"][i]
		if !jx.Bool(r, "content_deleted") || !jx.Bool(r, "proposal_hidden") || jx.Str(r, "text") != "" || jx.Str(r, "image_local") != "" {
			t.Fatalf("old content retained: %#v", r)
		}
		if jx.Str(r, "source_key") != "rss:source" || len(jx.Strs(r, "tags")) != 1 {
			t.Fatalf("rating data lost: %#v", r)
		}
	}
	if jx.Str(h["liked"][3], "image") != "image-3" || jx.Str(h["liked"][103], "image") != "other" || jx.Str(h["undecided"][0], "image") != "maybe" {
		t.Fatal("new posts, other topics and undecided votes must remain unchanged")
	}
	if n := len(pruneSelectedHistory(h, "cats", func(a, b string) bool { return a == b }, 103)); n != 0 {
		t.Fatalf("second pass removed %d", n)
	}
}

func TestPruneSelectedHistoryExpiresAcrossTopics(t *testing.T) {
	h := EmptyHistory()
	at := int64(2000000)
	h["liked"] = []jx.M{
		{"image": "old", "topic": "dogs", "timestamp": at - selectedPostMaxAge - 1, "text": "old"},
		{"image": "boundary", "topic": "cats", "timestamp": at - selectedPostMaxAge},
		{"image": "new", "topic": "cats", "timestamp": at},
	}
	removed := pruneSelectedHistory(h, "cats", func(a, b string) bool { return a == b }, at)
	if len(removed) != 1 || !jx.Bool(h["liked"][0], "content_deleted") || jx.Str(h["liked"][1], "image") != "boundary" {
		t.Fatalf("unexpected expiration: %#v", h["liked"])
	}
}

func TestSelectedHistoryPrunePreflightMatchesCompaction(t *testing.T) {
	same := func(a, b string) bool { return a == b }
	at := int64(2000000)
	for _, count := range []int{0, proposalsLimit, proposalsLimit + 1} {
		h := EmptyHistory()
		for i := 0; i < count; i++ {
			h["liked"] = append(h["liked"], jx.M{"image": fmt.Sprint(i), "topic": "cats", "timestamp": at})
		}
		h["liked"] = append(h["liked"], jx.M{"image": "hidden", "topic": "cats", "timestamp": at, "proposal_hidden": true})
		h["liked"] = append(h["liked"], jx.M{"image": "other", "topic": "dogs", "timestamp": at})
		needed := selectedHistoryNeedsPrune(h["liked"], "cats", same, at)
		if removed := pruneSelectedHistory(h, "cats", same, at); needed != (len(removed) != 0) {
			t.Fatalf("count %d: preflight=%t removed=%d", count, needed, len(removed))
		}
	}
	h := EmptyHistory()
	h["liked"] = []jx.M{{"image": "old-hidden", "topic": "dogs", "timestamp": at - selectedPostMaxAge - 1, "proposal_hidden": true}}
	if !selectedHistoryNeedsPrune(h["liked"], "cats", same, at) || len(pruneSelectedHistory(h, "cats", same, at)) != 1 {
		t.Fatal("expired hidden records must still be compacted")
	}
	if selectedHistoryNeedsPrune(h["liked"], "cats", same, at) {
		t.Fatal("already compacted records must not trigger another write")
	}
}
