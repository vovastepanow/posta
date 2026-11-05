package posta

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/mail"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/bcrypt"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

// Вход: сессии (кука → sha256 в базе), вход через Telegram-бота и свои логин + пароль.
// Новые анонимные профили создаются, только если в админке включено «Новые пользователи».

// ---- Бот входа ----

func (a *App) TelegramLoginBot(ctx context.Context) jx.M {
	d := KV(a, ctx, "telegram_login_bot", jx.M(nil))
	if d == nil {
		return nil
	}
	if enc := jx.Str(d, "token_enc"); enc != "" {
		if t, ok := a.decryptAny(enc, "telegram_login_bot"); ok {
			d["token"] = t
		}
		delete(d, "token_enc")
	}
	if jx.Str(d, "token") == "" {
		return nil
	}
	return d
}

func (a *App) TelegramLoginBotSave(ctx context.Context, token, username string) {
	a.KVSet(ctx, "telegram_login_bot", jx.M{"token_enc": a.Box.Encrypt(token, "telegram_login_bot"), "username": username}, 0)
}

// ---- Сессии ----

var sessionRe = regexp.MustCompile(`^[a-f0-9]{64}$`)

// SessionCreate — новая сессия: токен для куки (64 hex); в базе — только sha256.
func (a *App) SessionCreate(ctx context.Context, uid string) (string, error) {
	b := make([]byte, 32)
	rand.Read(b)
	token := hex.EncodeToString(b)
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		if err := ensureUser(ctx, tx, uid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO sessions (token_hash, user_id, created_at, last_seen) VALUES ($1, $2, $3, $3)`, sha256Hex(token), uid, now())
		return err
	})
	return token, err
}

// SessionUser — пользователь по токену куки или "".
func (a *App) SessionUser(ctx context.Context, token string) string {
	if !sessionRe.MatchString(token) {
		return ""
	}
	h := sha256Hex(token)
	var uid string
	var seen int64
	if a.DB.Pool.QueryRow(ctx, `SELECT user_id, last_seen FROM sessions WHERE token_hash = $1`, h).Scan(&uid, &seen) != nil {
		return ""
	}
	if now()-seen >= config.SessionTouchSeconds {
		a.DB.Pool.Exec(ctx, `UPDATE sessions SET last_seen = $1 WHERE token_hash = $2`, now(), h)
		a.DB.Pool.Exec(ctx, `UPDATE users SET last_seen = $1 WHERE id = $2`, now(), uid)
	}
	return uid
}

func (a *App) SessionDelete(ctx context.Context, token string) {
	if sessionRe.MatchString(token) {
		a.DB.Pool.Exec(ctx, `DELETE FROM sessions WHERE token_hash = $1`, sha256Hex(token))
	}
}

func (a *App) SessionDeleteAll(ctx context.Context, uid string) {
	a.DB.Pool.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1`, uid)
}

// Resolved — кто делает запрос: uid ("" — нет доступа), новый токен для куки или "".
type Resolved struct {
	UID, NewSID string
	DropLegacy  bool
}

// SessionResolve — пользователь по куке сессии; старая кука с id анонимного профиля переводится на сессию;
// иначе — новый анонимный профиль (если регистрация открыта в админке).
func (a *App) SessionResolve(ctx context.Context, sid, legacy string) (Resolved, error) {
	if uid := a.SessionUser(ctx, sid); uid != "" {
		return Resolved{UID: uid, DropLegacy: legacy != ""}, nil
	}
	if IsValidUserID(legacy) && a.UserExists(ctx, legacy) && !a.UserHasIdentity(ctx, legacy) {
		t, err := a.SessionCreate(ctx, legacy)
		return Resolved{UID: legacy, NewSID: t, DropLegacy: true}, err
	}
	if !a.TBool("registration") {
		return Resolved{DropLegacy: legacy != ""}, nil
	}
	uid := randHex(16)
	t, err := a.SessionCreate(ctx, uid)
	return Resolved{UID: uid, NewSID: t, DropLegacy: legacy != ""}, err
}

// UserHasIdentity — есть ли у профиля свой способ входа (Telegram или логин).
func (a *App) UserHasIdentity(ctx context.Context, uid string) bool {
	ok, _ := db.Exists(ctx, a.DB.Pool, `SELECT 1 FROM users WHERE id = $1 AND (telegram_id IS NOT NULL OR login IS NOT NULL OR vk_id IS NOT NULL)`, uid)
	return ok
}

// UserIsEmpty — анонимный профиль без оценок, каналов, очереди и денег.
func (a *App) UserIsEmpty(ctx context.Context, uid string) bool {
	if len(a.HistoryOf(ctx, uid).All()) > 0 || len(a.ChannelsOf(ctx, uid)) > 0 {
		return false
	}
	for _, q := range []string{`SELECT 1 FROM outbox WHERE user_id = $1`, `SELECT 1 FROM published WHERE user_id = $1`, `SELECT 1 FROM ledger WHERE user_id = $1`} {
		if ok, _ := db.Exists(ctx, a.DB.Pool, q, uid); ok {
			return false
		}
	}
	return true
}

// ---- Вход через Telegram (бот по ссылке, без домена и вебхука) ----

// LoginStart — ссылка t.me/<бот>?start=login_<токен> и токен для опроса.
func (a *App) LoginStart(ctx context.Context, uid string) (jx.M, error) {
	bot := a.TelegramLoginBot(ctx)
	if bot == nil {
		return nil, Bad("Вход через Telegram не настроен на сервере.")
	}
	var n int
	db.Value(ctx, a.DB.Pool, &n, `SELECT COUNT(*) FROM login_requests WHERE user_id = $1 AND created_at > $2 AND status IN ('pending', 'asked')`, uid, now()-config.LoginRequestTTL)
	if n >= 5 {
		return nil, Conflict("Слишком много попыток входа, подождите несколько минут")
	}
	token := randHex(16)
	if err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		if err := ensureUser(ctx, tx, uid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO login_requests (token_hash, user_id, created_at, status) VALUES ($1, $2, $3, 'pending')`, sha256Hex(token), uid, now())
		return err
	}); err != nil {
		return nil, err
	}
	return jx.M{"url": "https://t.me/" + jx.Str(bot, "username") + "?start=login_" + token, "token": token}, nil
}

type botCall struct {
	method string
	fields map[string]string
}

var (
	loginStartRe = regexp.MustCompile(`^/start login_([a-f0-9]{32})$`)
	loginCbRe    = regexp.MustCompile(`^login:([a-f0-9]{32})$`)
)

// LoginHandleUpdate — апдейт бота → вызовы API бота.
func (a *App) LoginHandleUpdate(ctx context.Context, upd jx.M) []botCall {
	var calls []botCall
	if msg := jx.Map(upd, "message"); msg != nil {
		chat, from := jx.Map(msg, "chat"), jx.Map(msg, "from")
		chatID := strconv.FormatInt(jx.Int(chat, "id"), 10)
		if jx.Str(chat, "type") == "private" && !jx.Bool(from, "is_bot") {
			if m := loginStartRe.FindStringSubmatch(jx.Str(msg, "text")); m != nil {
				h := sha256Hex(m[1])
				name := strings.TrimSpace(jx.Str(from, "first_name") + " " + jx.Str(from, "last_name"))
				n, _ := db.Exec(ctx, a.DB.Pool, `UPDATE login_requests SET status = 'asked', chat_id = $1, telegram_id = $2, tg_username = $3, tg_name = $4
					WHERE token_hash = $5 AND status = 'pending' AND created_at > $6`, jx.Int(chat, "id"), jx.Int(from, "id"), jx.Str(from, "username"), name, h, now()-config.LoginRequestTTL)
				if n > 0 {
					kb := jx.Marshal(jx.M{"inline_keyboard": [][]jx.M{{{"text": "Подтвердить вход", "callback_data": "login:" + m[1]}}}})
					calls = append(calls, botCall{"sendMessage", map[string]string{"chat_id": chatID, "reply_markup": string(kb),
						"text": "Войти на сайт Posta как " + name + "? Если вы сами не нажимали «Войти» на сайте — не подтверждайте."}})
				} else {
					calls = append(calls, botCall{"sendMessage", map[string]string{"chat_id": chatID, "text": "Ссылка для входа устарела. Нажмите «Войти через Telegram» на сайте ещё раз."}})
				}
			} else {
				calls = append(calls, botCall{"sendMessage", map[string]string{"chat_id": chatID, "text": "Я бот для входа на сайт Posta. Нажмите на сайте «Войти через Telegram»."}})
			}
		}
	}
	if cq := jx.Map(upd, "callback_query"); cq != nil {
		if m := loginCbRe.FindStringSubmatch(jx.Str(cq, "data")); m != nil {
			h := sha256Hex(m[1])
			n, _ := db.Exec(ctx, a.DB.Pool, `UPDATE login_requests SET status = 'confirmed' WHERE token_hash = $1 AND status = 'asked' AND created_at > $2 AND telegram_id = $3`,
				h, now()-config.LoginRequestTTL, jx.Int(jx.Map(cq, "from"), "id"))
			if n > 0 {
				msg := jx.Map(cq, "message")
				calls = append(calls, botCall{"answerCallbackQuery", map[string]string{"callback_query_id": jx.Str(cq, "id"), "text": "Готово"}},
					botCall{"editMessageText", map[string]string{"chat_id": strconv.FormatInt(jx.Int(jx.Map(msg, "chat"), "id"), 10), "message_id": strconv.FormatInt(jx.Int(msg, "message_id"), 10),
						"text": "Вход подтверждён. Вернитесь на сайт — страница обновится сама. Если ещё не задали логин и пароль в «Личном кабинете», задайте: по ним можно войти, когда Telegram недоступен."}})
			} else {
				calls = append(calls, botCall{"answerCallbackQuery", map[string]string{"callback_query_id": jx.Str(cq, "id"), "text": "Ссылка устарела, начните вход на сайте заново", "show_alert": "true"}})
			}
		}
	}
	return calls
}

var loginPollMu sync.Mutex

// LoginPollUpdates — забрать апдейты бота входа и ответить; второй одновременный вызов просто выходит.
func (a *App) LoginPollUpdates(ctx context.Context) error {
	bot := a.TelegramLoginBot(ctx)
	if bot == nil || !loginPollMu.TryLock() {
		return nil
	}
	defer loginPollMu.Unlock()
	token := jx.Str(bot, "token")
	offset := KV(a, ctx, "login_bot_offset", int64(0))
	raw, err := a.TelegramBotRequest(ctx, token, "getUpdates", map[string]string{"offset": strconv.FormatInt(offset, 10), "timeout": "0",
		"allowed_updates": `["message","callback_query"]`}, nil, 10*time.Second, nil)
	if err != nil {
		if strings.Contains(err.Error(), "409") || strings.Contains(strings.ToLower(err.Error()), "webhook") {
			return Bad("%s — у бота включён вебхук — выключите его: deleteWebhook", err.Error())
		}
		return err
	}
	var updates []jx.M
	json.Unmarshal(raw, &updates)
	for _, upd := range updates {
		for _, c := range a.LoginHandleUpdate(ctx, upd) {
			a.TelegramBotRequest(ctx, token, c.method, c.fields, nil, 30*time.Second, nil)
		}
		a.KVSet(ctx, "login_bot_offset", jx.Int(upd, "update_id")+1, 0)
	}
	return nil
}

// LoginStatus — pending | done | expired. При done возвращается новый токен сессии вошедшего профиля.
func (a *App) LoginStatus(ctx context.Context, uid, sid, token string) (string, string, error) {
	a.LoginPollUpdates(ctx)
	if !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(token) {
		return "expired", "", nil
	}
	h := sha256Hex(token)
	var status, tgUser, tgName string
	var tgID *int64
	err := a.DB.Pool.QueryRow(ctx, `SELECT status, telegram_id, tg_username, tg_name FROM login_requests WHERE token_hash = $1 AND user_id = $2 AND created_at > $3`,
		h, uid, now()-config.LoginRequestTTL).Scan(&status, &tgID, &tgUser, &tgName)
	if err != nil {
		return "expired", "", nil
	}
	switch status {
	case "pending", "asked":
		return "pending", "", nil
	case "confirmed":
		n, _ := db.Exec(ctx, a.DB.Pool, `UPDATE login_requests SET status = 'used' WHERE token_hash = $1 AND status = 'confirmed'`, h)
		if n == 0 || tgID == nil {
			return "expired", "", nil
		}
		var owner string
		db.Value(ctx, a.DB.Pool, &owner, `SELECT id FROM users WHERE telegram_id = $1`, *tgID)
		if owner == "" {
			a.DB.Pool.Exec(ctx, `UPDATE users SET telegram_id = $1 WHERE id = $2`, *tgID, uid)
			owner = uid
		}
		a.DB.Pool.Exec(ctx, `UPDATE users SET tg_username = $1, tg_name = $2 WHERE id = $3`, tgUser, tgName, owner)
		newSID, err := a.LoginSwitch(ctx, uid, sid, owner)
		return "done", newSID, err
	}
	return "expired", "", nil
}

// LoginSwitch — браузер переходит с uid на вошедший профиль owner; сессия пересоздаётся (защита от подмены сессии).
// Анонимный uid вливается в owner (пустой — удаляется); профиль со своим входом не трогаем.
func (a *App) LoginSwitch(ctx context.Context, uid, sid, owner string) (string, error) {
	a.StopParser(uid)
	if owner != uid && uid != "" {
		switch {
		case a.UserHasIdentity(ctx, uid):
		case !a.UserIsEmpty(ctx, uid):
			if err := a.UserMerge(ctx, uid, owner); err != nil {
				return "", err
			}
		default:
			a.DB.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, uid)
			os.RemoveAll(a.UserDir(uid))
		}
	}
	a.SessionDelete(ctx, sid)
	return a.SessionCreate(ctx, owner)
}

// ---- Свои учётные данные ----

const (
	loginFailureWindow   = 900
	loginFailuresPerUser = 10
	loginFailuresPerIP   = 30
	passwordMinLength    = 8
)

var (
	phoneLikeRe = regexp.MustCompile(`^[+\d][\d\s().-]*$`)
	nickRe      = regexp.MustCompile(`^[a-z][a-z0-9._-]{2,31}$`)
)

// CredentialsLoginNormalize — почта строчными; телефон «+» и цифры (8… и 10 цифр — Россия); ник — латиница.
func CredentialsLoginNormalize(login string) (string, error) {
	s := strings.TrimSpace(login)
	if strings.Contains(s, "@") {
		s = strings.ToLower(s)
		if a, err := mail.ParseAddress(s); err != nil || a.Address != s || len(s) > 254 {
			return "", Bad("Похоже на почту, но адрес неверный.")
		}
		return s, nil
	}
	if s != "" && phoneLikeRe.MatchString(s) {
		d := nonDigitsRe.ReplaceAllString(s, "")
		if !strings.HasPrefix(s, "+") {
			if len(d) == 11 && d[0] == '8' {
				d = "7" + d[1:]
			} else if len(d) == 10 {
				d = "7" + d
			}
		}
		if len(d) < 10 || len(d) > 15 {
			return "", Bad("Номер телефона — с кодом страны, 10–15 цифр.")
		}
		return "+" + d, nil
	}
	s = strings.ToLower(s)
	if !nickRe.MatchString(s) {
		return "", Bad("Логин — почта, номер телефона или ник латиницей (3–32 знака, с буквы; можно цифры и «._-»).")
	}
	return s, nil
}

func credentialsPasswordCheck(password, login string) error {
	switch {
	case runeLen(password) < passwordMinLength:
		return Bad("Пароль — не короче %d знаков.", passwordMinLength)
	case len(password) > 72:
		return Bad("Пароль слишком длинный: не больше 72 байт.")
	case strings.EqualFold(password, login):
		return Bad("Пароль не должен совпадать с логином.")
	}
	return nil
}

func (a *App) loginThrottleCheck(ctx context.Context, key, ip string) error {
	since := now() - loginFailureWindow
	count := func(k string) int {
		var n int
		db.Value(ctx, a.DB.Pool, &n, `SELECT COUNT(*) FROM login_failures WHERE key = $1 AND at > $2`, k, since)
		return n
	}
	if count("login:"+key) >= loginFailuresPerUser || count("ip:"+ip) >= loginFailuresPerIP {
		return Conflict("Слишком много неудачных попыток. Подождите 15 минут.")
	}
	return nil
}

func (a *App) loginFailureRecord(ctx context.Context, key, ip string) {
	a.DB.Pool.Exec(ctx, `INSERT INTO login_failures (key, at) VALUES ($1, $3), ($2, $3)`, "login:"+key, "ip:"+ip, now())
	a.DB.Pool.Exec(ctx, `DELETE FROM login_failures WHERE at < $1`, now()-86400)
}

// CredentialsSet — задать или сменить логин и пароль; если пароль уже был — нужен текущий. Остальные сессии закрываются.
func (a *App) CredentialsSet(ctx context.Context, uid, sid, login, password, current, ip string) (string, error) {
	var hash string
	var oldLogin *string
	if err := a.DB.Pool.QueryRow(ctx, `SELECT login, COALESCE(password_hash, '') FROM users WHERE id = $1`, uid).Scan(&oldLogin, &hash); err != nil {
		return "", Bad("Профиль не найден.")
	}
	login, err := CredentialsLoginNormalize(login)
	if err != nil {
		return "", err
	}
	if hash != "" {
		if err := a.loginThrottleCheck(ctx, "uid:"+uid, ip); err != nil {
			return "", err
		}
		if bcrypt.CompareHashAndPassword([]byte(hash), []byte(current)) != nil {
			a.loginFailureRecord(ctx, "uid:"+uid, ip)
			return "", Bad("Текущий пароль неверный.")
		}
	}
	if err := credentialsPasswordCheck(password, login); err != nil {
		return "", err
	}
	if taken, _ := db.Exists(ctx, a.DB.Pool, `SELECT 1 FROM users WHERE login = $1 AND id <> $2`, login, uid); taken {
		return "", Conflict("Этот логин уже занят другим профилем.")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	keep := ""
	if sessionRe.MatchString(sid) {
		keep = sha256Hex(sid)
	}
	return login, a.DB.Tx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE users SET login = $1, password_hash = $2 WHERE id = $3`, login, string(newHash), uid); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `DELETE FROM sessions WHERE user_id = $1 AND token_hash <> $2`, uid, keep)
		return err
	})
}

var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("posta-dummy-password"), bcrypt.DefaultCost)

// CredentialsLogin — вход по логину и паролю: браузер переходит на найденный профиль. Возвращает новый токен сессии.
func (a *App) CredentialsLogin(ctx context.Context, uid, sid, login, password, ip string) (string, string, error) {
	login, err := CredentialsLoginNormalize(login)
	if err != nil {
		return "", "", Bad("Неверный логин или пароль.")
	}
	if err := a.loginThrottleCheck(ctx, login, ip); err != nil {
		return "", "", err
	}
	var id, hash string
	a.DB.Pool.QueryRow(ctx, `SELECT id, COALESCE(password_hash, '') FROM users WHERE login = $1`, login).Scan(&id, &hash)
	check := []byte(hash)
	if hash == "" {
		check = dummyHash // по времени ответа не понять, есть ли такой логин
	}
	if bcrypt.CompareHashAndPassword(check, []byte(password)) != nil || hash == "" {
		a.loginFailureRecord(ctx, login, ip)
		return "", "", Bad("Неверный логин или пароль.")
	}
	if cost, err := bcrypt.Cost([]byte(hash)); err == nil && cost < bcrypt.DefaultCost {
		if h, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost); err == nil {
			a.DB.Pool.Exec(ctx, `UPDATE users SET password_hash = $1 WHERE id = $2`, string(h), id)
		}
	}
	newSID, err := a.LoginSwitch(ctx, uid, sid, id)
	return id, newSID, err
}

// AuthPublic — блок входа «Личного кабинета».
func (a *App) AuthPublic(ctx context.Context, uid string) jx.M {
	var tgID *int64
	var tgUser, tgName, hash string
	var login, vkID *string
	a.DB.Pool.QueryRow(ctx, `SELECT telegram_id, tg_username, tg_name, login, COALESCE(password_hash, ''), vk_id FROM users WHERE id = $1`, uid).Scan(&tgID, &tgUser, &tgName, &login, &hash, &vkID)
	var sessions int
	db.Value(ctx, a.DB.Pool, &sessions, `SELECT COUNT(*) FROM sessions WHERE user_id = $1`, uid)
	l := ""
	if login != nil {
		l = *login
	}
	return jx.M{"logged_in": tgID != nil || l != "" || vkID != nil, "vk": vkID != nil, "vk_configured": a.VKOAuthConfigured(), "telegram": tgID != nil, "tg_username": tgUser, "tg_name": tgName, "login": l, "has_password": hash != "",
		"sessions": sessions, "tg_configured": a.TelegramLoginBot(ctx) != nil}
}

// ---- Слияние профилей ----

// UserMerge — переносит в into всё ценное из from и удаляет from.
func (a *App) UserMerge(ctx context.Context, from, into string) error {
	if !IsValidUserID(from) || !IsValidUserID(into) || from == into {
		return nil
	}
	fromHist := a.HistoryOf(ctx, from)
	if _, err := a.historyUpdate(ctx, into, func(h History) History {
		for _, coll := range config.HistoryCollections {
			have := map[string]bool{}
			for _, r := range h[coll] {
				have[ContentImageKey(r)] = true
			}
			for _, r := range fromHist[coll] {
				if k := ContentImageKey(r); k != "" && !have[k] {
					h[coll] = append(h[coll], r)
				}
			}
		}
		return h
	}); err != nil {
		return err
	}
	if len(a.poolLoad(ctx, a.DB.Pool, into)) == 0 {
		fromPool := a.poolLoad(ctx, a.DB.Pool, from)
		a.User(ctx, into, nil).PoolUpdate(func([]jx.M) []jx.M { return fromPool })
	}
	fromConn := a.User(ctx, from, nil).Connections()
	a.User(ctx, into, nil).ConnectionsUpdate(func(c jx.M) jx.M {
		for k, v := range fromConn {
			if _, ok := c[k]; !ok {
				c[k] = v
			}
		}
		return c
	})
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		tx.Exec(ctx, `DELETE FROM channels WHERE user_id = $1 AND EXISTS (SELECT 1 FROM channels c2 WHERE c2.user_id = $2 AND c2.platform = channels.platform AND c2.ref = channels.ref)`, from, into)
		var next int
		tx.QueryRow(ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM channels WHERE user_id = $1`, into).Scan(&next)
		rows, _ := db.All(ctx, tx, `SELECT id FROM channels WHERE user_id = $1 ORDER BY position, connected_at`, from)
		for _, r := range rows {
			if err := a.channelMoveToUser(ctx, tx, jx.ToStr(r["id"]), from, into, next); err != nil {
				return err
			}
			next++
		}
		for _, q := range []string{`UPDATE ledger SET user_id = $1 WHERE user_id = $2`, `UPDATE post_usage SET user_id = $1 WHERE user_id = $2`,
			`UPDATE outbox SET user_id = $1 WHERE user_id = $2`, `UPDATE published SET user_id = $1 WHERE user_id = $2`,
			`UPDATE ledger SET related_user = $1 WHERE related_user = $2`, `UPDATE mirrors SET user_id = $1 WHERE user_id = $2`} {
			if _, err := tx.Exec(ctx, q, into, from); err != nil {
				return err
			}
		}
		tx.Exec(ctx, `INSERT INTO subscriptions (user_id, plan, channels, posts_per_day, price, started_at, until)
			SELECT $1, plan, channels, posts_per_day, price, started_at, until FROM subscriptions WHERE user_id = $2
			ON CONFLICT (user_id) DO UPDATE SET plan = EXCLUDED.plan, channels = EXCLUDED.channels, posts_per_day = EXCLUDED.posts_per_day,
			price = EXCLUDED.price, started_at = EXCLUDED.started_at, until = EXCLUDED.until WHERE subscriptions.until < EXCLUDED.until`, into, from)
		tx.Exec(ctx, `UPDATE users SET referred_by = $1 WHERE referred_by = $2 AND id <> $1`, into, from)
		tx.Exec(ctx, `UPDATE users SET referred_by = (SELECT referred_by FROM users WHERE id = $2) WHERE id = $1 AND referred_by = '' AND
			(SELECT referred_by FROM users WHERE id = $2) NOT IN ('', $1)`, into, from)
		var login *string
		var hash string
		tx.QueryRow(ctx, `SELECT login, COALESCE(password_hash, '') FROM users WHERE id = $1`, from).Scan(&login, &hash)
		if login != nil {
			var have *string
			tx.QueryRow(ctx, `SELECT login FROM users WHERE id = $1`, into).Scan(&have)
			if have == nil {
				tx.Exec(ctx, `UPDATE users SET login = NULL WHERE id = $1`, from)
				tx.Exec(ctx, `UPDATE users SET login = $1, password_hash = $2 WHERE id = $3`, *login, hash, into)
			}
		}
		_, err := tx.Exec(ctx, `DELETE FROM users WHERE id = $1`, from)
		return err
	})
	if err == nil {
		os.RemoveAll(filepath.Join(a.UsersDir(), from))
	}
	return err
}

// UsersGC — анонимные пустые профили без сессий старше суток (мусор от открытых вкладок и ботов).
func (a *App) UsersGC(ctx context.Context) int {
	rows, err := a.DB.Pool.Query(ctx, `SELECT u.id FROM users u WHERE u.telegram_id IS NULL AND u.login IS NULL AND u.vk_id IS NULL AND u.created_at < $1
		AND NOT EXISTS (SELECT 1 FROM sessions s WHERE s.user_id = u.id AND s.last_seen > $1)`, now()-86400)
	if err != nil {
		return 0
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	n := 0
	for _, id := range ids {
		if a.UserIsEmpty(ctx, id) && !a.IsOwner(ctx, id) {
			if _, err := a.DB.Pool.Exec(ctx, `DELETE FROM users WHERE id = $1`, id); err == nil {
				os.RemoveAll(a.UserDir(id))
				n++
			}
		}
	}
	return n
}

// IsOwner — владелец сайта (админка, радар, бета-соцсети): kv admin:owners.
func (a *App) IsOwner(ctx context.Context, uid string) bool {
	return uid != "" && contains(KV(a, ctx, "admin:owners", []string{}), uid)
}

var errNoUser = errors.New("нет пользователя")
