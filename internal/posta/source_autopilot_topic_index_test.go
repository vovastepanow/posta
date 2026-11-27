package posta

import (
	"strings"
	"testing"
)

func TestAutopilotTopicIndexPrecedenceAndAmbiguity(t *testing.T) {
	tree := []TreeBranch{
		{Title: "First", Aliases: []string{"shared", "exact", "unique"}, Children: []TreeLeaf{{Title: "Exact", Aliases: []string{"repeat", "repeat"}}}},
		{Title: "Second", Aliases: []string{"shared"}},
		{Title: "Third", Aliases: []string{"shared"}},
	}
	index := buildAutopilotTopicIndex(tree)
	for key, want := range map[string]string{"first": "First", "exact": "Exact", "unique": "First", "shared": "", "repeat": "Exact"} {
		if got := index[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
}

// Independently reproduce the pre-index lookup for every published alias and
// title to protect existing database keys when the taxonomy changes.
func TestAutopilotTopicIndexPreservesTaxonomy(t *testing.T) {
	type entry struct {
		title string
		aliases []string
	}
	var entries []entry
	for _, branch := range TopicTree {
		entries = append(entries, entry{branch.Title, branch.Aliases})
		for _, leaf := range branch.Children {
			entries = append(entries, entry{leaf.Title, leaf.Aliases})
		}
	}
	var inputs []string
	for _, e := range entries {
		inputs = append(inputs, e.title)
		inputs = append(inputs, e.aliases...)
	}
	for _, input := range inputs {
		key := TopicNorm(input)
		if key == "" { continue }
		want := ""
		for _, e := range entries {
			if TopicNorm(e.title) == key {
				want = e.title
				break
			}
		}
		if want == "" {
			matches := map[string]bool{}
			for _, e := range entries {
				for _, alias := range e.aliases {
					if TopicNorm(alias) == key { matches[e.title] = true }
				}
			}
			if len(matches) == 1 {
				for title := range matches { want = title }
			} else { want = strings.TrimSpace(input) }
		}
		if got := autopilotCanonicalTopic(input); got != want {
			t.Errorf("%q = %q, want %q", input, got, want)
		}
	}
	if autopilotTopicKey("smm") != autopilotTopicKey("SMM и блогинг") {
		t.Fatal("historical SMM key changed")
	}
}

func BenchmarkAutopilotTopicKey(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = autopilotTopicKey("SMM и блогинг")
	}
}
