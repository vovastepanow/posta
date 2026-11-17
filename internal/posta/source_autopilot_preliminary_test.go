package posta

import "testing"

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
