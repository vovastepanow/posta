package posta

import "testing"

func TestAutopilotStatusReason(t *testing.T) {
	base := autopilotStatusInput{
		Enabled: true, Eligible: true, Deficit: 10, Target: 20,
		FetchRemaining: 50, ModelRemaining: 20,
	}
	tests := []struct {
		name string
		edit func(*autopilotStatusInput)
		want string
	}{
		{"no sources", func(*autopilotStatusInput) {}, "no_sources_found"},
		{"paused", func(i *autopilotStatusInput) { i.Paused = true }, "paused"},
		{"disabled", func(i *autopilotStatusInput) { i.Enabled = false }, "disabled"},
		{"no demand", func(i *autopilotStatusInput) { i.Eligible = false }, "disabled"},
		{"working", func(i *autopilotStatusInput) { i.Running = true }, "running"},
		{"backend failure", func(i *autopilotStatusInput) { i.Failure = "search_unavailable" }, "search_unavailable"},
		{"sufficient posts", func(i *autopilotStatusInput) { i.Deficit = 0 }, "supplied"},
		{"full pool", func(i *autopilotStatusInput) { i.Available = 20 }, "pool_full"},
		{"fetch quota", func(i *autopilotStatusInput) { i.FetchRemaining = 0 }, "fetch_budget_exhausted"},
		{"model quota", func(i *autopilotStatusInput) { i.ModelRemaining = 0 }, "model_budget_exhausted"},
		{"probation", func(i *autopilotStatusInput) { i.Candidates = 3 }, "sources_under_review"},
		{"poor yield", func(i *autopilotStatusInput) { i.ActiveSources = 3 }, "low_eligible_yield"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			input := base
			tc.edit(&input)
			if got := autopilotStatusReason(input); got != tc.want {
				t.Fatalf("diagnosis = %q, want %q for %+v", got, tc.want, input)
			}
		})
	}
}
