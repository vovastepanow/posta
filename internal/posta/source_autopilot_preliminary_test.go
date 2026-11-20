package posta

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestAutopilotPreliminaryPass(t *testing.T) {
	tests := []struct {
		name                      string
		score, threshold, samples int
		want                      bool
	}{
		{"no samples", 100, 85, 0, false},
		{"single post", 100, 85, 1, false},
		{"two weak posts", 90, 85, 2, false},
		{"two exceptional posts", 95, 85, 2, true},
		{"two posts at capped threshold", 100, 95, 2, true},
		{"three normal posts", 85, 85, 3, true},
		{"three weak posts", 84, 85, 3, false},
		{"invalid threshold", 100, 101, 3, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := autopilotPreliminaryPass(tt.score, tt.threshold, tt.samples)
			if got != tt.want {
				t.Fatalf("autopilotPreliminaryPass(%d, %d, %d) = %t, want %t", tt.score, tt.threshold, tt.samples, got, tt.want)
			}
		})
	}
}

func TestAutopilotSourceReviewInterval(t *testing.T) {
	auto := &autopilotJob{Scope: autopilotAutoScope, Kind: autopilotAutoKind}
	shadow := &autopilotJob{Scope: autopilotShadowScope, Kind: autopilotShadowKind}
	for _, tc := range []struct {
		job   *autopilotJob
		state string
		want  time.Duration
	}{
		{auto, "candidate", 12 * time.Hour},
		{auto, "probation", 6 * time.Hour},
		{auto, "active", 3 * time.Hour},
		{auto, "rejected", 24 * time.Hour},
		{shadow, "active", 24 * time.Hour},
		{nil, "active", 24 * time.Hour},
	} {
		if got := autopilotSourceReviewInterval(tc.job, tc.state); got != tc.want {
			t.Errorf("state=%s job=%+v got=%v want=%v", tc.state, tc.job, got, tc.want)
		}
	}
}

// Auto discovery must inspect several independent platforms even when one
// early backend is already prolific, while staying bounded for a cycle.
func TestAutopilotWideSearchCoversSixPlatformsAndStopsAtTarget(t *testing.T) {
	calls := 0
	backends := make([]func() []DiscoveryCandidate, 10)
	for i := range backends {
		index := i
		backends[i] = func() []DiscoveryCandidate {
			calls++
			found := make([]DiscoveryCandidate, 11)
			for j := range found {
				found[j] = DiscoveryCandidate{Platform: PlatformRSS, ID: fmt.Sprintf("https://example.org/%d/%d.xml", index, j)}
			}
			return found
		}
	}
	out := autopilotRunSearchBackendsAdaptive(context.Background(), 0, backends, 6, 64)
	if calls != 6 || len(out) != 66 {
		t.Fatalf("calls=%d candidates=%d; want six platform calls and 66 candidates", calls, len(out))
	}
	calls = 0
	out = autopilotRunSearchBackendsAdaptive(context.Background(), 4, backends, 6, 64)
	if calls != 6 || len(out) != 66 || out[0].ID != "https://example.org/4/0.xml" {
		t.Fatalf("rotation failed: calls=%d candidates=%d first=%+v", calls, len(out), out[0])
	}
}

func TestAutopilotAdaptiveSearchStopsAfterEnoughUniqueCandidates(t *testing.T) {
	calls := 0
	makeBackend := func(url string) func() []DiscoveryCandidate {
		return func() []DiscoveryCandidate {
			calls++
			return []DiscoveryCandidate{{Platform: PlatformRSS, ID: url}}
		}
	}
	backends := []func() []DiscoveryCandidate{
		makeBackend("https://example.com/a"),
		makeBackend("https://example.com/b"),
		makeBackend("https://example.com/c"),
		makeBackend("https://example.com/d"),
	}
	out := autopilotRunSearchBackendsAdaptive(context.Background(), 0, backends, 2, 2)
	if calls != 2 || len(out) != 2 {
		t.Fatalf("called=%d candidates=%d; want 2,2", calls, len(out))
	}
	calls = 0
	out = autopilotRunSearchBackendsAdaptive(context.Background(), 2, backends, 3, 3)
	if calls != 3 || len(out) != 3 || out[0].ID != "https://example.com/c" {
		t.Fatalf("rotation/candidate goal failed: called=%d out=%+v", calls, out)
	}
}

func TestAutopilotCandidateBacklogGetsReviewPriority(t *testing.T) {
	for _, tc := range []struct {
		due   int
		drain bool
	}{
		{0, false}, {1, true}, {10, true}, {11, true}, {12, true}, {23, true}, {24, true}, {65, true},
	} {
		if got := autopilotShouldDrainCandidateBacklog(tc.due); got != tc.drain {
			t.Errorf("due=%d drain=%v want %v", tc.due, got, tc.drain)
		}
	}
}

func TestAutopilotAssessmentDiagnosticCategorizesFailures(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{errors.New("OpenRouter: free-models-per-day (HTTP 429)"), "provider_rate_limited"},
		{errors.New("OpenRouter HTTP 403"), "provider_forbidden"},
		{errors.New("OpenRouter HTTP 401"), "provider_unauthorized"},
		{errors.New("timed out: context deadline exceeded"), "model_timeout"},
		{errors.New("нет доступной модели"), "no_model_configured"},
		{errors.New("model internal error"), "model_error"},
	}
	for _, tt := range tests {
		if got := autopilotAssessmentCause(tt.err); got != tt.want {
			t.Errorf("cause(%v)=%s, want %s", tt.err, got, tt.want)
		}
	}
	ctx := withAutopilotAssessmentProbe(context.Background())
	autopilotNoteAssessmentCause(ctx, "provider_forbidden")
	if got := autopilotReadAssessmentCause(ctx); got != "provider_forbidden" {
		t.Fatalf("probe=%s", got)
	}
	autopilotResetAssessmentCause(ctx)
	if got := autopilotReadAssessmentCause(ctx); got != "" {
		t.Fatalf("stale cause after reset=%s", got)
	}
}

func TestAutopilotPublicSourceReviewEvidenceWhitelistsFields(t *testing.T) {
	input := []byte(`{"preliminary_score":77,"threshold":80,"http_status":403,"quality_samples":10,"assessment_cause":"provider_forbidden","provider_response":"Bearer secret","private_url":"https://example.test/?key=secret","raw_prompt":"secret"}`)
	got := autopilotPublicSourceReviewEvidence(input)
	if jx.Int(got, "preliminary_score") != 77 || jx.Int(got, "threshold") != 80 ||
		jx.Int(got, "quality_samples") != 10 || jx.Int(got, "http_status") != 403 ||
		jx.Str(got, "assessment_cause") != "provider_forbidden" {
		t.Fatalf("missing review facts: %+v", got)
	}
	for _, field := range []string{"provider_response", "private_url", "raw_prompt"} {
		if _, ok := got[field]; ok {
			t.Fatalf("sensitive field %q leaked: %+v", field, got)
		}
	}
	invalid := autopilotPublicSourceReviewEvidence([]byte(`{"assessment_cause":"raw HTTP response with secrets"}`))
	if _, ok := invalid["assessment_cause"]; ok {
		t.Fatalf("raw error leaked: %+v", invalid)
	}
}

func TestAutopilotAuditEvidenceContainsActualPublicPostsOnly(t *testing.T) {
	long := "Вступление: обзор SMM. " + strings.Repeat("Практический пример продвижения. ", 40) + "Вывод: измеряем эффективность контента."
	posts := []jx.M{
		{"text": long, "url": "https://example.org/posts/10?signature=private", "posted_at": int64(1795120000)},
		{"text": "Скрытая запись", "source_sensitive": true, "url": "https://example.org/private"},
		{"text": "Короткий медиапост", "image": "https://cdn.example.org/media?token=private", "url": "https://example.org/posts/11"},
		{"text": "Короткий медиапост", "image": "https://cdn.example.org/media?token=private", "url": "https://example.org/posts/11"},
		{"text": "Неподходящая ссылка", "url": "http://user:password@example.org/post"},
	}
	got := autopilotAuditPostSamples(posts)
	if len(got) != 3 {
		t.Fatalf("expected three safe, unique, public samples; got %+v", got)
	}
	if first := jx.Str(got[0], "excerpt"); !strings.Contains(first, "Вступление") ||
		!strings.Contains(first, "Вывод: измеряем эффективность контента.") || !strings.Contains(first, "[…]") {
		t.Fatalf("lost beginning/conclusion: %q", first)
	}
	if jx.Str(got[0], "url") != "https://example.org/posts/10" ||
		jx.Str(got[2], "url") != "" {
		t.Fatalf("unsafe query/credentials leaked: %+v", got)
	}
	exposed := autopilotPublicSourceReviewEvidence(jx.Marshal(jx.M{
		"review_samples": got,
		"raw_secret":     "private token",
	}))
	if len(jx.Maps(exposed, "review_samples")) != 3 {
		t.Fatalf("audit samples missing from export: %+v", exposed)
	}
	if _, exists := exposed["raw_secret"]; exists {
		t.Fatal("raw evidence leaked")
	}
	if strings.Contains(string(jx.Marshal(exposed)), "signature=private") {
		t.Fatalf("private query leaked in audit bundle: %+v", exposed)
	}
}
