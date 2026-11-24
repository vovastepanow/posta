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

func TestAutopilotSourceTopicVerdicts(t *testing.T) {
	tests := []struct {
		name, reply            string
		expected, score, known int
		pass, reject           bool
	}{
		{"fully relevant", `{"verdicts":["relevant","relevant","relevant"]}`, 3, 100, 3, true, false},
		{"mixed useful source", `{"verdicts":["relevant","relevant","relevant","adjacent","off_topic"]}`, 5, 70, 5, true, false},
		{"clearly unrelated", `{"verdicts":["off_topic","off_topic","off_topic","off_topic","relevant"]}`, 5, 20, 5, false, true},
		{"adjacent is not off-topic", `{"verdicts":["adjacent","adjacent","adjacent","adjacent","adjacent"]}`, 5, 50, 5, false, false},
		{"insufficient readable coverage", `{"verdicts":["relevant","relevant","unknown","unknown","unknown"]}`, 5, 50, 0, false, false},
		{"insufficient negative sample", `{"verdicts":["off_topic","off_topic","off_topic","unknown","unknown"]}`, 5, 50, 0, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev := autopilotSourceTopicVerdicts(tt.reply, tt.expected)
			if ev == nil || ev.Score != tt.score || ev.ReviewedSamples != tt.known {
				t.Fatalf("evaluation=%+v, want score=%d reviewed=%d", ev, tt.score, tt.known)
			}
			pass, reject := autopilotSourcePreliminaryDecision(ev, 85, tt.expected)
			if pass != tt.pass || reject != tt.reject {
				t.Fatalf("pass=%t reject=%t, want %t %t", pass, reject, tt.pass, tt.reject)
			}
		})
	}
	for _, reply := range []string{`{"verdicts":["relevant"]}`, `{"verdicts":["relevant","invalid","off_topic"]}`, `{"score":100}`} {
		if ev := autopilotSourceTopicVerdicts(reply, 3); ev != nil {
			t.Fatalf("accepted malformed verdicts: %+v", ev)
		}
	}
}

func TestAutopilotPreliminaryReject(t *testing.T) {
	tests := []struct {
		name                      string
		score, threshold, samples int
		want                      bool
	}{
		{"two poor posts are inconclusive", 10, 85, 2, false},
		{"four poor posts are inconclusive", 10, 85, 4, false},
		{"near threshold is inconclusive", 84, 85, 10, false},
		{"clear mismatch with sufficient sample", 55, 85, 5, true},
		{"clear mismatch boundary", 75, 85, 5, true},
		{"invalid negative score", -1, 85, 5, false},
		{"invalid threshold", 0, 101, 5, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := autopilotPreliminaryReject(tt.score, tt.threshold, tt.samples); got != tt.want {
				t.Fatalf("got %t, want %t", got, tt.want)
			}
		})
	}
}

func TestAutopilotRutubeChannelPosts(t *testing.T) {
	body := []byte(`{"results":[{"id":"one","title":"Public post","author":{"id":123,"name":"Publisher"}},{"id":"two","title":"Foreign post","author":{"id":456,"name":"Other"}},{"id":"three","title":"Sensitive post","is_adult":true,"author":{"id":123,"name":"Publisher"}}]}`)
	posts, err := autopilotRutubeChannelPosts(body, "123")
	if err != nil || len(posts) != 1 || jx.Str(posts[0], "source") != "Publisher" || jx.Str(posts[0], "source_key") != "rutube:123" {
		t.Fatalf("unexpected channel posts: %+v, err=%v", posts, err)
	}
	if _, err := autopilotRutubeChannelPosts([]byte(`{"error":"unavailable"}`), "123"); err == nil {
		t.Fatal("missing results must not be interpreted as sparse publisher")
	}
	if _, err := autopilotRutubeChannelPosts(body, "999"); err == nil {
		t.Fatal("unrelated authors must not be accepted as this channel")
	}
	if posts, err := autopilotRutubeChannelPosts([]byte(`{"results":[]}`), "123"); err != nil || len(posts) != 0 {
		t.Fatalf("valid empty channel listing: %+v, err=%v", posts, err)
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

func TestSMMSourceReviewGuidanceSeparatesAdjacentSubjects(t *testing.T) {
	prompt := sourceReviewTopicGuidance("SMM и блогинг")
	for _, word := range []string{"таргетированной рекламе", "вовлечённости", "НЕ являются SMM", "юридические консультации", "НЕСКОЛЬКИМ", "контекстная реклама"} {
		if !strings.Contains(prompt, word) {
			t.Fatalf("SMM boundary missing %q from model guidance", word)
		}
	}
	if sourceReviewTopicGuidance("Кошки") != "" {
		t.Fatal("SMM criteria must not leak into unrelated source topics")
	}
}

func TestAutopilotSearchInterleavesBackendRanksAndDeduplicates(t *testing.T) {
	tg := func(id string) DiscoveryCandidate { return DiscoveryCandidate{Platform: PlatformTelegram, ID: id} }
	groups := [][]DiscoveryCandidate{
		{tg("first_digest"), tg("second_digest"), tg("third_digest")},
		{tg("FIRST_DIGEST"), {Platform: PlatformRSS, ID: "https://example.org/top.xml"}},
		{{Platform: PlatformVK, ID: "public123"}, tg("invalidbot")},
	}
	got := autopilotInterleaveSearchResults(groups)
	want := []string{"telegram:first_digest", "vk:public123", "telegram:second_digest", "rss:https://example.org/top.xml", "telegram:third_digest"}
	if len(got) != len(want) {
		t.Fatalf("candidate count %d: %+v", len(got), got)
	}
	for i, c := range got {
		id, ok := autopilotCandidateID(c)
		if !ok || id != want[i] {
			t.Fatalf("rank %d: %q, want %q", i, id, want[i])
		}
	}
}
