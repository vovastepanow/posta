package posta

import (
	"regexp"
	"slices"

	"posta/internal/jx"
)

var maxCommentIDPattern = regexp.MustCompile(`^(mid[.])?[A-Za-z0-9_-]{1,256}$`)

// The parent receipt always remains successful even if the optional comment
// is rejected or uncertain. Never turn this into a retry of the parent post.
func firstCommentWarning(platform string) string {
	return platform + ": пост опубликован, но первый комментарий не подтверждён. Проверьте комментарии; не публикуйте основной пост повторно."
}

func ChannelExtraKinds(c *Channel) []string {
	if c == nil {
		return []string{}
	}
	kinds := postExtraKinds(c.Platform)
	var excluded string
	switch c.Platform {
	case "instagram":
		if jx.Str(c.Meta, "account_type") != "BUSINESS" {
			excluded = "ig_story"
		}
	case "max":
		if typ := jx.Str(c.Meta, "chat_type"); typ != "" && typ != "channel" {
			excluded = "first_comment"
		}
	}
	if excluded == "" {
		return kinds
	}
	// postExtraKinds returns a fresh slice for each call.
	return slices.DeleteFunc(kinds, func(kind string) bool { return kind == excluded })
}
