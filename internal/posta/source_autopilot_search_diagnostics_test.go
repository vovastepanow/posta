package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestAutopilotDiscoveryOutcome(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		backends, sources, direct int
		observed, available, directAttempted bool
		want                      string
	}{
		{"all disabled", 0, 0, 0, false, false, false, "disabled"},
		{"all unavailable", 3, 0, 0, true, false, false, "unavailable"},
		{"unobserved search", 3, 0, 0, false, false, false, "unknown"},
		{"healthy empty search", 3, 0, 0, true, true, false, "empty"},
		{"normal source results", 3, 2, 0, true, true, false, "results"},
		{"public post results", 0, 0, 2, false, false, true, "results"},
		{"only direct search empty", 0, 0, 0, false, false, true, "empty"},
		{"partial platform failure", 3, 1, 0, true, false, false, "results"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autopilotDiscoveryOutcome(tc.backends, tc.sources, tc.direct, tc.observed, tc.available, tc.directAttempted); got != tc.want {
				t.Fatalf("outcome = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAutopilotRecordDiscoveryMetricsPreservesCheckpoint(t *testing.T) {
	job := &autopilotJob{Checkpoint: jx.Marshal(jx.M{"query_turn": 13, "direct_accepted_total": 5})}
	autopilotRecordDiscoveryMetrics(job, 3, 2, 4, "results", 12345, map[string]string{
		"VK": "ok", "Lemmy": "empty", "Mastodon": "unavailable",
	})
	got := jx.Unmarshal(job.Checkpoint)
	for key, want := range map[string]int64{
		"query_turn": 13, "direct_accepted_total": 5,
		"last_search_at": 12345, "last_source_candidates": 3,
		"last_direct_sources": 2, "last_search_backends": 4,
		"last_backends_ok": 1, "last_backends_empty": 1,
		"last_backends_unavailable": 1,
	} {
		if jx.Int(got, key) != want {
			t.Errorf("%s = %d, want %d", key, jx.Int(got, key), want)
		}
	}
	if jx.Str(got, "last_search_outcome") != "results" {
		t.Fatalf("missing search outcome: %v", got)
	}
}
