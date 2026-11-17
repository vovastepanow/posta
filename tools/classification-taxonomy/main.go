// Export the current taxonomy for offline classification benchmarks; no App or DB.
package main

import (
	"encoding/json"
	"os"
	"posta/internal/posta"
)

func main() {
	parents := map[string]string{}
	var leaves []map[string]any
	for _, b := range posta.TopicTree {
		if b.NSFW {
			continue
		}
		parents[b.Key] = b.Title
		for _, c := range b.Children {
			if c.NSFW != nil && *c.NSFW {
				continue
			}
			leaves = append(leaves, map[string]any{"key": c.Key, "title": c.Title, "about": c.About, "hint": c.Hint, "parent": b.Key, "aliases": c.Aliases, "synonyms": c.Synonyms, "queries": c.Queries})
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"parents": parents, "leaves": leaves}); err != nil {
		panic(err)
	}
}
