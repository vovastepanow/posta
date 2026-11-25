package posta

import (
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
