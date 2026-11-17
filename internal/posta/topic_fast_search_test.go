package posta

import (
	"testing"
	"slices"

	"posta/internal/jx"
)

func TestShouldFastTopicSearch(t *testing.T) {
	tests := []struct {
		name              string
		hasQuery, enabled bool
		restricted        bool
		sources, minimum  int
		available, target int
		want              bool
	}{
		{"new public topic", false, true, false, 0, 3, 0, 20, true},
		{"some sources still missing", false, true, false, 2, 3, 5, 20, true},
		{"enough sources but empty pool", false, true, false, 3, 3, 0, 20, true},
		{"enough sources and a healthy buffer", false, true, false, 3, 3, 12, 20, false},
		{"many sources but few viable posts", false, true, false, 10, 3, 2, 20, true},
		{"full pool", false, true, false, 0, 3, 20, 20, false},
		{"private topic", false, true, true, 0, 3, 0, 20, false},
		{"discovery disabled", false, false, false, 0, 3, 0, 20, false},
		{"explicit search uses its own pipeline", true, true, false, 0, 3, 0, 20, false},
		{"invalid target", false, true, false, 0, 3, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldFastTopicSearch(tt.hasQuery, tt.enabled, tt.restricted, tt.sources, tt.minimum, tt.available, tt.target)
			if got != tt.want {
				t.Fatalf("shouldFastTopicSearch(%+v) = %v, want %v", tt, got, tt.want)
			}
		})
	}
}

func TestTagFastTopicResults(t *testing.T) {
	posts := map[string][]jx.M{
		"rss": {
			{"url": "https://example.org/post/1"},
			{"url": "https://example.org/post/2", "found_by": "publisher search"},
		},
	}
	tagFastTopicResults(posts, "SMM")
	if jx.Str(posts["rss"][0], "found_by") == "" {
		t.Fatal("untagged search result cannot suggest its source")
	}
	if got := jx.Str(posts["rss"][1], "found_by"); got != "publisher search" {
		t.Fatalf("existing search origin overwritten: %q", got)
	}
}

func TestTopicFastQueryPlanUsesCuratedVocabulary(t *testing.T) {
	entry := &Topic{Title: "SMM", Queries: []string{"контент стратегия", "рост аудитории"}, Aliases: []string{"смм", "smm"}}
	plan := topicFastQueryPlan(QueryPlan{RU: []string{"SMM", "публикации в соцсетях"}}, entry)
	if !slices.Contains(plan.RU, "контент стратегия") || !slices.Contains(plan.RU, "рост аудитории") {
		t.Fatalf("curated topic terms missing: %v", plan.RU)
	}
	if !slices.Contains(plan.EN, "smm") {
		t.Fatalf("latin alias missing from English search: %v", plan.EN)
	}
	if plan.RU[0] != "smm" {
		t.Fatalf("original query no longer searched first: %v", plan.RU)
	}
	if got := topicFastQueryPlan(QueryPlan{RU: []string{"cats"}}, nil); len(got.RU) != 1 {
		t.Fatalf("unknown topic should keep existing plan: %v", got.RU)
	}
}
