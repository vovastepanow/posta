package posta

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
	"posta/internal/tgclient"
)

// Радар лидов (только владелец): личный аккаунт читает публичные группы без вступления, дешёвый отсев по словам,
// модель роли «радар» решает, ищет ли автор то, что делает Posta.

const (
	leadBatch          = 15
	radarMinMembers    = 50
	radarAutoInterval  = 600
	radarMaxChats      = 50
	radarMaxQueries    = 15
)

var (
	RadarLeadStatuses    = []string{"new", "interested", "replied", "spam", "ignored"}
	radarDefaultQueries  = []string{"автопостинг", "чат админов телеграм", "админы каналов", "smm чат", "продвижение телеграм канала",
		"контент для канала", "отложенный постинг", "владельцы каналов", "ведение телеграм канала", "реклама в телеграм каналах"}
	leadKeywords = []string{"контент", "пост", "автопост", "отложен", "расписан", "канал", "паблик", "группу вк", "админ", "smm", "смм", "наполн", "мем", "рерайт", "бот"}
	leadAskRe    = regexp.MustCompile(`(?i)[?]|ищу|посоветуй|подскажи|нужен|нужна|кто знает|как |где |чем |помогите|помощь`)
)

func radarDefaults() jx.M {
	return jx.M{"enabled": false, "chats": []string{}, "last_ids": jx.M{}, "last_run": 0, "last_seen": 0, "last_error": nil, "resolved": jx.M{},
		"queries": []string{}, "chat_info": jx.M{}}
}

func (a *App) RadarSettings(ctx context.Context, uid string) jx.M {
	return jx.Defaults(KV(a, ctx, "lead_radar:"+uid, jx.M{}), radarDefaults())
}

func (a *App) radarSaveRun(ctx context.Context, uid string, changes jx.M) {
	KVUpdate(a, ctx, "lead_radar:"+uid, 0, func(s jx.M, _ bool) *jx.M {
		s = jx.Defaults(orEmpty(s), radarDefaults())
		for k, v := range changes {
			s[k] = v
		}
		return &s
	})
}

var radarChatRe = regexp.MustCompile(`^@[A-Za-z0-9_]{4,32}$|^-100\d+$`)

func RadarValidateChats(refs []string) []string {
	out := []string{}
	for _, l := range refs {
		l = strings.TrimSpace(strings.NewReplacer("https://t.me/", "@", "t.me/", "@").Replace(strings.TrimSpace(l)))
		if radarChatRe.MatchString(l) && !contains(out, l) && len(out) < radarMaxChats {
			out = append(out, l)
		}
	}
	return out
}

// LeadPrefilter — дешёвый отсев: длина, слова про контент и признаки просьбы.
func LeadPrefilter(text string) bool {
	text = strings.TrimSpace(text)
	if n := runeLen(text); n < 15 || n > 1500 {
		return false
	}
	low := strings.ToLower(text)
	hit := false
	for _, k := range leadKeywords {
		if strings.Contains(low, k) {
			hit = true
			break
		}
	}
	return hit && leadAskRe.MatchString(text)
}

func leadPrompt(text string) string {
	return `Ты отбираешь потенциальных клиентов для сервиса Posta. Сервис сам находит свежий контент по теме (посты, мемы, видео из Telegram, VK, форумов) и публикует его в канал владельца по расписанию.

Лид (1) — автор САМ ищет такое решение или жалуется на эту боль: ищет, где брать контент для канала, чем автоматизировать постинг, как вести канал без затрат времени, просит посоветовать сервис/бота для отложенных постов или подбора контента, ищет админа/контент-менеджера для наполнения канала.
Не лид (0) — реклама своих услуг (SMM-специалист предлагает себя), продажа каналов, спам, флуд, вопросы не про контент и постинг, сообщения без запроса.

Примеры:
«Посоветуйте бота, чтобы посты сами выходили по расписанию» → 1
«Где брать мемы для паблика каждый день? Устал искать вручную» → 1
«Ищу человека, который будет наполнять мой телеграм-канал про авто» → 1
«Веду ваши соцсети под ключ, пишите в лс» → 0
«Продам канал 5к подписчиков» → 0
«Кто-нибудь знает, когда обновят тарифы?» → 0

Сообщение:
"""` + trimRunes(text, 1500) + `"""

Ответь JSON: {"lead": 0 или 1, "reason": "до 12 слов, почему"}`
}

// LeadClassify — модель роли «радар»: lead 0|1 и причина.
func (a *App) LeadClassify(ctx context.Context, uid, text string) (int, string, error) {
	schema := jx.M{"type": "object", "properties": jx.M{"lead": jx.M{"type": "integer", "enum": []int{0, 1}}, "reason": jx.M{"type": "string"}}, "required": []string{"lead", "reason"}}
	resp, err := a.LLM.Generate(ctx, &llm.Request{Role: llm.RoleRadar, Prompt: leadPrompt(text), Schema: schema, MaxTokens: 120, Temperature: 0, UserID: uid, Quiet: true,
		Timeout: 2 * time.Minute})
	if err != nil {
		return 0, "", fmt.Errorf("модель не ответила: %w", err)
	}
	out := llm.DecodeJSON(resp.Text)
	if out == nil || !jx.Has(out, "lead") {
		return 0, "", fmt.Errorf("модель вернула некорректный JSON")
	}
	return int(min(1, max(0, jx.Int(out, "lead")))), trimRunes(jx.Str(out, "reason"), 100), nil
}

// ---- Проход ----

type radarState struct {
	mu    sync.Mutex
	lines []string
}

var radarRuns sync.Map // uid → *radarState

func radarStateOf(uid string) *radarState {
	s, _ := radarRuns.LoadOrStore(uid, &radarState{})
	return s.(*radarState)
}

func (a *App) radarLogPath(uid string) string { return filepath.Join(a.UsersDir(), ".radar-"+uid+".log") }

func (a *App) RadarRunning(uid string) bool {
	s := radarStateOf(uid)
	if s.mu.TryLock() {
		s.mu.Unlock()
		return false
	}
	return true
}

func plural(n int, forms [3]string) string {
	n = max(n, -n) % 100
	d := n % 10
	switch {
	case n > 10 && n < 20:
		return forms[2]
	case d == 1:
		return forms[0]
	case d >= 2 && d <= 4:
		return forms[1]
	}
	return forms[2]
}

// RadarRun — проход: чтение чатов, отсев, очередь, классификация. say — строка журнала (может быть nil).
func (a *App) RadarRun(ctx context.Context, uid string, say func(string)) jx.M {
	st := radarStateOf(uid)
	if !st.mu.TryLock() {
		return jx.M{"skipped": "уже идёт"}
	}
	defer st.mu.Unlock()
	f, _ := os.Create(a.radarLogPath(uid))
	if f != nil {
		defer f.Close()
	}
	log := func(format string, args ...any) {
		line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
		if f != nil {
			f.WriteString(line + "\n")
		}
		if say != nil {
			say(line)
		}
	}
	u := a.User(ctx, uid, nil)
	s := a.RadarSettings(ctx, uid)
	chats := jx.Strs(s, "chats")
	stop := ""
	switch {
	case !jx.Bool(s, "enabled"):
		stop = "Радар выключен"
	case len(chats) == 0:
		stop = "Нет чатов в списке"
	case !u.TelegramUserConnected():
		stop = "Аккаунт Telegram не подключён"
	}
	if stop != "" {
		a.radarSaveRun(ctx, uid, jx.M{"last_error": stop})
		log("Остановлено: %s", stop)
		return jx.M{"error": stop}
	}
	a.radarSaveRun(ctx, uid, jx.M{"last_run": now()})
	res := jx.M{"seen": 0, "queued": 0, "classified": 0, "leads": 0}
	errs := []string{}
	log("Читаю %d %s…", len(chats), plural(len(chats), [3]string{"чат", "чата", "чатов"}))
	lastIDs := jx.Map(s, "last_ids")
	var specs []tgclient.ChatSpec
	for _, ref := range chats {
		specs = append(specs, tgclient.ChatSpec{Ref: ref, MinID: int(jx.Int(lastIDs, ref))})
	}
	result, err := u.TelegramUserReadChats(specs)
	if err != nil {
		a.radarSaveRun(ctx, uid, jx.M{"last_error": err.Error()})
		log("Ошибка Telegram: %s", err)
		return jx.M{"error": err.Error()}
	}
	resolved, info := jx.Map(s, "resolved"), jx.Map(s, "chat_info")
	seen, queued, skipped := 0, 0, 0
	for _, c := range result {
		ci := orEmpty(jx.Map(info, c.Ref))
		ci["title"] = c.Title
		ci["error"] = nil
		if c.Error != "" {
			ci["error"] = c.Error
			errs = append(errs, c.Ref+": "+c.Error)
			log("%s: ошибка — %s", c.Ref, c.Error)
		} else {
			log("%s: %d %s", c.Ref, len(c.Messages), plural(len(c.Messages), [3]string{"новое сообщение", "новых сообщения", "новых сообщений"}))
		}
		info[c.Ref] = ci
		if c.ChatID != 0 {
			resolved[c.Ref] = c.ChatID
		}
		for _, m := range c.Messages {
			seen++
			if int64(m.ID) > jx.Int(lastIDs, c.Ref) {
				lastIDs[c.Ref] = m.ID
			}
			if !LeadPrefilter(m.Text) {
				skipped++
				continue
			}
			n, _ := db.Exec(ctx, a.DB.Pool, `INSERT INTO telegram_lead_queue (user_id, chat_id, message, message_id, status, chat_ref, chat_title, chat_username,
				sender_id, sender_username, sender_name, message_date) VALUES ($1, $2, $3, $4, 'pending', $5, $6, $7, $8, $9, $10, to_timestamp($11))
				ON CONFLICT DO NOTHING`, uid, c.ChatID, m.Text, m.ID, c.Ref, c.Title, c.Username, m.SenderID, m.SenderUsername, m.SenderName, m.Date)
			queued += int(n)
		}
	}
	res["seen"], res["queued"] = seen, queued
	log("Отсеяно без модели: %d, в очередь: %d", skipped, queued)
	a.DB.Pool.Exec(ctx, `UPDATE telegram_lead_queue SET status = 'pending' WHERE user_id = $1 AND status = 'processing' AND created_at < now() - interval '15 minutes'`, uid)
	a.DB.Pool.Exec(ctx, `UPDATE telegram_lead_queue SET status = 'pending' WHERE user_id = $1 AND status = 'failed' AND attempts < 3`, uid)
	items, _ := db.All(ctx, a.DB.Pool, `SELECT id, message, chat_id, message_id, chat_title, chat_username, sender_id, sender_username, sender_name
		FROM telegram_lead_queue WHERE status = 'pending' AND user_id = $1 ORDER BY id LIMIT $2`, uid, leadBatch)
	classified, leads := 0, 0
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		id, msg := jx.ToInt(it["id"]), jx.ToStr(it["message"])
		preview := trimRunes(collapseSpaces(msg), 80)
		a.DB.Pool.Exec(ctx, `UPDATE telegram_lead_queue SET status = 'processing' WHERE id = $1`, id)
		if a.UAFilterOn() && TextIsUkrainian(msg) {
			a.DB.Pool.Exec(ctx, `UPDATE telegram_lead_queue SET status = 'processed', ollama_result = 0, ollama_reason = 'украинский текст: фильтр Украины', processed_at = now() WHERE id = $1`, id)
			log("Пропуск: украинский текст — «%s»", preview)
			continue
		}
		started := time.Now()
		lead, reason, err := a.LeadClassify(ctx, uid, msg)
		if err != nil {
			a.DB.Pool.Exec(ctx, `UPDATE telegram_lead_queue SET status = 'failed', attempts = attempts + 1, ollama_error = $1 WHERE id = $2`, trimRunes(err.Error(), 300), id)
			errs = append(errs, "Классификация: "+err.Error())
			log("Модель: ошибка — %s · «%s»", err, preview)
			continue
		}
		classified++
		verdict := "не лид"
		if lead == 1 {
			verdict = "лид"
		}
		log("Модель (%d с): %s — %s · «%s»", int(time.Since(started).Seconds()+0.5), verdict, reason, preview)
		if lead == 1 {
			leads++
			var msgURL any
			if un := jx.ToStr(it["chat_username"]); un != "" {
				msgURL = fmt.Sprintf("https://t.me/%s/%d", un, jx.ToInt(it["message_id"]))
			}
			a.DB.Pool.Exec(ctx, `INSERT INTO telegram_leads (queue_id, user_id, chat_id, message, message_id, lead_status, chat_title, sender_id, sender_name,
				telegram_username, message_url, reason) VALUES ($1, $2, $3, $4, $5, 'new', $6, $7, $8, $9, $10, $11) ON CONFLICT DO NOTHING`,
				id, uid, it["chat_id"], msg, it["message_id"], it["chat_title"], it["sender_id"], it["sender_name"], it["sender_username"], msgURL, reason)
		}
		a.DB.Pool.Exec(ctx, `UPDATE telegram_lead_queue SET status = 'processed', ollama_result = $1, ollama_reason = $2, processed_at = now() WHERE id = $3`, lead, reason, id)
	}
	res["classified"], res["leads"], res["errors"] = classified, leads, errs
	var lastErr any
	if len(errs) > 0 {
		lastErr = strings.Join(errs, "; ")
	}
	a.radarSaveRun(ctx, uid, jx.M{"last_ids": lastIDs, "resolved": resolved, "chat_info": info, "last_seen": seen, "last_run": now(), "last_error": lastErr})
	log("Готово: просмотрено %d, лидов %d", seen, leads)
	return res
}

// ---- Админка радара ----

func (a *App) connectedUIDs(ctx context.Context) []string {
	rows, err := a.DB.Pool.Query(ctx, `SELECT id FROM users WHERE connections ? 'telegram_user' ORDER BY created_at`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		if a.User(ctx, id, nil).TelegramUserConnected() {
			out = append(out, id)
		}
	}
	return out
}

// RadarAccountUID — аккаунт радара: выбранный владельцем или единственный подключённый.
func (a *App) RadarAccountUID(ctx context.Context) string {
	conf := KV(a, ctx, "admin:radar", jx.M{})
	connected := a.connectedUIDs(ctx)
	if uid := jx.Str(conf, "account_uid"); contains(connected, uid) {
		return uid
	}
	if len(connected) == 1 {
		a.KVSet(ctx, "admin:radar", jx.M{"account_uid": connected[0]}, 0)
		return connected[0]
	}
	return ""
}

func (a *App) radarAccountRequired(ctx context.Context) (string, error) {
	if uid := a.RadarAccountUID(ctx); uid != "" {
		return uid, nil
	}
	return "", Bad("Нет аккаунта Telegram: подключите его в «Личном кабинете» или выберите аккаунт.")
}

func radarValidateQueries(in []string) ([]string, error) {
	out := []string{}
	for _, q := range in {
		q = collapseSpaces(q)
		if n := runeLen(q); n >= 2 && n <= 60 && !contains(out, q) {
			out = append(out, q)
		}
	}
	if len(out) > radarMaxQueries {
		return nil, Bad("Запросов не больше %d.", radarMaxQueries)
	}
	return out, nil
}

// RadarAPI — операции вкладки «Радар» (GET и POST).
func (a *App) RadarAPI(ctx context.Context, op string, in jx.M) (jx.M, error) {
	switch op {
	case "accounts":
		sel := a.RadarAccountUID(ctx)
		list := []jx.M{}
		for _, uid := range a.connectedUIDs(ctx) {
			var created int64
			db.Value(ctx, a.DB.Pool, &created, `SELECT created_at FROM users WHERE id = $1`, uid)
			list = append(list, jx.M{"uid": uid, "short": uid[:8], "created_at": created, "selected": uid == sel})
		}
		return jx.M{"accounts": list}, nil
	case "account_select":
		uid := jx.Str(in, "uid")
		if !contains(a.connectedUIDs(ctx), uid) {
			return nil, Bad("Такого подключённого аккаунта нет.")
		}
		return jx.M{}, a.KVSet(ctx, "admin:radar", jx.M{"account_uid": uid}, 0)
	case "settings":
		uid := a.RadarAccountUID(ctx)
		if uid == "" {
			return jx.M{"account_uid": nil, "enabled": false, "chats": []any{}, "queries": radarDefaultQueries, "last_run": nil, "last_seen": 0, "last_error": nil}, nil
		}
		s := a.RadarSettings(ctx, uid)
		info := jx.Map(s, "chat_info")
		chats := []jx.M{}
		for _, ref := range jx.Strs(s, "chats") {
			ci := jx.Map(info, ref)
			chats = append(chats, jx.M{"ref": ref, "title": jx.Str(ci, "title"), "members": jx.Int(ci, "members"), "error": ci["error"]})
		}
		q := jx.Strs(s, "queries")
		if len(q) == 0 {
			q = radarDefaultQueries
		}
		return jx.M{"account_uid": uid, "enabled": jx.Bool(s, "enabled"), "queries": q, "chats": chats, "last_run": s["last_run"], "last_seen": jx.Int(s, "last_seen"),
			"last_error": s["last_error"]}, nil
	case "settings_save":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		items := jx.List(in, "chats")
		if len(items) > radarMaxChats {
			return nil, Bad("Чатов не больше %d.", radarMaxChats)
		}
		var refs []string
		members := map[string]int64{}
		for _, c := range items {
			if m := jx.ToMap(c); m != nil {
				refs = append(refs, jx.Str(m, "ref"))
				if n := jx.Int(m, "members"); n > 0 {
					members[strings.ToLower(jx.Str(m, "ref"))] = n
				}
			} else {
				refs = append(refs, jx.ToStr(c))
			}
		}
		chats := RadarValidateChats(refs)
		queries, err := radarValidateQueries(jx.Strs(in, "queries"))
		if err != nil {
			return nil, err
		}
		_, err = KVUpdate(a, ctx, "lead_radar:"+uid, 0, func(s jx.M, _ bool) *jx.M {
			s = jx.Defaults(orEmpty(s), radarDefaults())
			oldInfo, oldIDs := jx.Map(s, "chat_info"), jx.Map(s, "last_ids")
			info, ids := jx.M{}, jx.M{}
			for _, ref := range chats {
				ci := orEmpty(jx.Map(oldInfo, ref))
				if n, ok := members[strings.ToLower(ref)]; ok {
					ci["members"] = n
				}
				info[ref] = ci
				if v, ok := oldIDs[ref]; ok {
					ids[ref] = v
				}
			}
			s["enabled"], s["chats"], s["queries"], s["chat_info"], s["last_ids"] = jx.Bool(in, "enabled"), chats, queries, info, ids
			return &s
		})
		return jx.M{}, err
	case "chats_search":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		queries, err := radarValidateQueries(jx.Strs(in, "queries"))
		if err != nil {
			return nil, err
		}
		if len(queries) == 0 {
			return nil, Bad("Введите хотя бы один запрос.")
		}
		added := map[string]bool{}
		for _, c := range jx.Strs(a.RadarSettings(ctx, uid), "chats") {
			added[strings.ToLower(c)] = true
		}
		found, err := a.User(ctx, uid, nil).TelegramUserFindChats(queries)
		if err != nil {
			return nil, err
		}
		out := []jx.M{}
		for _, g := range found {
			if g.Members >= radarMinMembers && !added["@"+strings.ToLower(g.Username)] {
				out = append(out, jx.M{"ref": "@" + g.Username, "title": g.Title, "members": g.Members, "query": g.Query})
			}
		}
		sortMapsDesc(out, "members")
		return jx.M{"chats": out}, nil
	case "scan_start":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		if jx.Bool(in, "auto") {
			s := a.RadarSettings(ctx, uid)
			if !jx.Bool(s, "enabled") || now()-jx.Int(s, "last_run") <= radarAutoInterval {
				return jx.M{"running": a.RadarRunning(uid), "started": false}, nil
			}
		}
		if a.RadarRunning(uid) {
			return jx.M{"running": true, "started": false}, nil
		}
		a.Go("radar-scan", func(ctx context.Context) { a.RadarRun(ctx, uid, nil) })
		return jx.M{"running": true, "started": true}, nil
	case "scan_log":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		lines := []string{}
		if b, err := os.ReadFile(a.radarLogPath(uid)); err == nil {
			for _, l := range strings.Split(string(b), "\n") {
				if strings.TrimSpace(l) != "" {
					lines = append(lines, l)
				}
			}
		}
		return jx.M{"running": a.RadarRunning(uid), "lines": lines[max(0, len(lines)-200):]}, nil
	case "leads":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		status := jx.Str(in, "status")
		if !contains(RadarLeadStatuses, status) {
			status = "new"
		}
		offset := max(0, jx.Int(in, "offset"))
		rows, _ := db.All(ctx, a.DB.Pool, `SELECT id, message, chat_title, sender_name, telegram_username, message_url, reason, lead_status, created_at
			FROM telegram_leads WHERE user_id = $1 AND lead_status = $2 ORDER BY created_at DESC, id DESC LIMIT 20 OFFSET $3`, uid, status, offset)
		userRe, urlRe := regexp.MustCompile(`^[A-Za-z0-9_]{3,32}$`), regexp.MustCompile(`^https://t\.me/[A-Za-z0-9_]{4,32}/\d+$`)
		for _, r := range rows {
			name := jx.ToStr(r["telegram_username"])
			r["profile_url"] = nil
			if userRe.MatchString(name) {
				r["profile_url"] = "https://t.me/" + name
			}
			if !urlRe.MatchString(jx.ToStr(r["message_url"])) {
				r["message_url"] = nil
			}
			delete(r, "telegram_username")
			r["sender_username"] = name
		}
		var total int
		db.Value(ctx, a.DB.Pool, &total, `SELECT COUNT(*) FROM telegram_leads WHERE user_id = $1 AND lead_status = $2`, uid, status)
		return jx.M{"leads": orMaps(rows), "total": total, "offset": offset}, nil
	case "stats":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		statuses := jx.M{}
		for _, s := range RadarLeadStatuses {
			var n int
			db.Value(ctx, a.DB.Pool, &n, `SELECT COUNT(*) FROM telegram_leads WHERE user_id = $1 AND lead_status = $2`, uid, s)
			statuses[s] = n
		}
		var queued, seen, classified int
		db.Value(ctx, a.DB.Pool, &queued, `SELECT COUNT(*) FROM telegram_lead_queue WHERE user_id = $1 AND status = 'pending'`, uid)
		db.Value(ctx, a.DB.Pool, &seen, `SELECT COUNT(*) FROM telegram_lead_queue WHERE user_id = $1 AND created_at > now() - interval '1 day'`, uid)
		db.Value(ctx, a.DB.Pool, &classified, `SELECT COUNT(*) FROM telegram_lead_queue WHERE user_id = $1 AND status = 'processed' AND processed_at > now() - interval '1 day'`, uid)
		return jx.M{"statuses": statuses, "queued": queued, "seen_24h": seen, "classified_24h": classified}, nil
	case "lead_status":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		status := jx.Str(in, "status")
		if !contains(RadarLeadStatuses, status) {
			return nil, Bad("Неизвестный статус.")
		}
		n, _ := db.Exec(ctx, a.DB.Pool, `UPDATE telegram_leads SET lead_status = $1, updated_at = now(), replied_at = CASE WHEN $1 = 'replied' THEN now() ELSE replied_at END
			WHERE id = $2 AND user_id = $3`, status, jx.Int(in, "id"), uid)
		if n == 0 {
			return nil, NotFound("Лид не найден.")
		}
		return jx.M{}, nil
	case "rejected":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		rows, _ := db.All(ctx, a.DB.Pool, `SELECT q.id, left(q.message, 300) AS message, q.chat_title, q.sender_name, q.ollama_reason AS reason, q.processed_at
			FROM telegram_lead_queue q WHERE q.user_id = $1 AND q.status = 'processed' AND NOT EXISTS (SELECT 1 FROM telegram_leads l WHERE l.queue_id = q.id)
			ORDER BY q.processed_at DESC NULLS LAST, q.id DESC LIMIT 30`, uid)
		return jx.M{"rejected": orMaps(rows)}, nil
	case "rejected_retry":
		uid, err := a.radarAccountRequired(ctx)
		if err != nil {
			return nil, err
		}
		n, _ := db.Exec(ctx, a.DB.Pool, `UPDATE telegram_lead_queue SET status = 'pending', attempts = 0, ollama_result = NULL, ollama_reason = NULL, processed_at = NULL
			WHERE id = $1 AND user_id = $2 AND status = 'processed'`, jx.Int(in, "id"), uid)
		if n == 0 {
			return nil, NotFound("Сообщение не найдено.")
		}
		return jx.M{}, nil
	case "ua_filter":
		return a.uaFilterPublic(ctx), nil
	case "ua_filter_save":
		if jx.Has(in, "on") {
			a.UAFilterSet(ctx, jx.Bool(in, "on"))
		}
		if bl := jx.Map(in, "blocklist"); bl != nil {
			var l UABlocklist
			jsonInto(jx.Marshal(bl), &l)
			a.UABlocklistSave(ctx, l)
		}
		return a.uaFilterPublic(ctx), nil
	case "ua_unblock":
		id := strings.TrimSpace(jx.Str(in, "id"))
		if id == "" {
			return nil, Bad("Не указан источник для разблокировки.")
		}
		a.UAUnblockSource(ctx, id)
		return a.uaFilterPublic(ctx), nil
	case "models":
		return a.LLMPublic(ctx), nil
	}
	return nil, NotFound("Неизвестная операция радара.")
}

func (a *App) uaFilterPublic(ctx context.Context) jx.M {
	st := a.uaState(true)
	blocked := []jx.M{}
	for sid, e := range st.Blocked {
		name := e.Name
		if name == "" {
			name = sid
		}
		blocked = append(blocked, jx.M{"id": sid, "name": name, "reason": e.Reason, "at": e.At})
	}
	return jx.M{"on": st.On, "blocked_sources": blocked, "blocklist": st.Blocklist, "stats": a.UASkippedLastDay(ctx)}
}

func sortMapsDesc(l []jx.M, key string) {
	for i := 1; i < len(l); i++ {
		for j := i; j > 0 && jx.Float(l[j], key) > jx.Float(l[j-1], key); j-- {
			l[j], l[j-1] = l[j-1], l[j]
		}
	}
}
