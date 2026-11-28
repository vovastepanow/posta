package web

import (
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/posta"
)

func init() {
	get("database_options", databaseOptions)
	get("database_read", databaseRead)
	for _, a := range []string{"database_add", "database_update", "database_delete"} {
		post(a, databaseChange)
	}
	post("database_reset", func(c *Ctx) (any, error) {
		removed := c.U.ResetProjectData()
		return ok(jx.M{"removed_media": removed, "parser": c.U.StartParser(true)}), nil
	})
}

// ---- Базы данных ----

type dbDef struct {
	Label       string
	Collections [][2]string
	ByTopic     bool
	SQL         bool
	ReadOnly    bool
}

var dbHiddenKV = []string{"telegram_app_keys", "vk_tokens", "telegram_login_bot", "admin:llm"}

var sqlTables = [][2]string{{"analysis_cache", "Кэш анализов ИИ"}, {"kv", "Конфиг (ключ-значение)"}, {"outbox", "Очередь постов"}, {"published", "Опубликовано"},
	{"screened_cache", "Кэш оценок контента"}, {"topics", "Темы поиска"}, {"users", "Ваш профиль"}, {"ledger", "Баланс: операции"},
	{"subscriptions", "Тариф"}, {"post_usage", "Учёт постов"}, {"channels", "Каналы и соцсети"}, {"mirrors", "Зеркала"}, {"source_verdicts", "Итоги по источникам"}}

func dbDefinitions() map[string]dbDef {
	defs := map[string]dbDef{
		"topic_sources":   {Label: "Источники по темам", Collections: [][2]string{{"all", "Все площадки"}}, ReadOnly: true},
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

var dbOrder = []string{"topic_sources", "content_pool", "content_history", "sources_db"}

func ownerDatabase(database string) bool {
	switch database {
	case "analysis_cache", "screened_cache", "kv", "source_verdicts", "sources_db":
		return true
	}
	return false
}

func databaseOptions(c *Ctx) (any, error) {
	defs := dbDefinitions()
	order := append([]string{}, dbOrder...)
	for _, t := range sqlTables {
		order = append(order, t[0])
	}
	list := []jx.M{}
	for _, id := range order {
		if ownerDatabase(id) && ownerOnly(c) != nil {
			continue
		}
		d := defs[id]
		colls := []jx.M{}
		for _, cl := range d.Collections {
			colls = append(colls, jx.M{"id": cl[0], "label": cl[1]})
		}
		list = append(list, jx.M{"id": id, "label": d.Label, "is_sql_table": d.SQL, "read_only": d.SQL || d.ReadOnly, "by_topic": d.ByTopic, "collections": colls})
	}
	return jx.M{"databases": list, "topics": topicsPublicList(c.U)}, nil
}

func collLabel(d dbDef, coll string) (string, bool) {
	for _, cl := range d.Collections {
		if cl[0] == coll {
			return cl[1], true
		}
	}
	return "", false
}

// Validate access and collection names without loading any records.
func databaseDefinition(c *Ctx, database, coll string) (dbDef, error) {
	d, known := dbDefinitions()[database]
	if ownerDatabase(database) {
		if err := ownerOnly(c); err != nil {
			return d, err
		}
	}
	if !known {
		return d, fail(400, "Неизвестная база данных.")
	}
	if _, ok := collLabel(d, coll); !ok {
		return d, fail(400, "Неизвестная коллекция.")
	}
	return d, nil
}

// databaseOpen — записи коллекции: пул и история — только выбранной темы.
func databaseOpen(c *Ctx, database, coll string) (dbDef, []jx.M, error) {
	d, err := databaseDefinition(c, database, coll)
	if err != nil {
		return d, nil, err
	}
	u := c.U
	if database == "topic_sources" {
		topic := u.CurrentTopic()
		if id := c.Q("topic"); id != "" {
			e := u.TopicByID(id)
			if e == nil || (!u.AdultAllowed() && u.TopicIsRestricted(e.Title)) {
				return d, nil, fail(404, "Такой темы нет.")
			}
			topic = e.Title
		}
		return d, u.TopicSourcesFor(topic), nil
	}
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
	} else if db == "topic_sources" {
		topic = c.U.CurrentTopic()
		if e := c.U.TopicByID(c.Q("topic")); e != nil {
			topic = e.Title
		}
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
		if expected, supplied := c.In["expected_topic"]; supplied && jx.ToStr(expected) != c.U.SearchTopicKey() {
			return nil, fail(409, "Тема изменилась с момента загрузки списка. Обновите список и повторите.")
		}
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
	d, err := databaseDefinition(c, database, coll)
	if err != nil {
		return nil, err
	}
	if d.SQL || d.ReadOnly {
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
