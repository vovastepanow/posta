package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestAutopilotControlPanelSummary(t *testing.T) {
	checkpoint := jx.M{autopilotControlPanelKey: jx.M{"last_attempt_at": 1000, "results": jx.M{
		"Buffer":            jx.M{"status": "readable", "fresh": 12, "preliminary_completed": true, "preliminary_score": 30, "preliminary_pass": false, "post_probe": jx.M{"status": "completed_or_partial", "submitted": 6, "completed": 5, "eligible": 2, "secret": "not public"}},
		"Hootsuite":         jx.M{"status": "not_readable", "http_status": 429},
		"unknown publisher": jx.M{"status": "readable", "post_probe": jx.M{"eligible": 100}},
	}}}
	out := autopilotControlPanelSummary(checkpoint)
	if jx.Int(out, "panel_size") != 8 || jx.Int(out, "attempted") != 2 || jx.Int(out, "readable") != 1 ||
		jx.Int(out, "preliminary_completed") != 1 || jx.Int(out, "preliminary_pass") != 0 ||
		jx.Int(out, "submitted") != 6 || jx.Int(out, "completed") != 5 || jx.Int(out, "eligible") != 2 ||
		jx.Int(out, "sources_with_eligible") != 1 || jx.Bool(out, "independent_quality_measured") {
		t.Fatalf("invalid summary: %+v", out)
	}
	if raw := string(jx.Marshal(out)); strings.Contains(raw, "secret") || strings.Contains(raw, "unknown publisher") {
		t.Fatalf("untrusted checkpoint data escaped: %s", raw)
	}
}
