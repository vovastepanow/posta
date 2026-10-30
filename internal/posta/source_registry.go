package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// Реестр проверенных источников (таблица source_registry): каждый источник, который уже находили, предлагали или отклоняли,
// — одна строка. Статус — итог по источнику; решение по отдельной теме — в topics. Подбор проверяет реестр до любого запроса к сети.

const (
	RegStatusCatalog    = "catalog"
	RegStatusSuggested  = "suggested"
	RegStatusOwnerYes   = "owner_yes"
	RegStatusOwnerNo    = "owner_no"
	RegStatusOwnerMaybe = "owner_maybe"
	RegStatusRejected   = "rejected"
	RegStatusDead       = "dead"
	RegStatusFailed     = "failed"
	RegStatusUkraine    = "ukraine"

	// RegistryFailMax — сколько раз источник может не загрузиться, прежде чем стать мёртвым.
	RegistryFailMax = 3
	// RegistryRetryAfter — повтор не раньше чем через сутки.
	RegistryRetryAfter = int64(config.DaySeconds)
	// registryMovedReason — причина для записей, перенесённых из старой памяти checked_at.
	registryMovedReason = "перенесено, причина неизвестна"
)

// registryTopic — итог проверки источника по одной теме.
type registryTopic struct {
	Score  int    `json:"score"`
	Reason string `json:"reason,omitempty"`
	At     int64  `json:"at"`
}

// registryRow — строка реестра.
type registryRow struct {
	SourceID, Platform, Name, URL, Status, Reason string
	FirstSeen, CheckedAt                          int64
	CheckCount, FailCount                         int
	Topics                                        map[string]registryTopic
	Metrics, Origin                               jx.M
	Sample                                        []jx.M
	HasSample                                     bool // сэмпл есть в реестре (в массовой выборке сам сэмпл не читается)
	SampleAt                                      int64
}

type registryVerdict int

const (
	regNew    registryVerdict = iota // проверить по сети
	regSkip                          // пропустить
	regSample                        // отклонён по другой теме: оценить по сохранённому сэмплу без сети
)

// registryDecide — решение по строке реестра для темы. nil — источника в реестре нет.
func registryDecide(r *registryRow, topic string, at, recheckDays, sampleDays int64) (registryVerdict, string) {
	if r == nil {
		return regNew, ""
	}
	switch r.Status {
	case RegStatusCatalog:
		return regSkip, "уже в каталоге"
	case RegStatusSuggested:
		return regSkip, "уже предлагался"
	case RegStatusOwnerYes:
		return regSkip, "владелец одобрил"
	case RegStatusOwnerNo:
		return regSkip, "владелец отклонил"
	case RegStatusOwnerMaybe:
		return regSkip, "владелец отметил «не знаю»"
	case RegStatusDead:
		return regSkip, "мёртвый, пустой или закрытый источник"
	case RegStatusUkraine:
		return regSkip, "украинский источник"
	case RegStatusFailed:
		if r.FailCount >= RegistryFailMax {
			return regSkip, "не загрузился трижды"
		}
		if at-r.CheckedAt < RegistryRetryAfter {
			return regSkip, "не загрузился — повтор через сутки"
		}
		return regNew, ""
	case RegStatusRejected:
		if t, ok := r.Topics[TopicNorm(topic)]; ok {
			if recheckDays > 0 && at-t.At >= recheckDays*config.DaySeconds {
				return regNew, ""
			}
			return regSkip, "уже проверен по этой теме и не подошёл"
		}
		if r.HasSample && at-r.SampleAt < sampleDays*config.DaySeconds {
			return regSample, ""
		}
		return regSkip, "отклонён по другой теме, сохранённый сэмпл устарел"
	}
	return regNew, ""
}

// registryGet — строка реестра по ключу источника; nil — нет.
func (u *U) registryGet(sid string) *registryRow {
	rows, err := u.DB.Pool.Query(u.Ctx, registrySelect+` WHERE source_id = $1`, sid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	if !rows.Next() {
		return nil
	}
	r, err := scanRegistryRow(rows)
	if err != nil {
		return nil
	}
	return r
}

const registrySelect = `SELECT source_id, platform, name, url, status, reason, first_seen_at, checked_at, check_count, fail_count,
	topics, metrics, origin, sample, sample_at FROM source_registry`

func scanRegistryRow(rows interface{ Scan(...any) error }) (*registryRow, error) {
	var r registryRow
	var topics, metrics, origin, sample []byte
	if err := rows.Scan(&r.SourceID, &r.Platform, &r.Name, &r.URL, &r.Status, &r.Reason, &r.FirstSeen, &r.CheckedAt, &r.CheckCount, &r.FailCount,
		&topics, &metrics, &origin, &sample, &r.SampleAt); err != nil {
		return nil, err
	}
	json.Unmarshal(topics, &r.Topics)
	r.Metrics, r.Origin = jx.Unmarshal(metrics), jx.Unmarshal(origin)
	if r.Metrics == nil {
		r.Metrics = jx.M{}
	}
	if r.Origin == nil {
		r.Origin = jx.M{}
	}
	if sample != nil {
		json.Unmarshal(sample, &r.Sample)
		r.HasSample = true
	}
	return &r, nil
}

// RegistryVerdict — решение реестра по кандидату темы; до любого сетевого запроса.
func (u *U) RegistryVerdict(sid, topic string) (registryVerdict, string, *registryRow) {
	r := u.registryGet(sid)
	v, reason := registryDecide(r, topic, now(), int64(u.TInt("registry_recheck_days")), int64(u.TInt("registry_sample_days")))
	if v == regSkip && r != nil && r.Status == RegStatusFailed {
		u.registryUpsert(registryWrite{SourceID: sid, Status: RegStatusDead, Reason: "не загрузился трижды"})
	}
	return v, reason, r
}

// registryKnown — источники, которые по теме пропускаются без запросов к сети (сэмпл-проверка в этот набор не входит).
func (u *U) registryKnown(topic string) map[string]bool {
	out := map[string]bool{}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT source_id, status, check_count, fail_count, checked_at, topics, sample_at, sample IS NOT NULL FROM source_registry`)
	if err != nil {
		u.Log("Реестр источников: не прочитан: %v", err)
		return out
	}
	defer rows.Close()
	recheck, sample, at := int64(u.TInt("registry_recheck_days")), int64(u.TInt("registry_sample_days")), now()
	for rows.Next() {
		var r registryRow
		var topics []byte
		if rows.Scan(&r.SourceID, &r.Status, &r.CheckCount, &r.FailCount, &r.CheckedAt, &topics, &r.SampleAt, &r.HasSample) != nil {
			continue
		}
		json.Unmarshal(topics, &r.Topics)
		if v, _ := registryDecide(&r, topic, at, recheck, sample); v == regSkip {
			out[r.SourceID] = true
		}
	}
	return out
}

// registrySampleCheck — отклонён по другой теме: оценка по сохранённым подписям, без сетевых запросов (один вызов модели).
func (u *U) registrySampleCheck(c DiscoveryCandidate, topic string, r *registryRow, stats map[string]int) {
	// украинский источник в сэмпле не оцениваем: статус ukraine — пропуск навсегда
	uaPosts := 0
	for _, p := range r.Sample {
		if u.UAItemBlocked(p) != "" {
			uaPosts++
		}
	}
	if u.UASourceBlocked(r.SourceID, jx.M{"title": r.Name}) != "" || uaPosts >= 2 {
		u.registryUpsert(registryWrite{SourceID: r.SourceID, Platform: r.Platform, Status: RegStatusUkraine, Reason: "украинский источник"})
		u.discoveryCheapNote(c, "реестр: украинский источник")
		return
	}
	var caps []string
	for _, p := range r.Sample {
		if t := strings.TrimSpace(jx.Str(p, "text")); t != "" && t != EmptyPostText {
			caps = append(caps, t)
		}
	}
	if len(caps) == 0 {
		u.discoveryCheapNote(c, "реестр: сэмпл без подписей")
		return
	}
	rev := u.sourceTextReview(r.Name, "", topic, caps)
	if rev == nil {
		return
	}
	s := rev.Score
	ev := &sourceEvaluation{Score: s, Text: &s, TextOnly: s < config.DiscoveryTextRejectScore, Reason: rev.Reason}
	info := jx.M{"id": c.ID, "title": r.Name, "description": "", "posts": []any{}}
	result := u.storeDiscoveredSource(c, info, ev, topic, nil, 0)
	stats[result]++
	u.Log("-> %s по сохранённому сэмплу: по теме «%s» %d%% — %s.", DiscoveryLabel(c), topic, s,
		map[string]string{"added": "добавлен в каталог", "suggested": "в предложения", "rejected": "не подходит", "failed": "не сохранено"}[result])
}

const registrySampleSize = 8

// registrySample — последние посты для сэмпла реестра: до 8 коротких записей с подписью или картинкой; без постов — nil (старый сэмпл сохраняется).
func registrySample(posts []jx.M) []jx.M {
	var out []jx.M
	for _, p := range posts {
		if len(out) == registrySampleSize {
			break
		}
		text := trimRunes(strings.TrimSpace(jx.Str(p, "text")), 300)
		img := jx.Str(p, "image")
		if text == "" && img == "" {
			continue
		}
		out = append(out, jx.M{"text": text, "image": img, "url": jx.Str(p, "url"), "posted_at": jx.Int(p, "posted_at")})
	}
	return out
}

// registryURL — адрес источника для записи реестра.
func registryURL(platform, id string) string {
	switch platform {
	case PlatformTelegram:
		return "https://t.me/" + id
	case PlatformVK:
		return "https://vk.com/" + id
	case PlatformTikTok:
		return "https://www.tiktok.com/@" + id
	case PlatformRSS:
		if IsValidRSSURL(id) {
			return id
		}
	}
	return ""
}

// RegistrySkip — пропустить кандидата до сетевого запроса? Нужен для каждого пути, где появляется кандидат.
func (u *U) RegistrySkip(sourceID, topic string) (skip bool, reason string) {
	v, reason, _ := u.RegistryVerdict(sourceID, topic)
	return v == regSkip, reason
}

// registryWrite — запись в реестр: итог проверки или решение. Статусы владельца не перезаписывает проверка сети.
type registryWrite struct {
	SourceID, Platform, Name, URL string
	Status, Reason                string
	Owner                         bool   // решение владельца: перекрывает любой статус
	Checked                       bool   // проверка по сети: считается в check_count
	Failed                        bool   // не загрузилось: fail_count + 1
	Topic                         string // ключ темы (TopicNorm); пусто — без темы
	Result                        *registryTopic
	Metrics, Origin               jx.M
	Sample                        []jx.M
}

// registryUpsert — запись строки реестра. Решение владельца (owner_*) не перекрывают ни проверка сети, ни заморозка;
// проверка сети не перекрывает каталог и предложение.
func (u *U) registryUpsert(w registryWrite) error {
	if w.SourceID == "" {
		return nil
	}
	at := now()
	topics := jx.M{}
	if w.Topic != "" && w.Result != nil {
		topics[w.Topic] = *w.Result
	}
	checkCount, failCount := 0, 0
	if w.Checked {
		checkCount = 1
	}
	if w.Failed {
		failCount = 1
	}
	var sample any
	if w.Sample != nil {
		sample = string(jx.Marshal(w.Sample))
	}
	// $1 источник · $2 площадка · $3 название · $4 адрес · $5 статус · $6 причина · $7 время · $8 проверок · $9 неудач
	// $10 темы · $11 метрики · $12 происхождение · $13 сэмпл · $14 решение владельца
	_, err := u.DB.Pool.Exec(u.Ctx, `INSERT INTO source_registry (source_id, platform, name, url, status, reason, first_seen_at, checked_at, check_count,
			fail_count, topics, metrics, origin, sample, sample_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7::bigint, CASE WHEN $8::int > 0 THEN $7::bigint ELSE 0 END, $8::int, $9::int, $10::jsonb, $11::jsonb,
			$12::jsonb, $13::jsonb, CASE WHEN $13::jsonb IS NULL THEN 0 ELSE $7::bigint END)
		ON CONFLICT (source_id) DO UPDATE SET
			platform = CASE WHEN EXCLUDED.platform <> '' THEN EXCLUDED.platform ELSE source_registry.platform END,
			name = CASE WHEN EXCLUDED.name <> '' THEN EXCLUDED.name ELSE source_registry.name END,
			url = CASE WHEN EXCLUDED.url <> '' THEN EXCLUDED.url ELSE source_registry.url END,
			status = CASE WHEN NOT $14::bool AND source_registry.status LIKE 'owner_%' THEN source_registry.status
				WHEN EXCLUDED.check_count > 0 AND source_registry.status IN ('catalog', 'suggested') THEN source_registry.status
				ELSE EXCLUDED.status END,
			reason = CASE WHEN NOT $14::bool AND source_registry.status LIKE 'owner_%' THEN source_registry.reason
				WHEN EXCLUDED.check_count > 0 AND source_registry.status IN ('catalog', 'suggested') THEN source_registry.reason
				ELSE EXCLUDED.reason END,
			checked_at = CASE WHEN EXCLUDED.check_count > 0 THEN EXCLUDED.checked_at ELSE source_registry.checked_at END,
			check_count = source_registry.check_count + EXCLUDED.check_count,
			fail_count = CASE WHEN EXCLUDED.fail_count > 0 THEN source_registry.fail_count + 1
				WHEN EXCLUDED.check_count > 0 THEN 0 ELSE source_registry.fail_count END,
			topics = source_registry.topics || EXCLUDED.topics,
			metrics = CASE WHEN EXCLUDED.metrics = '{}'::jsonb THEN source_registry.metrics ELSE EXCLUDED.metrics END,
			origin = CASE WHEN source_registry.origin = '{}'::jsonb THEN EXCLUDED.origin ELSE source_registry.origin END,
			sample = COALESCE(EXCLUDED.sample, source_registry.sample),
			sample_at = CASE WHEN EXCLUDED.sample IS NOT NULL THEN EXCLUDED.sample_at ELSE source_registry.sample_at END`,
		w.SourceID, w.Platform, w.Name, w.URL, w.Status, w.Reason, at, checkCount, failCount,
		string(jx.Marshal(topics)), string(jx.Marshal(orEmptyM(w.Metrics))), string(jx.Marshal(orEmptyM(w.Origin))), sample, w.Owner)
	return err
}

func orEmptyM(m jx.M) jx.M {
	if m == nil {
		return jx.M{}
	}
	return m
}

// registrySetOwner — решение владельца по источнику (голос, ручное добавление, удаление).
func (u *U) registrySetOwner(sid, platform, name, status, topic string) {
	if err := u.registryUpsert(registryWrite{SourceID: sid, Platform: platform, Name: name, Status: status, Reason: "решение владельца", Owner: true, Topic: TopicNorm(topic)}); err != nil {
		u.Log("Реестр источников: не сохранено решение по %s: %v", sid, err)
	}
}

// registryRowsFromCatalog — перенос старых данных: каталог, голоса, предложения и память checked_at → строки реестра.
// Приоритет статуса: решение владельца > каталог > предложение > отклонение; оценки по темам копятся в topics.
type registryCounts struct {
	Catalog, Votes, Suggestions, Checked, Unkeyed int
	ByStatus                                      map[string]int
	Rows                                          int
}

func registryFromCatalog(d *SourceDB, at int64) (map[string]*registryRow, registryCounts) {
	rows := map[string]*registryRow{}
	cnt := registryCounts{ByStatus: map[string]int{}}
	get := func(sid, platform, name string) *registryRow {
		r := rows[sid]
		if r == nil {
			r = &registryRow{SourceID: sid, Platform: platform, Name: name, Topics: map[string]registryTopic{}, Metrics: jx.M{}, Origin: jx.M{}, FirstSeen: at}
			rows[sid] = r
		}
		if r.Name == "" {
			r.Name = name
		}
		if r.Platform == "" {
			r.Platform = platform
		}
		return r
	}
	// статус: сильнее — решение владельца, каталог, предложение, отказ
	rank := map[string]int{RegStatusRejected: 1, RegStatusSuggested: 2, RegStatusCatalog: 3, RegStatusOwnerNo: 4, RegStatusOwnerMaybe: 4, RegStatusOwnerYes: 4}
	setStatus := func(r *registryRow, status, reason string) {
		if rank[status] >= rank[r.Status] {
			r.Status, r.Reason = status, reason
		}
	}
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			id := jx.Str(e, "id")
			if id == "" {
				continue
			}
			sid := SourceIDFor(c.Platform, id)
			r := get(sid, c.Platform, jx.Str(e, "name"))
			setStatus(r, RegStatusCatalog, "в каталоге")
			cnt.Catalog++
		}
	}
	voteStatus := map[string]string{"approve": RegStatusOwnerYes, "reject": RegStatusOwnerNo, "maybe": RegStatusOwnerMaybe}
	for _, v := range d.Votes {
		sid := jx.Str(v, "source_id")
		status, ok := voteStatus[jx.Str(v, "vote")]
		if sid == "" || !ok {
			cnt.Unkeyed++
			continue
		}
		r := get(sid, jx.Str(v, "platform"), jx.Str(v, "name"))
		// последний голос по источнику — итог; порядок в списке — хронологический
		r.Status, r.Reason = status, "решение владельца"
		cnt.Votes++
	}
	for _, s := range d.Suggestions {
		sid := jx.Str(s, "source_id")
		if sid == "" {
			cnt.Unkeyed++
			continue
		}
		r := get(sid, jx.Str(s, "platform"), jx.Str(s, "name"))
		setStatus(r, RegStatusSuggested, "предложен автоподбором")
		topic := TopicNorm(jx.Str(s, "topic"))
		if topic == "" {
			topic = TopicNorm(config.DefaultTopic)
		}
		r.Topics[topic] = registryTopic{Score: int(jx.Int(s, "score")), Reason: jx.Str(s, "reason"), At: jx.ToInt(s["timestamp"])}
		cnt.Suggestions++
	}
	for key, when := range jx.Map(d.Discovery, "checked_at") {
		topic, sid := TopicNorm(config.DefaultTopic), key
		if i := strings.Index(key, "|"); i >= 0 {
			sid = key[i+1:]
			if key[:i] != "" {
				topic = TopicNorm(key[:i])
			}
		}
		if sid == "" {
			cnt.Unkeyed++
			continue
		}
		r := get(sid, platformOfSourceID(sid), "")
		setStatus(r, RegStatusRejected, "не подошёл — причина неизвестна")
		r.Topics[topic] = registryTopic{Reason: registryMovedReason, At: jx.ToInt(when)}
		cnt.Checked++
	}
	for _, r := range rows {
		cnt.ByStatus[r.Status]++
		r.CheckedAt = maxTopicTime(r.Topics)
		r.CheckCount = len(r.Topics)
	}
	cnt.Rows = len(rows)
	return rows, cnt
}

func maxTopicTime(topics map[string]registryTopic) int64 {
	var at int64
	for _, t := range topics {
		if t.At > at {
			at = t.At
		}
	}
	return at
}

// platformOfSourceID — площадка по префиксу ключа источника.
func platformOfSourceID(sid string) string {
	for prefix, platform := range map[string]string{"telegram:": PlatformTelegram, "vk:": PlatformVK, "rss:": PlatformRSS, "ok:": PlatformOK,
		"tiktok:": PlatformTikTok, "4chan:": Platform4chan, "dvach:": PlatformDvach} {
		if strings.HasPrefix(sid, prefix) {
			return platform
		}
	}
	return ""
}

// MigrateSourceRegistry — однократно при старте: старые данные каталога → реестр. Флаг в kv; повтор безопасен (ON CONFLICT DO NOTHING).
func (a *App) MigrateSourceRegistry(ctx context.Context) error {
	const flag = "source_registry_migrated"
	if KV(a, ctx, flag, false) {
		return nil
	}
	d := a.Catalog()
	rows, cnt := registryFromCatalog(d, now())
	ids := make([]string, 0, len(rows))
	for sid := range rows {
		ids = append(ids, sid)
	}
	sort.Strings(ids)
	tx, err := a.DB.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	for _, sid := range ids {
		r := rows[sid]
		topics := string(jx.Marshal(r.Topics))
		if _, err := tx.Exec(ctx, `INSERT INTO source_registry (source_id, platform, name, status, reason, first_seen_at, checked_at, check_count, topics)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb) ON CONFLICT (source_id) DO NOTHING`,
			sid, r.Platform, r.Name, r.Status, r.Reason, r.FirstSeen, r.CheckedAt, r.CheckCount, topics); err != nil {
			return err
		}
	}
	var stored int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM source_registry`).Scan(&stored); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	// память checked_at из блоба убирается: её заменила строка реестра
	if _, err := a.UpdateCatalog(ctx, func(d *SourceDB) bool {
		if _, ok := d.Discovery["checked_at"]; !ok {
			return false
		}
		delete(d.Discovery, "checked_at")
		return true
	}); err != nil {
		return err
	}
	a.KVSet(ctx, flag, true, 0)
	log.Printf("Реестр источников: перенесено строк %d (каталог %d, голоса %d, предложения %d, проверено %d, без ключа %d). По статусам: %s. В таблице теперь %d.",
		len(rows), cnt.Catalog, cnt.Votes, cnt.Suggestions, cnt.Checked, cnt.Unkeyed, registryStatusLine(cnt.ByStatus), stored)
	return nil
}

func registryStatusLine(byStatus map[string]int) string {
	var parts []string
	keys := make([]string, 0, len(byStatus))
	for k := range byStatus {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s %d", k, byStatus[k]))
	}
	return strings.Join(parts, ", ")
}

// registryView — запись реестра для вкладки «Проверенные» (без сэмпла: он нужен только оценке).
type registryView struct {
	SourceID   string                   `json:"source_id"`
	Platform   string                   `json:"platform"`
	Name       string                   `json:"name"`
	URL        string                   `json:"url"`
	Status     string                   `json:"status"`
	Reason     string                   `json:"reason"`
	FirstSeen  int64                    `json:"first_seen_at"`
	CheckedAt  int64                    `json:"checked_at"`
	CheckCount int                      `json:"check_count"`
	Topics     map[string]registryTopic `json:"topics"`
	Metrics    jx.M                     `json:"metrics"`
	Origin     jx.M                     `json:"origin"`
	HasSample  bool                     `json:"has_sample"`
}

const registryListLimit = 300

// RegistryList — проверенные источники: фильтры по статусу, площадке, теме и поиск по имени; счётчики по статусам.
func (u *U) RegistryList(status, platform, topic, q string) ([]registryView, jx.M, error) {
	where, args := []string{"TRUE"}, []any{}
	arg := func(v any) string {
		args = append(args, v)
		return fmt.Sprintf("$%d", len(args))
	}
	if status != "" {
		where = append(where, "status = "+arg(status))
	}
	if platform != "" {
		where = append(where, "platform = "+arg(platform))
	}
	if topic != "" {
		where = append(where, "topics ? "+arg(TopicNorm(topic)))
	}
	if q = strings.TrimSpace(q); q != "" {
		n := arg(q)
		where = append(where, "(name ILIKE '%' || "+n+" || '%' OR source_id ILIKE '%' || "+n+" || '%')")
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT source_id, platform, name, url, status, reason, first_seen_at, checked_at, check_count, fail_count,
		topics, metrics, origin, CASE WHEN sample IS NULL THEN NULL ELSE '[]'::jsonb END, sample_at
		FROM source_registry WHERE `+strings.Join(where, " AND ")+` ORDER BY checked_at DESC, source_id LIMIT `+fmt.Sprint(registryListLimit), args...)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	out := []registryView{}
	for rows.Next() {
		r, err := scanRegistryRow(rows)
		if err != nil {
			return nil, nil, err
		}
		out = append(out, registryView{SourceID: r.SourceID, Platform: r.Platform, Name: r.Name, URL: r.URL, Status: r.Status, Reason: r.Reason,
			FirstSeen: r.FirstSeen, CheckedAt: r.CheckedAt, CheckCount: r.CheckCount, Topics: r.Topics, Metrics: r.Metrics, Origin: r.Origin, HasSample: r.HasSample})
	}
	counts := jx.M{}
	crows, err := u.DB.Pool.Query(u.Ctx, `SELECT status, count(*) FROM source_registry GROUP BY status`)
	if err != nil {
		return nil, nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var st string
		var n int
		if crows.Scan(&st, &n) == nil {
			counts[st] = n
		}
	}
	return out, counts, nil
}

// RegistryReadd — «Предложить снова»: источник возвращается в предложения текущей темы; решение владельца по теме снимается.
func (u *U) RegistryReadd(sid string) error {
	r := u.registryGet(sid)
	if r == nil {
		return Bad("Источника нет в реестре.")
	}
	if r.Status == RegStatusCatalog || r.Status == RegStatusSuggested {
		return Bad("Источник уже в каталоге или в предложениях.")
	}
	topic := u.CurrentTopic()
	score := r.Topics[TopicNorm(topic)].Score
	// TopicSame берёт мьютекс каталога, а UpdateCatalog его уже держит: сравнения тем — до него
	sameTopic := map[string]bool{}
	for _, v := range u.Catalog().Votes {
		sameTopic[jx.Str(v, "topic")] = u.TopicSame(jx.Str(v, "topic"), topic)
	}
	_, err := u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		var votes []jx.M
		for _, v := range d.Votes {
			if !(jx.Str(v, "source_id") == sid && sameTopic[jx.Str(v, "topic")]) {
				votes = append(votes, v)
			}
		}
		d.Votes = orEmptyList(votes)
		d.Suggestions = append(d.Suggestions, jx.M{"source_id": sid, "platform": r.Platform, "id": registryID(sid), "name": SourceDisplayName(r.Platform, r.Name),
			"score": score, "topic": topic, "timestamp": now(), "origin": jx.M{"kind": "registry", "detail": "возвращён из проверенных"}, "refs": []any{},
			"reason": "Возвращён из проверенных: решение прежнее — " + r.Reason + "."})
		return true
	})
	if err != nil {
		return err
	}
	return u.registryUpsert(registryWrite{SourceID: sid, Platform: r.Platform, Name: r.Name, Status: RegStatusSuggested, Reason: "возвращён из проверенных", Owner: true})
}

// RegistryForget — «Забыть»: строка уходит из реестра, и источник может снова найтись.
func (u *U) RegistryForget(sid string) error {
	_, err := u.DB.Pool.Exec(u.Ctx, `DELETE FROM source_registry WHERE source_id = $1`, sid)
	return err
}

// registryID — идентификатор источника без префикса площадки («telegram:имя» → «имя»).
func registryID(sid string) string {
	if i := strings.Index(sid, ":"); i >= 0 {
		return sid[i+1:]
	}
	return sid
}
