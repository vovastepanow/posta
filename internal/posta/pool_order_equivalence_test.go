package posta

import (
	"math/rand"
	"reflect"
	"sort"
	"testing"

	"posta/internal/config"
	"posta/internal/jx"
)

// Reference the original full-tail removal algorithm. Equal scores, mixed
// topics, postponed entries and repeated sources must preserve exact order.
func TestPoolOrderMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(42))
	bonus := func(item jx.M) int64 { return jx.Int(item, "bonus") }
	reference := func(pool []jx.M) []jx.M {
		groups := map[string][]jx.M{}
		var topics []string
		for _, item := range pool {
			topic := jx.Str(item, "topic")
			if _, ok := groups[topic]; !ok {
				topics = append(topics, topic)
			}
			groups[topic] = append(groups[topic], item)
		}
		score := func(item jx.M) int64 {
			at := jx.Int(item, "posted_at")
			if at <= 0 { at = jx.Int(item, "pooled_at") }
			if !jx.Bool(item, "postponed_at") { at += bonus(item) }
			return at
		}
		out := make([]jx.M, 0, len(pool))
		for _, topic := range topics {
			g := groups[topic]
			sort.SliceStable(g, func(i, j int) bool {
				a, b := jx.Int(g[i], "postponed_at"), jx.Int(g[j], "postponed_at")
				if a != b { return a < b }
				return score(g[i]) > score(g[j])
			})
			last, hasLast := "", false
			for len(g) > 0 {
				pick := 0
				if hasLast && ContentSourceKey(g[0]) == last {
					for k := 1; k < len(g) && k <= config.PoolDiversityWindow; k++ {
						if ContentSourceKey(g[k]) != last && jx.Bool(g[k], "postponed_at") == jx.Bool(g[0], "postponed_at") {
							pick = k
							break
						}
					}
				}
				item := g[pick]
				last, hasLast = ContentSourceKey(item), true
				out = append(out, item)
				g = append(g[:pick], g[pick+1:]...)
			}
		}
		return out
	}
	for n := 0; n < 100; n++ {
		pool := make([]jx.M, n)
		for i := range pool {
			pool[i] = jx.M{
				"id": i, "topic": []string{"a", "b", "c"}[rng.Intn(3)],
				"source": []string{"one", "two", "three", "one"}[rng.Intn(4)],
				"posted_at": rng.Intn(5), "pooled_at": rng.Intn(5),
				"postponed_at": rng.Intn(3), "bonus": rng.Intn(5)-2,
			}
		}
		if got, want := poolOrderWithPreference(pool, bonus), reference(pool); !reflect.DeepEqual(got, want) {
			t.Fatalf("order differs for size %d: got %v, want %v", n, got, want)
		}
	}
}
