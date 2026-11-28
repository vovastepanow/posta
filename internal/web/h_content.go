package web

import (
	"crypto/rand"
	"encoding/hex"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/posta"
)

// Подбор, предложения, просмотр материалов и анализ изображения.

func randUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func init() {
	get("get_item_video", getItemVideo)
	post("retry_item_video", retryItemVideo)
	get("get_current_item", getCurrentItem)
	get("content_pool", contentPool)
	post("content_personal_blogs", func(c *Ctx) (any, error) {
		mode := "exclude"
		if jx.Bool(c.In, "include") {
			mode = "any"
		}
		if err := c.U.SavePersonalBlogMode(c.U.CurrentTopic(), mode); err != nil {
			return nil, err
		}
		return ok(jx.M{"personal_blog_mode": mode}), nil
	})
	get("parser_status", parserStatus)
	get("studio_live", studioLive)
	get("studio_rejected", func(c *Ctx) (any, error) {
		return jx.M{"items": c.U.RejectedPosts(c.U.SearchTopicKey())}, nil
	})
	post("tab_open", func(c *Ctx) (any, error) {
		tab, err := tabID(c)
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"parser": c.U.TabOpen(tab)}), nil
	})
	post("tab_heartbeat", func(c *Ctx) (any, error) {
		tab, err := tabID(c)
		if err != nil {
			return nil, err
		}
		return c.U.TabHeartbeat(tab), nil
	})
	post("tab_close", func(c *Ctx) (any, error) {
		tab, err := tabID(c)
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"stopped": c.U.TabClose(tab)}), nil
	})
	refresh := func(c *Ctx) (any, error) {
		if r := c.U.StartParser(true); r == "started" || r == "running" {
			return jx.M{"status": "loading", "message": "Ищем контент..."}, nil
		}
		return nil, fail(409, "Фоновый подбор отключён. Включите его вручную в разделе «Источники».")
	}
	post("refresh_content", refresh)
	post("control_search", func(c *Ctx) (any, error) {
		if err := ownerOnly(c); err != nil {
			return nil, err
		}
		c.U.ResumeParser()
		switch c.U.StartParser(true) {
		case "started":
			return jx.M{"status": "loading", "message": "Сбор контента запущен."}, nil
		case "running":
			return jx.M{"status": "loading", "message": "Сбор контента уже идёт."}, nil
		default:
			return nil, fail(503, "Не удалось запустить сбор контента на сервере.")
		}
	})
	post("restart_content", func(c *Ctx) (any, error) {
		if err := ownerOnly(c); err != nil {
			return nil, err
		}
		c.U.ResumeParser()
		r := c.U.RestartParser()
		if r != "started" {
			if r == "running" {
				return nil, fail(409, "Предыдущий проход ещё останавливается. Попробуйте снова через несколько секунд.")
			}
			return nil, fail(503, "Не удалось перезапустить подбор на сервере.")
		}
		return ok(jx.M{"parser": r}), nil
	})
	post("stop_content", func(c *Ctx) (any, error) {
		if err := ownerOnly(c); err != nil {
			return nil, err
		}
		stopped := c.U.PauseParser()
		return ok(jx.M{"stopped": stopped, "stopping": c.A.ParserRunning(c.U.ID)}), nil
	})
	post("analyze_image", analyzeImage)
	post("vote", vote)
	get("proposals_progress", func(c *Ctx) (any, error) {
		p := c.U.ProposalProgress()
		return jx.M{"refreshing": c.U.ProposalRefreshing() && jx.Bool(p, "running"), "progress": p}, nil
	})
	get("proposals", func(c *Ctx) (any, error) {
		if err := c.U.PruneSelectedPosts(); err != nil {
			return nil, err
		}
		refreshing := c.U.ProposalBackfillAsync()
		var progress any
		if refreshing {
			progress = c.U.ProposalProgress()
		}
		return jx.M{"items": c.U.ProposalItems(c.U.History()), "refreshing": refreshing, "progress": progress}, nil
	})
	post("proposal_hide", func(c *Ctx) (any, error) {
		image := c.Str("image")
		if image == "" {
			return nil, fail(400, "Не указана запись.")
		}
		if err := c.U.ProposalHide(image); err != nil {
			return nil, err
		}
		return ok(nil), nil
	})
	post("proposal_refetch", func(c *Ctx) (any, error) {
		image := c.Str("image")
		if image == "" {
			return nil, fail(400, "Не указана запись.")
		}
		u, cancel := c.userDetached(5 * time.Minute)
		defer cancel()
		item, gone, err := u.ProposalRefetch(image)
		if err != nil {
			return nil, err
		}
		if gone {
			return jx.M{"gone": true}, nil
		}
		return item, nil
	})
	post("proposal_repair", func(c *Ctx) (any, error) {
		stats := c.U.MediaRepairUser(false)
		return ok(jx.M{"refreshing": c.U.ProposalBackfillAsync(), "repair": stats}), nil
	})
	post("debug_reset", func(c *Ctx) (any, error) { return nil, fail(403, "Демо-режим убран.") })
	get("article_read", articleRead)
}

func tabID(c *Ctx) (string, error) {
	t := c.Str("tab")
	if !posta.IsValidTabID(t) {
		return "", fail(400, "Некорректный идентификатор вкладки.")
	}
	return t, nil
}

// Explicit retry after Telegram account connection updates the saved pool
// record before enqueueing, rather than just changing the browser state.
func retryItemVideo(c *Ctx) (any, error) {
	u := c.U
	key := c.Str("item_key")
	if key == "" {
		return nil, fail(400, "Не указана запись.")
	}
	item := posta.PoolFindItem(u.PoolForCurrentTopic(), key)
	if item == nil {
		return nil, fail(404, "Запись больше не доступна в предложке.")
	}
	if !u.TelegramUserConnected() {
		return nil, fail(409, "Подключите личный аккаунт Telegram.")
	}
	page := telegramPoolVideoPage(item)
	if page == "" {
		return nil, fail(400, "Запись не содержит скачиваемого видео Telegram.")
	}
	if jx.Str(item, "video") != "" {
		return u.TranslateDisplay(u.ContentForDisplay(item, nil, true)), nil
	}
	updated, err := startTelegramPoolVideo(c, item, false)
	if err != nil {
		return nil, err
	}
	return u.TranslateDisplay(u.ContentForDisplay(updated, nil, false)), nil
}

// A Telegram post URL alone does not prove that the record contains video.
func telegramPoolVideoPage(item jx.M) string {
	if !jx.Bool(item, "video_unavailable") && jx.Str(item, "video_via") != "tgu" {
		return ""
	}
	page := jx.Str(item, "video_page")
	if posta.VideoPageVia(page) != "tgu" {
		page = posta.SourcePostURL(item)
	}
	if posta.VideoPageVia(page) != "tgu" || posta.ContentItemKey(item) == "" {
		return ""
	}
	return page
}

// Try legacy poster-only records automatically once, but do not silently
// restart a failed download on every render. The explicit retry remains.
func telegramPoolVideoAutoEligible(item jx.M, connected bool) bool {
	return connected && jx.Str(item, "video") == "" && jx.Bool(item, "video_unavailable") &&
		jx.Str(item, "video_error") == "" && !jx.Bool(item, "video_auto_started") && telegramPoolVideoPage(item) != ""
}

func startTelegramPoolVideo(c *Ctx, item jx.M, automatic bool) (jx.M, error) {
	u := c.U
	key := posta.ContentItemKey(item)
	var updated jx.M
	var enqueue bool
	_, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
		for _, record := range pool {
			if posta.ContentItemKey(record) != key {
				continue
			}
			page := telegramPoolVideoPage(record)
			if page == "" || jx.Str(record, "video") != "" || (automatic && !telegramPoolVideoAutoEligible(record, true)) {
				updated = jx.Copy(record)
				return nil
			}
			record["video_unavailable"], record["video_via"], record["video_page"], record["video_error"] = false, "tgu", page, ""
			record["video_auto_started"] = true
			updated = jx.Copy(record)
			enqueue = true
			return pool
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if updated == nil {
		return nil, fail(404, "Записи уже нет в предложке.")
	}
	if enqueue {
		c.A.VideoEnqueue(c.R.Context(), u.ID, posta.ContentItemKey(updated), jx.Str(updated, "video_page"), "tgu", true)
	}
	return updated, nil
}

// Used for every route into the main review card, including explicit pool
// selection and stale fallback. Merely listing thumbnails never starts jobs.
func currentItemDisplay(c *Ctx, item jx.M, upcoming []jx.M) jx.M {
	u := c.U
	if telegramPoolVideoAutoEligible(item, u.TelegramUserConnected()) {
		if updated, err := startTelegramPoolVideo(c, item, true); err == nil {
			item = updated
		} else {
			item = jx.Copy(item)
			item["video_error"] = "Не удалось запустить автоматическое скачивание."
		}
	}
	return u.TranslateDisplay(u.ContentForDisplay(item, upcoming, true))
}

func getItemVideo(c *Ctx) (any, error) {
	key := c.Q("image") // legacy parameter name; the value is an item key.
	if key == "" {
		return nil, fail(400, "Не указана запись.")
	}
	item := posta.PoolFindItem(c.U.Pool(), key)
	if item == nil {
		item = c.U.SavedProposalRecord(key)
	}
	if item == nil {
		return nil, fail(404, "Записи уже нет в подборке или предложке.")
	}
	key = posta.ContentItemKey(item)
	video, local := jx.Str(item, "video"), jx.Str(item, "video_local")
	display := ""
	if video != "" {
		if display = c.A.MediaExisting(local); display == "" {
			display = video
		}
	}
	var progress any
	if video == "" {
		// Запись открыта на экране и ждёт ролик: он идёт первым, а не после роликов заранее поставленных соседних записей.
		// Обычный ролик YouTube заранее не качается — только здесь, когда его ждут на экране.
		via, page := jx.Str(item, "video_via"), jx.Str(item, "video_page")
		if via == "tgu" && !c.U.TelegramUserConnected() {
			return jx.M{"video_unavailable": true, "error": "Подключите личный аккаунт Telegram для скачивания."}, nil
		}
		if (via == "ytdlp" || via == "tgu") && page != "" && !jx.Bool(item, "video_unavailable") {
			c.A.VideoEnqueue(c.R.Context(), c.U.ID, key, page, via, true)
		}
		progress = c.A.VideoProgressPublic(c.R.Context(), c.U.ID, key)
	}
	return jx.M{"video": video, "video_local": local, "display_video": display, "video_unavailable": jx.Bool(item, "video_unavailable"), "error": jx.Str(item, "video_error"), "progress": progress}, nil
}

var warmingPoolItems sync.Map

// Prepare the next cards while the current one is being viewed.
func warmUpcomingItems(c *Ctx, items []jx.M) {
	for _, item := range items {
		key := c.U.ID + ":" + posta.ContentItemKey(item)
		if _, loaded := warmingPoolItems.LoadOrStore(key, struct{}{}); loaded {
			continue
		}
		item := jx.Copy(item)
		u, cancel := c.userDetached(45 * time.Second)
		go func() {
			defer cancel()
			defer warmingPoolItems.Delete(key)
			var checks sync.WaitGroup
			checks.Add(1)
			go func() {
				defer checks.Done()
				u.TelegramPreflightItem(item, true)
			}()
			u.TranslateWarmCache(item)
			checks.Wait()
		}()
	}
}

// Return compact previews for every visible unrated record, without
// translating, downloading or preflighting the whole pool.
func contentPool(c *Ctx) (any, error) {
	u := c.U
	items := []jx.M{}
	if u.TopicSaveDenied(u.CurrentTopic()) != "" {
		return jx.M{"items": items}, nil
	}
	for _, item := range u.PoolWithoutLostVideo(u.PoolForCurrentTopic()) {
		key := posta.ContentItemKey(item)
		if key == "" {
			continue
		}
		text := []rune(posta.CleanPostDisplayText(jx.Str(item, "text"), item))
		if len(text) > 420 {
			text = append(text[:420], '…')
		}
		image := u.MediaExisting(jx.Str(item, "image_local"))
		if image == "" {
			image = posta.ContentImageKey(item)
		}
		items = append(items, jx.M{"item_key": key, "text": string(text), "image": image,
			"text_post": jx.Bool(item, "text_post"), "article": jx.Map(item, "article") != nil,
			"video": posta.PostItemHasVideo(item), "source": jx.Str(item, "source"),
			"relevance": item["relevance"], "posted_at": item["posted_at"]})
	}
	return jx.M{"items": items}, nil
}

func getCurrentItem(c *Ctx) (any, error) {
	u := c.U
	if err := u.PruneOldPool(); err != nil {
		return nil, err
	}
	u.PruneUnavailablePoolVideos()
	topic := u.CurrentTopic()
	if u.TopicSaveDenied(topic) != "" {
		return jx.M{"status": "empty", "message": "Тема «" + topic + "» недоступна: выберите другую тему."}, nil
	}
	pool := u.PoolWithoutLostVideo(u.PoolForCurrentTopic())
	if key := c.Q("item_key"); key != "" {
		if item := posta.PoolFindItem(pool, key); item != nil {
			return currentItemDisplay(c, item, nil), nil
		}
		return nil, fail(404, "Запись больше не доступна в предложке.")
	}
	if len(pool) > 0 {
		// Continue replenishing when every visible record is older than an
		// hour, but never hide saved pool records just because newer posts
		// arrived. The collector and the display have different age rules.
		hasFresh := false
		for _, item := range pool {
			if posta.PoolItemIsFresh(item, 0) {
				hasFresh = true
				break
			}
		}
		if !hasFresh {
			u.StartParser(false)
		}
		warmUpcomingItems(c, pool[1:min(4, len(pool))])
		return currentItemDisplay(c, pool[0], pool[1:min(4, len(pool))]), nil
	}
	started := u.StartParser(false)
	rt := c.A.ParserRuntimeSeconds(u.ID)
	if archive := u.PoolWithoutLostVideo(u.PoolStaleForCurrentTopic()); len(archive) > 0 {
		warmUpcomingItems(c, archive[1:min(4, len(archive))])
		r := currentItemDisplay(c, archive[0], archive[1:min(4, len(archive))])
		return r, nil
	}
	if started != "started" && started != "running" {
		if started == "paused" {
			return jx.M{"status": "empty", "message": "Сбор остановлен администратором. Обновите страницу, чтобы возобновить работу."}, nil
		}
		return jx.M{"status": "empty", "message": "Нового контента пока нет. Можно повторить поиск или добавить запись вручную."}, nil
	}
	var runtime int64
	if rt != nil {
		runtime = *rt
	}
	return jx.M{"status": "loading", "message": "Ищем контент...", "parser_runtime": runtime}, nil
}

func parserStatus(c *Ctx) (any, error) {
	running := c.A.ParserRunning(c.U.ID)
	phase := "ничего не делается"
	if running {
		if phase = c.A.ParserPhaseText(c.U.ID); phase == "" {
			phase = "парсер работает"
		}
	}
	r := jx.M{"running": running, "stopping": running && c.A.ParserPaused(c.U.ID), "paused": c.A.ParserPaused(c.U.ID), "phase": phase, "activity": c.A.ParserActivity(c.U.ID)}
	if running {
		r["runtime"], r["progress"] = c.A.ParserRuntimeSeconds(c.U.ID), c.A.ParserProgressPublic(c.U.ID)
	}
	return r, nil
}

// studioLive — сводка подбора: последние отобранные посты пула (новые сверху) и ход сборщика одним запросом.
func studioLive(c *Ctx) (any, error) {
	u := c.U
	if err := u.PruneOldPool(); err != nil {
		return nil, err
	}
	if err := u.PruneSelectedPosts(); err != nil {
		return nil, err
	}
	u.PruneUnavailablePoolVideos()
	pool := u.PoolWithoutLostVideo(u.PoolForCurrentTopic())
	// The badge and recent feed describe the same visible pool snapshot.
	poolCount := len(pool)
	sort.SliceStable(pool, func(i, j int) bool { return jx.Int(pool[i], "timestamp") > jx.Int(pool[j], "timestamp") })
	items := make([]jx.M, 0, min(30, poolCount))
	for _, it := range pool[:min(30, poolCount)] {
		img := u.MediaExisting(jx.Str(it, "image_local"))
		if img == "" {
			img = jx.Str(it, "image")
		}
		vid := ""
		if jx.Str(it, "video") != "" {
			if vid = u.MediaExisting(jx.Str(it, "video_local")); vid == "" {
				vid = jx.Str(it, "video")
			}
		}
		items = append(items, jx.M{"key": posta.ContentImageKey(it), "display_image": img, "display_video": vid, "text": jx.Str(it, "text"),
			"description": jx.Str(it, "description"), "why_relevant": jx.Str(it, "why_relevant"), "source": jx.Str(it, "source"),
			"source_key": jx.Str(it, "source_key"), "source_url": jx.Str(it, "source_url"), "topic": jx.Str(it, "topic"), "board": jx.Str(it, "board"),
			"posted_at": jx.Int(it, "posted_at"), "timestamp": jx.Int(it, "timestamp"), "relevance": jx.Int(it, "relevance")})
	}
	r, err := parserStatus(c)
	if err != nil {
		return nil, err
	}
	st := r.(jx.M)
	if a, _ := st["activity"].([]string); len(a) > 8 {
		st["activity"] = a[len(a)-8:]
	}
	// журнал подбора: 40 строк с шагом и временем; шаг текущего прохода и воронка последнего — вне работы
	st["journal"], st["stage"] = c.A.ParserJournal(c.U.ID, 40), c.A.ParserStageOf(c.U.ID)
	if running, _ := st["running"].(bool); !running {
		st["last_pass"] = c.A.ParserLastPass(c.U.ID)
	}
	st["items"], st["pool"], st["topic"] = items, poolCount, u.CurrentTopic()
	st["source_discovery"] = u.SourcePipelinePublic(u.CurrentTopic())
	return st, nil
}

func analyzeImage(c *Ctx) (any, error) {
	data, err := readRequestImage(c)
	if err != nil {
		return nil, err
	}
	u, cancel := c.userDetached(10 * time.Minute)
	defer cancel()
	return u.AnalyzeImageLocal(data, u.History(), u.CurrentTopic())
}

func vote(c *Ctx) (any, error) {
	u := c.U
	v, image := c.Str("vote"), c.Str("image")
	coll, known := config.ContentVoteCollections[v]
	if (!known && v != "postpone") || image == "" {
		return nil, fail(400, "Некорректный голос или запись")
	}
	var reason any
	if v == "wrong" && contains(config.ComplaintReasons, c.Str("reason")) {
		reason = c.Str("reason")
	}
	before, after, item, record, err := u.VotePool(image, v, reason)
	if err != nil {
		return nil, err
	}
	if item == nil {
		return nil, fail(404, "Запись больше не доступна в предложке.")
	}
	if record != nil {
		c.A.CH.Vote(posta.VoteEvent(u.ID, jx.Str(record, "topic"), v, reason, record))
	}
	if coll == "liked" {
		// The rating has committed. Maintenance failure must not tell the
		// client the vote was lost or cause the user to retry it.
		if err := u.PruneSelectedPosts(); err != nil {
			u.Log("Не удалось очистить старые сохранённые посты: %v", err)
		}
	}
	if v != "postpone" {
		u.MarkSourceRest(u.SearchTopicKey(), item)
	}
	if !contains(config.ProposalCollections, coll) {
		c.A.MediaReleaseRemoved(c.R.Context(), before, after)
	}
	return ok(nil), nil
}

func articleRead(c *Ctx) (any, error) {
	link := strings.TrimSpace(c.Q("url"))
	if !c.U.ArticleURLIsOwn(link) {
		return nil, fail(403, "Статья не из ваших записей.")
	}
	a := c.A.ArticleBlocksFor(link)
	if a == nil {
		return nil, fail(404, "Статья не открылась или на странице нет текста.")
	}
	return jx.M{"url": link, "article": a}, nil
}


func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// readRequestImage — файл image_file из формы или картинка по ссылке image_url.
func readRequestImage(c *Ctx) ([]byte, error) {
	if strings.HasPrefix(c.R.Header.Get("Content-Type"), "multipart/") {
		if err := parseUploadForm(c, config.MaxImageBytes+(1<<20), config.MaxImageBytes); err != nil {
			return nil, err
		}
		defer c.R.MultipartForm.RemoveAll()
		if f, h, err := c.R.FormFile("image_file"); err == nil {
			defer f.Close()
			if h.Size > config.MaxImageBytes {
				return nil, fail(400, "Изображение должно быть не больше 10 МБ.")
			}
			return io.ReadAll(f)
		}
		c.In["image_url"] = c.R.FormValue("image_url")
	}
	link := strings.TrimSpace(c.Str("image_url"))
	if link == "" {
		return nil, fail(400, "Добавьте ссылку или выберите файл изображения.")
	}
	return c.A.FetchPublicImage(link)
}
