package posta

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"math/bits"
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

// ContentItemKey — ключ для оценок: картинка, а у поста без картинки — ключ поста или текста.
func ContentItemKey(item jx.M) string {
	if k := ContentImageKey(item); k != "" || item == nil {
		return k
	}
	if k := CandidatePostKey(item); k != "" {
		return k
	}
	return CandidateTextKey(item)
}

func PoolFindItem(pool []jx.M, key string) jx.M {
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

// PoolWithoutLostVideo убирает записи, у которых пропал кэш видео (ссылки Telegram с истёкшим токеном не играют).
func (u *U) PoolWithoutLostVideo(pool []jx.M) []jx.M {
	lost := func(it jx.M) bool {
		for _, f := range append([]jx.M{it}, jx.Maps(it, "gallery")...) {
			if jx.Str(f, "video") != "" && jx.Str(f, "video_local") != "" && u.MediaExisting(jx.Str(f, "video_local")) == "" {
				return true
			}
		}
		return false
	}
	bad := map[string]bool{}
	for _, it := range pool {
		if lost(it) {
			bad[ContentImageKey(it)] = true
		}
	}
	if len(bad) > 0 {
		u.PoolUpdate(func(all []jx.M) []jx.M {
			out := []jx.M{}
			for _, it := range all {
				if !bad[ContentImageKey(it)] {
					out = append(out, it)
				}
			}
			return out
		})
	}
	out := []jx.M{}
	for _, it := range pool {
		hidden := jx.Str(it, "video_via") == "ytdlp" && jx.Bool(it, "video_unavailable")
		if !bad[ContentImageKey(it)] && !hidden {
			out = append(out, it)
		}
	}
	return out
}

// PoolOrder — порядок показа: по свежести поста, подряд не больше одного поста источника (если в окне есть другой).
// Темы не смешиваются; отложенные («✋») — после остальных. card_score порядок не решает.
func PoolOrder(pool []jx.M) []jx.M {
	type entry struct {
		item jx.M
		seq  int
	}
	var order []string
	groups := map[string][]entry{}
	for i, it := range pool {
		t := jx.Str(it, "topic")
		if _, ok := groups[t]; !ok {
			order = append(order, t)
		}
		groups[t] = append(groups[t], entry{it, i})
	}
	fresh := func(it jx.M) int64 {
		if jx.Has(it, "posted_at") {
			return jx.Int(it, "posted_at")
		}
		return jx.Int(it, "pooled_at")
	}
	out := make([]jx.M, 0, len(pool))
	for _, t := range order {
		g := groups[t]
		sort.SliceStable(g, func(a, b int) bool {
			pa, pb := jx.Int(g[a].item, "postponed_at"), jx.Int(g[b].item, "postponed_at")
			if pa != pb {
				return pa < pb
			}
			fa, fb := fresh(g[a].item), fresh(g[b].item)
			if fa != fb {
				return fa > fb
			}
			return g[a].seq < g[b].seq
		})
		last := ""
		hasLast := false
		for len(g) > 0 {
			pick := 0
			if hasLast && ContentSourceKey(g[0].item) == last {
				for k := 1; k < len(g) && k <= config.PoolDiversityWindow; k++ {
					c := g[k].item
					if ContentSourceKey(c) != last && jx.Bool(c, "postponed_at") == jx.Bool(g[0].item, "postponed_at") {
						pick = k
						break
					}
				}
			}
			it := g[pick].item
			g = append(g[:pick], g[pick+1:]...)
			last, hasLast = ContentSourceKey(it), true
			out = append(out, it)
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
		img, ok := f["image"].(string)
		if !ok || img == "" {
			continue
		}
		v, _ := f["video"].(string)
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
	rec := jx.M{"image": ContentImageKey(item), "text": jx.Str(item, "text"), "description": item["description"], "why_relevant": item["why_relevant"],
		"relevance": item["relevance"], "rating_assigned": item["rating"], "format": jx.Str(item, "format"), "tags": a.ContentTagsClean(item["tags"]),
		"entity": jx.Str(item, "entity"), "source_hashtags": orList(jx.List(item, "source_hashtags")), "topic": item["topic"], "source": item["source"],
		"source_key": ContentSourceKey(item), "board": item["board"], "thread_id": item["thread_id"], "post_id": item["post_id"],
		"source_url": SourcePostURL(item), "posted_at": item["posted_at"], "engagement": item["engagement"], "complaint_reason": complaintReason,
		"video": jx.Str(item, "video"), "image_local": jx.Str(item, "image_local"), "video_local": jx.Str(item, "video_local"),
		"phash": jx.Str(item, "phash"), "gallery": ContentGallery(item), "timestamp": now()}
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
	url := jx.Str(item, "source_url")
	if IsTrustedSourceURL(url) || RSSPostURLTrusted(item) {
		return url
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

func PostItemHasVideo(item jx.M) bool {
	if strings.TrimSpace(jx.Str(item, "video")) != "" {
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
	hasMain := false
	for _, f := range ContentGallery(item) {
		if jx.Str(f, "image") == main {
			f = jx.Merge(f, jx.M{"image_local": own["image_local"], "video_local": own["video_local"]})
			hasMain = true
		}
		gallery = append(gallery, display(f))
	}
	if len(gallery) > 0 && !hasMain {
		gallery = append([]jx.M{display(own)}, gallery...)
	}
	shown := display(own)
	caption := jx.Str(item, "text")
	if caps := jx.List(item, "captions"); len(caps) > 0 {
		caption = jx.ToStr(caps[0])
	}
	check := jx.M{"status": "ok", "problems": []any{}, "warnings": []any{}, "bad": []any{}}
	if jx.Str(item, "video") != "" || len(gallery) > 0 {
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
	for k, v := range (jx.M{"item_key": ContentItemKey(item), "images": []string{main}, "captions": []string{caption}, "source_url": SourcePostURL(item),
		"found_by": jx.Str(item, "found_by"), "gallery": gallery, "post_check": check, "display_image": shown["display_image"],
		"display_video": shown["display_video"], "tags_info": u.ContentTagsPublic(item["tags"]), "hashtags": u.PostHashtags(item), "preload": preload}) {
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
	_, format, err := image.DecodeConfig(strings.NewReader(string(data)))
	ext, ok := imageExt[format]
	if err != nil || !ok {
		return ""
	}
	p, err := a.MediaWrite(MediaCacheName(url, ext), data)
	if err != nil {
		return ""
	}
	return p
}

// MediaStoreVideo — видео в кэш: из файла или скачать. Перекодированное — всегда .mp4.
func (u *U) MediaStoreVideo(url, file string, forceMP4 bool, headers map[string]string) string {
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
	data, err := os.ReadFile(file)
	if err != nil || len(data) == 0 {
		return ""
	}
	p, _ := u.MediaWrite(MediaCacheName(url, ext), data)
	return p
}

// CacheDownloadedVideo — скачанный ролик → браузерный вариант → кэш.
func (u *U) CacheDownloadedVideo(url, downloaded string) string {
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
	var used bool
	a.DB.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM pool_items WHERE data::text LIKE $1) OR EXISTS (SELECT 1 FROM history_items WHERE data::text LIKE $1)`,
		"%"+strings.ReplaceAll(url, "%", `\%`)+"%").Scan(&used)
	if used {
		return
	}
	if p := a.MediaPath(url); p != "" {
		os.Remove(p)
	}
}

// MediaCleanup — удалить файлы, на которые не ссылается ни одна запись; свежие щадит. Массовое удаление блокируется.
func (u *U) MediaCleanup(pool []jx.M, includeFresh, force bool) int {
	keep := map[string]bool{}
	for _, it := range pool {
		for _, url := range MediaItemURLs(it) {
			if p := u.MediaPath(url); p != "" {
				keep[filepath.Base(p)] = true
			}
		}
	}
	for name := range u.VideoPreviewKeepNames() {
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
		if os.Remove(filepath.Join(u.Cfg.MediaDir, name)) == nil {
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

// PoolTopicCount — свежие записи пула по теме.
func (u *U) PoolTopicCount(key string) int {
	n := 0
	norm := TopicNorm(key)
	for _, it := range u.Pool() {
		if PoolItemIsFresh(it, 0) && TopicNorm(jx.Str(it, "topic")) == norm {
			n++
		}
	}
	return n
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
	return u.PoolTopicCount(key) < config.ContentPoolTarget
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

// GalleryCacheFiles — кэширует файлы галереи, кроме главного.
func (u *U) GalleryCacheFiles(gallery []jx.M, mainImage string) []jx.M {
	done := 0
	for i, f := range gallery {
		isMain := jx.Str(f, "image") == mainImage
		if mainImage == "" {
			isMain = i == 0
		}
		if isMain || jx.Str(f, "image_local") != "" || jx.Str(f, "video_local") != "" {
			continue
		}
		if done >= galleryCacheMaxFiles {
			continue
		}
		done++
		if b := u.HTTPGet(jx.Str(f, "image"), 20*time.Second); b != nil && len(b) <= config.MaxImageBytes {
			gallery[i]["image_local"] = u.MediaStoreImage(jx.Str(f, "image"), galleryPrepareImage(b))
		}
		if v := jx.Str(f, "video"); v != "" {
			if file := u.DownloadVideoTemp(v, config.VideoMaxBytes, nil); file != "" {
				gallery[i]["video_local"] = u.CacheDownloadedVideo(v, file)
				os.Remove(file)
			}
		}
	}
	return gallery
}

// CachePoolGalleries — догружает кэш галерей у записей пула (по нескольку за запуск).
func (u *U) CachePoolGalleries(limit int) int {
	todo := map[string][]jx.M{}
	for _, it := range u.Pool() {
		g := jx.Maps(it, "gallery")
		if len(g) < 2 || len(todo) >= limit {
			continue
		}
		cached := false
		for _, f := range g[1:] {
			if jx.Str(f, "image_local") != "" || jx.Str(f, "video_local") != "" {
				cached = true
			}
		}
		if cached {
			continue
		}
		todo[ContentImageKey(it)] = u.GalleryCacheFiles(g, ContentImageKey(it))
	}
	if len(todo) == 0 {
		return 0
	}
	u.PoolUpdate(func(pool []jx.M) []jx.M {
		for _, it := range pool {
			if g, ok := todo[ContentImageKey(it)]; ok {
				it["gallery"] = g
			}
		}
		return pool
	})
	u.Log("Кэш галерей: обработано записей: %d.", len(todo))
	return len(todo)
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

// ContentAdultBlocked — не пускать в подбор: 18+ при выключенном 18+; в обычной теме — эротику, порно и жесть всегда (кроме пошлого юмора).
func (u *U) ContentAdultBlocked(r jx.M, topic string) bool {
	if !u.ContentIsAdult(jx.Defaults(r, jx.M{"topic": topic})) {
		return false
	}
	if !u.AdultAllowed() {
		return true
	}
	if u.TopicIsRestricted(topic) {
		return false
	}
	cat := u.ContentTagCatalog()
	for _, k := range jx.Strs(r, "tags") {
		if k != ContentLewdHumorTag && cat[k].NSFW {
			return true
		}
	}
	return false
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
	var poolItem jx.M
	u.PoolUpdate(func(pool []jx.M) []jx.M {
		poolItem = PoolFindItem(pool, key)
		if poolItem == nil {
			return nil
		}
		return PoolWithoutImage(pool, key)
	})
	if poolItem != nil {
		u.HistoryAppend("liked", jx.Merge(u.ContentHistoryRecord(poolItem, nil), jx.M{"proposal_hidden": true, "published_at": now()}))
		return "liked"
	}
	h := u.History()
	for _, c := range config.HistoryCollections {
		for _, r := range h[c] {
			if ContentItemKey(r) != key {
				continue
			}
			if c == "liked" {
				if jx.Bool(r, "published_at") {
					return "already"
				}
				u.HistoryUpdate(func(h History) History { return ContentMarkInLiked(h, key, now()) })
				return "liked"
			}
			u.HistoryAppend("liked", jx.Merge(r, jx.M{"proposal_hidden": true, "published_at": now()}))
			return "liked"
		}
	}
	return ""
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
	u.HistoryUpdate(func(h History) History {
		changed := false
		for i, r := range h["liked"] {
			if ContentItemKey(r) != key || !jx.Bool(r, "published_at") {
				continue
			}
			r = jx.Copy(r)
			delete(r, "published_at")
			r["proposal_hidden"] = false
			h["liked"][i] = r
			changed = true
		}
		done = changed
		if changed {
			return h
		}
		return nil
	})
	return done
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
