package posta

import (
	"net/url"
	"strings"

	"posta/internal/jx"
)

// Some editorial RSS articles contain their published body and a cover
// image. For topics allowing text, assess the body as a text publication.
// Never claim the discarded image was checked or attach it to delivery.
// A caption, summary, video or an unrelated publisher URL cannot use this
// path. The full post admission and owner filters still apply.
func autopilotTextArticleAlternative(source string, post jx.M, allowText bool) jx.M {
	if !allowText || !jx.Bool(post, "article_source") || !jx.Bool(post, "rss_article_body") ||
		ContentImageKey(post) == "" || PostItemHasVideo(post) ||
		jx.Str(post, "video_page") != "" || jx.Str(post, "video_via") != "" || jx.Bool(post, "video_unavailable") ||
		jx.Bool(post, "source_sensitive") || jx.Bool(post, "nsfw") || len(jx.ToStrs(post["safety"])) > 0 ||
		!strings.HasPrefix(source, "rss:") || runeLen(AnalysisText(post)) < 800 {
		return nil
	}
	feed, ferr := url.Parse(strings.TrimPrefix(source, "rss:"))
	link, lerr := url.Parse(jx.Str(post, "url"))
	if ferr != nil || lerr != nil || feed.Hostname() == "" || link.Hostname() == "" ||
		!autopilotPublicPostURL(jx.Str(post, "url"), false) || RSSFeedKind(feed.String()) != "RSS" ||
		!strings.EqualFold(strings.TrimPrefix(feed.Hostname(), "www."), strings.TrimPrefix(link.Hostname(), "www.")) {
		return nil
	}
	copy := jx.Copy(post)
	for _, field := range []string{"image", "images", "image_fallback", "gallery", "media", "image_local", "video", "video_local", "phash", "repeat_file_hashes"} {
		delete(copy, field)
	}
	copy["text_post"] = true
	copy["autopilot_article_text_only"] = true
	return copy
}
