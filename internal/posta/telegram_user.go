package posta

import (
	"context"
	"errors"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/tgclient"
)

// Личный аккаунт Telegram (MTProto): «Отложенные» канала, поиск каналов и постов, большие видео, чтение групп для радара.
// Сессия и api_hash хранятся зашифрованными в users.connections.telegram_user.

type TGChannel = tgclient.Channel

func tguAAD(uid, field string) string { return "tgu|" + uid + "|" + field }

func (a *App) decryptAny(enc, aad string) (string, bool) {
	if enc == "" {
		return "", false
	}
	if s, ok := a.Box.Decrypt(enc, aad); ok {
		return s, true
	}
	return a.Box.Decrypt(enc, "")
}

// TelegramAppKeys — api_id и api_hash приложения: вводятся один раз, дальше подставляются сами.
func (a *App) TelegramAppKeys(ctx context.Context) (int, string) {
	d := KV(a, ctx, "telegram_app_keys", jx.M{})
	hash, ok := a.decryptAny(jx.Str(d, "api_hash_enc"), "telegram_app_keys")
	id, _ := strconv.Atoi(jx.Str(d, "api_id"))
	if !ok || id == 0 {
		return 0, ""
	}
	return id, hash
}

func (a *App) TelegramAppKeysSave(ctx context.Context, id int, hash string) {
	a.KVSet(ctx, "telegram_app_keys", jx.M{"api_id": strconv.Itoa(id), "api_hash_enc": a.Box.Encrypt(hash, "telegram_app_keys"), "saved_at": now()}, 0)
}

var tguLocks sync.Map

func tguLock(uid string) *sync.Mutex {
	m, _ := tguLocks.LoadOrStore(uid, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// TelegramUserAccount — подключённый аккаунт или nil.
func (u *U) TelegramUserAccount() *tgclient.Account {
	e := jx.Map(u.Connections(), "telegram_user")
	hash, ok1 := u.decryptAny(jx.Str(e, "api_hash_enc"), tguAAD(u.ID, "api_hash"))
	sess, ok2 := u.decryptAny(jx.Str(e, "session_enc"), tguAAD(u.ID, "session"))
	id, _ := strconv.Atoi(jx.Str(e, "api_id"))
	if !ok1 || !ok2 || id == 0 || sess == "" {
		return nil
	}
	return &tgclient.Account{AppID: id, AppHash: hash, Session: sess}
}

func (u *U) TelegramUserConnected() bool { return u.TelegramUserAccount() != nil }

// tguCall — вызов от имени аккаунта по одному за раз; обновлённая сессия сохраняется.
func (u *U) tguCall(timeout time.Duration, fn func(ctx context.Context, acc *tgclient.Account) error) error {
	l := tguLock(u.ID)
	l.Lock()
	defer l.Unlock()
	acc := u.TelegramUserAccount()
	if acc == nil {
		return Conflict("Аккаунт Telegram не подключён.")
	}
	before := acc.Session
	ctx, cancel := context.WithTimeout(u.Ctx, timeout)
	defer cancel()
	err := fn(ctx, acc)
	if acc.Session != before && acc.Session != "" {
		enc := u.Box.Encrypt(acc.Session, tguAAD(u.ID, "session"))
		u.ConnectionsUpdate(func(c jx.M) jx.M {
			e := jx.Map(c, "telegram_user")
			if e == nil {
				return nil
			}
			e["session_enc"] = enc
			c["telegram_user"] = e
			return c
		})
	}
	if err != nil {
		if errors.Is(err, tgclient.ErrUnauthorized) {
			return Conflict("%s", err.Error())
		}
		return Bad("%s", err.Error())
	}
	return nil
}

// TelegramUserFindChannels — поиск каналов (queries) или похожих (similarTo).
func (u *U) TelegramUserFindChannels(queries, similarTo []string) ([]TGChannel, error) {
	if (len(queries) == 0 && len(similarTo) == 0) || !u.TelegramUserConnected() {
		return nil, nil
	}
	var out []TGChannel
	err := u.tguCall(2*time.Minute, func(ctx context.Context, acc *tgclient.Account) error {
		var err error
		if len(queries) > 0 {
			out, err = tgclient.SearchChannels(ctx, acc, queries, 30)
		} else {
			out, err = tgclient.SimilarChannels(ctx, acc, similarTo)
		}
		return err
	})
	var clean []TGChannel
	for _, c := range out {
		if IsValidTelegramChannel(c.Username) {
			clean = append(clean, c)
		}
	}
	return clean, err
}

// TelegramUserResolveUsernames — существуют ли каналы (не больше 10 за вызов): имя → true | false | nil (не удалось проверить).
func (u *U) TelegramUserResolveUsernames(names []string) map[string]*bool {
	var valid []string
	for _, n := range names {
		if IsValidTelegramChannel(n) && len(valid) < 10 {
			valid = append(valid, n)
		}
	}
	out := map[string]*bool{}
	if len(valid) == 0 || !u.TelegramUserConnected() {
		return out
	}
	u.tguCall(time.Minute, func(ctx context.Context, acc *tgclient.Account) error {
		res, err := tgclient.ResolveUsernames(ctx, acc, valid)
		for _, r := range res {
			out[r.Username] = r.Exists
		}
		return err
	})
	return out
}

// TelegramUserFindChats — публичные группы по запросам (радар лидов).
func (u *U) TelegramUserFindChats(queries []string) ([]tgclient.Group, error) {
	if len(queries) == 0 || !u.TelegramUserConnected() {
		return nil, nil
	}
	var out []tgclient.Group
	err := u.tguCall(2*time.Minute, func(ctx context.Context, acc *tgclient.Account) error {
		var err error
		out, err = tgclient.SearchGroups(ctx, acc, queries, 30)
		return err
	})
	var clean []tgclient.Group
	for _, g := range out {
		if IsValidTelegramChannel(g.Username) {
			clean = append(clean, g)
		}
	}
	return clean, err
}

func (u *U) TelegramUserReadChats(specs []tgclient.ChatSpec) ([]tgclient.ChatResult, error) {
	if !u.TelegramUserConnected() {
		return nil, nil
	}
	var out []tgclient.ChatResult
	err := u.tguCall(3*time.Minute, func(ctx context.Context, acc *tgclient.Account) error {
		var err error
		out, err = tgclient.ReadChats(ctx, acc, specs)
		return err
	})
	return out, err
}

func (u *U) TelegramUserSearchPosts(query string, hashtag bool, limit int) (tgclient.PostsResult, error) {
	var r tgclient.PostsResult
	err := u.tguCall(time.Minute, func(ctx context.Context, acc *tgclient.Account) error {
		var err error
		if hashtag {
			r, err = tgclient.SearchPosts(ctx, acc, query, "", limit)
		} else {
			r, err = tgclient.SearchPosts(ctx, acc, "", query, limit)
		}
		return err
	})
	return r, err
}

// TelegramUserChannelRef — @имя или id канала, к которому подключён бот.
func TelegramUserChannelRef(c *Channel) string {
	if c.ChatUsername != "" {
		return "@" + c.ChatUsername
	}
	return strconv.FormatInt(c.ChatID, 10)
}

// TelegramUserSchedule — пост в «Отложенные» канала от имени аккаунта.
func (u *U) TelegramUserSchedule(c *Channel, text string, prepared []Prepared, publishAt int64, progress progressFn) ([]int, error) {
	var media []tgclient.Media
	for _, f := range prepared {
		media = append(media, tgclient.Media{Kind: f.Kind, Path: f.Path, Width: f.Width, Height: f.Height, Duration: f.Duration})
	}
	if progress == nil {
		progress = noProgress
	}
	var ids []int
	err := u.tguCall(config.TelegramUserTimeout*time.Second, func(ctx context.Context, acc *tgclient.Account) error {
		var err error
		ids, err = tgclient.Schedule(ctx, acc, TelegramUserChannelRef(c), text, media, time.Unix(publishAt, 0), func(p float64) { progress("Отправка в Telegram", &p) })
		return err
	})
	return ids, err
}

func (u *U) TelegramUserCancel(c *Channel, ids []int) error {
	return u.tguCall(time.Minute, func(ctx context.Context, acc *tgclient.Account) error {
		return tgclient.Cancel(ctx, acc, TelegramUserChannelRef(c), ids)
	})
}

// TelegramUserDownloadVideo — видео поста публичного канала через аккаунт пользователя → файл в dir.
func (a *App) TelegramUserDownloadVideo(ctx context.Context, uid, channel string, postID int, dir string) (string, error) {
	if err := os.MkdirAll(dir, 0o775); err != nil {
		return "", Bad("Не удалось создать папку для видео.")
	}
	u := a.User(ctx, uid, nil)
	var path string
	err := u.tguCall(config.TGUVideoTimeout*time.Second, func(ctx context.Context, acc *tgclient.Account) error {
		var err error
		path, _, err = tgclient.DownloadVideo(ctx, acc, strings.TrimPrefix(channel, "@"), postID, dir, config.TGUVideoMaxBytes)
		return err
	})
	if err != nil {
		return "", err
	}
	if !fileExists(path) {
		return "", Bad("Telegram не отдал файл видео.")
	}
	return path, nil
}

// ---- Вход в аккаунт: код, пароль, QR ----

var (
	apiIDRe   = regexp.MustCompile(`^\d{3,10}$`)
	apiHashRe = regexp.MustCompile(`(?i)^[a-f0-9]{32}$`)
	nonDigitsRe  = regexp.MustCompile(`\D+`)
	phoneRe   = regexp.MustCompile(`^\+\d{8,15}$`)
)

// TelegramUserKeysFromInput — ключи из формы или сохранённые (пустые поля или мусор от менеджера паролей).
func (u *U) TelegramUserKeysFromInput(in jx.M) (int, string, error) {
	id, hash := strings.TrimSpace(jx.Str(in, "api_id")), strings.TrimSpace(jx.Str(in, "api_hash"))
	sid, shash := u.TelegramAppKeys(u.Ctx)
	valid := apiIDRe.MatchString(id) && apiHashRe.MatchString(hash)
	if (id == "" && hash == "") || (!valid && sid != 0) {
		if sid == 0 {
			return 0, "", Bad("Введите api_id и api_hash: их выдаёт my.telegram.org/apps (один раз, дальше они запомнятся).")
		}
		return sid, shash, nil
	}
	if !apiIDRe.MatchString(id) {
		return 0, "", Bad("api_id — число, его выдаёт my.telegram.org.")
	}
	if !apiHashRe.MatchString(hash) {
		return 0, "", Bad("api_hash — 32 символа (цифры и буквы a–f), его выдаёт my.telegram.org.")
	}
	n, _ := strconv.Atoi(id)
	if sid == 0 {
		u.TelegramAppKeysSave(u.Ctx, n, hash)
	}
	return n, hash, nil
}

// NormalizePhone — 8999…, 7999…, 999… → +7999… (как normalizePhone в js/account.js).
func NormalizePhone(v string) string {
	d := nonDigitsRe.ReplaceAllString(v, "")
	if d == "" {
		return ""
	}
	if strings.HasPrefix(strings.TrimSpace(v), "+") && d[0] != '7' {
		return "+" + d
	}
	switch {
	case len(d) == 11 && (d[0] == '7' || d[0] == '8'):
		return "+7" + d[1:]
	case len(d) == 10:
		return "+7" + d
	}
	return "+" + d
}

// TelegramUserPublic — состояние подключения для «Личного кабинета».
func (u *U) TelegramUserPublic() jx.M {
	c := u.Connections()
	e, p := jx.Map(c, "telegram_user"), jx.Map(c, "telegram_user_pending")
	id, _ := u.TelegramAppKeys(u.Ctx)
	return jx.M{"connected": jx.Str(e, "session_enc") != "", "name": jx.Str(e, "name"), "username": jx.Str(e, "username"), "connected_at": jx.Int(e, "connected_at"),
		"warning": jx.Str(e, "warning"), "app_keys": id != 0, "awaiting_code": p != nil && now()-jx.Int(p, "at") < config.TelegramUserPendingTTL,
		"phone": jx.Str(p, "phone"), "qr": jx.Bool(p, "qr"), "code_type": jx.Str(p, "code_type"), "next_type": jx.Str(p, "next_type")}
}

func (u *U) requireBot() (*Channel, error) {
	c := u.TelegramConnection("")
	if c == nil {
		return nil, Bad("Сначала подключите канал.")
	}
	return c, nil
}

func (u *U) tguPending() (*tgclient.Account, jx.M, error) {
	p := jx.Map(u.Connections(), "telegram_user_pending")
	if p == nil || now()-jx.Int(p, "at") > config.TelegramUserPendingTTL {
		return nil, nil, Conflict("Код устарел: запросите новый.")
	}
	hash, ok1 := u.decryptAny(jx.Str(p, "api_hash_enc"), tguAAD(u.ID, "api_hash"))
	sess, ok2 := u.decryptAny(jx.Str(p, "session_enc"), tguAAD(u.ID, "session"))
	if !ok1 || (!ok2 && jx.Str(p, "session_enc") != "") {
		return nil, nil, Conflict("Не удалось прочитать сохранённые данные: запросите код заново.")
	}
	id, _ := strconv.Atoi(jx.Str(p, "api_id"))
	return &tgclient.Account{AppID: id, AppHash: hash, Session: sess}, p, nil
}

func (u *U) savePending(base jx.M, acc *tgclient.Account, info tgclient.CodeInfo) {
	p := jx.Merge(base, jx.M{"session_enc": u.Box.Encrypt(acc.Session, tguAAD(u.ID, "session")), "phone_code_hash": info.PhoneCodeHash,
		"code_type": info.CodeType, "next_type": info.NextType, "at": now()})
	u.ConnectionsUpdate(func(c jx.M) jx.M { c["telegram_user_pending"] = p; return c })
}

func tguErr(err error) error {
	if err == nil {
		return nil
	}
	var e *Error
	if errors.As(err, &e) {
		return err
	}
	return Bad("%s", err.Error())
}

// TelegramUserStart — {api_id, api_hash, phone}: Telegram присылает код входа.
func (u *U) TelegramUserStart(in jx.M) error {
	if _, err := u.requireBot(); err != nil {
		return err
	}
	id, hash, err := u.TelegramUserKeysFromInput(in)
	if err != nil {
		return err
	}
	phone := NormalizePhone(jx.Str(in, "phone"))
	if !phoneRe.MatchString(phone) {
		return Bad("Номер указывается с кодом страны: +79991234567.")
	}
	acc := &tgclient.Account{AppID: id, AppHash: hash}
	ctx, cancel := context.WithTimeout(u.Ctx, time.Minute)
	defer cancel()
	info, err := tgclient.SendCode(ctx, acc, phone)
	if err != nil {
		return tguErr(err)
	}
	u.savePending(jx.M{"api_id": strconv.Itoa(id), "api_hash_enc": u.Box.Encrypt(hash, tguAAD(u.ID, "api_hash")), "phone": phone}, acc, info)
	return nil
}

func (u *U) TelegramUserResend() error {
	acc, p, err := u.tguPending()
	if err != nil {
		return err
	}
	if jx.Str(p, "next_type") == "" {
		return Conflict("Другого способа доставки кода Telegram не предлагает: подождите несколько минут и запросите код заново.")
	}
	ctx, cancel := context.WithTimeout(u.Ctx, time.Minute)
	defer cancel()
	info, err := tgclient.ResendCode(ctx, acc, jx.Str(p, "phone"), jx.Str(p, "phone_code_hash"))
	if err != nil {
		return tguErr(err)
	}
	u.savePending(p, acc, info)
	return nil
}

func channelWarning(c tgclient.ChannelCheck) string {
	if c.CanPost {
		return ""
	}
	return "Вы вошли как " + c.Account + ", а в канале «" + c.Title + "» у него роль: " + c.Role +
		". Нужен владелец или администратор с правом публикации — иначе отложенные посты будут идти через очередь приложения."
}

// telegramUserFinish — сохраняет вошедший аккаунт после проверки прав в канале.
func (u *U) telegramUserFinish(acc *tgclient.Account, user *tgclient.User) error {
	bot, err := u.requireBot()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(u.Ctx, time.Minute)
	defer cancel()
	check, err := tgclient.CheckChannel(ctx, acc, TelegramUserChannelRef(bot))
	if err != nil {
		return tguErr(err)
	}
	if user == nil {
		user = &tgclient.User{}
	}
	e := jx.M{"api_id": strconv.Itoa(acc.AppID), "api_hash_enc": u.Box.Encrypt(acc.AppHash, tguAAD(u.ID, "api_hash")),
		"session_enc": u.Box.Encrypt(acc.Session, tguAAD(u.ID, "session")), "name": user.Name, "username": user.Username, "connected_at": now(), "warning": channelWarning(check)}
	_, err = u.ConnectionsUpdate(func(c jx.M) jx.M {
		delete(c, "telegram_user_pending")
		delete(c, "telegram_user_qr")
		c["telegram_user"] = e
		return c
	})
	if id, _ := u.TelegramAppKeys(u.Ctx); id == 0 {
		u.TelegramAppKeysSave(u.Ctx, acc.AppID, acc.AppHash)
	}
	return err
}

// TelegramUserConfirm — {code, password?}: завершает вход; при двухэтапной защите — needs_password=true.
func (u *U) TelegramUserConfirm(in jx.M) (needsPassword bool, err error) {
	if _, err := u.requireBot(); err != nil {
		return false, err
	}
	acc, p, err := u.tguPending()
	if err != nil {
		return false, err
	}
	code, password := nonDigitsRe.ReplaceAllString(jx.Str(in, "code"), ""), jx.Str(in, "password")
	if code == "" && password == "" {
		return false, Bad("Введите код из Telegram.")
	}
	ctx, cancel := context.WithTimeout(u.Ctx, time.Minute)
	defer cancel()
	var user *tgclient.User
	if jx.Bool(p, "qr") {
		user, err = tgclient.Password(ctx, acc, password)
	} else {
		user, err = tgclient.SignIn(ctx, acc, jx.Str(p, "phone"), code, jx.Str(p, "phone_code_hash"), password)
	}
	if errors.Is(err, tgclient.ErrNeedsPassword) {
		enc := u.Box.Encrypt(acc.Session, tguAAD(u.ID, "session"))
		u.ConnectionsUpdate(func(c jx.M) jx.M {
			pp := jx.Map(c, "telegram_user_pending")
			if pp == nil {
				return nil
			}
			pp["session_enc"] = enc
			c["telegram_user_pending"] = pp
			return c
		})
		return true, nil
	}
	if err != nil {
		return false, tguErr(err)
	}
	return false, u.telegramUserFinish(acc, user)
}

// Вход по QR: горутина держит соединение до 150 с; состояние — в памяти процесса.
type qrRun struct {
	state  tgclient.QRState
	cancel context.CancelFunc
	appID  int
	hash   string
}

var qrRuns = struct {
	sync.Mutex
	m map[string]*qrRun
}{m: map[string]*qrRun{}}

func (u *U) TelegramUserQRStart(in jx.M) error {
	if _, err := u.requireBot(); err != nil {
		return err
	}
	id, hash, err := u.TelegramUserKeysFromInput(in)
	if err != nil {
		return err
	}
	u.ConnectionsUpdate(func(c jx.M) jx.M { delete(c, "telegram_user_pending"); return c })
	ctx, cancel := context.WithCancel(u.App.Context())
	run := &qrRun{state: tgclient.QRState{Status: "starting"}, cancel: cancel, appID: id, hash: hash}
	qrRuns.Lock()
	if old := qrRuns.m[u.ID]; old != nil {
		old.cancel()
	}
	qrRuns.m[u.ID] = run
	qrRuns.Unlock()
	u.Go("tg-qr", func(context.Context) {
		tgclient.QRLogin(ctx, id, hash, 150*time.Second, func(s tgclient.QRState) {
			qrRuns.Lock()
			run.state = s
			qrRuns.Unlock()
		})
	})
	return nil
}

// TelegramUserQRStatus — waiting (+svg) | needs_password | ok | busy | expired | error | none.
func (u *U) TelegramUserQRStatus() jx.M {
	if u.TelegramUserConnected() {
		return jx.M{"status": "ok", "telegram_user": u.TelegramUserPublic()}
	}
	qrRuns.Lock()
	run := qrRuns.m[u.ID]
	var st tgclient.QRState
	if run != nil {
		st = run.state
		if st.Status == "ok" || st.Status == "needs_password" {
			run.state.Status = "finishing"
		}
	}
	qrRuns.Unlock()
	if run == nil || u.TelegramConnection("") == nil {
		return jx.M{"status": "none"}
	}
	switch st.Status {
	case "needs_password":
		acc := &tgclient.Account{AppID: run.appID, AppHash: run.hash, Session: st.Session}
		u.savePending(jx.M{"api_id": strconv.Itoa(run.appID), "api_hash_enc": u.Box.Encrypt(run.hash, tguAAD(u.ID, "api_hash")), "qr": true, "phone": ""}, acc, tgclient.CodeInfo{})
		u.qrForget()
		return jx.M{"status": "needs_password"}
	case "ok":
		acc := &tgclient.Account{AppID: run.appID, AppHash: run.hash, Session: st.Session}
		if err := u.telegramUserFinish(acc, st.User); err != nil {
			msg := "Вход выполнен, но канал проверить не удалось: " + err.Error()
			qrRuns.Lock()
			run.state = tgclient.QRState{Status: "error", Error: msg}
			qrRuns.Unlock()
			return jx.M{"status": "error", "error": msg}
		}
		u.qrForget()
		return jx.M{"status": "ok", "telegram_user": u.TelegramUserPublic()}
	case "finishing":
		return jx.M{"status": "busy"}
	case "expired":
		return jx.M{"status": "expired"}
	case "error":
		return jx.M{"status": "error", "error": st.Error}
	}
	return jx.M{"status": "waiting", "svg": st.SVG}
}

func (u *U) qrForget() {
	qrRuns.Lock()
	delete(qrRuns.m, u.ID)
	qrRuns.Unlock()
}

// TelegramUserDisconnect — забыть аккаунт (сессия в Telegram остаётся, её можно завершить в «Устройствах»).
func (u *U) TelegramUserDisconnect() error {
	_, err := u.ConnectionsUpdate(func(c jx.M) jx.M {
		delete(c, "telegram_user")
		delete(c, "telegram_user_pending")
		delete(c, "telegram_user_qr")
		return c
	})
	return err
}
