package posta

import (
	"context"
	"fmt"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestAutopilotSearchDefaultsAndRotation(t *testing.T) {
	for _, key := range []string{"autopilot_search_habr", "autopilot_search_tumblr"} {
		tunable := tunableByKey(key)
		if tunable == nil || tunable.Def != true {
			t.Fatalf("%s must default to enabled", key)
		}
	}
	for turn := int64(0); turn < 3; turn++ {
		ctx, cancel := context.WithCancel(context.Background())
		var order []int
		backends := make([]func() []DiscoveryCandidate, 3)
		for i := range backends {
			n := i
			backends[i] = func() []DiscoveryCandidate {
				order = append(order, n)
				cancel()
				return []DiscoveryCandidate{{ID: fmt.Sprint(n)}}
			}
		}
		got := autopilotRunSearchBackends(ctx, turn, backends)
		cancel()
		if len(order) != 1 || order[0] != int(turn) || len(got) != 1 || got[0].ID != fmt.Sprint(turn) {
			t.Fatalf("turn=%d order=%v partial=%v", turn, order, got)
		}
	}
}

func TestAutopilotSearchSavedDisableOverridesDefault(t *testing.T) {
	tunables.Lock()
	oldValues, oldRead := tunables.vals, tunables.read
	tunables.vals = jx.M{"autopilot_search_habr": false, "autopilot_search_tumblr": false}
	tunables.read = time.Now()
	tunables.Unlock()
	t.Cleanup(func() {
		tunables.Lock()
		tunables.vals, tunables.read = oldValues, oldRead
		tunables.Unlock()
	})
	a := &App{}
	if a.TBool("autopilot_search_habr") || a.TBool("autopilot_search_tumblr") {
		t.Fatal("explicitly saved disable was overwritten by defaults")
	}
}

func TestAutopilotSearchPreservesPartialResultsAtBudgetLimit(t *testing.T) {
	ctx := withAutopilotBudgetTracker(context.Background())
	called := false
	backends := []func() []DiscoveryCandidate{
		func() []DiscoveryCandidate {
			noteAutopilotBudgetError(ctx, errAutopilotBudgetExhausted)
			return []DiscoveryCandidate{{ID: "partial"}}
		},
		func() []DiscoveryCandidate { called = true; return nil },
	}
	got := autopilotRunSearchBackends(ctx, 0, backends)
	if called || len(got) != 1 || got[0].ID != "partial" || autopilotBudgetError(ctx) != errAutopilotBudgetExhausted {
		t.Fatalf("called=%v results=%v budget=%v", called, got, autopilotBudgetError(ctx))
	}
}
