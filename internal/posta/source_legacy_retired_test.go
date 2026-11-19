package posta

import "testing"

// Retirement must be unconditional: an old saved "requested" flag or parser
// shortage cannot bring back a second manual approval/source discovery lane.
func TestLegacyManualSourceDiscoveryIsRetired(t *testing.T) {
	u := &U{}
	if u.SourceDiscoveryDue(nil, "SMM и блогинг") {
		t.Fatal("retired legacy source discovery must never be scheduled")
	}
	if result := u.StartSourceDiscovery("SMM и блогинг", ""); result != "auto_only" {
		t.Fatalf("retired source discovery started: %q", result)
	}
	// Old parser entry points must not access application state or mutate data.
	u.RunSourceDiscovery("SMM и блогинг", 10)
}
