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
	removed := pruneSelectedHistory(h, "cats", func(a, b string) bool { return a == b })
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
	if n := len(pruneSelectedHistory(h, "cats", func(a, b string) bool { return a == b })); n != 0 {
		t.Fatalf("second pass removed %d", n)
	}
}
