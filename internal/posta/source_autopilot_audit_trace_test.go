package posta

import (
	"context"
	"strings"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestAutopilotPostAuditBoundedAndNoPayload(t *testing.T) {
	post := jx.M{"url": "https://example.org/posts/1", "text": strings.Repeat("x", 7000), "posted_at": 100}
	ctx, trace := newAutopilotPostAudit(context.Background(), []jx.M{post, post})
	if len(trace.entries) != 1 {
		t.Fatalf("trace entries=%d", len(trace.entries))
	}
	entry := autopilotPostAuditEntry(ctx, post)
	entry["sent_text_runes"] = len([]rune(textAnalysisRecord(AnalysisText(post))))
	autopilotPostAuditVerdict(entry, jx.M{"model": "test-model", "model_relevance": 85, "relevance": 50, "description": "PRIVATE_TEXT"}, false, false, false, 65, "PRIVATE_REASON")
	if jx.Int(entry, "normalized_text_runes") < jx.Int(entry, "sent_text_runes") || jx.Int(entry, "sent_text_runes") > 6000 || jx.Int(entry, "model_relevance") != 85 || jx.Int(entry, "relevance") != 50 || jx.Str(entry, "admission") != "relevance" {
		t.Fatalf("unexpected trace: %+v", entry)
	}
	raw := string(jx.Marshal(trace.entries))
	for _, secret := range []string{"PRIVATE_TEXT", "PRIVATE_REASON", "example.org", strings.Repeat("x", 30)} {
		if strings.Contains(raw, secret) {
			t.Fatalf("trace includes payload %q", secret)
		}
	}
}

func TestAutopilotPostAuditDetailLimit(t *testing.T) {
	trace := &autopilotPostAudit{}
	for i := 0; i < 20; i++ {
		trace.entries = append(trace.entries, jx.M{"post_hash": strings.Repeat("x", 64), "kind": "text", "outcome": "verdict_completed", "model": strings.Repeat("m", 120), "eligible": true, "model_relevance": 90})
	}
	evidence := jx.M{}
	autopilotAttachPostAudit(evidence, trace, time.Unix(100, 0))
	if jx.Int(evidence, "post_audit_total") != 20 || len(jx.Marshal(evidence["post_audit"])) > 3500 || !autopilotPostAuditHasUseful(trace) {
		t.Fatalf("bad bounded audit: %+v", evidence)
	}
}
