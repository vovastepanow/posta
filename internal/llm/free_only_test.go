package llm

import "testing"

func TestFreeOnlyBlocksPaidEvenWithAvailableBudgets(t *testing.T) {
	r := NewRouter(NewOllama("http://127.0.0.1:1"), NewOpenRouter("http://127.0.0.1:1", "test", "", ""))
	s := Preset("cloud")
	req := &Request{FreeOnly: true}
	if r.blocked(CloudVision, req, s) == "" {
		t.Fatal("paid route allowed")
	}
	if why := r.blocked(FreeVision, req, s); why != "" {
		t.Fatal(why)
	}
	if why := r.blocked(LocalSmart, req, s); why != "" {
		t.Fatal(why)
	}
}
