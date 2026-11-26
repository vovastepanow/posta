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
		typ := strings.ToLower(strings.TrimSpace(strings.Split(f.Type, ";")[0]))
		raw := rssHTTPS(f.URL, base)
		if raw == "" {
			if f.Medium == "video" || strings.HasPrefix(typ, "video/") {
				incomplete = true
			}
			continue
		}
		if seen[raw] { continue }
		u, _ := url.Parse(raw)
		ext := strings.ToLower(path.Ext(u.Path))
		isVideo := strings.HasPrefix(typ, "video/") || f.Medium == "video" || (typ == "" && (ext == ".mp4" || ext == ".webm" || ext == ".mov"))
		switch {
		case isVideo:
			if typ == "video/mp4" || typ == "video/webm" || (typ == "" && (ext == ".mp4" || ext == ".webm")) {
				m := jx.M{"video": raw}
				// A global thumbnail is ambiguous with multiple video
				// enclosures. Attach it only to the first video.
				localPoster:=rssHTTPS(f.Poster,base)
				if !rssImageOK(localPoster){localPoster=""}
				if firstVideo == "" {
					firstVideo = raw
					if localPoster==""{localPoster=poster}
				}
				if localPoster!=""{m["image"]=localPoster}
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

// Prefer a supported complete representation marked isDefault. When the
// declared default is unsupported, a supported alternative is preferable.
func rssMediaGroupChoice(group *xnode) *xnode {
	var best *xnode
	bestScore:=-1
	for _,n:=range group.kids("media:content"){
		if n.A["url"]==""&&n.A["href"]==""{continue}
		t:=strings.ToLower(strings.TrimSpace(strings.Split(n.A["type"],";")[0]))
		raw:=n.A["url"];if raw==""{raw=n.A["href"]}
		score:=0
		switch{
		case t=="video/mp4" || t=="video/webm":score=10
		case strings.HasPrefix(t,"image/") && rssImageOK(raw):score=10
		case t=="" && (imageExtRe.MatchString(raw)||strings.HasSuffix(strings.ToLower(raw),".mp4")||strings.HasSuffix(strings.ToLower(raw),".webm")):score=9
		}
		if strings.EqualFold(n.A["expression"],"sample"){score-=3}
		if strings.EqualFold(n.A["isDefault"],"true"){score++}
		if best==nil||score>bestScore{best,bestScore=n,score}
	}
	return best
}
