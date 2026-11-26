package posta

import (
	"net/url"
	"path"
	"strings"

	"posta/internal/jx"
)

// A thumbnail is a poster, not an additional attachment. Explicit enclosure
// URLs take precedence over description images, which may contain tracking
// pixels or duplicates. Keep unsupported declared videos visibly incomplete.
func rssOrderedMedia(files []rssFile, base string, fallbackImages []string) ([]jx.M, string, bool) {
	var media []jx.M
	seen := map[string]bool{}
	poster := ""
	for _, f := range files {
		if f.Medium == "thumbnail" {
			poster = rssHTTPS(f.URL, base)
			if poster != "" && rssImageOK(poster) {
				break
			}
			poster = ""
		}
	}
	firstVideo := ""
	incomplete := false
	for _, f := range files {
		if f.Medium == "thumbnail" {
			continue
		}
		raw := rssHTTPS(f.URL, base)
		if raw == "" || seen[raw] {
			continue
		}
		typ := strings.ToLower(strings.TrimSpace(strings.Split(f.Type, ";")[0]))
		u, _ := url.Parse(raw)
		ext := strings.ToLower(path.Ext(u.Path))
		isVideo := strings.HasPrefix(typ, "video/") || f.Medium == "video" || (typ == "" && (ext == ".mp4" || ext == ".webm" || ext == ".mov"))
		switch {
		case isVideo:
			if typ == "video/mp4" || typ == "video/webm" || (typ == "" && (ext == ".mp4" || ext == ".webm")) {
				m := jx.M{"video": raw}
				// A global thumbnail is ambiguous with multiple video
				// enclosures. Attach it only to the first video.
				if firstVideo == "" {
					firstVideo = raw
					if poster != "" { m["image"] = poster }
				}
				media = append(media, m)
				seen[raw] = true
			} else {
				incomplete = true
			}
		case rssIsImage(f) && rssImageOK(raw):
			raw = rssUpgradeImageURL(raw)
			if !seen[raw] {
				media = append(media, jx.M{"image": raw})
				seen[raw] = true
			}
		}
	}
	if len(media) == 0 && !incomplete {
		for _, raw := range fallbackImages {
			if raw != "" && !seen[raw] {
				media = append(media, jx.M{"image": raw})
				seen[raw] = true
			}
		}
	}
	return media, firstVideo, incomplete
}
