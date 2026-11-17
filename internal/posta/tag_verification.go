package posta

import (
	"posta/internal/config"
	"regexp"
	"strings"

	"posta/internal/jx"
)

// The relevance judge sees the requested topic. Check its labels separately,
// without the collection topic or source name, using the actual topic meanings.
func (u *U) verifyContentTags(raw any, description, caption string, private bool) []string {
	safety := u.contentSafetyTags(raw)
	// Newly detected sensitive content must not be sent to another external call.
	if len(safety) > 0 {
		private = true
	}
	tags := u.classifyContent(description, caption, private)
	if tags == nil {
		if !private {
			return nil
		}
		tags = []string{}
	}
	if len(tags)+len(safety) > config.ContentTagsMax {
		tags = tags[:max(0, config.ContentTagsMax-len(safety))]
	}
	tags = append(tags, safety...)
	return tags
}

func (u *U) confirmedContentTags(tags []string, response jx.M, text string) []string {
	normalize := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(s, "ё", "е"))), " ")
	}
	text = normalize(text)
	confirmed := map[string]bool{}
	for _, entry := range jx.Maps(response, "verified") {
		quote := normalize(jx.Str(entry, "evidence"))
		if jx.Bool(entry, "matches") && runeLen(quote) >= 12 && contentEvidenceGrounded(text, quote) {
			confirmed[jx.Str(entry, "tag")] = true
		}
	}
	out := []string{}
	for _, key := range tags {
		if tagInfo[key].NSFW || confirmed[key] {
			out = append(out, key)
		}
	}
	return out
}

var contentMetadataToken = regexp.MustCompile(`(^|[^\pL\pN_])([#@][\pL\pN_]+|https?://[^\s]+)`)

// A literal quote is necessary, but metadata alone is not evidence of subject.
// This is a provenance check, not a keyword-based semantic classifier.
func contentEvidenceGrounded(text, quote string) bool {
	normalize := func(s string) string {
		return strings.Join(strings.Fields(strings.ToLower(strings.ReplaceAll(s, "ё", "е"))), " ")
	}
	text, quote = normalize(text), normalize(quote)
	if quote == "" || !strings.Contains(text, quote) {
		return false
	}
	text = normalize(contentMetadataToken.ReplaceAllString(text, "$1 "))
	quote = normalize(contentMetadataToken.ReplaceAllString(quote, "$1 "))
	return runeLen(quote) >= 4 && strings.Contains(text, quote)
}

func (u *U) contentSafetyTags(raw any) []string {
	out := []string{}
	for _, key := range u.ContentTagsClean(raw) {
		if tagInfo[key].NSFW && !contains(out, key) {
			out = append(out, key)
		}
	}
	return out
}

// Topic relevance and optional content labels are independent decisions. A
// provider outage must not erase a valid relevance analysis or its safety flags.
func (u *U) applyContentClassification(result jx.M, description, caption string, private bool) {
	raw := result["tags"]
	tags := u.verifyContentTags(raw, description, caption, private)
	pending := tags == nil && !private
	if pending {
		tags = u.contentSafetyTags(raw)
	}
	result["tags"] = tags
	result["tags_classification_pending"] = pending
	if pending {
		reason := u.classificationReason
		if reason == "" {
			reason = "unresolved"
		}
		result["tags_classification_reason"] = reason
	} else {
		delete(result, "tags_classification_reason")
	}
}

// The collector's relevance analysis can use a storyboard, but topic labels
// must be based on the complete video and its sound.
func (u *U) applyVideoClassification(result jx.M, item jx.M, localVideo, caption string, private bool) {
	safety := u.contentSafetyTags(result["tags"])
	if private || jx.Bool(item, "source_sensitive") || jx.Bool(item, "nsfw") || len(jx.ToStrs(item["safety"])) > 0 || len(safety) > 0 {
		result["tags"] = safety
		result["tags_classification_pending"] = true
		result["tags_classification_reason"] = "sensitive_media"
		return
	}
	u.classificationMediaSafety = nil
	var tags []string
	var unsafe bool
	if localVideo != "" {
		tags, unsafe = u.classifyPublicVideoFile(localVideo, caption)
	} else {
		tags, unsafe = u.classifyPublicVideo(jx.Str(item, "video"), caption)
	}
	if unsafe {
		result["tags"] = u.contentAnalysisTags("", safety, u.classificationMediaSafety)
		result["tags_classification_pending"] = true
		result["tags_classification_reason"] = "sensitive_media"
		return
	}
	result["tags"] = tags
	result["tags_classification_pending"] = tags == nil
	if tags == nil {
		reason := u.classificationReason
		if reason == "" {
			reason = "unresolved"
		}
		result["tags_classification_reason"] = reason
	} else {
		delete(result, "tags_classification_reason")
	}
}
