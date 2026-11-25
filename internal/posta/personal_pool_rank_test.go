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

func TestPersonalAffinityIgnoresAutomaticPublication(t *testing.T) {
	at := int64(2000000000)
	h := EmptyHistory()
	for i := 0; i < 20; i++ {
		h["liked"] = append(h["liked"], jx.M{"topic": "SMM", "timestamp": at-1, "source_key": "rss:automatic", "preference_vote": "publication"})
	}
	for i := 0; i < 6; i++ {
		h["liked"] = append(h["liked"], jx.M{"topic": "SMM", "timestamp": at-1, "source_key": "rss:disliked", "preference_vote": "dislike"})
	}
	a := buildPersonalPoolAffinity(h, "SMM", "SMM", strings.EqualFold, at)
	if a.votes != 6 || a.bonus(jx.M{"source_key": "rss:automatic"}) != 0 || a.bonus(jx.M{"source_key": "rss:disliked"}) >= 0 {
		t.Fatalf("publication bookkeeping leaked into explicit preference: %+v", a)
	}
	if sign, ok := preferenceVoteSign(jx.M{"preference_vote": "like"}, "liked"); !ok || sign != 1 {
		t.Fatal("explicit like lost")
	}
}
