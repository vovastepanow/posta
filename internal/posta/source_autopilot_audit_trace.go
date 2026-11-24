package posta

import (
	"context"
	"time"

	"posta/internal/jx"
)

type autopilotPostAuditKey struct{}

type autopilotPostAudit struct {
	entries []jx.M
	byHash  map[string]jx.M
}

// A bounded trace lives only in the public source review context. It stores
// no payloads, prompts, source URLs, user IDs or raw provider errors.
// Prepared media frames are not claimed to be actual provider input.
func newAutopilotPostAudit(ctx context.Context, posts []jx.M) (context.Context, *autopilotPostAudit) {
	trace := &autopilotPostAudit{entries: make([]jx.M, 0, min(len(posts), 20)), byHash: map[string]jx.M{}}
	for _, post := range posts {
		if len(trace.entries) >= 20 {
			break
		}
		key := CandidatePostKey(post)
		if key == "" {
			continue
		}
		hash := repeatDigest(key)
		if trace.byHash[hash] != nil {
			continue
		}
		kind := "text"
		if PostItemHasVideo(post) {
			kind = "video"
		} else if ContentImageKey(post) != "" {
			kind = "image"
		}
		entry := jx.M{"post_hash": hash, "kind": kind, "outcome": "not_attempted",
			"normalized_text_runes": len([]rune(AnalysisText(post))), "published_at": jx.Int(post, "posted_at")}
		trace.entries = append(trace.entries, entry)
		trace.byHash[hash] = entry
	}
	return context.WithValue(ctx, autopilotPostAuditKey{}, trace), trace
}

func autopilotPostAuditEntry(ctx context.Context, post jx.M) jx.M {
	if trace, ok := ctx.Value(autopilotPostAuditKey{}).(*autopilotPostAudit); ok {
		if entry := trace.byHash[repeatDigest(CandidatePostKey(post))]; entry != nil {
			return entry
		}
	}
	return jx.M{}
}

func autopilotPostAuditVerdict(entry, analysis jx.M, relevant, eligible, ad bool, threshold int, admission string) {
	entry["outcome"] = "verdict_completed"
	entry["relevant"], entry["eligible"], entry["advertising"] = relevant, eligible, ad
	entry["threshold"] = threshold
	for _, key := range []string{"model_relevance", "relevance", "interest", "practical_value", "specificity"} {
		if validModelPercent(analysis[key]) {
			entry[key] = jx.Float(analysis, key)
		}
	}
	if model := jx.Str(analysis, "model"); model != "" {
		entry["model"] = trimRunes(model, 120)
	}
	// Do not persist arbitrary rejection messages or model explanations.
	switch {
	case eligible:
		entry["admission"] = "eligible"
	case ad:
		entry["admission"] = "advertising"
	case admission == "спам":
		entry["admission"] = "spam"
	case !relevant:
		entry["admission"] = "relevance"
	case admission == "нет конкретной пользы, фактов или интересного вывода":
		entry["admission"] = "professional_value"
	default:
		entry["admission"] = "other_content_filter"
	}
}

// Retain aggregate coverage of every entry but bound detail bytes. The
// complete in-memory sample, not this truncated diagnostic, drives policy.
func autopilotAttachPostAudit(evidence jx.M, trace *autopilotPostAudit, at time.Time) {
	counts := map[string]int{}
	kinds := map[string]int{}
	details := make([]jx.M, 0)
	for _, entry := range trace.entries {
		counts[jx.Str(entry, "outcome")]++
		kinds[jx.Str(entry, "kind")]++
		candidate := append(details, entry)
		if len(jx.Marshal(candidate)) <= 3500 {
			details = candidate
		}
	}
	evidence["post_audit"] = details
	evidence["post_audit_at"] = at.Unix()
	evidence["post_audit_outcomes"] = counts
	evidence["post_audit_kinds"] = kinds
	evidence["post_audit_total"] = len(trace.entries)
}

func autopilotPostAuditHasUseful(trace *autopilotPostAudit) bool {
	for _, entry := range trace.entries {
		if jx.Bool(entry, "eligible") || jx.Bool(entry, "cached_eligible") {
			return true
		}
	}
	return false
}
