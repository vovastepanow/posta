package posta

import (
	"slices"
	"testing"

	"posta/internal/jx"
)

func TestDeferredCollectorKeysPreserveVideosAndShortText(t *testing.T) {
	items := []jx.M{
		{"image": "same", "video": "https://example.org/one.mp4", "source_key": "rss:a"},
		{"image": "same", "video": "https://example.org/two.mp4", "source_key": "rss:b"},
		{"text_post": true, "text": "SEO", "source_key": "rss:a"},
		{"text_post": true, "text": "SMM", "source_key": "rss:c"},
	}
	unique := map[string]jx.M{}
	var order []string
	for _, it := range items {
		k := ContentItemKey(it)
		unique[k] = it
		order = append(order, k)
	}
	got := collectorDeferredKeys(unique, order, len(items))
	if len(got) != len(items) {
		t.Fatalf("fallback lost eligible records: %v", got)
	}
	seen := map[string]bool{}
	for _, k := range got {
		if unique[k] == nil || seen[k] {
			t.Fatalf("fallback returned a missing or duplicate record: %q", k)
		}
		seen[k] = true
	}
	if len(collectorDeferredKeys(unique, order, 1)) != 1 || len(collectorDeferredKeys(unique, order, 0)) != 0 {
		t.Fatal("fallback ignored its limit")
	}
}

func TestCollectorExclusionHintsAreNotProofOfIrrelevance(t *testing.T) {
	// Both a relevant comparison and an irrelevant post contain the same
	// keyword. The shortlist may prioritize them but only analysis decides.
	for _, text := range []string{"Разбор маркетинга в игровой индустрии: кейс казино", "Казино"} {
		if !collectorExcludeHint(text, []string{"казино"}) {
			t.Fatalf("hint not recognized: %s", text)
		}
	}
}

func TestInterleaveByKeyPreservesStableRoundOrder(t *testing.T) {
	items := []string{"a1", "a2", "a3", "b1", "c1", "b2", "a4"}
	key := func(item string) string { return item[:1] }
	want := []string{"a1", "b1", "c1", "a2", "b2", "a3", "a4"}
	if got := interleaveByKey(items, key); !slices.Equal(got, want) {
		t.Fatalf("interleave=%v want=%v", got, want)
	}
	if got := interleaveByKey([]string(nil), key); got != nil {
		t.Fatalf("empty input returned %v", got)
	}
}

func TestHotRelativeHeatKeepsSourceAndPlatformBaselines(t *testing.T) {
	at := now()
	items := []jx.M{
		{"source_key": "rss:a", "posted_at": at, "views": 30},
		{"source_key": "rss:a", "posted_at": at, "views": 60},
		{"source_key": "rss:a", "posted_at": at, "views": 90},
		{"source_key": "rss:b", "posted_at": at, "views": 120},
		{"source_key": "rss:c", "posted_at": at},
		{"source_key": "rss:d", "posted_at": at, "views": 0},
	}
	want := []float64{0.5, 1, 1.5, 2, 1, 0}
	if got := hotRelativeHeat(items); !slices.Equal(got, want) {
		t.Fatalf("heat=%v want=%v", got, want)
	}
}

func TestCollectorSortCandidatesKeepsPriorityAndStableTies(t *testing.T) {
	query := "marketing"
	item := func(id, source, body string, at int64) jx.M {
		return jx.M{"id": id, "source_key": source, "text": body, "posted_at": at}
	}
	items := []jx.M{
		item("cold", "rss:cold", query, 10),
		item("old", "rss:neutral", query, 1),
		item("unmatched", "rss:best", "astronomy", 20),
		item("new", "rss:neutral", query, 3),
		withQueryRerankHint(item("boosted", "rss:neutral", query, 1), query, 100),
		item("tie", "rss:neutral", query, 3),
	}
	collectorSortCandidates(items, map[string][2]int{"rss:cold": {0, 1000}, "rss:best": {1000, 1000}}, query)
	want := []string{"boosted", "new", "tie", "old", "cold", "unmatched"}
	var got []string
	for _, item := range items {
		got = append(got, jx.Str(item, "id"))
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rank=%v want=%v", got, want)
	}
}
