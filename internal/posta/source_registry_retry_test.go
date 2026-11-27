package posta

import "testing"

func TestRegistryRetryDelay(t *testing.T) {
	for _, tc := range []struct{ attempts, days int }{{0, 1}, {1, 1}, {2, 2}, {3, 4}, {4, 7}, {10, 7}} {
		if got := registryRetryDelay(tc.attempts); got != int64(tc.days)*RegistryRetryAfter {
			t.Errorf("attempts=%d: delay=%d, want %d days", tc.attempts, got, tc.days)
		}
	}
}
