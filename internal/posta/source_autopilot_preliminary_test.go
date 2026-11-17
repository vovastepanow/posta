package posta

import (
	"testing"
	"time"
)

func TestAutopilotPreliminaryPass(t *testing.T) {
	tests := []struct {
		name                      string
		score, threshold, samples int
		want                      bool
	}{
		{"no samples", 100, 85, 0, false},
		{"single post", 100, 85, 1, false},
		{"two weak posts", 90, 85, 2, false},
		{"two exceptional posts", 95, 85, 2, true},
		{"two posts at capped threshold", 100, 95, 2, true},
		{"three normal posts", 85, 85, 3, true},
		{"three weak posts", 84, 85, 3, false},
		{"invalid threshold", 100, 101, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := autopilotPreliminaryPass(tt.score, tt.threshold, tt.samples)
			if got != tt.want {
				t.Fatalf("autopilotPreliminaryPass(%d, %d, %d) = %t, want %t", tt.score, tt.threshold, tt.samples, got, tt.want)
			}
		})
	}
}

func TestAutopilotSourceReviewInterval(t *testing.T) {
	auto := &autopilotJob{Scope: autopilotAutoScope, Kind: autopilotAutoKind}
	shadow := &autopilotJob{Scope: autopilotShadowScope, Kind: autopilotShadowKind}
	for _, tc := range []struct {
		job   *autopilotJob
		state string
		want  time.Duration
	}{
		{auto, "candidate", 12 * time.Hour},
		{auto, "probation", 6 * time.Hour},
		{auto, "active", 3 * time.Hour},
		{auto, "rejected", 24 * time.Hour},
		{shadow, "active", 24 * time.Hour},
		{nil, "active", 24 * time.Hour},
	} {
		if got := autopilotSourceReviewInterval(tc.job, tc.state); got != tc.want {
			t.Errorf("state=%s job=%+v got=%v want=%v", tc.state, tc.job, got, tc.want)
		}
	}
}
