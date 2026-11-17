package posta

import "testing"

func TestShouldFastTopicSearch(t *testing.T) {
	tests := []struct {
		name                  string
		hasQuery, enabled     bool
		restricted            bool
		sources, minimum      int
		available, target     int
		want                  bool
	}{
		{"new public topic", false, true, false, 0, 3, 0, 20, true},
		{"some sources still missing", false, true, false, 2, 3, 5, 20, true},
		{"enough sources", false, true, false, 3, 3, 0, 20, false},
		{"full pool", false, true, false, 0, 3, 20, 20, false},
		{"private topic", false, true, true, 0, 3, 0, 20, false},
		{"discovery disabled", false, false, false, 0, 3, 0, 20, false},
		{"explicit search uses its own pipeline", true, true, false, 0, 3, 0, 20, false},
		{"invalid target", false, true, false, 0, 3, 0, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldFastTopicSearch(tt.hasQuery, tt.enabled, tt.restricted, tt.sources, tt.minimum, tt.available, tt.target)
			if got != tt.want {
				t.Fatalf("shouldFastTopicSearch(%+v) = %v, want %v", tt, got, tt.want)
			}
		})
	}
}
