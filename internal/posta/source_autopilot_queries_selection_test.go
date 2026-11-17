package posta

import "testing"

func TestAutopilotSelectQueryColdStartExploresEveryTurn(t *testing.T) {
	queries := []string{"business", "marketing", "growth"}
	for turn, want := range []string{"business", "marketing", "growth", "business", "marketing", "growth"} {
		if got := autopilotSelectQuery(queries, nil, int64(turn)); got != want {
			t.Fatalf("turn %d: got %q, want %q", turn, got, want)
		}
	}
}

func TestAutopilotSelectQueryUsesObservedYield(t *testing.T) {
	queries := []string{"business", "marketing", "growth"}
	scores := map[string]int64{"marketing": 4, "growth": 2}
	for _, tc := range []struct {
		turn int64
		want string
	}{
		{0, "business"},
		{1, "marketing"},
		{2, "marketing"},
		{3, "marketing"},
		{4, "marketing"},
		{5, "marketing"},
		{8, "growth"},
	} {
		if got := autopilotSelectQuery(queries, scores, tc.turn); got != tc.want {
			t.Errorf("turn %d: got %q, want %q", tc.turn, got, tc.want)
		}
	}
	if got := autopilotSelectQuery(nil, nil, 0); got != "" {
		t.Fatalf("empty query list: %q", got)
	}
}
