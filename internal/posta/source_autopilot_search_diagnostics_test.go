package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestAutopilotDiscoveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name string
		backends, sources, direct int
		available bool
		want string
	}{
		{"all disabled", 0, 0, 0, false, "disabled"},
		{"all unavailable", 3, 0, 0, false, "unavailable"},
		{"healthy empty search", 3, 0, 0, true, "empty"},
		{"normal source results", 3, 2, 0, true, "results"},
		{"public post results", 0, 0, 2, false, "results"},
		{"partial platform failure", 3, 1, 0, false, "results"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autopilotDiscoveryOutcome(tc.backends, tc.sources, tc.direct, tc.available); got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAutopilotRecordDiscoveryMetricsPreservesCheckpoint(t *testing.T) {
	job := &autopilotJob{Checkpoint: jx.Marshal(jx.M{"query_turn": 13, "direct_accepted_total": 5})}
	autopilotRecordDiscoveryMetrics(job, 3, 2, 4, "results", 12345)
	got := jx.Unmarshal(job.Checkpoint)
	for key, want := range map[string]int64{
		"query_turn": 13, "direct_accepted_total": 5,
		"last_search_at": 12345, "last_source_candidates": 3,
		"last_direct_sources": 2, "last_search_backends": 4,
	} {
		if jx.Int(got, key) != want {
			t.Errorf("%s = %d, want %d", key, jx.Int(got, key), want)
		}
	}
	if jx.Str(got, "last_search_outcome") != "results" {
		t.Fatalf("missing search outcome: %v", got)
	}
}
