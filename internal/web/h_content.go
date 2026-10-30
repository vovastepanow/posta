package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/posta"
)

// Вкладки «Подбор», «Предложка», «Дерево», «Тест ИИ» и базы данных.

func randUID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func init() {
	get("get_item_video", getItemVideo)
	get("get_current_item", getCurrentItem)
	get("parser_status", parserStatus)
	get("studio_live", studioLive)
	get("studio_city", studioCity)
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
		return nil, fail(503, "Не удалось запустить поиск контента на сервере.")
	}
	post("refresh_content", refresh)
	post("control_search", refresh)
	get("settings", settings)
	get("topic_record", topicRecord)
	get("topics", func(c *Ctx) (any, error) {
		var cur any
		if e := c.U.TopicLookup(c.U.CurrentTopic()); e != nil {
			cur = e.ID
		}
		return jx.M{"current": cur, "topics": topicsPublicList(c.U)}, nil
	})
	post("save_topic", saveTopic)
	post("save_second_topic", saveSecondTopic)
	post("save_query", saveQuery)
	post("save_filters", saveFilters)
	post("adult_allow", adultAllow)
	post("topic_reprofile", func(c *Ctx) (any, error) {
		e := c.U.TopicByID(c.Str("id"))
		if e == nil {
			return nil, fail(400, "Такой темы нет.")
		}
		cp := *e
		cp.Profile = nil
		_, err := c.A.TopicSave(c.R.Context(), &cp)
		return ok(nil), err
	})
	post("topic_parent", func(c *Ctx) (any, error) {
		e, err := c.U.TopicSetParent(c.Str("id"), c.Str("parent"))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"topic_info": c.U.TopicPublic(e, nil)}), nil
	})
	post("topic_rubric", func(c *Ctx) (any, error) {
		e, err := c.U.TopicSetRubric(c.Str("id"), c.Str("rubric"))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"topic_info": c.U.TopicPublic(e, nil)}), nil
	})
	post("analyze_image", analyzeImage)
	post("vote", vote)
	get("proposals_progress", func(c *Ctx) (any, error) {
		p := c.U.ProposalProgress()
		return jx.M{"refreshing": c.U.ProposalRefreshing() && jx.Bool(p, "running"), "progress": p}, nil
	})
	get("proposals", func(c *Ctx) (any, error) {
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
		c.U.ProposalHide(image)
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
	get("database_options", databaseOptions)
	get("database_read", databaseRead)
	for _, a := range []string{"database_add", "database_update", "database_delete"} {
		post(a, databaseChange)
	}
	post("database_reset", func(c *Ctx) (any, error) {
		removed := c.U.ResetProjectData()
		return ok(jx.M{"removed_media": removed, "parser": c.U.StartParser(true)}), nil
	})
	post("debug_reset", func(c *Ctx) (any, error) { return nil, fail(403, "Демо-режим убран.") })
	get("topic_info", topicInfo)
	post("topic_format_save", topicFormatSave)
	get("article_read", articleRead)
}

func tabID(c *Ctx) (string, error) {
	t := c.Str("tab")
	if !posta.IsValidTabID(t) {
		return "", fail(400, "Некорректный идентификатор вкладки.")
	}
	return t, nil
}

func getItemVideo(c *Ctx) (any, error) {
	image := c.Q("image")
	var item jx.M
	for _, i := range c.U.Pool() {
		if posta.ContentImageKey(i) == image {
			item = i
			break
		}
	}
	if item == nil {
		return nil, fail(404, "Записи уже нет в подборке.")
	}
	video, local := jx.Str(item, "video"), jx.Str(item, "video_local")
	display := ""
	if video != "" {
		if display = c.A.MediaExisting(local); display == "" {
			display = video
		}
	}
	var progress any
	if video == "" {
		progress = c.A.VideoProgressPublic(c.R.Context(), c.U.ID, image)
	}
	return jx.M{"video": video, "video_local": local, "display_video": display, "video_unavailable": jx.Bool(item, "video_unavailable"), "progress": progress}, nil
}

func getCurrentItem(c *Ctx) (any, error) {
	u := c.U
	topic := u.CurrentTopic()
	if u.TopicSaveDenied(topic) != "" {
		return jx.M{"status": "empty", "message": "Тема «" + topic + "» недоступна: выберите другую тему."}, nil
	}
	if pool := u.PoolWithoutLostVideo(u.PoolForCurrentTopic()); len(pool) > 0 {
		return u.TranslateDisplay(u.ContentForDisplay(pool[0], pool[1:min(4, len(pool))], true)), nil
	}
	started := u.StartParser(false)
	if started != "started" && started != "running" {
		return jx.M{"status": "empty", "message": "Нового контента пока нет. Можно повторить поиск или добавить запись вручную."}, nil
	}
	rt := c.A.ParserRuntimeSeconds(u.ID)
	if rt != nil && *rt >= config.ParserSlowThresholdSeconds {
		frozen := map[string]bool{}
		if jx.Bool(u.ContentFilterValues(), "active_sources_only") {
			frozen = u.FrozenSourceLabels()
		}
		key, adult := u.SearchTopicKey(), u.AdultAllowed()
		var archive []jx.M
		for _, i := range u.PoolArchiveItems(frozen) {
			if u.PoolItemInTopic(i, topic, key, adult) {
				archive = append(archive, i)
			}
		}
		if len(archive) > 0 {
			r := u.TranslateDisplay(u.ContentForDisplay(archive[0], archive[1:min(4, len(archive))], true))
			r["archive"], r["fire_warning"], r["parser_runtime"], r["parser_status"] = true, "🔥 Пост скоро исчезнет", *rt, "slow"
			return r, nil
		}
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
	r := jx.M{"running": running, "phase": phase, "activity": c.A.ParserActivity(c.U.ID)}
	if running {
		r["runtime"], r["progress"] = c.A.ParserRuntimeSeconds(c.U.ID), c.A.ParserProgressPublic(c.U.ID)
	}
	return r, nil
}

// studioLive — «смотровое окно» студии: последние отобранные посты пула (новые сверху) и ход сборщика одним запросом.
func studioLive(c *Ctx) (any, error) {
	u := c.U
	pool := u.PoolWithoutLostVideo(u.PoolForCurrentTopic())
	sorted := append([]jx.M{}, pool...)
	sort.SliceStable(sorted, func(i, j int) bool { return jx.Int(sorted[i], "timestamp") > jx.Int(sorted[j], "timestamp") })
	items := []jx.M{}
	for _, it := range sorted[:min(30, len(sorted))] {
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
			"source_key": jx.Str(it, "source_key"), "source_url": jx.Str(it, "source_url"), "board": jx.Str(it, "board"),
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
	// журнал студии: 40 строк с шагом и временем; шаг текущего прохода и воронка последнего — вне работы
	st["journal"], st["stage"] = c.A.ParserJournal(c.U.ID, 40), c.A.ParserStageOf(c.U.ID)
	if running, _ := st["running"].(bool); !running {
		st["last_pass"] = c.A.ParserLastPass(c.U.ID)
	}
	st["items"], st["pool"], st["topic"] = items, len(pool), u.CurrentTopic()
	return st, nil
}

// studioCity — «город» студии: по дням за год — пул по текущей теме и публикации.
func studioCity(c *Ctx) (any, error) {
	start, pool, published, err := c.U.StudioCity(time.Now())
	if err != nil {
		return nil, err
	}
	return jx.M{"start": start.Format("2006-01-02"), "days": len(pool), "pool": pool, "published": published, "topic": c.U.CurrentTopic()}, nil
}

func settings(c *Ctx) (any, error) {
	u := c.U
	topic := u.CurrentTopic()
	var info any
	if e := c.A.TopicEnsure(c.R.Context(), topic); e != nil {
		info = u.TopicPublic(e, nil)
	}
	return jx.M{"topic": topic, "topic2": u.CurrentTopic2(), "query": u.CurrentQuery(), "topic_info": info, "filters": u.ContentFilterValues(),
		"adult_allowed": u.AdultAllowed(), "adult_available": c.A.TBool("adult_available")}, nil
}

func topicRecord(c *Ctx) (any, error) {
	e := c.U.TopicByID(c.Q("id"))
	if e == nil {
		return nil, fail(404, "Такой темы нет.")
	}
	return jx.M{"topic": e, "summary": c.U.TopicPublic(e, c.A.Catalog())}, nil
}

func topicsPublicList(u *posta.U) []jx.M {
	d := u.Catalog()
	adult := u.AdultAllowed()
	var out []jx.M
	for _, e := range u.TopicRegistry() {
		if adult || !u.TopicIsRestricted(e.Title) {
			out = append(out, u.TopicPublic(e, d))
		}
	}
	key := func(t jx.M) (string, int, string) {
		parent := jx.Map(t, "parent")
		if parent != nil {
			return jx.Str(parent, "title"), 1, jx.Str(t, "title")
		}
		return jx.Str(t, "title"), 0, jx.Str(t, "title")
	}
	sort.SliceStable(out, func(i, j int) bool {
		a1, a2, a3 := key(out[i])
		b1, b2, b3 := key(out[j])
		if a1 != b1 {
			return a1 < b1
		}
		if a2 != b2 {
			return a2 < b2
		}
		return a3 < b3
	})
	if out == nil {
		out = []jx.M{}
	}
	return out
}

func saveTopic(c *Ctx) (any, error) {
	u := c.U
	typed := posta.NormalizeTopic(c.Str("topic"))
	if typed == "" {
		return nil, fail(400, "Тема поиска не может быть пустой.")
	}
	if d := u.TopicSaveDenied(typed); d != "" {
		return nil, fail(409, d)
	}
	previous, previousKey := u.CurrentTopic(), u.SearchTopicKey()
	stopped := !u.TopicSame(typed, previous) && c.A.StopParser(u.ID)
	res := u.TopicResolve(typed, jx.Bool(c.In, "force_new"))
	if d := u.TopicSaveDenied(res.Topic.Title); d != "" {
		if stopped {
			u.StartParser(true)
		}
		return nil, fail(409, d)
	}
	topic := res.Topic.Title
	if err := u.SaveSettings(jx.M{"topic": topic}); err != nil {
		return nil, err
	}
	lacks := u.TopicLacksSources(c.A.Catalog(), topic)
	if lacks {
		c.A.RequestSourceDiscovery()
	}
	parser := "unchanged"
	changed := !u.TopicSame(topic, previous)
	if changed {
		before := u.Pool()
		after, _ := u.PoolUpdate(func(pool []jx.M) []jx.M {
			out := []jx.M{}
			for _, i := range pool {
				if posta.TopicNorm(jx.Str(i, "topic")) != posta.TopicNorm(previousKey) {
					out = append(out, i)
				}
			}
			return out
		})
		c.A.MediaReleaseRemoved(c.R.Context(), before, after)
	}
	if (changed || stopped) && u.ContentPoolNeedsFill(u.SearchTopicKey()) {
		parser = u.StartParser(true)
	}
	return ok(jx.M{"topic": topic, "parser": parser, "typed": typed, "matched": res.Matched, "created": res.Created, "ai": res.AI, "discovery": lacks,
		"topic_info": u.TopicPublic(res.Topic, nil)}), nil
}

func saveSecondTopic(c *Ctx) (any, error) {
	u := c.U
	typed := posta.NormalizeTopic(c.Str("topic2"))
	if d := u.TopicSaveDenied(typed); d != "" {
		return nil, fail(409, d)
	}
	topic2 := ""
	if typed != "" {
		topic2 = u.TopicResolve(typed, false).Topic.Title
	}
	if d := u.TopicSaveDenied(topic2); d != "" {
		return nil, fail(409, d)
	}
	if topic2 != "" && u.TopicSame(topic2, u.CurrentTopic()) {
		return nil, fail(400, "Вторая тема совпадает с первой.")
	}
	if posta.TopicNorm(topic2) == posta.TopicNorm(u.CurrentTopic2()) {
		return ok(jx.M{"topic2": topic2, "parser": "unchanged"}), nil
	}
	if err := u.SaveSettings(jx.M{"topic2": topic2}); err != nil {
		return nil, err
	}
	u.PoolUpdate(func([]jx.M) []jx.M { return []jx.M{} })
	return ok(jx.M{"topic2": topic2, "parser": u.StartParser(true)}), nil
}

func saveQuery(c *Ctx) (any, error) {
	u := c.U
	q := posta.NormalizeQuery(c.Str("query"))
	if d := u.TopicSaveDenied(q); d != "" {
		return nil, fail(409, d)
	}
	if posta.TopicNorm(q) == posta.TopicNorm(u.CurrentQuery()) {
		return ok(jx.M{"query": q, "parser": "unchanged"}), nil
	}
	if err := u.SaveSettings(jx.M{"query": q}); err != nil {
		return nil, err
	}
	// новый запрос: текущий проход по старому останавливается (до 5 с) и сразу стартует новый, без паузы между проходами
	return ok(jx.M{"query": q, "parser": u.RestartParser()}), nil
}

func saveFilters(c *Ctx) (any, error) {
	u := c.U
	before := u.ContentFilterValues()
	changes := jx.M{}
	for k := range before {
		if v, has := c.In[k]; has {
			changes[k] = jx.Truthy(v)
		}
	}
	if err := u.SaveSettings(changes); err != nil {
		return nil, err
	}
	after := u.ContentFilterValues()
	same := true
	for k := range after {
		if k != "auto_translate" && jx.Bool(after, k) != jx.Bool(before, k) {
			same = false
		}
	}
	if same {
		return ok(jx.M{"filters": after, "parser": "unchanged"}), nil
	}
	u.PoolForCurrentTopic()
	return ok(jx.M{"filters": after, "parser": u.StartParser(true)}), nil
}

func adultAllow(c *Ctx) (any, error) {
	u := c.U
	allowed := jx.Bool(c.In, "allowed")
	if allowed && !c.A.TBool("adult_available") {
		return nil, fail(403, "Контент 18+ на этом сайте выключен.")
	}
	if err := u.SetAdultAllowed(allowed); err != nil {
		return nil, err
	}
	reset := !allowed && u.TopicIsRestricted(u.CurrentTopic())
	if reset {
		c.A.StopParser(u.ID)
		u.SaveSettings(jx.M{"topic": c.A.TStr("default_topic")})
	}
	return ok(jx.M{"adult_allowed": allowed, "topic_reset": reset}), nil
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
	var before []jx.M
	var saved []struct {
		r jx.M
		c string
	}
	after, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
		before = append([]jx.M(nil), pool...)
		return c.A.ApplyVote(pool, image, v, reason, func(r jx.M, coll string) {
			saved = append(saved, struct {
				r jx.M
				c string
			}{r, coll})
		})
	})
	if err != nil {
		return nil, err
	}
	for _, s := range saved {
		u.HistoryAppend(s.c, s.r)
		c.A.CH.Vote(posta.VoteEvent(u.ID, u.CurrentTopic(), v, reason, s.r))
	}
	if it := posta.PoolFindItem(before, image); it != nil && v != "postpone" {
		u.MarkSourceRest(u.SearchTopicKey(), it)
	}
	if !contains(config.ProposalCollections, coll) {
		c.A.MediaReleaseRemoved(c.R.Context(), before, after)
	}
	return ok(nil), nil
}

// ---- Базы данных ----

type dbDef struct {
	Label       string
	Collections [][2]string
	ByTopic     bool
	SQL         bool
}

var dbHiddenKV = []string{"telegram_app_keys", "vk_tokens", "telegram_login_bot", "admin:llm"}

var sqlTables = [][2]string{{"analysis_cache", "Кэш анализов ИИ"}, {"kv", "Конфиг (ключ-значение)"}, {"outbox", "Очередь постов"}, {"published", "Опубликовано"},
	{"screened_cache", "Кэш оценок контента"}, {"topics", "Темы поиска"}, {"users", "Ваш профиль"}, {"ledger", "Баланс: операции"},
	{"subscriptions", "Тариф"}, {"post_usage", "Учёт постов"}, {"channels", "Каналы и соцсети"}, {"mirrors", "Зеркала"}, {"source_verdicts", "Итоги по источникам"}}

func dbDefinitions() map[string]dbDef {
	defs := map[string]dbDef{
		"content_pool":    {Label: "Пул контента", Collections: [][2]string{{"items", "Записи"}}, ByTopic: true},
		"content_history": {Label: "История оценок", Collections: [][2]string{{"liked", "Лайки"}, {"disliked", "Дизлайки"}, {"undecided", "Без решения"}, {"complaints", "Жалобы"}}, ByTopic: true},
		"sources_db": {Label: "Каталог источников", Collections: [][2]string{{"2ch_boards", "Доски 2ch"}, {"vk_publics", "Паблики ВКонтакте"}, {"telegram_channels", "Каналы Telegram"},
			{"rss_feeds", "Ленты RSS"}, {"ok_groups", "Группы Одноклассников"}, {"tiktok_accounts", "Аккаунты TikTok"}, {"source_votes", "Оценки источников"}}},
	}
	for _, t := range sqlTables {
		defs[t[0]] = dbDef{Label: t[1], Collections: [][2]string{{"all", "Все записи"}}, SQL: true}
	}
	return defs
}

var dbOrder = []string{"content_pool", "content_history", "sources_db"}

func databaseOptions(c *Ctx) (any, error) {
	defs := dbDefinitions()
	order := append([]string{}, dbOrder...)
	for _, t := range sqlTables {
		order = append(order, t[0])
	}
	list := []jx.M{}
	for _, id := range order {
		d := defs[id]
		colls := []jx.M{}
		for _, cl := range d.Collections {
			colls = append(colls, jx.M{"id": cl[0], "label": cl[1]})
		}
		list = append(list, jx.M{"id": id, "label": d.Label, "is_sql_table": d.SQL, "by_topic": d.ByTopic, "collections": colls})
	}
	return jx.M{"databases": list}, nil
}

func collLabel(d dbDef, coll string) (string, bool) {
	for _, cl := range d.Collections {
		if cl[0] == coll {
			return cl[1], true
		}
	}
	return "", false
}

// databaseOpen — записи коллекции: пул и история — только темы, выбранной во вкладках; SQL-таблицы — только свои строки.
func databaseOpen(c *Ctx, database, coll string) (dbDef, []jx.M, error) {
	d, known := dbDefinitions()[database]
	if !known {
		return d, nil, fail(400, "Неизвестная база данных.")
	}
	if _, ok := collLabel(d, coll); !ok {
		return d, nil, fail(400, "Неизвестная коллекция.")
	}
	u := c.U
	if d.SQL {
		rows, err := u.SQLTableRows(database)
		if err != nil {
			return d, nil, err
		}
		var out []jx.M
		for _, r := range rows {
			if database == "kv" {
				k := jx.Str(r, "key")
				if contains(dbHiddenKV, k) || strings.HasPrefix(k, "admin:") || (strings.Contains(k, ":") && !strings.HasSuffix(k, ":"+u.ID)) {
					continue
				}
			}
			delete(r, "connections")
			delete(r, "password_hash")
			delete(r, "secrets_enc")
			delete(r, "token_enc")
			if data := jx.Map(r, "data"); data != nil {
				sys := jx.M{}
				for _, k := range []string{"id", "user_id", "position", "image_key", "collection", "proposal_hidden"} {
					if v, has := r[k]; has {
						sys[k] = v
					}
				}
				r = jx.Merge(data, sys)
			}
			out = append(out, r)
		}
		return d, out, nil
	}
	var all []jx.M
	switch database {
	case "content_pool":
		all = u.Pool()
	case "content_history":
		all = u.History()[coll]
	default:
		all = catalogCollection(c.A.Catalog(), coll)
	}
	if !d.ByTopic {
		return d, all, nil
	}
	var vis []jx.M
	for _, i := range u.PoolTopicPositions(all) {
		vis = append(vis, all[i])
	}
	return d, vis, nil
}

func catalogCollection(d *posta.SourceDB, coll string) []jx.M {
	switch coll {
	case "2ch_boards":
		out := []jx.M{}
		for _, b := range d.Boards {
			out = append(out, jx.M{"value": b})
		}
		return out
	case "source_votes":
		return d.Votes
	}
	if l := d.List(coll); l != nil {
		return *l
	}
	return nil
}

func databaseRead(c *Ctx) (any, error) {
	db, coll := c.Q("database"), c.Q("collection")
	d, items, err := databaseOpen(c, db, coll)
	if err != nil {
		return nil, err
	}
	if items == nil {
		items = []jx.M{}
	}
	label, _ := collLabel(d, coll)
	topic := ""
	if d.ByTopic {
		topic = c.U.SearchTopicKey()
	}
	cols := []string{}
	seen := map[string]bool{}
	for _, i := range items {
		for _, k := range jx.Keys(i) {
			if !seen[k] {
				seen[k] = true
				cols = append(cols, k)
			}
		}
	}
	if len(cols) == 0 {
		cols = []string{"value"}
	}
	missing := []int{}
	for n, r := range items {
		for _, f := range []string{"image_local", "video_local"} {
			if p := c.A.MediaPath(jx.Str(r, f)); p != "" && !fileExists(p) {
				missing = append(missing, n)
				break
			}
		}
	}
	return jx.M{"database_label": d.Label, "collection_label": label, "topic": topic, "columns": cols, "items": items, "media_missing": missing}, nil
}

func validateRecord(database, coll string, r any) error {
	m, isObj := r.(map[string]any)
	var e string
	switch {
	case database == "content_pool":
		if !isObj || strings.TrimSpace(jx.Str(m, "image")) == "" {
			e = "Записи нужны поля-объект и непустой image."
		} else if v, has := m["rating"]; has && (jx.ToFloat(v) < 0 || jx.ToFloat(v) > 100) {
			e = "rating должен быть числом от 0 до 100."
		}
	case contains(config.HistoryCollections, coll):
		if _, isStr := m["text"].(string); !isObj || !isStr {
			e = "Для записи истории нужны объект, строка text и число rating_assigned (или null)."
		}
	case coll == "2ch_boards":
		if !isObj || !posta.IsValidDvachBoard(jx.Str(m, "value")) {
			e = "Доска 2ch должна содержать только буквы, цифры, _ или - (до 30 символов)."
		}
	case coll == "source_votes":
		if !isObj || strings.TrimSpace(jx.Str(m, "source_id")) == "" || !contains([]string{"approve", "reject"}, jx.Str(m, "vote")) {
			e = "Оценке источника нужны source_id и vote: approve или reject."
		}
	default:
		if !isObj || strings.TrimSpace(jx.Str(m, "id")) == "" || strings.TrimSpace(jx.Str(m, "name")) == "" {
			e = "Источнику нужны непустые поля id и name."
		}
	}
	if e != "" {
		return fail(400, e)
	}
	return nil
}

// databaseEdit — правка коллекции: номер в списке — позиция среди записей выбранной темы.
func databaseEdit(c *Ctx, all []jx.M, database, coll string) ([]jx.M, error) {
	action := c.Action
	var visible []int
	if database == "content_pool" || database == "content_history" {
		visible = c.U.PoolTopicPositions(all)
	} else {
		for i := range all {
			visible = append(visible, i)
		}
	}
	items := append([]jx.M(nil), all...)
	rec, hasRec := c.In["record"]
	if action != "database_delete" && !hasRec {
		return nil, fail(400, "Не переданы данные записи.")
	}
	if action == "database_add" {
		if err := validateRecord(database, coll, rec); err != nil {
			return nil, err
		}
		return append(items, jx.ToMap(rec)), nil
	}
	idx, isNum := c.In["index"].(float64)
	if !isNum || idx < 0 || idx != float64(int(idx)) {
		return nil, fail(400, "Некорректный номер записи.")
	}
	i := int(idx)
	if i >= len(visible) {
		return nil, fail(409, "Запись уже удалена или не существует. Обновите список.")
	}
	pos := visible[i]
	if exp, has := c.In["expected"]; has && !jx.Equal(exp, items[pos]) {
		return nil, fail(409, "Запись изменилась с момента загрузки списка. Обновите список и повторите.")
	}
	if action == "database_delete" {
		return append(items[:pos], items[pos+1:]...), nil
	}
	if err := validateRecord(database, coll, rec); err != nil {
		return nil, err
	}
	items[pos] = jx.ToMap(rec)
	return items, nil
}

func databaseChange(c *Ctx) (any, error) {
	database, coll := c.Str("database"), c.Str("collection")
	d, _, err := databaseOpen(c, database, coll)
	if err != nil {
		return nil, err
	}
	if d.SQL {
		return nil, fail(400, "Редактирование системных таблиц недопустимо из интерфейса базы данных.")
	}
	u := c.U
	var editErr error
	switch database {
	case "content_pool":
		before := u.Pool()
		after, err := u.PoolUpdate(func(pool []jx.M) []jx.M {
			out, e := databaseEdit(c, pool, database, coll)
			if e != nil {
				editErr = e
				return nil
			}
			return out
		})
		if editErr != nil {
			return nil, editErr
		}
		if err != nil {
			return nil, err
		}
		c.A.MediaReleaseRemoved(c.R.Context(), before, after)
		return ok(jx.M{"count": len(after)}), nil
	case "content_history":
		h, err := u.HistoryUpdate(func(h posta.History) posta.History {
			out, e := databaseEdit(c, h[coll], database, coll)
			if e != nil {
				editErr = e
				return nil
			}
			h[coll] = out
			return h
		})
		if editErr != nil {
			return nil, editErr
		}
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"count": len(h[coll])}), nil
	}
	count := 0
	_, err = c.A.UpdateCatalog(c.R.Context(), func(d *posta.SourceDB) bool {
		out, e := databaseEdit(c, catalogCollection(d, coll), database, coll)
		if e != nil {
			editErr = e
			return false
		}
		count = len(out)
		switch coll {
		case "2ch_boards":
			d.Boards = nil
			for _, b := range out {
				d.Boards = append(d.Boards, jx.Str(b, "value"))
			}
		case "source_votes":
			d.Votes = out
		default:
			*d.List(coll) = out
		}
		return true
	})
	if editErr != nil {
		return nil, editErr
	}
	return ok(jx.M{"count": count}), err
}

func topicInfo(c *Ctx) (any, error) {
	u := c.U
	topic := u.CurrentTopic()
	e := u.TopicLookup(topic)
	children := []jx.M{}
	total := 0
	var parent any
	if e != nil {
		kids := u.TopicChildren(e)
		total = len(kids)
		for _, k := range kids[:min(12, len(kids))] {
			children = append(children, jx.M{"title": k.Title, "key": k.Key})
		}
		if p := u.TopicParent(e); p != nil {
			parent = jx.M{"title": p.Title}
		}
	}
	return jx.M{"topic": topic, "format": u.TopicFormatOf(topic), "overridden": u.TopicFormatOverridden(topic), "children": children, "children_total": total, "parent": parent}, nil
}

func topicFormatSave(c *Ctx) (any, error) {
	u := c.U
	err := u.TopicFormatSave(u.CurrentTopic(), jx.M{"media": c.In["media"], "text": c.In["text"], "judge": c.In["judge"]}, jx.Bool(c.In, "reset"))
	if err != nil {
		return nil, err
	}
	return ok(jx.M{"format": u.TopicFormatOf(u.CurrentTopic())}), nil
}

func articleRead(c *Ctx) (any, error) {
	link := strings.TrimSpace(c.Q("url"))
	if !c.U.ArticleURLIsOwn(link) {
		return nil, fail(403, "Статья не из ваших записей.")
	}
	ctx, cancel := context.WithTimeout(c.R.Context(), time.Minute)
	defer cancel()
	_ = ctx
	a := c.A.ArticleBlocksFor(link)
	if a == nil {
		return nil, fail(404, "Статья не открылась или на странице нет текста.")
	}
	return jx.M{"url": link, "article": a}, nil
}

var _ = fmt.Sprint

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// readRequestImage — файл image_file из формы или картинка по ссылке image_url.
func readRequestImage(c *Ctx) ([]byte, error) {
	if strings.HasPrefix(c.R.Header.Get("Content-Type"), "multipart/") {
		if err := c.R.ParseMultipartForm(config.MaxImageBytes + 1<<20); err != nil {
			return nil, fail(400, "Изображение должно быть не больше 10 МБ.")
		}
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
