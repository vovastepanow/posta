package posta

import (
	"posta/internal/jx"
	"testing"
	"time"
)

func TestSourceSuggestionExclusionsExpireAndAreScoped(t *testing.T) {
	at := int64(1000)
	active := activeSourceExclusions(map[string]int64{"postponed": at + int64(sourcePostponeDuration/time.Second), "expired": at}, at)
	if !active["postponed"] || active["expired"] {
		t.Fatalf("active exclusions: %v", active)
	}
	if sourceSuggestionKey("u1", "Космос", "postponed") == sourceSuggestionKey("u1", "Спорт", "postponed") || sourceSuggestionKey("u1", "Космос", "postponed") == sourceSuggestionKey("u2", "Космос", "postponed") {
		t.Fatal("exclusions must be scoped to user and topic")
	}
}

func TestSourceCandidatesInterleaveAndDeduplicate(t *testing.T) {
	c := func(id string) jx.M { return jx.M{"source_id": id} }
	got := interleaveSourceCandidates([]jx.M{c("a"), c("b"), c("c")}, []jx.M{c("a"), c("d"), c("e")})
	want := []string{"a", "b", "d", "c", "e"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i, id := range want {
		if jx.Str(got[i], "source_id") != id {
			t.Fatalf("got %v", got)
		}
	}
}

func TestRecommendationSourceKeyYouTubeVariants(t *testing.T) {
	videos := "rss:https://www.youtube.com/feeds/videos.xml?channel_id=UCabcdefghijklmnopqrstuv"
	shorts := "rss:https://www.youtube.com/feeds/videos.xml?playlist_id=UUSHabcdefghijklmnopqrstuv"
	if recommendationSourceKey(videos) != recommendationSourceKey(shorts) {
		t.Fatalf("channel variants differ: %s %s", recommendationSourceKey(videos), recommendationSourceKey(shorts))
	}
	if recommendationSourceKey("telegram:One") == recommendationSourceKey("telegram:Two") {
		t.Fatal("different sources collapsed")
	}
}
