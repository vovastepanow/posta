package posta

import (
	"posta/internal/config"
	"strings"

	"posta/internal/jx"
)

// The relevance judge sees the requested topic. Check its labels separately,
// without the collection topic or source name, using the actual topic meanings.
func (u *U) verifyContentTags(raw any, description, caption string, private bool) []string {
	tags := u.classifyContent(description, caption, private)
	if tags == nil {
		if !private {
			return nil
		}
		tags = []string{}
	}
	safety := []string{}
	for _, key := range u.ContentTagsClean(raw) {
		if tagInfo[key].NSFW && !contains(safety, key) {
			safety = append(safety, key)
		}
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
		if jx.Bool(entry, "matches") && runeLen(quote) >= 12 && strings.Contains(text, quote) {
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
