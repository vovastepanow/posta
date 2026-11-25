package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestPersonalPoolAffinityBoundedAndTopicLocal(t *testing.T) {
	at := int64(2_000_000_000)
	same := func(a, b string) bool { return strings.EqualFold(a, b) }
	record := func(source, topic string, ts int64) jx.M {
		return jx.M{"source_key": source, "topic": topic, "timestamp": ts, "tags": []string{"tag"}, "format": "photo"}
	}
	h := EmptyHistory()
	for i := 0; i < 6; i++ {
		h["liked"] = append(h["liked"], record("rss:good", "SMM", at-10))
		h["disliked"] = append(h["disliked"], record("rss:bad", "SMM", at-10))
		h["disliked"] = append(h["disliked"], record("rss:good", "Other", at-10), record("rss:good", "SMM", at-31*86400))
	}
	a := buildPersonalPoolAffinity(h, "smm", "smm", same, at)
	good := jx.M{"source_key": "rss:good", "topic": "SMM", "posted_at": at-300, "format": "photo"}
	bad := jx.M{"source_key": "rss:bad", "topic": "SMM", "posted_at": at-10, "format": "photo"}
	if a.votes != 12 || a.bonus(good) <= 0 || a.bonus(bad) >= 0 || a.bonus(good) > personalPoolMaxBonus {
		t.Fatalf("unexpected affinity: %+v", a)
	}
	if got := poolOrderWithPreference([]jx.M{bad, good}, a.bonus); jx.Str(got[0], "source_key") != "rss:good" {
		t.Fatal("preference did not influence nearby items")
	}
	good["postponed_at"] = at
	if got := poolOrderWithPreference([]jx.M{good, bad}, a.bonus); jx.Str(got[0], "source_key") != "rss:bad" {
		t.Fatal("preference overrode postponement")
	}
	neutral := buildPersonalPoolAffinity(History{"liked": []jx.M{record("rss:good", "SMM", at-10)}}, "SMM", "SMM", same, at)
	if neutral.bonus(good) != 0 {
		t.Fatal("one vote dominated the feed")
	}
}
