package posta

import (
	"testing"
	"time"
)

func TestAutopilotRetryInterval(t *testing.T) {
	tests := []struct {
		name string
		mode string
		covered bool
		want time.Duration
	}{
		{"new topic", "bootstrap", false, time.Hour},
		{"bootstrap with observed yield", "bootstrap", true, time.Hour},
		{"low supply", "refill", false, 2 * time.Hour},
		{"refill with adequate observed yield", "refill", true, 12 * time.Hour},
		{"healthy", "healthy", false, 24 * time.Hour},
		{"unknown mode", "blocked", false, 24 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autopilotRetryInterval(tt.mode, tt.covered); got != tt.want {
				t.Fatalf("autopilotRetryInterval(%q, %v) = %v, want %v", tt.mode, tt.covered, got, tt.want)
			}
		})
	}
}
