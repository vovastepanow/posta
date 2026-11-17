package posta

import (
	"posta/internal/jx"
	"testing"
)

func TestAutopilotProbationPoolLimits(t *testing.T) {
	probation := map[string]bool{"rss:a": true, "rss:b": true, "rss:c": true}
	pool := []jx.M{{"topic": "C++", "source_key": "rss:a"}, {"topic": "C++", "source_key": "rss:b"}, {"topic": "C", "source_key": "rss:c"}}
	if autopilotProbationPoolAllowed(pool, "C++", "rss:a", probation, 8, 25) {
		t.Fatal("more than one post per probation source")
	}
	if autopilotProbationPoolAllowed(pool, "C++", "rss:c", probation, 8, 25) {
		t.Fatal("probation share exceeded")
	}
	if !autopilotProbationPoolAllowed(pool, "C", "rss:b", probation, 8, 25) {
		t.Fatal("other profile counted toward probation share")
	}
	if !autopilotProbationPoolAllowed(pool, "C++", "rss:confirmed", probation, 8, 25) {
		t.Fatal("confirmed source was limited by probation policy")
	}
}
