package posta

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// «Предложка»: лайки и «не знаю» из истории; файлы достраиваются в фоне, пока ссылки источника живы.

const proposalsLimit = 200

func (a *App) proposalMediaLost(r jx.M) bool {
	return jx.Bool(r, "media_gone") && a.MediaExisting(jx.Str(r, "image_local")) == "" && a.MediaExisting(jx.Str(r, "video_local")) == ""
}

// ProposalItems — записи «Предложки» текущей темы для показа, новые первыми.
func (u *U) ProposalItems(h History) []jx.M {
	items := []jx.M{}
	topic := u.CurrentTopic()
	adult := u.AdultAllowed()
	for _, c := range config.ProposalCollections {
		for _, r := range h[c] {
			if jx.Bool(r, "proposal_hidden") || u.UARecordHidden(r) || u.proposalMediaLost(r) || ContentImageKey(r) == "" || (!adult && u.ContentIsAdult(r)) {
				continue
			}
			if t := jx.Str(r, "topic"); t != "" && !u.TopicSame(t, topic) {
				continue
			}
			it := u.ContentForDisplay(r, nil, false)
			it["vote"] = c
			items = append(items, it)
		}
	}
	sort.SliceStable(items, func(i, j int) bool { return jx.Int(items[i], "timestamp") > jx.Int(items[j], "timestamp") })
	if len(items) > proposalsLimit {
		items = items[:proposalsLimit]
	}
	return items
}

// UpdateHistoryProposals — change(запись) → новая запись или nil (не менять). Возвращает число изменённых.
func (u *U) UpdateHistoryProposals(change func(r jx.M) jx.M) int {
	changed := 0
	u.HistoryUpdate(func(h History) History {
		for _, c := range config.ProposalCollections {
			for i, r := range h[c] {
				if n := change(r); n != nil {
					h[c][i] = n
					changed++
				}
			}
		}
		if changed == 0 {
			return nil
		}
		return h
	})
	return changed
}

func (u *U) ProposalHide(image string) {
	var hidden []jx.M
	u.UpdateHistoryProposals(func(r jx.M) jx.M {
		if ContentImageKey(r) != image || jx.Bool(r, "proposal_hidden") {
			return nil
		}
		hidden = append(hidden, r)
		n := jx.Copy(r)
		n["proposal_hidden"] = true
		return n
	})
	for _, r := range hidden {
		for _, url := range MediaItemURLs(r) {
			u.MediaRemove(u.Ctx, url)
		}
	}
}

var proposalTmeRe = regexp.MustCompile(`^https://t\.me/([A-Za-z0-9_]{3,64})/(\d+)$`)

// proposalFetchMedia — медиа поста из источника: nil — источник недоступен, gone=true — пост удалён.
func (u *U) proposalFetchMedia(r jx.M) (m jx.M, gone bool) {
	image := ContentImageKey(r)
	if mm := proposalTmeRe.FindStringSubmatch(jx.Str(r, "source_url")); mm != nil {
		var id int64
		for _, ch := range mm[2] {
			id = id*10 + int64(ch-'0')
		}
		html := u.TelegramChannelPageCached(mm[1], id+1)
		if html == "" {
			return nil, false
		}
		for _, p := range TelegramPostsFromHTML(html, jx.M{"id": mm[1]}, 30, true, 0) {
			if jx.Str(p, "post_id") != mm[2] {
				continue
			}
			img := jx.Str(p, "image")
			for i, g := range jx.Maps(r, "gallery") {
				if jx.Str(g, "image") == image {
					if pg := jx.Maps(p, "gallery"); i < len(pg) {
						img = jx.Str(pg[i], "image")
					}
				}
			}
			return jx.M{"video": jx.Str(p, "video"), "gallery": orMaps(jx.Maps(p, "gallery")), "image": img}, false
		}
		return nil, true
	}
	if board, thread, post := jx.Str(r, "board"), jx.Str(r, "thread_id"), jx.Str(r, "post_id"); board != "" && thread != "" && post != "" {
		data := u.Fetch2chJSON(board, thread)
		if data == nil {
			return nil, false
		}
		for _, p := range DvachThreadPosts(data) {
			if jx.Str(p, "num") != post {
				continue
			}
			files := DvachPostMedia(board, p, true)
			video := ""
			for _, f := range files {
				if jx.Str(f, "image") == image {
					video = jx.Str(f, "video")
				}
			}
			gallery := []jx.M{}
			if len(files) > 1 {
				gallery = files
			}
			return jx.M{"video": video, "gallery": gallery}, false
		}
		return nil, true
	}
	return jx.M{"video": "", "gallery": []jx.M{}}, false
}

func (a *App) galleryDownloadImage(url string) []byte {
	if url == "" {
		return nil
	}
	b := a.HTTPGet(url, 20*time.Second)
	if len(b) == 0 || len(b) > config.MaxImageBytes {
		return nil
	}
	return b
}

// proposalCacheMedia — файлы поста в кэш, пока ссылки свежие.
func (u *U) proposalCacheMedia(r, m jx.M) jx.M {
	m = jx.Defaults(jx.Copy(m), jx.M{"video_local": "", "image_local": "", "image": ""})
	key := ContentImageKey(r)
	video := jx.Str(m, "video")
	if video == "" {
		video = jx.Str(r, "video")
	}
	if video != "" && jx.Str(r, "video_local") == "" {
		if f := u.DownloadVideoTemp(video, config.VideoMaxBytes, nil); f != "" {
			m["video_local"] = u.CacheDownloadedVideo(video, f)
			os.Remove(f)
		}
	}
	if u.MediaExisting(jx.Str(r, "image_local")) == "" {
		b := u.galleryDownloadImage(jx.Str(m, "image"))
		if b == nil {
			b = u.galleryDownloadImage(key)
		}
		if b != nil {
			m["image_local"] = u.MediaStoreImage(key, galleryPrepareImage(b))
		}
	}
	gallery := jx.Maps(m, "gallery")
	if len(gallery) == 0 {
		gallery = jx.Maps(r, "gallery")
	}
	if len(gallery) > 1 {
		m["gallery"] = u.GalleryCacheFiles(gallery, key)
	}
	return m
}

func proposalApply(r, m jx.M) jx.M {
	n := jx.Copy(r)
	pick := func(k string) {
		if v := m[k]; v != nil && jx.ToStr(v) != "" {
			n[k] = v
		}
	}
	pick("video")
	pick("video_local")
	pick("image_local")
	if g := jx.Maps(m, "gallery"); len(g) > 0 {
		n["gallery"] = g
	}
	n["media_checked"], n["media_cached"] = true, true
	return n
}

// ProposalRefetch — загрузить медиа одной записи заново; gone — пост удалён у источника.
func (u *U) ProposalRefetch(image string) (jx.M, bool, error) {
	h := sha1.Sum([]byte(image))
	key := "proposal_refetch:" + u.ID + ":" + hex.EncodeToString(h[:])[:12]
	if now()-KV(u.App, u.Ctx, key, int64(0)) < 5 {
		return nil, false, &Error{429, "Эта запись уже загружается, подождите несколько секунд."}
	}
	u.KVSet(u.Ctx, key, now(), time.Minute)
	var record jx.M
	hist := u.History()
	for _, c := range config.ProposalCollections {
		for _, r := range hist[c] {
			if record == nil && ContentImageKey(r) == image {
				record = r
			}
		}
	}
	if record == nil {
		return nil, false, NotFound("Запись не найдена.")
	}
	m, gone := u.proposalFetchMedia(record)
	if m == nil && !gone {
		m, gone = u.proposalFetchMedia(record) // источник мог моргнуть: одна повторная попытка
	}
	if gone {
		u.UpdateHistoryProposals(func(r jx.M) jx.M {
			if ContentImageKey(r) != image {
				return nil
			}
			n := jx.Copy(r)
			n["media_gone"] = true
			return n
		})
		return nil, true, nil
	}
	if m == nil {
		return nil, false, &Error{503, "Источник не отвечает, попробуйте позже."}
	}
	m = u.proposalCacheMedia(record, m)
	if jx.Str(m, "image_local") == "" && jx.Str(m, "video_local") == "" && u.MediaExisting(jx.Str(record, "image_local")) == "" && u.MediaExisting(jx.Str(record, "video_local")) == "" {
		return nil, false, &Error{502, "Не удалось скачать файл: источник не отдал его."}
	}
	u.UpdateHistoryProposals(func(r jx.M) jx.M {
		if ContentImageKey(r) != image {
			return nil
		}
		return proposalApply(r, m)
	})
	return u.ContentForDisplay(proposalApply(record, m), nil, true), false, nil
}

func (u *U) proposalPending() []jx.M {
	var out []jx.M
	h := u.History()
	for _, c := range config.ProposalCollections {
		for _, r := range h[c] {
			if !jx.Bool(r, "media_cached") && !jx.Bool(r, "proposal_hidden") && !jx.Bool(r, "media_gone") {
				out = append(out, r)
			}
		}
	}
	return out
}

func (u *U) ProposalProgress() jx.M { return KV(u.App, u.Ctx, "proposal_progress:"+u.ID, jx.M{}) }

func (u *U) proposalProgressWrite(p jx.M) { u.KVSet(u.Ctx, "proposal_progress:"+u.ID, p, 24*time.Hour) }

var backfillRuns sync.Map // uid → struct{}

// ProposalBackfillAsync — несверенные записи достраиваются в фоне. true — идёт.
func (u *U) ProposalBackfillAsync() bool {
	if _, running := backfillRuns.Load(u.ID); running {
		return true
	}
	pending := u.proposalPending()
	if len(pending) == 0 && now()-u.CheckedAt("proposals") < config.PoolLivenessInterval {
		return false
	}
	if _, loaded := backfillRuns.LoadOrStore(u.ID, struct{}{}); loaded {
		return true
	}
	u.proposalProgressWrite(jx.M{"total": len(pending), "done": 0, "running": true, "updated_at": now()})
	uid := u.ID
	u.Go("backfill", func(ctx context.Context) {
		defer backfillRuns.Delete(uid)
		w := u.User(ctx, uid, nil)
		w.ProposalPruneDeadThreads(false)
		for i := 0; i < 50 && ctx.Err() == nil; i++ {
			if w.ProposalBackfillMedia(6) == 0 && len(w.proposalPending()) == 0 {
				break
			}
		}
		p := w.ProposalProgress()
		p["running"], p["updated_at"] = false, now()
		w.proposalProgressWrite(p)
	})
	return true
}

func (u *U) ProposalRefreshing() bool {
	_, running := backfillRuns.Load(u.ID)
	return running
}

// ProposalBackfillMedia — до limit записей сверяются с источником и кэшируются. Возвращает число обновлённых.
func (u *U) ProposalBackfillMedia(limit int) int {
	todo := map[string]jx.M{}
	var order []string
	for _, r := range u.proposalPending() {
		if len(order) < limit {
			k := ContentImageKey(r)
			if _, ok := todo[k]; !ok {
				todo[k] = r
				order = append(order, k)
			}
		}
	}
	if len(order) == 0 {
		return 0
	}
	found, gone := map[string]jx.M{}, map[string]bool{}
	for _, k := range order {
		r := todo[k]
		m, g := u.proposalFetchMedia(r)
		switch {
		case g:
			gone[k] = true
		case m != nil:
			c := u.proposalCacheMedia(r, m)
			if jx.Str(c, "image_local") != "" || jx.Str(c, "video_local") != "" || u.MediaExisting(jx.Str(r, "image_local")) != "" ||
				u.MediaExisting(jx.Str(r, "video_local")) != "" || jx.Str(r, "image_local") == "" {
				found[k] = c
			}
		}
		p := u.ProposalProgress()
		p["done"], p["updated_at"] = jx.Int(p, "done")+1, now()
		u.proposalProgressWrite(p)
	}
	if len(found) == 0 && len(gone) == 0 {
		// источник недоступен: помечаем проверенными, чтобы не крутиться по кругу
		return 0
	}
	return u.UpdateHistoryProposals(func(r jx.M) jx.M {
		k := ContentImageKey(r)
		if m, ok := found[k]; ok && !jx.Bool(r, "media_cached") {
			return proposalApply(r, m)
		}
		if gone[k] && !jx.Bool(r, "media_gone") {
			n := jx.Copy(r)
			n["media_gone"] = true
			return n
		}
		return nil
	})
}

// dvachThreadsLiveness — ключ треда → false (удалён) | номера живых постов | nil (неизвестно).
func (a *App) dvachThreadsLiveness(threads map[string][2]string) map[string]any {
	states := map[string]any{}
	for key, t := range threads {
		state, posts := a.DvachThreadState(t[0], t[1])
		switch state {
		case "gone":
			states[key] = false
		case "alive":
			nums := map[string]bool{}
			for _, p := range posts {
				nums[jx.Str(p, "num")] = true
			}
			states[key] = nums
		default:
			states[key] = nil
		}
		time.Sleep(300 * time.Millisecond)
	}
	return states
}

func dvachItemGone(item jx.M, states map[string]any) bool {
	switch s := states[jx.Str(item, "board")+"/"+jx.Str(item, "thread_id")].(type) {
	case bool:
		return !s
	case map[string]bool:
		return jx.Str(item, "post_id") != "" && !s[jx.Str(item, "post_id")]
	}
	return false
}

// ProposalPruneDeadThreads — прячет посты мёртвых тредов без своей копии картинки.
func (u *U) ProposalPruneDeadThreads(force bool) int {
	if !force && now()-u.CheckedAt("proposals") < config.PoolLivenessInterval {
		return 0
	}
	threads := map[string][2]string{}
	h := u.History()
	for _, c := range config.ProposalCollections {
		for _, r := range h[c] {
			if !jx.Bool(r, "proposal_hidden") && jx.Str(r, "board") != "" && jx.Str(r, "thread_id") != "" && len(threads) < config.PoolLivenessMaxThreads {
				threads[jx.Str(r, "board")+"/"+jx.Str(r, "thread_id")] = [2]string{jx.Str(r, "board"), jx.Str(r, "thread_id")}
			}
		}
	}
	states := u.dvachThreadsLiveness(threads)
	hidden := u.UpdateHistoryProposals(func(r jx.M) jx.M {
		if jx.Bool(r, "proposal_hidden") || !dvachItemGone(r, states) || u.MediaExisting(jx.Str(r, "image_local")) != "" {
			return nil
		}
		n := jx.Copy(r)
		n["proposal_hidden"], n["proposal_dead"] = true, true
		return n
	})
	u.MarkChecked("proposals")
	return hidden
}

var oldMediaRe = regexp.MustCompile(`^media_cache/([a-f0-9]{32}\.[a-z0-9]{2,5})$`)

// mediaRepairRecord — ссылки на пропавшие файлы очищаются; запись «Предложки» с источником помечается на повторную загрузку.
func (a *App) mediaRepairRecord(r jx.M, redownload bool, stats map[string]int) jx.M {
	changed := false
	fix := func(e jx.M) jx.M {
		e = jx.Copy(e)
		for _, f := range []string{"image_local", "video_local"} {
			url := jx.Str(e, f)
			if url == "" {
				continue
			}
			if m := oldMediaRe.FindStringSubmatch(url); m != nil && fileExists(a.Cfg.MediaDir+"/"+m[1]) {
				e[f] = strings.TrimRight(a.Cfg.MediaURL, "/") + "/" + m[1]
				stats["fixed"]++
				changed = true
			} else if a.MediaExisting(url) == "" {
				e[f] = ""
				stats["cleared"]++
				changed = true
			}
		}
		return e
	}
	before := stats["cleared"]
	n := fix(r)
	if g := jx.Maps(r, "gallery"); len(g) > 0 {
		var ng []jx.M
		for _, x := range g {
			ng = append(ng, fix(x))
		}
		n["gallery"] = ng
	}
	hasSource := strings.HasPrefix(jx.Str(r, "source_url"), "https://t.me/") || (jx.Str(r, "board") != "" && jx.Str(r, "thread_id") != "" && jx.Str(r, "post_id") != "")
	if redownload && stats["cleared"] > before && hasSource && !jx.Bool(r, "media_gone") {
		delete(n, "media_cached")
		delete(n, "media_checked")
		stats["marked"]++
	}
	if !changed {
		return nil
	}
	return n
}

// MediaRepairUser — ремонт ссылок на кэш в пуле и истории.
func (u *U) MediaRepairUser(dryRun bool) map[string]int {
	stats := map[string]int{"fixed": 0, "cleared": 0, "marked": 0}
	u.PoolUpdate(func(pool []jx.M) []jx.M {
		changed := false
		for i, r := range pool {
			if n := u.mediaRepairRecord(r, false, stats); n != nil {
				pool[i], changed = n, true
			}
		}
		if !changed || dryRun {
			return nil
		}
		return pool
	})
	u.HistoryUpdate(func(h History) History {
		changed := false
		for _, c := range config.HistoryCollections {
			for i, r := range h[c] {
				if n := u.mediaRepairRecord(r, contains(config.ProposalCollections, c), stats); n != nil {
					h[c][i], changed = n, true
				}
			}
		}
		if !changed || dryRun {
			return nil
		}
		return h
	})
	return stats
}
