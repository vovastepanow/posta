package posta

import "testing"

func TestAutopilotJobSnapshotRunning(t *testing.T) {
	active := autopilotJobSnapshot{Lease: 200, Enabled: true, Eligible: true}
	for _, tc := range []struct {
		name                  string
		job                   autopilotJobSnapshot
		at                    int64
		enabled, paused, live bool
		want                  bool
	}{
		{"active", active, 100, true, false, true, true},
		{"expired", active, 200, true, false, true, false},
		{"paused", active, 100, true, true, true, false},
		{"worker absent", active, 100, true, false, false, false},
		{"mode disabled", active, 100, false, false, true, false},
		{"job disabled", autopilotJobSnapshot{Lease: 200, Eligible: true}, 100, true, false, true, false},
		{"no demand", autopilotJobSnapshot{Lease: 200, Enabled: true}, 100, true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.job.running(tc.at, tc.enabled, tc.paused, tc.live); got != tc.want {
				t.Fatalf("running = %v, want %v", got, tc.want)
			}
		})
	}
}
