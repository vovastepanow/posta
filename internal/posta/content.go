package posta

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"math/bits"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	xdraw "golang.org/x/image/draw"

	"posta/internal/config"
	"posta/internal/imgx"
	"posta/internal/jx"
)

// ContentImageKey — запись определяется адресом своей картинки (у видео — превью).
func ContentImageKey(item jx.M) string {
	if item == nil {
		return ""
	}
	if v, ok := item["image"]; ok && v != nil {
		s, _ := v.(string)
		return s
	}
	if imgs := jx.List(item, "images"); len(imgs) > 0 {
		s, _ := imgs[0].(string)
		return s
	}
	return ""
}

// ContentItemKey addresses a record, not just its poster. Keep photo keys
// compatible with older clients, but distinguish videos sharing a thumbnail.
func ContentItemKey(item jx.M) string {
	if item == nil {
		return ""
	}
	// A publisher may reuse its logo or cover across unrelated articles.
	// Use the original article identity, not that shared decoration.
	if len(jx.Map(item, "article")) > 0 || jx.Bool(item, "article_source") {
		if key := CandidatePostKey(item); key != "" {
			return key
		}
	}
	if PostItemHasVideo(item) || jx.Str(item, "video_via") != "" || jx.Bool(item, "video_unavailable") {
		// Use the stable post or page identity even after the worker replaces
		// the remote video with a local cache path.
		if key := CandidatePostKey(item); key != "" {
			return key
		}
		if page := jx.Str(item, "video_page"); page != "" {
			return "video:" + page
		}
		if video := jx.Str(item, "video"); video != "" {
			return "video:" + video
		}
		for _, f := range ContentGallery(item) {
			if video := jx.Str(f, "video"); video != "" {
				return "video:" + video
			}
		}
	}
	if key := ContentImageKey(item); key != "" {
		return key
	}
	if key := CandidatePostKey(item); key != "" {
		return key
	}
	if key := CandidateTextKey(item); key != "" {
		return key
	}
	if jx.Bool(item, "text_post") {
		if text := strings.TrimSpace(jx.Str(item, "text")); text != "" && text != EmptyPostText {
			return "short:" + repeatDigest(text)
		}
	}
	return ""
}

// Old queue entries stored a thumbnail instead of the record key. Only
// resolve a legacy alias if it is unambiguous in the user's history.
func historyCanonicalKey(h History, key string) string {
	resolved := ""
	for _, item := range h.All() {
		canonical := ContentItemKey(item)
		if canonical == "" || (canonical != key && ContentImageKey(item) != key) {
			continue
		}
		if resolved != "" && resolved != canonical {
			return ""
		}
		resolved = canonical
	}
	return resolved
}

func PoolFindItem(pool []jx.M, key string) jx.M {
	if key == "" {
		return nil
	}
	for _, it := range pool {
		if ContentItemKey(it) == key {
			return it
		}
	}
	return nil
}

func PoolWithoutImage(pool []jx.M, key string) []jx.M {
	out := []jx.M{}
	for _, it := range pool {
		if ContentItemKey(it) != key {
			out = append(out, it)
		}
	}
	return out
}

// poolUnavailableVideo — ролик окончательно не загрузился и не может быть показан в подборе.
func poolUnavailableVideo(it jx.M) bool {
	return jx.Str(it, "video_via") == "ytdlp" && jx.Bool(it, "video_unavailable")
}

// PruneUnavailablePoolVideos removes failed videos, including records left by older versions.
func (u *U) PruneUnavailablePoolVideos() {
	before := u.Pool()
	if !hasUnavailablePoolVideo(before) {
		return
	}
	after, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
		if !hasUnavailablePoolVideo(pool) {
			return nil
		}
		out := make([]jx.M, 0, len(pool))
		for _, it := range pool {
			if !poolUnavailableVideo(it) {
				out = append(out, it)
			}
		}
		return out
	})
	if err == nil {
		u.MediaReleaseRemoved(u.Ctx, before, after)
	}
}

func hasUnavailablePoolVideo(pool []jx.M) bool {
	for _, it := range pool {
		if poolUnavailableVideo(it) {
			return true
		}
	}
	return false
}

// poolVideoCacheLost checks the actual record, not its cover URL. A missing
// cover is shared by text posts and coverless videos and is not an identity.
func (a *App) poolVideoCacheLost(it jx.M) bool {
	missing := func(f jx.M) bool {
		local := jx.Str(f, "video_local")
		return jx.Str(f, "video") != "" && local != "" && a.MediaExisting(local) == ""
	}
	if missing(it) {
		return true
	}
	for _, f := range jx.Maps(it, "gallery") {
		if missing(f) {
			return true
		}
	}
	return false
}

// PoolWithoutLostVideo removes only records whose cached video is missing.
// Recheck under the content lock in case the worker repaired a file meanwhile.
func (u *U) PoolWithoutLostVideo(pool []jx.M) []jx.M {
	filter := func() ([]jx.M, bool) {
		out := make([]jx.M, 0, len(pool))
		hasLost := false
		for _, it := range pool {
			if u.poolVideoCacheLost(it) {
				hasLost = true
			} else if !poolUnavailableVideo(it) {
				out = append(out, it)
			}
		}
		return out, hasLost
	}
	out, hasLost := filter()
	if !hasLost {
		return out
	}
	u.PoolUpdate(func(all []jx.M) []jx.M {
		kept := make([]jx.M, 0, len(all))
		for _, it := range all {
			if !u.poolVideoCacheLost(it) {
				kept = append(kept, it)
			}
		}
		if len(kept) == len(all) {
			return nil
		}
		return kept
	})
	out, _ = filter()
	return out
}

// PoolOrder — порядок показа: по свежести поста, подряд не больше одного поста источника (если в окне есть другой).
// Темы не смешиваются; отложенные («✋») — после остальных. card_score порядок не решает.
func PoolOrder(pool []jx.M) []jx.M {
	return poolOrderWithPreference(pool, nil)
}

// Personal preferences adjust freshness by only a small bounded interval;
// the existing topic groups, postponement and source diversity still apply.
func poolOrderWithPreference(pool []jx.M, bonus func(jx.M) int64) []jx.M {
	type entry struct {
		item               jx.M
		source             string
		score, postponedAt int64
		postponed          bool
	}
	var order []string
	groups := map[string][]entry{}
	for _, it := range pool {
		t := jx.Str(it, "topic")
		if _, ok := groups[t]; !ok {
			order = append(order, t)
		}
		fresh := jx.Int(it, "posted_at")
		if fresh <= 0 {
			fresh = jx.Int(it, "pooled_at")
		}
		postponed := jx.Bool(it, "postponed_at")
		if bonus != nil && !postponed {
			fresh += bonus(it)
		}
		groups[t] = append(groups[t], entry{
			item: it, source: ContentSourceKey(it), score: fresh,
			postponedAt: jx.Int(it, "postponed_at"), postponed: postponed,
		})
	}
	out := make([]jx.M, 0, len(pool))
	for _, t := range order {
		g := groups[t]
		sort.SliceStable(g, func(a, b int) bool {
			if g[a].postponedAt != g[b].postponedAt {
				return g[a].postponedAt < g[b].postponedAt
			}
			return g[a].score > g[b].score
		})
		last := ""
		hasLast := false
		for len(g) > 0 {
			pick := 0
			if hasLast && g[0].source == last {
				for k := 1; k < len(g) && k <= config.PoolDiversityWindow; k++ {
					if g[k].source != last && g[k].postponed == g[0].postponed {
						pick = k
						break
					}
				}
			}
			chosen := g[pick]
			// Shift only the bounded prefix, not every remaining entry.
			copy(g[1:pick+1], g[:pick])
			g = g[1:]
			last, hasLast = chosen.source, true
			out = append(out, chosen.item)
		}
	}
	return out
}

func ContentSourceKey(item jx.M) string {
	if k := jx.Str(item, "source_key"); k != "" {
		return k
	}
	if b := jx.Str(item, "board"); b != "" {
		return "dvach:" + b
	}
	return "source:" + strings.ToLower(jx.Str(item, "source"))
}

// ContentGallery — все файлы поста; пусто, если файл один.
func ContentGallery(item jx.M) []jx.M {
	var g []jx.M
	for _, f := range jx.Maps(item, "gallery") {
		img, v := jx.Str(f, "image"), jx.Str(f, "video")
		if img == "" && v == "" {
			continue
		}
		e := jx.M{"image": img, "video": v}
		for _, k := range []string{"image_local", "video_local"} {
			if s, ok := f[k].(string); ok && s != "" {
				e[k] = s
			}
		}
		g = append(g, e)
	}
	if len(g) > 1 {
		return g
	}
	return []jx.M{}
}

// ContentHistoryRecord — запись для истории оценок.
func (a *App) ContentHistoryRecord(item jx.M, complaintReason any) jx.M {
	rec := jx.M{"image": ContentImageKey(item), "text": jx.Str(item, "text"), "text_html": item["text_html"], "forwarded": item["forwarded"], "forwarded_from": item["forwarded_from"], "forwarded_url": item["forwarded_url"], "description": item["description"], "why_relevant": item["why_relevant"],
		"relevance": item["relevance"], "rating_assigned": item["rating"], "format": jx.Str(item, "format"), "tags": a.ContentTagsClean(item["tags"]),
		"entity": jx.Str(item, "entity"), "source_hashtags": orList(jx.List(item, "source_hashtags")), "topic": item["topic"], "source": item["source"],
		"source_key": ContentSourceKey(item), "board": item["board"], "thread_id": item["thread_id"], "post_id": item["post_id"],
		"source_url": SourcePostURL(item), "posted_at": item["posted_at"], "engagement": item["engagement"], "complaint_reason": complaintReason,
		"video": jx.Str(item, "video"), "video_via": jx.Str(item, "video_via"), "video_page": jx.Str(item, "video_page"), "image_local": jx.Str(item, "image_local"), "video_local": jx.Str(item, "video_local"),
		"phash": jx.Str(item, "phash"), "repeat_file_hashes": item["repeat_file_hashes"], "text_post": item["text_post"], "gallery": ContentGallery(item), "timestamp": now()}
	for _, k := range []string{"url", "article", "article_text", "article_source", "title", "source_sensitive", "nsfw", "safety", "interest", "practical_value", "specificity", "evidence", "analysis_model", "analysis_evidence", "media_incomplete", "text_incomplete", "video_unavailable", "video_error", "expected_video_count", "source_attachments", "attachment_inventory_incomplete", "attachment_recovered", "fetched_via"} {
		if v, ok := item[k]; ok {
			rec[k] = v
		}
	}
	if g := GeoClean(item["geo"]); g != nil {
		rec["geo"] = g
	}
	if jx.Has(item, "pooled_at") {
		rec["pooled_at"] = item["pooled_at"] // когда попал в пул: по нему студия строит «город»
	}
	for _, k := range []string{"description", "why_relevant", "topic", "source", "board", "thread_id", "post_id"} {
		if rec[k] == nil {
			rec[k] = ""
		}
	}
	if rec["posted_at"] == nil {
		rec["posted_at"] = 0
	}
	return rec
}

func orList(l []any) []any {
	if l == nil {
		return []any{}
	}
	return l
}

var dvachSourceRe = regexp.MustCompile(`(?i)Двач\s+/([a-z0-9_-]+)/`)

// SourcePostURL — ссылка на исходный пост: сохранённая (известные площадки по HTTPS) или собранная из полей Двача.
func SourcePostURL(item jx.M) string {
	// Prefer the post permalink over a channel URL when the parser has both.
	for _, link := range []string{jx.Str(item, "url"), jx.Str(item, "source_url")} {
		if link == "" {
			continue
		}
		probe := jx.Copy(item)
		probe["source_url"] = link
		if IsTrustedSourceURL(link) || RSSPostURLTrusted(probe) || federatedPostURLTrusted(probe) {
			return link
		}
	}
	board := jx.Str(item, "board")
	if board == "" {
		if m := dvachSourceRe.FindStringSubmatch(jx.Str(item, "source")); m != nil {
			board = m[1]
		}
	}
	thread, post := jx.Str(item, "thread_id"), jx.Str(item, "post_id")
	if IsValidDvachBoard(board) && IsValidDvachID(thread) && IsValidDvachID(post) {
		return DvachPostURL(board, thread, post)
	}
	return ""
}

// Fediverse post URLs can belong to different public instances. They are only
// exposed as HTTPS links, never fetched here; reject local or IP destinations.
func federatedPostURLTrusted(item jx.M) bool {
	key := jx.Str(item, "source_key")
	if !strings.HasPrefix(key, "mastodon:") && !strings.HasPrefix(key, "lemmy:") {
		return false
	}
	u, err := url.Parse(jx.Str(item, "source_url"))
	if err != nil || u.Scheme != "https" || u.User != nil || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return strings.Contains(h, ".") && net.ParseIP(h) == nil && !strings.HasSuffix(h, ".local") && !strings.HasSuffix(h, ".internal")
}

func PostItemHasVideo(item jx.M) bool {
	for _, f := range jx.Maps(item, "source_attachments") {
		if jx.Str(f, "kind") == "video" {
			return true
		}
	}
	if strings.TrimSpace(jx.Str(item, "video")) != "" ||
		(jx.Str(item, "video_via") != "" && jx.Str(item, "video_page") != "" && !jx.Bool(item, "video_unavailable")) {
		return true
	}
	for _, f := range ContentGallery(item) {
		if strings.TrimSpace(jx.Str(f, "video")) != "" {
			return true
		}
	}
	return false
}

func PostItemHasMedia(item jx.M) bool {
	if len(jx.Maps(item, "source_attachments")) > 0 {
		return true
	}
	if strings.TrimSpace(jx.Str(item, "image")) != "" || strings.TrimSpace(jx.Str(item, "video")) != "" || jx.Str(item, "video_via") != "" {
		return true
	}
	for _, s := range jx.Strs(item, "images") {
		if strings.TrimSpace(s) != "" {
			return true
		}
	}
	return len(ContentGallery(item)) > 0
}

var (
	textLinkRe    = regexp.MustCompile(`(?i)(?:https?://|t\.me/)\S+`)
	textHashRe    = regexp.MustCompile(`#[\p{L}\p{N}_]+`)
	textMentionRe = regexp.MustCompile(`@[\p{L}\p{N}_]+`)
	letterRe      = regexp.MustCompile(`\p{L}`)
)

// PostItemHasText — есть настоящий текст (хэштеги, ссылки, упоминания и эмодзи не считаются; меньше двух букв — нет).
func PostItemHasText(item jx.M) bool {
	caps := jx.List(item, "captions")
	cands := []string{jx.Str(item, "text")}
	if len(caps) > 0 {
		cands = append(cands, jx.ToStr(caps[0]))
	}
	for _, t := range cands {
		if t == "" || t == EmptyPostText {
			continue
		}
		t = textMentionRe.ReplaceAllString(textHashRe.ReplaceAllString(textLinkRe.ReplaceAllString(t, " "), " "), " ")
		if len(letterRe.FindAllStringIndex(t, 2)) >= 2 {
			return true
		}
	}
	return false
}

// ContentPassesMediaFilters — фильтры «Только с медиа» и «Только медиа».
func ContentPassesMediaFilters(item, filters jx.M) bool {
	if jx.Bool(filters, "media_only") || jx.Bool(filters, "media_no_text") {
		if !PostItemHasMedia(item) {
			return false
		}
	}
	return !jx.Bool(filters, "media_no_text") || !PostItemHasText(item)
}

// ContentForDisplay — запись для карточки: display_* — копия из кэша или исходная ссылка, preload — картинки следующих.
func (u *U) ContentForDisplay(item jx.M, upcoming []jx.M, remoteCheck bool) jx.M {
	itemKey := ContentItemKey(item)
	item = jx.Copy(item)
	// Video extraction needs the original Markdown attachment before display cleanup.
	rawText := jx.Str(item, "text")
	item["text"] = CleanPostDisplayText(rawText, item)
	if caps := jx.List(item, "captions"); len(caps) > 0 {
		cleaned := make([]any, len(caps))
		for i, c := range caps {
			cleaned[i] = CleanPostDisplayText(jx.ToStr(c), item)
		}
		item["captions"] = cleaned
	}
	// Older Lemmy records may have retained the video only in the Markdown body.
	if strings.HasPrefix(jx.Str(item, "source_key"), "lemmy:") && jx.Str(item, "video") == "" {
		if video := lemmyBodyVideo(rawText); video != "" {
			item = jx.Copy(item)
			item["video"] = video
		}
	}
	main := ContentImageKey(item)
	display := func(f jx.M) jx.M {
		out := jx.Copy(f)
		img := u.MediaExisting(jx.Str(f, "image_local"))
		if img == "" {
			img = jx.Str(f, "image")
		}
		out["display_image"] = img
		vid := ""
		if jx.Str(f, "video") != "" {
			if vid = u.MediaExisting(jx.Str(f, "video_local")); vid == "" {
				vid = jx.Str(f, "video")
			}
		}
		out["display_video"] = vid
		return out
	}
	own := jx.M{"image": main, "video": jx.Str(item, "video"), "image_local": jx.Str(item, "image_local"), "video_local": jx.Str(item, "video_local")}
	gallery := []jx.M{}
	files := ContentGallery(item)
	mainIndex := galleryRecordMainIndex(files, item)
	for i, f := range files {
		if i == mainIndex {
			for _, field := range []string{"image_local", "video_local"} {
				if u.MediaExisting(jx.Str(own, field)) != "" {
					f[field] = own[field]
				}
			}
		}
		gallery = append(gallery, display(f))
	}
	if len(gallery) > 0 && mainIndex < 0 {
		gallery = append([]jx.M{display(own)}, gallery...)
	}
	shown := display(own)
	caption := jx.Str(item, "text")
	if caps := jx.List(item, "captions"); len(caps) > 0 {
		caption = jx.ToStr(caps[0])
	}
	check := jx.M{"status": "ok", "problems": []any{}, "warnings": []any{}, "bad": []any{}}
	if PostItemHasVideo(item) || jx.Bool(item, "media_incomplete") || jx.Bool(item, "text_incomplete") || jx.Bool(item, "video_unavailable") || len(gallery) > 0 {
		check = u.TelegramPreflightItem(item, remoteCheck)
	}
	preload := []string{}
	for _, it := range upcoming {
		p := u.MediaExisting(jx.Str(it, "image_local"))
		if p == "" {
			p = ContentImageKey(it)
		}
		if p != "" {
			preload = append(preload, p)
		}
	}
	out := jx.Copy(item)
	qualityKey := ContentSourceKey(item)
	if jx.Str(item, "thread_id") != "" && (strings.HasPrefix(qualityKey, "dvach:") || strings.HasPrefix(qualityKey, "4chan:")) {
		qualityKey = SourceQualityKey(qualityKey) + ":" + jx.Str(item, "thread_id")
	}
	for k, v := range (jx.M{"item_key": itemKey, "images": []string{main}, "captions": []string{caption}, "source_url": SourcePostURL(item),
		"found_by": jx.Str(item, "found_by"), "gallery": gallery, "post_check": check, "display_image": shown["display_image"],
		"display_video": shown["display_video"], "source_quality": u.SourceQualityOf(u.Ctx, qualityKey, jx.Str(item, "topic")), "board_quality": u.BoardQualityOf(u.Ctx, qualityKey, jx.Str(item, "topic")), "tags_info": u.ContentTagsPublic(item["tags"]), "hashtags": u.PostHashtags(item), "preload": preload}) {
		out[k] = v
	}
	return out
}

// ---- Кэш медиа: имя файла — хэш исходного адреса ----

var mediaNameRe = regexp.MustCompile(`^[a-f0-9]{32}\.[a-z0-9]{2,5}$`)

func MediaCacheName(url, ext string) string {
	h := sha256.Sum256([]byte(url))
	return hex.EncodeToString(h[:])[:32] + "." + ext
}

func (a *App) MediaWrite(name string, data []byte) (string, error) {
	if err := os.MkdirAll(a.Cfg.MediaDir, 0o775); err != nil {
		return "", Bad("Не удалось создать каталог кэша медиа.")
	}
	tmp, err := os.CreateTemp(a.Cfg.MediaDir, ".tmp-")
	if err != nil {
		return "", Bad("Не удалось сохранить файл в кэш медиа.")
	}
	_, werr := tmp.Write(data)
	tmp.Close()
	if werr != nil || os.Chmod(tmp.Name(), 0o644) != nil || os.Rename(tmp.Name(), filepath.Join(a.Cfg.MediaDir, name)) != nil {
		os.Remove(tmp.Name())
		return "", Bad("Не удалось сохранить файл в кэш медиа.")
	}
	return a.Cfg.MediaURL + "/" + name, nil
}

var imageExt = map[string]string{"jpeg": "jpg", "png": "png", "gif": "gif", "webp": "webp", "bmp": "bmp"}

// MediaStoreImage — картинка в кэш; адрес для браузера или "".
func (a *App) MediaStoreImage(url string, data []byte) string {
	_, format, err := image.DecodeConfig(bytes.NewReader(data))
	ext, ok := imageExt[format]
	if err != nil || !ok {
		return ""
	}
	if isJoyreactorImageURL(url) {
		if cropped := cropJoyreactorBadge(data, format); len(cropped) > 0 {
			data = cropped
		}
	}
	p, err := a.MediaWrite(MediaCacheName(url, ext), data)
	if err != nil {
		return ""
	}
	return p
}

// The footer is recognised by both its full-width orange strip and the shape of
// its logo. Anything ambiguous is left intact, including yellow meme captions.
var joyLogoMask = []string{
	"..##.................................................##....................................",
	"..##.................................................##....................................",
	"...#.................................................###...................................",
	".####...####..###...########..#####...#####....##########...####...#####.......####...#####",
	"..###..######.###..##..#####.######...#####...######.####..######..#####......######.#####.",
	"..###.###..##..##..##..###..########....####.###.....###..###..###.###.......###.....##....",
	"..###.##...###.######..##...########..######.###.....##...##...###.###.......##.....###....",
	"..###.##...###..####...##...###......#######.###.....##...##...###.###.......##.....###....",
	"..###.###..##...####...##....##.....####.###.###.....###..###..###.###....#..###.....##....",
	"..###..######...####...##....######.########..######.####..######..###...###..######.#####.",
	"..###...####.....###...##.....#####...######...####...###...####...###...##....####...#####",
	"..###............##........................................................................",
	".###............###........................................................................",
	"###.............##.........................................................................",
}

func isJoyreactorImageURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	return host == "joyreactor.cc" || strings.HasSuffix(host, ".joyreactor.cc") || host == "reactor.cc" || strings.HasSuffix(host, ".reactor.cc")
}

func cropJoyreactorBadge(data []byte, format string) []byte {
	if format != "png" && format != "jpeg" {
		return nil
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil
	}
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 200 || h < 100 {
		return nil
	}
	orange := func(x, y int) bool {
		r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
		return r>>8 >= 235 && g>>8 >= 175 && g>>8 <= 210 && bl>>8 >= 35 && bl>>8 <= 75
	}
	// The left three quarters of every footer row must be almost solid orange.
	start := h
	for y := h - 1; y >= 0 && h-y <= min(64, h/20); y-- {
		n := 0
		for x := 0; x < w*3/4; x++ {
			if orange(x, y) {
				n++
			}
		}
		if n*100 < (w*3/4)*98 {
			break
		}
		start = y
	}
	if h-start < 5 || start == 0 || h-start >= min(64, h/20) {
		return nil
	}
	minX, minY, maxX, maxY := w, h, -1, -1
	for y := start; y < h; y++ {
		for x := w * 3 / 4; x < w; x++ {
			r, g, bl, _ := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			if r>>8 < 125 && g>>8 < 125 && bl>>8 < 125 {
				minX = min(minX, x)
				maxX = max(maxX, x)
				minY = min(minY, y)
				maxY = max(maxY, y)
			}
		}
	}
	lw, lh := maxX-minX+1, maxY-minY+1
	if lw < 30 || lh < 5 || lw < lh*5 || lw > lh*10 || maxX < w*9/10 {
		return nil
	}
	mismatch := 0
	for y := 0; y < len(joyLogoMask); y++ {
		for x := 0; x < len(joyLogoMask[y]); x++ {
			sx := minX + x*lw/len(joyLogoMask[y])
			sy := minY + y*lh/len(joyLogoMask)
			r, g, bl, _ := src.At(b.Min.X+sx, b.Min.Y+sy).RGBA()
			dark := r>>8 < 125 && g>>8 < 125 && bl>>8 < 125
			if dark != (joyLogoMask[y][x] == '#') {
				mismatch++
			}
		}
	}
	if mismatch*100 > len(joyLogoMask)*len(joyLogoMask[0])*25 {
		return nil
	}
	cropped := src.(interface {
		SubImage(image.Rectangle) image.Image
	}).SubImage(image.Rect(b.Min.X, b.Min.Y, b.Max.X, b.Min.Y+start))
	var out bytes.Buffer
	if format == "png" {
		err = png.Encode(&out, cropped)
	} else {
		err = jpeg.Encode(&out, cropped, &jpeg.Options{Quality: 92})
	}
	if err != nil {
		return nil
	}
	return out.Bytes()
}

// MediaStoreVideo — видео в кэш: из файла или скачать. Перекодированное — всегда .mp4.
func (u *U) MediaStoreVideo(url, file string, forceMP4 bool, headers map[string]string) string {
	if strings.TrimSpace(url) == "" {
		return ""
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(urlPath(url)), "."))
	if forceMP4 || (ext != "mp4" && ext != "webm") {
		ext = "mp4"
	}
	tmp := ""
	if file == "" || !fileExists(file) {
		tmp = u.DownloadVideoTemp(url, config.VideoMaxBytes, headers)
		if tmp == "" {
			return ""
		}
		defer os.Remove(tmp)
		file = tmp
	}
	// Preserve the actual container even when the source URL is a page.
	switch sniffMime(file) {
	case "video/webm":
		ext = "webm"
	case "video/mp4":
		ext = "mp4"
	}
	return u.mediaCopyVideo(MediaCacheName(url, ext), file)
}

// Stream large videos into a temporary file in the destination filesystem;
// a failed or interrupted copy never replaces the existing playable cache.
func (a *App) mediaCopyVideo(name, file string) string {
	in, err := os.Open(file)
	if err != nil {
		return ""
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() <= 0 || st.Size() > config.TelegramUploadMaxBytes {
		return ""
	}
	if err := os.MkdirAll(a.Cfg.MediaDir, 0o775); err != nil {
		return ""
	}
	tmp, err := os.CreateTemp(a.Cfg.MediaDir, ".tmp-")
	if err != nil {
		return ""
	}
	defer os.Remove(tmp.Name())
	n, copyErr := io.Copy(tmp, io.LimitReader(in, config.TelegramUploadMaxBytes+1))
	closeErr := tmp.Close()
	if copyErr != nil || closeErr != nil || n != st.Size() || n > config.TelegramUploadMaxBytes {
		return ""
	}
	if os.Chmod(tmp.Name(), 0o644) != nil || os.Rename(tmp.Name(), filepath.Join(a.Cfg.MediaDir, name)) != nil {
		return ""
	}
	return a.Cfg.MediaURL + "/" + name
}

// CacheDownloadedVideo — скачанный ролик → браузерный вариант → кэш.
func (u *U) CacheDownloadedVideo(url, downloaded string) string {
	if strings.TrimSpace(url) == "" || !fileExists(downloaded) {
		return ""
	}
	playable := u.BrowserPlayableVideo(downloaded)
	local := u.MediaStoreVideo(url, playable, playable != downloaded, nil)
	if playable != downloaded {
		os.Remove(playable)
	}
	return local
}

// MediaPath — путь файла кэша по адресу для браузера или "".
func (a *App) MediaPath(url string) string {
	prefix := a.Cfg.MediaURL + "/"
	if !strings.HasPrefix(url, prefix) {
		return ""
	}
	name := url[len(prefix):]
	if !mediaNameRe.MatchString(name) {
		return ""
	}
	return filepath.Join(a.Cfg.MediaDir, name)
}

func (a *App) MediaExisting(url string) string {
	if p := a.MediaPath(url); p != "" && fileExists(p) {
		return url
	}
	return ""
}

func MediaItemURLs(item jx.M) []string {
	var out []string
	for _, e := range append([]jx.M{item}, jx.Maps(item, "gallery")...) {
		for _, f := range []string{"image_local", "video_local"} {
			if s := jx.Str(e, f); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

// MediaRemove — удалить файл, если он не показывается ни одному пользователю.
func (a *App) MediaRemove(ctx context.Context, url string) {
	a.removeUnreferencedMedia(ctx, url)
}

// Recheck the live database immediately before deleting a cached file.
// A failed read must never be treated as absence of references.
func (a *App) removeUnreferencedMedia(ctx context.Context, url string) bool {
	p := a.MediaPath(url)
	if p == "" || a.DB == nil {
		return false
	}
	if _, protected := mediaProtect.Load(filepath.Base(p)); protected {
		return false
	}
	var used bool
	err := a.DB.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pool_items WHERE strpos(data::text, $1) > 0) OR EXISTS (SELECT 1 FROM history_items WHERE strpos(data::text, $1) > 0) OR EXISTS (SELECT 1 FROM public_posts WHERE strpos(data::text, $1) > 0)`, url).Scan(&used)
	if err != nil || used {
		return false
	}
	return os.Remove(p) == nil
}

// MediaCleanup — удалить файлы, на которые не ссылается ни одна запись; свежие щадит. Массовое удаление блокируется.
func (u *U) MediaCleanup(pool []jx.M, includeFresh, force bool) int {
	if pool == nil {
		u.Log("Кэш медиа: очистка отменена — не удалось прочитать записи из базы.")
		return 0
	}
	keep := map[string]bool{}
	for _, it := range pool {
		for _, url := range MediaItemURLs(it) {
			if p := u.MediaPath(url); p != "" {
				keep[filepath.Base(p)] = true
			}
		}
	}
	previews := u.VideoPreviewKeepNames()
	if previews == nil {
		u.Log("Кэш медиа: очистка отменена — не удалось прочитать список видео.")
		return 0
	}
	for name := range previews {
		keep[name] = true
	}
	entries, _ := os.ReadDir(u.Cfg.MediaDir)
	all := 0
	var remove []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		all++
		info, err := e.Info()
		if _, protected := mediaProtect.Load(e.Name()); protected {
			continue
		}
		if err != nil || keep[e.Name()] || (!includeFresh && time.Since(info.ModTime()) < config.MediaCacheGraceSeconds*time.Second) {
			continue
		}
		remove = append(remove, e.Name())
	}
	if !force && ((all > 20 && len(pool) == 0) || (all > 0 && float64(len(remove))/float64(all) > config.MediaCacheCleanupMaxShare)) {
		u.Log("Кэш медиа: очистка остановлена — удалилось бы %d из %d файлов. Проверьте базу; удалить принудительно: posta media-cleanup --force", len(remove), all)
		return 0
	}
	n := 0
	for _, name := range remove {
		if u.removeUnreferencedMedia(u.Ctx, u.Cfg.MediaURL+"/"+name) {
			n++
		}
	}
	return n
}

// MediaReleaseRemoved — файлы записей, которые были в before и пропали из after.
func (a *App) MediaReleaseRemoved(ctx context.Context, before, after []jx.M) {
	kept := map[string]bool{}
	for _, it := range after {
		for _, url := range MediaItemURLs(it) {
			kept[url] = true
		}
	}
	for _, it := range before {
		for _, url := range MediaItemURLs(it) {
			if !kept[url] {
				a.MediaRemove(ctx, url)
			}
		}
	}
}

// PruneOldPool removes collected posts after a week, including archived fallback items.
func (u *U) PruneOldPool() error {
	var before []jx.M
	cutoff := now() - 7*24*60*60
	after, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
		kept := make([]jx.M, 0, len(pool))
		changed := false
		for _, item := range pool {
			at := jx.Int(item, "pooled_at")
			if at <= 0 {
				at = jx.Int(item, "timestamp")
			}
			if at <= 0 {
				item["pooled_at"] = now()
				changed = true
			} else if at < cutoff {
				changed = true
				continue
			}
			kept = append(kept, item)
		}
		if !changed {
			return nil
		}
		before = pool
		return kept
	})
	if err != nil {
		return err
	}
	if before != nil {
		u.MediaReleaseRemoved(u.Ctx, before, after)
	}
	return nil
}

func PoolItemIsFresh(item jx.M, at int64) bool {
	if !jx.Has(item, "pooled_at") {
		return true
	}
	if at == 0 {
		at = now()
	}
	return at-jx.Int(item, "pooled_at") <= config.PoolMaxAgeSeconds
}

// PoolStampLegacy — старым записям без метки попадания в пул ставит её сейчас.
func (u *U) PoolStampLegacy() {
	u.PoolUpdate(func(pool []jx.M) []jx.M {
		changed := false
		for _, it := range pool {
			if !jx.Has(it, "pooled_at") {
				it["pooled_at"] = now()
				changed = true
			}
		}
		if changed {
			return pool
		}
		return nil
	})
}

// PoolTopicCount counts fresh records available under the current filters,
// not hidden records that happen to occupy storage.
func (u *U) PoolTopicCount(key string) int {
	visible, err := u.poolVisibilityFilter(key)
	if err != nil {
		return 0
	}
	return len(u.visibleFreshPool(u.Pool(), key, visible))
}

// PoolArchiveItems — старые записи с кэшированным медиа: показываются, пока сборщик медленный.
func (u *U) PoolArchiveItems(frozen map[string]bool) []jx.M {
	var out []jx.M
	for _, it := range u.Pool() {
		if PoolItemIsFresh(it, 0) || frozen[strings.ToLower(jx.Str(it, "source"))] || jx.Bool(it, "proposal_hidden") {
			continue
		}
		if jx.Str(it, "image_local") != "" || jx.Str(it, "video_local") != "" {
			out = append(out, it)
		}
	}
	return out
}

func (u *U) ContentPoolNeedsFill(key string) bool {
	u.PoolStampLegacy()
	_, allowed, err := u.poolRefillState(key, u.TInt("pool_target"))
	return err == nil && allowed
}

// ResetProjectData — пустые пул и история пользователя; каталог общий и остаётся.
func (u *U) ResetProjectData() int {
	u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{} })
	u.HistoryUpdate(func(History) History { return EmptyHistory() })
	return u.MediaCleanup(u.AllUsersPoolItems(u.Ctx), false, false)
}

// ---- Галерея поста ----

const galleryCacheMaxFiles = 4

func galleryPrepareImage(data []byte) []byte {
	w, h, ok := imgx.Size(data)
	_, format, _ := image.DecodeConfig(strings.NewReader(string(data)))
	if !ok || format == "gif" || (max(w, h) <= 1600 && len(data) <= 600*1024) {
		return data
	}
	if j := imgx.Shrink(data, 1600, 85, true); len(j) < len(data) {
		return j
	}
	return data
}

// galleryMainIndex skips exactly one already handled media entry. Several
// different videos may share the same cover image.
func galleryMainIndex(gallery []jx.M, mainImage string) int {
	if mainImage == "" && len(gallery) > 0 {
		return 0
	}
	for i, f := range gallery {
		if jx.Str(f, "image") == mainImage {
			return i
		}
	}
	return -1
}

// Match the actual attachment, not only its poster. A photo and several
// distinct videos can share a poster URL.
func galleryRecordMainIndex(gallery []jx.M, record jx.M) int {
	image, video := ContentImageKey(record), jx.Str(record, "video")
	for i, f := range gallery {
		if jx.Str(f, "image") == image && jx.Str(f, "video") == video {
			return i
		}
	}
	return -1
}

// GalleryCacheFiles — кэширует файлы галереи, кроме главного.
func (u *U) GalleryCacheFiles(gallery []jx.M, mainImage string) []jx.M {
	done := 0
	main := galleryMainIndex(gallery, mainImage)
	for i, f := range gallery {
		if !u.Alive() || done >= galleryCacheMaxFiles {
			break
		}
		if i == main {
			continue
		}
		image, video := jx.Str(f, "image"), jx.Str(f, "video")
		needImage := image != "" && u.MediaExisting(jx.Str(f, "image_local")) == ""
		needVideo := video != "" && u.MediaExisting(jx.Str(f, "video_local")) == ""
		if !needImage && !needVideo {
			continue
		}
		done++
		if needImage {
			if b := u.HTTPGet(image, 20*time.Second); b != nil && len(b) <= config.MaxImageBytes {
				gallery[i]["phash"] = ImagePhash(b)
				gallery[i]["image_local"] = u.MediaStoreImage(image, galleryPrepareImage(b))
			}
		}
		if needVideo {
			if file := u.DownloadVideoTemp(video, config.VideoMaxBytes, nil); file != "" {
				gallery[i]["video_local"] = u.CacheDownloadedVideo(video, file)
				os.Remove(file)
			}
		}
	}
	return gallery
}

// CachePoolGalleries — догружает недостающие файлы, а не только полностью
// некэшированные альбомы. Compare the original gallery before applying the
// result so a concurrent edit or download cannot be overwritten.
func (u *U) CachePoolGalleries(limit int) int {
	type galleryUpdate struct {
		before string
		after  []jx.M
	}
	todo := map[string]galleryUpdate{}
	attempted := 0
	for _, it := range u.Pool() {
		if !u.Alive() || attempted >= limit {
			break
		}
		g := jx.Maps(it, "gallery")
		if len(g) < 2 {
			continue
		}
		key := ContentItemKey(it)
		if key == "" {
			continue
		}
		missing := false
		main := galleryMainIndex(g, ContentImageKey(it))
		for i, f := range g {
			if i == main {
				continue
			}
			if (jx.Str(f, "image") != "" && u.MediaExisting(jx.Str(f, "image_local")) == "") ||
				(jx.Str(f, "video") != "" && u.MediaExisting(jx.Str(f, "video_local")) == "") {
				missing = true
				break
			}
		}
		if !missing {
			continue
		}
		attempted++
		before := string(jx.Marshal(g))
		after := u.GalleryCacheFiles(g, ContentImageKey(it))
		if before != string(jx.Marshal(after)) {
			todo[key] = galleryUpdate{before, after}
		}
	}
	if len(todo) == 0 {
		return 0
	}
	changed := 0
	if _, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
		for _, it := range pool {
			if update, ok := todo[ContentItemKey(it)]; ok && string(jx.Marshal(jx.Maps(it, "gallery"))) == update.before {
				it["gallery"] = update.after
				changed++
			}
		}
		if changed == 0 {
			return nil
		}
		return pool
	}); err != nil {
		u.Log("Не удалось сохранить кэш галерей: %v", err)
		return 0
	}
	u.Log("Кэш галерей: обработано записей: %d.", changed)
	return changed
}

// ---- Живость тредов Двача у записей пула ----

// PruneDeadPoolItems — убирает записи удалённых тредов и постов. Раз в PoolLivenessInterval; nil — ещё рано.
func (u *U) PruneDeadPoolItems(dryRun, force, logIt bool) jx.M {
	if !force && now()-u.CheckedAt("threads") < config.PoolLivenessInterval {
		return nil
	}
	pool := u.Pool()
	type th struct{ board, thread string }
	var threads []th
	seen := map[string]bool{}
	for _, it := range pool {
		b, t := jx.Str(it, "board"), jx.Str(it, "thread_id")
		if b != "" && t != "" && !seen[b+"/"+t] {
			seen[b+"/"+t] = true
			threads = append(threads, th{b, t})
		}
	}
	if len(threads) > config.PoolLivenessMaxThreads {
		threads = threads[:config.PoolLivenessMaxThreads]
	}
	states := map[string]any{} // false — удалён, map — живые посты, nil — неизвестно
	gone := 0
	for _, t := range threads {
		state, posts := u.DvachThreadState(t.board, t.thread)
		switch state {
		case "gone":
			states[t.board+"/"+t.thread] = false
			gone++
		case "alive":
			nums := map[string]bool{}
			for _, p := range posts {
				nums[jx.Str(p, "num")] = true
			}
			states[t.board+"/"+t.thread] = nums
		default:
			states[t.board+"/"+t.thread] = nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	isGone := func(it jx.M) bool {
		s := states[jx.Str(it, "board")+"/"+jx.Str(it, "thread_id")]
		if s == false {
			return true
		}
		if nums, ok := s.(map[string]bool); ok && jx.Str(it, "post_id") != "" && !nums[jx.Str(it, "post_id")] {
			return true
		}
		return false
	}
	removed := 0
	if !dryRun {
		after, _ := u.PoolUpdate(func(p []jx.M) []jx.M {
			var kept []jx.M
			removed = 0
			for _, it := range p {
				if isGone(it) {
					removed++
				} else {
					kept = append(kept, it)
				}
			}
			if removed == 0 {
				return nil
			}
			if kept == nil {
				kept = []jx.M{}
			}
			return kept
		})
		if removed > 0 {
			u.MediaReleaseRemoved(u.Ctx, pool, after)
		}
		u.MarkChecked("threads")
	} else {
		for _, it := range pool {
			if isGone(it) {
				removed++
			}
		}
	}
	if logIt && removed > 0 {
		u.Log("Проверка тредов: проверено %d, удалённых тредов %d, убрано записей из пула: %d.", len(states), gone, removed)
	}
	return jx.M{"threads": len(states), "gone": gone, "removed": removed}
}

// ---- Повторы: отпечаток картинки (dHash 256 бит) ----

// ImagePhash — отпечаток (64 hex-символа): одинаков у пережатой, уменьшенной копии. "" — не картинка.
func ImagePhash(data []byte) string {
	src, err := imgx.Decode(data)
	if err != nil {
		return ""
	}
	small := image.NewRGBA(image.Rect(0, 0, 17, 16))
	xdraw.BiLinear.Scale(small, small.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	lum := func(x, y int) float64 {
		c := small.RGBAAt(x, y)
		return 0.299*float64(c.R) + 0.587*float64(c.G) + 0.114*float64(c.B)
	}
	var out [32]byte
	bit := 0
	for row := 0; row < 16; row++ {
		for col := 0; col < 16; col++ {
			if lum(col, row) > lum(col+1, row) {
				out[bit/8] |= 1 << (7 - bit%8)
			}
			bit++
		}
	}
	return hex.EncodeToString(out[:])
}

// PhashDistance — сколько бит из 256 различаются.
func PhashDistance(a, b string) int {
	if len(a) != 64 || len(b) != 64 {
		return 256
	}
	x, err1 := hex.DecodeString(a)
	y, err2 := hex.DecodeString(b)
	if err1 != nil || err2 != nil {
		return 256
	}
	d := 0
	for i := range x {
		d += bits.OnesCount8(x[i] ^ y[i])
	}
	return d
}

// PhashFind — первый известный отпечаток ближе PhashSameDistance или "".
func PhashFind[V any](hash string, known map[string]V) (string, bool) {
	for k := range known {
		if PhashDistance(hash, k) <= config.PhashSameDistance {
			return k, true
		}
	}
	return "", false
}

// ---- 18+ и «Жесть» ----

func (u *U) AdultAllowed() bool {
	if !u.TBool("adult_available") {
		return false
	}
	var ok bool
	u.DB.Pool.QueryRow(u.Ctx, `SELECT adult_allowed FROM users WHERE id = $1`, u.ID).Scan(&ok)
	return ok
}

// TopicIsRestricted — тема 18+ или «Жесть»: по реестру или по словам.
func (a *App) TopicIsRestricted(topic string) bool {
	if topic == "" {
		return false
	}
	return a.TopicIsNSFW(topic) || TopicIsAdult(topic)
}

// ContentIsAdult — запись 18+ или «Жесть».
func (a *App) ContentIsAdult(r jx.M) bool {
	if jx.Bool(r, "source_sensitive") || jx.Bool(r, "nsfw") {
		return true
	}
	cat := a.ContentTagCatalog()
	for _, k := range jx.Strs(r, "tags") {
		if t, ok := cat[k]; ok && t.NSFW {
			return true
		}
	}
	if a.TopicIsRestricted(jx.Str(r, "topic")) {
		return true
	}
	return jx.Str(r, "board") != "" && contains(DvachBoardTags(jx.Str(r, "board")), DvachAdultTag)
}

const ContentLewdHumorTag = "adult.lewd_humor"

// ContentAdultBlocked — настройка 18+ действует одинаково во всех темах.
// Соответствие теме отдельно проверяется полным анализом.
func (u *U) ContentAdultBlocked(r jx.M, topic string) bool {
	if !u.ContentIsAdult(jx.Defaults(r, jx.M{"topic": topic})) {
		return false
	}
	return u.contentAdultBlocked(r, topic, u.AdultAllowed())
}

func (a *App) contentAdultBlocked(r jx.M, topic string, allowed bool) bool {
	return !allowed && a.ContentIsAdult(jx.Defaults(r, jx.M{"topic": topic}))
}

// ContentRecordByMedia — запись пользователя, у которой этот адрес — картинка, видео или их копия.
func (u *U) ContentRecordByMedia(src string) jx.M {
	if src == "" {
		return nil
	}
	lists := [][]jx.M{u.Pool()}
	h := u.History()
	for _, c := range config.HistoryCollections {
		lists = append(lists, h[c])
	}
	for _, list := range lists {
		for _, r := range list {
			if contains(jx.Strs(r, "images"), src) {
				return r
			}
			for _, f := range append([]jx.M{r}, jx.Maps(r, "gallery")...) {
				for _, field := range []string{"image", "video", "image_local", "video_local"} {
					if jx.Str(f, field) == src {
						return r
					}
				}
			}
		}
	}
	return nil
}

func (u *U) TopicSaveDenied(topic string) string {
	if !u.AdultAllowed() && u.TopicIsRestricted(topic) {
		return "Темы 18+ и «Жесть» недоступны."
	}
	return ""
}

// PublishMediaDenied — отказ опубликовать медиа (текст) или "".
func (u *U) PublishMediaDenied(items []jx.M) string {
	adultOK := u.AdultAllowed()
	for _, it := range items {
		src := jx.Str(it, "src")
		if jx.Str(it, "upload") != "" || src == "" {
			continue
		}
		if path := u.MediaPath(src); path != "" && !u.CachedMediaAllowed(u.Ctx, u.ID, filepath.Base(path)) {
			return "Файл недоступен для этого аккаунта."
		}
		r := u.ContentRecordByMedia(src)
		if r == nil {
			continue
		}
		if u.UARecordHidden(r) {
			return "В посте есть украинский материал — публиковать нельзя."
		}
		if !adultOK && u.ContentIsAdult(r) {
			return "В посте есть материал 18+ или «Жесть» — такое через Posta публиковать нельзя."
		}
	}
	return ""
}

// ContentMarkPosted — публикация или постановка в очередь = лайк: запись уходит в «liked» и скрыта из «Предложки».
// "liked" — отметили сейчас, "already" — раньше, "" — такой записи нет.
func (u *U) ContentMarkPosted(key string) string {
	if key == "" {
		return ""
	}
	var poolItem, record jx.M
	_, err := u.poolUpdate(func(tx pgx.Tx, pool []jx.M) ([]jx.M, error) {
		poolItem = PoolFindItem(pool, key)
		if poolItem == nil {
			return nil, nil
		}
		record = jx.Merge(u.ContentHistoryRecord(poolItem, nil), jx.M{"proposal_hidden": true, "published_at": now(), "preference_vote": "publication"})
		if err := u.historyAppendTx(tx, "liked", record); err != nil {
			return nil, err
		}
		return PoolWithoutImage(pool, key), nil
	})
	if err != nil {
		u.Log("Не удалось отметить опубликованный пост: %v", err)
		return ""
	}
	if poolItem != nil {
		u.historyVoteCommitted("liked", record)
		return "liked"
	}
	marked := ""
	_, err = u.HistoryUpdate(func(h History) History {
		canonical := historyCanonicalKey(h, key)
		if canonical == "" {
			return nil
		}
		for _, collection := range config.HistoryCollections {
			for i, r := range h[collection] {
				if ContentItemKey(r) != canonical {
					continue
				}
				at := now()
				if collection == "liked" {
					marked = "liked"
					if jx.Bool(r, "published_at") {
						marked = "already"
					}
					h[collection][i] = jx.Merge(r, jx.M{"proposal_hidden": true, "published_at": at, "repeat_action_at": at})
				} else {
					h[collection] = append(h[collection][:i], h[collection][i+1:]...)
					record = jx.Merge(r, jx.M{"proposal_hidden": true, "published_at": at, "repeat_action_at": at})
					if jx.Str(record, "preference_vote") == "" {
						// Legacy explicit dislikes must not turn into likes just
						// because the post entered the calendar.
						if collection == "disliked" {
							record["preference_vote"] = "dislike"
						} else {
							record["preference_vote"] = "publication"
						}
					}
					h["liked"] = append(h["liked"], record)
					marked = "liked"
				}
				return h
			}
		}
		return nil
	})
	if err != nil {
		u.Log("Не удалось обновить историю публикации: %v", err)
		return ""
	}
	if marked != "" && record != nil {
		u.historyVoteCommitted("liked", record)
	}
	return marked
}

func ContentMarkInLiked(h History, key string, at int64) History {
	changed := false
	for i, r := range h["liked"] {
		if ContentItemKey(r) != key || jx.Bool(r, "published_at") {
			continue
		}
		h["liked"][i] = jx.Merge(r, jx.M{"proposal_hidden": true, "published_at": at})
		changed = true
	}
	if changed {
		return h
	}
	return nil
}

// ContentUnmarkPosted — пост снят из очереди до выхода: запись возвращается в «Предложку» (лайк остаётся).
func (u *U) ContentUnmarkPosted(key string) bool {
	if key == "" {
		return false
	}
	done := false
	_, err := u.historyUpdateTx(u.Ctx, u.ID, func(tx pgx.Tx, h History) (History, error) {
		canonical := historyCanonicalKey(h, key)
		if canonical == "" {
			return nil, nil
		}
		// The queue and publisher use this same owner lock. A concurrent
		// finalization or schedule cannot slip between the check and restore.
		var locked int
		if err := tx.QueryRow(u.Ctx, `SELECT 1 FROM users WHERE id=$1 FOR NO KEY UPDATE`, u.ID).Scan(&locked); err != nil {
			return nil, err
		}
		var used bool
		if err := tx.QueryRow(u.Ctx, `SELECT EXISTS(SELECT 1 FROM outbox WHERE user_id=$1 AND data->>'record' IN ($2,$3))
			OR EXISTS(SELECT 1 FROM published WHERE user_id=$1 AND data->>'record' IN ($2,$3) AND data->>'status'='ok')`, u.ID, key, canonical).Scan(&used); err != nil {
			return nil, err
		}
		if used {
			return nil, nil
		}
		for i, r := range h["liked"] {
			if ContentItemKey(r) != canonical || !jx.Bool(r, "published_at") {
				continue
			}
			r = jx.Copy(r)
			delete(r, "published_at")
			r["proposal_hidden"] = false
			h["liked"][i] = r
			done = true
		}
		if done {
			return h, nil
		}
		return nil, nil
	})
	return err == nil && done
}

// historyAppendTx — для слияния профилей: запись в истории в рамках транзакции.
var _ = pgx.ErrNoRows

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

func urlPath(raw string) string {
	if i := strings.IndexAny(raw, "?#"); i >= 0 {
		raw = raw[:i]
	}
	if i := strings.Index(raw, "://"); i >= 0 {
		raw = raw[i+3:]
		if j := strings.Index(raw, "/"); j >= 0 {
			return raw[j:]
		}
		return "/"
	}
	return raw
}

func fmtMB(n int64) string { return fmt.Sprintf("%.1f", float64(n)/(1<<20)) }
