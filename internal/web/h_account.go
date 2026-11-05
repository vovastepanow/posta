package web

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/llm"
	"posta/internal/posta"
)

// «Личный кабинет»: вход, VK ID, Telegram (бот и аккаунт), публикация, очередь и календарь, тариф, токен API.

func init() {
	get("account_info", accountInfo)
	get("auth_status", func(c *Ctx) (any, error) {
		bot := c.A.TelegramLoginBot(c.R.Context())
		var r jx.M
		if c.U != nil {
			r = c.A.AuthPublic(c.R.Context(), c.U.ID)
		} else {
			r = jx.M{"logged_in": false, "telegram": false, "login": "", "has_password": false, "sessions": 0, "tg_configured": bot != nil, "vk_configured": c.A.VKOAuthConfigured()}
		}
		r["configured"], r["bot"], r["registration"] = bot != nil, jx.Str(bot, "username"), c.A.TBool("registration")
		return r, nil
	})
	post("password_login", func(c *Ctx) (any, error) {
		uid := ""
		if c.U != nil {
			uid = c.U.ID
		}
		_, sid, err := c.A.CredentialsLogin(c.R.Context(), uid, c.SID, c.Str("login"), c.Str("password"), clientIP(c.R))
		if err != nil {
			return nil, err
		}
		c.setSessionCookie(sid)
		return ok(nil), nil
	})
	post("credentials_save", func(c *Ctx) (any, error) {
		login, err := c.A.CredentialsSet(c.R.Context(), c.U.ID, c.SID, c.Str("login"), c.Str("password"), c.Str("current"), clientIP(c.R))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"login": login, "auth": c.A.AuthPublic(c.R.Context(), c.U.ID)}), nil
	})
	post("login_start", func(c *Ctx) (any, error) {
		r, err := c.A.LoginStart(c.R.Context(), c.U.ID)
		if err != nil {
			return nil, err
		}
		return ok(r), nil
	})
	get("login_status", func(c *Ctx) (any, error) {
		token := c.Q("token")
		if c.U == nil || !regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(token) {
			return jx.M{"status": "expired"}, nil
		}
		st, sid, err := c.A.LoginStatus(c.R.Context(), c.U.ID, c.SID, token)
		if err != nil {
			return jx.M{"status": "expired"}, nil
		}
		if sid != "" {
			c.setSessionCookie(sid)
		}
		return jx.M{"status": st}, nil
	})
	post("logout", func(c *Ctx) (any, error) { return logout(c, false) })
	post("logout_all", func(c *Ctx) (any, error) { return logout(c, true) })

	get("vk_oauth_status", func(c *Ctx) (any, error) {
		t := c.A.VKTokensPublic(c.U.Ctx)
		return jx.M{"configured": c.A.VKOAuthConfigured(), "connected": jx.Bool(t, "connected"), "user_id": t["user_id"], "redirect_uri": c.A.VKOAuthRedirectURI()}, nil
	})
	post("vk_login_prepare", vkLoginPrepare)
	get("vk_oauth_start", vkOAuthStart)
	get("vk_oauth_callback", vkOAuthCallback)

	post("telegram_connect", telegramConnect)
	post("telegram_disconnect", func(c *Ctx) (any, error) {
		id := c.Str("channel")
		if id == "" {
			return nil, fail(400, "Не указан канал.")
		}
		if d := c.U.ChannelDeleteDenied(id); d != "" {
			return nil, fail(409, d)
		}
		if !c.U.ChannelDelete(id) {
			return nil, fail(404, "Такого канала нет.")
		}
		return ok(jx.M{"channels": channelsPublic(c)}), nil
	})
	post("telegram_channel_default", func(c *Ctx) (any, error) {
		if err := c.U.ChannelMakeDefault(c.Str("channel")); err != nil {
			return nil, err
		}
		return ok(jx.M{"channels": channelsPublic(c), "telegram": telegramPublic(c)}), nil
	})
	post("telegram_user_start", func(c *Ctx) (any, error) {
		if err := c.U.TelegramUserStart(c.In); err != nil {
			return nil, err
		}
		return ok(jx.M{"telegram_user": c.U.TelegramUserPublic()}), nil
	})
	post("telegram_user_resend", func(c *Ctx) (any, error) {
		if err := c.U.TelegramUserResend(); err != nil {
			return nil, err
		}
		return ok(jx.M{"telegram_user": c.U.TelegramUserPublic()}), nil
	})
	post("telegram_user_confirm", func(c *Ctx) (any, error) {
		needs, err := c.U.TelegramUserConfirm(c.In)
		if err != nil {
			return nil, err
		}
		if needs {
			return jx.M{"status": "needs_password"}, nil
		}
		return ok(jx.M{"telegram_user": c.U.TelegramUserPublic()}), nil
	})
	post("telegram_user_qr_start", func(c *Ctx) (any, error) {
		if err := c.U.TelegramUserQRStart(c.In); err != nil {
			return nil, err
		}
		return jx.M{"status": "waiting"}, nil
	})
	get("telegram_user_qr_status", func(c *Ctx) (any, error) {
		r := c.U.TelegramUserQRStatus()
		if jx.Str(r, "status") == "ok" {
			return ok(jx.M{"telegram_user": r["telegram_user"]}), nil
		}
		return r, nil
	})
	post("telegram_user_disconnect", func(c *Ctx) (any, error) {
		if err := c.U.TelegramUserDisconnect(); err != nil {
			return nil, err
		}
		return ok(jx.M{"telegram_user": c.U.TelegramUserPublic()}), nil
	})

	post("telegram_publish", publish)
	get("telegram_publish_progress", func(c *Ctx) (any, error) {
		p := posta.PublishProgressGet(c.Q("id"))
		if p == nil {
			return jx.M{"stage": nil}, nil
		}
		return jx.M{"stage": p["stage"], "percent": p["percent"]}, nil
	})
	post("telegram_retry", telegramRetry)
	post("telegram_reschedule", telegramReschedule)
	get("telegram_next_slot", func(c *Ctx) (any, error) {
		ch, err := c.U.PublishChannelResolve(c.Q("channel"))
		if err != nil {
			return nil, err
		}
		if len(jx.Map(posta.ChannelSchedule(ch), "slots")) == 0 {
			return jx.M{"none": true}, nil
		}
		if s := c.A.ChannelNextSlot(ch, now(), c.U.Outbox()); s != nil {
			return s, nil
		}
		return jx.M{"none": true}, nil
	})
	post("channel_schedule_save", func(c *Ctx) (any, error) {
		s := jx.Map(c.In, "schedule")
		if s == nil {
			return nil, fail(400, "Некорректное расписание.")
		}
		if _, err := c.U.ChannelScheduleSave(c.Str("channel"), s); err != nil {
			return nil, err
		}
		return ok(jx.M{"channels": channelsPublic(c)}), nil
	})
	post("channel_hashtags_save", func(c *Ctx) (any, error) {
		mode := c.Str("mode")
		if !contains(posta.ChannelHashtagModes, mode) {
			return nil, fail(400, "Неизвестный режим хэштегов.")
		}
		if c.U.Channel(c.Str("channel")) == nil {
			return nil, fail(400, "Такого канала нет.")
		}
		if err := c.U.ChannelUpdateField(c.Str("channel"), "hashtags", mode); err != nil {
			return nil, err
		}
		return ok(jx.M{"channels": channelsPublic(c)}), nil
	})
	get("hashtag_suggest", func(c *Ctx) (any, error) { return ok(jx.M{"tags": posta.HashtagSuggest(c.Q("q"), 12)}), nil })
	get("channel_style", func(c *Ctx) (any, error) {
		ch, err := styleChannel(c, c.Q("channel"))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"style": c.U.ChannelStylePublic(ch)}), nil
	})
	post("channel_style_learn", func(c *Ctx) (any, error) {
		ch, err := styleChannel(c, c.Str("channel"))
		if err != nil {
			return nil, err
		}
		started, err := c.U.ChannelStyleLearnAsync(ch.ID)
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"started": started, "style": c.U.ChannelStylePublic(c.U.Channel(ch.ID))}), nil
	})
	post("channel_style_save", func(c *Ctx) (any, error) {
		ch, err := styleChannel(c, c.Str("channel"))
		if err != nil {
			return nil, err
		}
		if _, err := c.U.ChannelStyleSaveManual(ch.ID, c.Str("summary")); err != nil {
			return nil, err
		}
		return ok(jx.M{"style": c.U.ChannelStylePublic(c.U.Channel(ch.ID))}), nil
	})
	post("ai_caption", func(c *Ctx) (any, error) {
		ch, err := styleChannel(c, c.Str("channel"))
		if err != nil {
			return nil, err
		}
		u, cancel := c.userDetached(5 * time.Minute)
		defer cancel()
		text, err := u.ChannelCaptionGenerate(ch.ID, jx.M{"description": c.In["description"], "original": c.In["original"], "current": c.In["current"]})
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"text": text}), nil
	})
	post("news_excerpt", func(c *Ctx) (any, error) {
		u, cancel := c.userDetached(5 * time.Minute)
		defer cancel()
		text, err := u.NewsExcerpt(strings.TrimSpace(c.Str("url")))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"text": text}), nil
	})
	get("telegram_queue", telegramQueue)
	get("calendar", calendar)
	post("calendar_fill_plan", calendarFillPlan)
	post("calendar_fill_apply", func(c *Ctx) (any, error) {
		u, cancel := c.userDetached(15 * time.Minute)
		defer cancel()
		r, err := u.CalendarFillApply(jx.Maps(c.In, "items"))
		if err != nil {
			return nil, err
		}
		return ok(r), nil
	})
	post("calendar_check", func(c *Ctx) (any, error) {
		dup := c.A.CalendarDuplicateOf(jx.M{"image": c.Str("image"), "text": c.Str("text")}, c.Str("channel"), c.U.CalendarHistory(c.U.Outbox()), map[string]string{})
		return ok(jx.M{"duplicate": dup}), nil
	})
	post("hint_seen", func(c *Ctx) (any, error) {
		name := c.Str("name")
		if name != "calendar_v3" {
			return nil, fail(400, "Неизвестная подсказка.")
		}
		hints := jx.Map(c.U.Settings(), "hints")
		if hints == nil {
			hints = jx.M{}
		}
		hints[name] = now()
		return ok(jx.M{"hints_seen": hints}), c.U.SaveSettings(jx.M{"hints": hints})
	})
	post("telegram_duplicate", func(c *Ctx) (any, error) {
		id := c.Str("id")
		if !postIDRe.MatchString(id) {
			return nil, fail(400, "Некорректный номер поста.")
		}
		at := jx.Int(c.In, "publish_at")
		if err := checkPublishTime(c, at); err != nil {
			return nil, err
		}
		if d := c.U.BillingPostDenied(true); d != "" {
			return nil, fail(429, d)
		}
		cp, err := c.U.OutboxDuplicatePost(id, at)
		if err != nil {
			return nil, err
		}
		if cp == nil {
			return nil, fail(409, "Копировать можно только пост нашей очереди, который ждёт или не вышел.")
		}
		c.U.KickPublisher()
		return ok(jx.M{"id": cp["id"], "publish_at": at}), nil
	})
	get("post_thumb", func(c *Ctx) (any, error) {
		id := c.Q("id")
		if !c.U.ThumbOwned(id) {
			return nil, fail(404, "Нет такой миниатюры.")
		}
		c.serveFile(c.A.ThumbFile(id), "image/jpeg", "private, max-age=86400")
		return nil, nil
	})
	get("outbox_media", func(c *Ctx) (any, error) {
		id := c.Q("id")
		for _, e := range c.U.Outbox() {
			if jx.Str(e, "id") != id || jx.Str(e, "status") == "telegram" || !postIDRe.MatchString(id) {
				continue
			}
			media := jx.Maps(e, "media")
			index := 0
			if c.Q("index") != "" {
				var err error
				index, err = strconv.Atoi(c.Q("index"))
				if err != nil {
					break
				}
			}
			if index < 0 || index >= len(media) {
				break
			}
			dir, _ := c.A.OutboxMediaDir(id)
			path := filepath.Clean(jx.Str(media[index], "path"))
			rel, err := filepath.Rel(dir, path)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
				break
			}
			c.serveFile(path, "", "private, no-store")
			return nil, nil
		}
		return nil, fail(404, "Нет такого файла.")
	})
	get("outbox_edit_get", getOutboxEdit)
	post("outbox_edit_save", saveOutboxEdit)
	post("telegram_cancel", telegramCancel)

	get("model_status", modelStatus)
	post("model_control", modelControl)
	post("app_stop", func(c *Ctx) (any, error) {
		if !c.A.IsOwner(c.R.Context(), c.U.ID) {
			return nil, fail(404, "Неизвестное действие API.")
		}
		if t := c.Str("tab"); posta.IsValidTabID(t) {
			c.U.TabClose(t)
		}
		shared := c.A.SharedCollectionRunning()
		n := c.A.PauseAllParsers()
		if shared {
			n++
		}
		c.A.LLM.Ollama.UnloadAll(c.R.Context())
		remaining := len(c.A.RunningParsers())
		if c.A.SharedCollectionRunning() {
			remaining++
		}
		return ok(jx.M{"stopped": max(0, n-remaining), "stopping": remaining}), nil
	})

	get("billing_info", billingInfo)
	post("billing_buy", func(c *Ctx) (any, error) {
		plan, err := c.U.BillingBuy(c.Str("plan"), int(jx.Int(c.In, "channels")), int(jx.Int(c.In, "posts_per_day")))
		if err != nil {
			return nil, err
		}
		return ok(jx.M{"plan": plan, "balance": posta.KopToRub(c.A.BalanceKop(c.R.Context(), c.A.DB.Pool, c.U.ID))}), nil
	})
	post("referral_bind", func(c *Ctx) (any, error) {
		reason := c.U.ReferralBind(c.Str("code"))
		return ok(jx.M{"bound": reason == "", "message": reason}), nil
	})
	get("api_token_status", func(c *Ctx) (any, error) { return jx.M{"exists": c.U.APITokenExists()}, nil })
	post("api_token_create", func(c *Ctx) (any, error) { return ok(jx.M{"token": c.U.APITokenCreate()}), nil })
	post("api_token_revoke", func(c *Ctx) (any, error) { c.U.APITokenRevoke(); return ok(nil), nil })
	post("api_add_post", func(c *Ctx) (any, error) { return nil, fail(400, "Нужен токен API.") })
}

func (c *Ctx) setSessionCookie(token string) {
	secure := c.R.TLS != nil || strings.EqualFold(c.R.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(c.W, &http.Cookie{Name: config.SessionCookie, Value: token, Path: strings.TrimRight(c.A.Cfg.BasePath, "/") + "/", MaxAge: config.UserCookieDays * 86400,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
	c.SID = token
}

var postIDRe = regexp.MustCompile(`^[a-f0-9]{12}$`)

func logout(c *Ctx, all bool) (any, error) {
	ctx := c.R.Context()
	if !c.A.UserHasIdentity(ctx, c.U.ID) {
		return nil, fail(400, "Вы не вошли: выход сотрёт ваш анонимный профиль. Сначала задайте логин и пароль или войдите через Telegram.")
	}
	if all {
		c.A.SessionDeleteAll(ctx, c.U.ID)
	} else {
		c.A.SessionDelete(ctx, c.SID)
	}
	http.SetCookie(c.W, &http.Cookie{Name: config.SessionCookie, Value: "", Path: strings.TrimRight(c.A.Cfg.BasePath, "/") + "/", MaxAge: -1})
	if c.A.TBool("registration") {
		if t, err := c.A.SessionCreate(ctx, randUID()); err == nil {
			c.setSessionCookie(t)
		}
	}
	return ok(nil), nil
}

func accountInfo(c *Ctx) (any, error) {
	u := c.U
	h := u.History()
	counts := jx.M{}
	for _, coll := range config.HistoryCollections {
		counts[coll] = len(h[coll])
	}
	files := []jx.M{}
	entries, _ := os.ReadDir(c.A.UserDir(u.ID))
	for _, e := range entries {
		if info, err := e.Info(); err == nil && !e.IsDir() {
			about := "Служебный файл"
			if strings.HasPrefix(e.Name(), "parser-") && strings.HasSuffix(e.Name(), ".log") {
				about = "Журнал вашего сборщика (то, что видно в «Журнале»)"
			}
			files = append(files, jx.M{"name": e.Name(), "size": info.Size(), "modified": info.ModTime().Unix(), "about": about})
		}
	}
	sort.Slice(files, func(i, j int) bool { return jx.Int(files[i], "size") > jx.Int(files[j], "size") })
	cookies := []jx.M{}
	info := map[string][2]string{config.SessionCookie: {"Ваша сессия: случайный ключ, по нему сайт узнаёт ваш профиль. ID профиля в ней нет.",
		strconv.Itoa(config.UserCookieDays) + " дней, продлевается при каждом заходе"}}
	for _, ck := range c.R.Cookies() {
		i, known := info[ck.Name]
		purpose, life := i[0], i[1]
		if !known {
			purpose, life = "Неизвестная кука: этот сайт её не ставит (её прислал браузер для этого адреса).", "—"
		}
		cookies = append(cookies, jx.M{"name": ck.Name, "value": ck.Value, "known": known, "lifetime": life, "purpose": purpose, "http_only": ck.Name == config.SessionCookie})
	}
	var lastSeen int64
	c.A.DB.Pool.QueryRow(c.R.Context(), `SELECT last_seen FROM users WHERE id = $1`, u.ID).Scan(&lastSeen)
	return jx.M{"user_id": u.ID, "topic": u.CurrentTopic(), "last_seen": lastSeen, "pool": len(u.PoolTopicPositions(u.Pool())), "counts": counts, "files": files,
		"cookies": cookies, "session_keys": []string{}, "retention_days": 0, "exposed": []string{}, "fix_command": "", "telegram": telegramPublic(c),
		"channels": channelsPublic(c), "telegram_user": u.TelegramUserPublic(), "auth": c.A.AuthPublic(c.R.Context(), u.ID), "owner": c.A.IsOwner(c.R.Context(), u.ID)}, nil
}

func telegramPublic(c *Ctx) any {
	t := c.U.TelegramConnection("")
	if t == nil {
		return nil
	}
	tok := t.Token
	if r := []rune(tok); len(r) > 10 {
		tok = string(r[:6]) + "…" + string(r[len(r)-4:])
	}
	return jx.M{"bot": t.BotUsername, "chat_title": t.ChatTitle, "chat_username": t.ChatUsername, "chat_type": t.ChatType, "token": tok, "connected_at": t.ConnectedAt}
}

// channelsPublic — каналы Telegram без токенов; первый — канал по умолчанию.
func channelsPublic(c *Ctx) []jx.M {
	out := []jx.M{}
	for i, ch := range c.U.TelegramChannels() {
		out = append(out, jx.M{"id": ch.ID, "title": ch.ChatTitle, "username": ch.ChatUsername, "bot_username": ch.BotUsername, "type": ch.ChatType,
			"connected_at": ch.ConnectedAt, "default": i == 0, "schedule": posta.ChannelSchedule(ch), "style": c.U.ChannelStylePublic(ch), "hashtags": ch.Hashtags})
	}
	return out
}

func styleChannel(c *Ctx, v string) (*posta.Channel, error) {
	if v == "" || v == "main" {
		l := c.U.TelegramChannels()
		if len(l) == 0 {
			return nil, fail(409, "Telegram-канал не подключён.")
		}
		return l[0], nil
	}
	ch := c.U.Channel(v)
	if ch == nil {
		return nil, fail(409, "Такого канала нет.")
	}
	return ch, nil
}

// ---- VK ID (OAuth 2 + PKCE), одно подключение на сайт ----

func vkRedirect(c *Ctx, result string) (any, error) {
	if !contains([]string{"connected", "signed_in", "misconfigured", "error"}, result) {
		result = "error"
	}
	base := strings.TrimRight(c.A.Cfg.PublicURL, "/")
	if base == "" {
		base = strings.TrimRight(c.A.Cfg.BasePath, "/")
	}
	c.redirect(base + "/?vk=" + url.QueryEscape(result) + "#account")
	return nil, nil
}

func b64url(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func vkOAuthStart(c *Ctx) (any, error) {
	if !c.A.VKOAuthConfigured() {
		return vkRedirect(c, "misconfigured")
	}
	state, verifier := randUID()+randUID()[:16], posta.PKCEVerifier()
	c.A.KVSet(c.R.Context(), "vk_oauth:"+state, jx.M{"verifier": verifier, "uid": c.U.ID, "at": now()}, 10*time.Minute)
	sum := sha256.Sum256([]byte(verifier))
	q := url.Values{"response_type": {"code"}, "client_id": {c.A.Cfg.VKClientID}, "redirect_uri": {c.A.VKOAuthRedirectURI()}, "state": {state},
		"code_challenge": {b64url(sum[:])}, "code_challenge_method": {"S256"}, "lang_id": {"0"}}
	if c.A.Cfg.VKOAuthScope != "" {
		q.Set("scope", c.A.Cfg.VKOAuthScope)
	}
	c.redirect("https://id.vk.ru/authorize?" + q.Encode())
	return nil, nil
}

// VK ID OneTap uses a server-generated challenge bound to this browser session.
func vkLoginPrepare(c *Ctx) (any, error) {
	if !c.A.VKOAuthConfigured() || c.U == nil {
		return nil, fail(400, "VK ID не настроен.")
	}
	state, verifier := randUID()+randUID()[:16], posta.PKCEVerifier()
	sessionHash := sha256.Sum256([]byte(c.SID))
	if err := c.A.KVSet(c.R.Context(), "vk_login:"+state, jx.M{"verifier": verifier, "uid": c.U.ID, "session": b64url(sessionHash[:])}, 10*time.Minute); err != nil {
		return nil, err
	}
	challenge := sha256.Sum256([]byte(verifier))
	return jx.M{"app": c.A.Cfg.VKClientID, "redirect_url": c.A.VKOAuthRedirectURI(), "state": state, "code_challenge": b64url(challenge[:])}, nil
}

func vkLoginCallback(c *Ctx, cb map[string]string) (any, error) {
	ctx := c.R.Context()
	// Consume atomically: a code and state may only be used once.
	var raw []byte
	err := c.A.DB.Pool.QueryRow(ctx, `DELETE FROM kv WHERE key = $1 AND (expires_at = 0 OR expires_at > $2) RETURNING value`, "vk_login:"+cb["state"], now()).Scan(&raw)
	saved := jx.Unmarshal(raw)
	hash := sha256.Sum256([]byte(c.SID))
	if err != nil || c.U == nil || jx.Str(saved, "uid") != c.U.ID || jx.Str(saved, "session") != b64url(hash[:]) || cb["code"] == "" || cb["device_id"] == "" {
		return vkRedirect(c, "error")
	}
	token, err := c.A.VKOAuthToken(cb["code"], jx.Str(saved, "verifier"), cb["device_id"], cb["state"])
	if err != nil {
		return vkRedirect(c, "error")
	}
	vkID := jx.Str(token, "user_id")
	if !regexp.MustCompile(`^[1-9][0-9]{0,19}$`).MatchString(vkID) {
		return vkRedirect(c, "error")
	}
	var owner string
	err = c.A.DB.Pool.QueryRow(ctx, `SELECT id FROM users WHERE vk_id = $1`, vkID).Scan(&owner)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return vkRedirect(c, "error")
		}
		if !c.A.TBool("registration") && !c.A.UserHasIdentity(ctx, c.U.ID) {
			return vkRedirect(c, "error")
		}
		tag, e := c.A.DB.Pool.Exec(ctx, `UPDATE users SET vk_id = $1 WHERE id = $2 AND (vk_id IS NULL OR vk_id = $1)`, vkID, c.U.ID)
		if e != nil || tag.RowsAffected() != 1 {
			return vkRedirect(c, "error")
		}
		owner = c.U.ID
	}
	sid, err := c.A.LoginSwitch(ctx, c.U.ID, c.SID, owner)
	if err != nil {
		return vkRedirect(c, "error")
	}
	c.setSessionCookie(sid)
	return vkRedirect(c, "signed_in")
}

func vkOAuthCallback(c *Ctx) (any, error) {
	q := c.R.URL.Query()
	cb := map[string]string{"state": q.Get("state"), "code": q.Get("code"), "device_id": q.Get("device_id")}
	var payload map[string]any
	if json.Unmarshal([]byte(q.Get("payload")), &payload) == nil {
		for k := range cb {
			if v := jx.Str(payload, k); v != "" {
				cb[k] = v
			}
		}
	}
	ctx := c.R.Context()
	if posta.KV(c.A, ctx, "vk_login:"+cb["state"], jx.M(nil)) != nil {
		return vkLoginCallback(c, cb)
	}
	saved := posta.KV(c.A, ctx, "vk_oauth:"+cb["state"], jx.M(nil))
	c.A.KVDelete(ctx, "vk_oauth:"+cb["state"])
	if saved == nil || cb["code"] == "" || cb["device_id"] == "" || !c.A.VKOAuthConfigured() {
		return vkRedirect(c, "error")
	}
	uid := jx.Str(saved, "uid")
	if !posta.IsValidUserID(uid) {
		return vkRedirect(c, "error")
	}
	userCtx := c.A.User(ctx, uid, nil).Ctx
	if err := c.A.VKOAuthExchange(userCtx, cb["code"], jx.Str(saved, "verifier"), cb["device_id"], cb["state"]); err != nil {
		return vkRedirect(c, "error")
	}
	return vkRedirect(c, "connected")
}

// ---- Telegram: бот и канал ----

var (
	botTokenRe   = regexp.MustCompile(`^\d{5,12}:[A-Za-z0-9_-]{30,50}$`)
	channelRefRe = regexp.MustCompile(`^(@[A-Za-z][A-Za-z0-9_]{4,31}|-100\d{5,15})$`)
	tmePrefixRe  = regexp.MustCompile(`(?i)^(https?://)?t\.me/`)
)

func telegramConnect(c *Ctx) (any, error) {
	u := c.U
	channel := tmePrefixRe.ReplaceAllString(strings.TrimSpace(c.Str("channel")), "@")
	var token string
	if same := c.Str("bot"); same != "" {
		ch := u.Channel(same)
		if ch == nil || ch.Token == "" {
			return nil, fail(400, "Бот не найден: введите токен.")
		}
		token = ch.Token
	} else {
		token = strings.TrimSpace(c.Str("token"))
		if !botTokenRe.MatchString(token) {
			return nil, fail(400, "Токен бота выглядит иначе: 123456789:AA… — его выдаёт @BotFather.")
		}
	}
	if !channelRefRe.MatchString(channel) {
		return nil, fail(400, "Укажите канал как @имя_канала или числовой id вида -100…")
	}
	ctx := c.R.Context()
	call := func(method string, fields map[string]string) (jx.M, error) {
		raw, err := c.A.TelegramBotRequest(ctx, token, method, fields, nil, 20*time.Second, nil)
		if err != nil {
			return nil, fail(400, "Telegram: "+err.Error())
		}
		return jx.Unmarshal(raw), nil
	}
	bot, err := call("getMe", nil)
	if err != nil {
		return nil, err
	}
	chat, err := call("getChat", map[string]string{"chat_id": channel})
	if err != nil {
		return nil, err
	}
	chatID := jx.Int(chat, "id")
	member, err := call("getChatMember", map[string]string{"chat_id": strconv.FormatInt(chatID, 10), "user_id": strconv.FormatInt(jx.Int(bot, "id"), 10)})
	if err != nil {
		return nil, err
	}
	status := jx.Str(member, "status")
	if status != "creator" && !(status == "administrator" && (jx.Str(chat, "type") != "channel" || jx.Bool(member, "can_post_messages"))) {
		title := jx.Str(chat, "title")
		if title == "" {
			title = channel
		}
		return nil, fail(400, "Бот @"+jx.Str(bot, "username")+" не может публиковать в «"+title+"»: добавьте его администратором канала с правом публикации.")
	}
	if d := u.ChannelConnectDenied(chatID); d != "" {
		return nil, fail(409, d)
	}
	over := u.ChannelOverLimit(chatID)
	if _, err := u.ChannelSave(&posta.Channel{Platform: "telegram", Token: token, BotID: jx.Int(bot, "id"), BotUsername: jx.Str(bot, "username"), ChatID: chatID,
		ChatTitle: jx.Str(chat, "title"), ChatUsername: jx.Str(chat, "username"), ChatType: jx.Str(chat, "type"), Position: -1}); err != nil {
		return nil, err
	}
	return ok(jx.M{"channels": channelsPublic(c), "over_limit": over, "telegram": telegramPublic(c)}), nil
}

// ---- Публикация ----

func checkPublishTime(c *Ctx, at int64) error {
	if at < now()+int64(c.A.TInt("min_delay")) {
		return fail(400, "Отложенный пост нужно назначить минимум на минуту вперёд.")
	}
	if at > now()+config.TelegramMaxDelayDays*86400 {
		return fail(400, "Отложить пост можно не больше чем на "+strconv.Itoa(config.TelegramMaxDelayDays)+" дней.")
	}
	return nil
}

func publish(c *Ctx) (any, error) {
	u := c.U
	fields := c.In
	var uploads map[string]string
	if strings.HasPrefix(c.R.Header.Get("Content-Type"), "multipart/") {
		if err := c.R.ParseMultipartForm(64 << 20); err != nil {
			return nil, fail(413, "Файлы вместе весят слишком много для одной отправки: уберите часть файлов.")
		}
		defer c.R.MultipartForm.RemoveAll()
		fields = jx.M{}
		for k, v := range c.R.MultipartForm.Value {
			if len(v) > 0 {
				fields[k] = v[0]
			}
		}
		uploads = map[string]string{}
		for i, fh := range c.R.MultipartForm.File["files[]"] {
			if fh.Size > config.TelegramUploadMaxBytes {
				return nil, fail(400, "Файл «"+fh.Filename+"» больше "+strconv.Itoa(config.TelegramUploadMaxBytes>>20)+" МБ.")
			}
			f, err := fh.Open()
			if err != nil {
				return nil, fail(400, "Не удалось загрузить файл «"+fh.Filename+"».")
			}
			tmp, _ := os.CreateTemp("", "posta-pub-*"+filepath.Ext(fh.Filename))
			io.Copy(tmp, f)
			f.Close()
			tmp.Close()
			defer os.Remove(tmp.Name())
			uploads[strconv.Itoa(i)] = tmp.Name()
		}
		for i, fh := range c.R.MultipartForm.File["files"] {
			f, err := fh.Open()
			if err != nil {
				continue
			}
			tmp, _ := os.CreateTemp("", "posta-pub-*"+filepath.Ext(fh.Filename))
			io.Copy(tmp, f)
			f.Close()
			tmp.Close()
			defer os.Remove(tmp.Name())
			uploads[strconv.Itoa(i)] = tmp.Name()
		}
	}
	ch, err := u.PublishChannelResolve(jx.Str(fields, "channel"))
	if err != nil {
		return nil, err
	}
	text := posta.PostCaptionClean(jx.Str(fields, "text"))
	var rawItems []jx.M
	if s, isStr := fields["media"].(string); isStr && s != "" {
		if json.Unmarshal([]byte(s), &rawItems) != nil {
			return nil, fail(400, "Некорректный список медиа.")
		}
	} else {
		rawItems = jx.Maps(fields, "media")
	}
	if len(rawItems) > config.TelegramMaxMedia+1 {
		rawItems = rawItems[:config.TelegramMaxMedia+1]
	}
	if d := u.PublishMediaDenied(rawItems); d != "" {
		return nil, fail(422, d)
	}
	var items []posta.MediaItem
	var kinds []string
	for _, it := range rawItems {
		up := ""
		if v, has := it["upload"]; has && v != nil {
			up = jx.ToStr(v)
		}
		items = append(items, posta.MediaItem{Kind: jx.Str(it, "kind"), Src: jx.Str(it, "src"), Name: jx.Str(it, "name"), Upload: up})
		kinds = append(kinds, jx.Str(it, "kind"))
	}
	at := jx.Int(fields, "scheduled_ts")
	mode := jx.Str(fields, "mode")
	if mode == "" {
		mode = "now"
		if at != 0 {
			mode = "native"
		}
	}
	if !contains([]string{"now", "native", "queue", "slot"}, mode) {
		return nil, fail(400, "Неизвестный режим публикации.")
	}
	var poll jx.M
	switch p := fields["poll"].(type) {
	case string:
		if p != "" {
			if poll = jx.Unmarshal([]byte(p)); poll == nil {
				return nil, fail(400, "Некорректный опрос.")
			}
		}
	case map[string]any:
		poll = p
	}
	var record jx.M
	switch r := fields["record"].(type) {
	case string:
		record = jx.Unmarshal([]byte(r))
	case map[string]any:
		record = r
	}
	recordKey := ""
	if record != nil {
		recordKey = posta.ContentItemKey(record)
	}
	if poll != nil && mode == "native" {
		return nil, fail(422, "Опросы ставятся в очередь Posta или публикуются сразу: через «Отложенные» Telegram опросы пока не ставятся.")
	}
	if poll != nil && len(items) > 0 {
		return nil, fail(422, "В опросе не бывает медиа: уберите файлы.")
	}
	platform := ch.Platform
	var preSkipped []string
	if platform != "telegram" {
		n, _ := c.A.NetworkFor(platform)
		title := platform
		if n != nil {
			title = n.Title
		}
		if mode == "native" || poll != nil {
			return nil, fail(422, title+": отложка средствами соцсети и опросы пока только в Telegram.")
		}
		_, skipped, err := c.A.NetworkPreflight(platform, text, kinds)
		if err != nil {
			return nil, fail(422, err.Error())
		}
		preSkipped = skipped
		if len(skipped) > 0 && !jx.Truthy(fields["confirm_skip"]) {
			return Resp{409, jx.M{"status": "error", "confirm_skip": true, "skipped": skipped, "message": "Не все файлы уйдут в " + title + ": " + strings.Join(skipped, "; ") + ". Опубликовать без них?"}}, nil
		}
	}
	if mode == "now" {
		at = 0
	}
	if (mode == "native" || mode == "queue") && at == 0 {
		return nil, fail(400, "Укажите время публикации.")
	}
	var slotAt *int64
	if mode == "slot" {
		if len(jx.Map(posta.ChannelSchedule(ch), "slots")) == 0 {
			return nil, fail(422, "У канала не настроено расписание: «Соцсети» → канал → «Расписание».")
		}
		s := c.A.ChannelNextSlot(ch, now(), u.Outbox())
		if s == nil {
			return nil, fail(422, "В расписании канала нет свободного слота в ближайшие 60 дней.")
		}
		at = jx.Int(s, "publish_at")
		sa := jx.Int(s, "slot_at")
		slotAt = &sa
	}
	if at != 0 {
		if err := checkPublishTime(c, at); err != nil {
			return nil, err
		}
	}
	if mode == "native" && !u.TelegramUserConnected() {
		return nil, fail(422, "Отложенные посты ставятся средствами Telegram: подключите свой аккаунт в «Личном кабинете» → «Соцсети» → Telegram. Без отложки пост можно опубликовать сразу. Или выберите «Через очередь Posta».")
	}
	if d := u.BillingPostDenied(at != 0); d != "" {
		return nil, fail(429, d)
	}
	// дальше — долгая работа: обрыв соединения браузером её не прерывает
	w, cancel := c.userDetached(15 * time.Minute)
	defer cancel()
	progressID := jx.Str(fields, "progress_id")
	progress := func(stage string, p *float64) { posta.PublishProgressSet(progressID, stage, p) }
	work, err := posta.TelegramWorkDir()
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	var response jx.M
	if poll != nil {
		if poll, err = posta.TelegramPollCheck(poll); err != nil {
			return nil, err
		}
		text = jx.Str(poll, "question")
	} else {
		limit := config.TelegramTextLimit
		if len(items) > 0 {
			limit = config.TelegramCaptionLimit
		}
		if platform != "telegram" {
			if n, _ := c.A.NetworkFor(platform); n != nil {
				limit = posta.NetworkTextLimit(n, len(items) > 0)
			}
		}
		text = posta.TextWithHashtags(text, w.ChannelHashtags(ch, posta.PublishHashtagsInput(fields["hashtags"])), limit)
		if platform == "telegram" {
			if err := posta.TelegramCheckText(text, len(items)); err != nil {
				return nil, err
			}
		}
	}
	route := "bot"
	if mode == "native" {
		route = "account"
	}
	prepared, err := w.TelegramPrepareMedia(items, uploads, work, route, progress)
	if err != nil {
		return nil, fail(422, err.Error())
	}
	switch mode {
	case "queue", "slot":
		r, err := w.OutboxQueuePost(ch, text, prepared, at, slotAt, work, poll, posta.PublishArticleInput(fields["article"]), "", recordKey)
		if err != nil {
			return nil, fail(422, err.Error())
		}
		response = r
		if platform != "telegram" {
			response["skipped"] = preSkipped
		}
		w.KickPublisher()
	case "native":
		ids, err := w.TelegramUserSchedule(ch, text, prepared, at, progress)
		if err != nil {
			return nil, fail(422, err.Error())
		}
		response = jx.M{"scheduled": true, "id": w.TelegramRecordNativePost(text, prepared, at, ids, ch.ID), "publish_at": at, "native": true}
	default:
		post := jx.M{"text": text, "media": posta.PreparedMaps(prepared)}
		if poll != nil {
			post["poll"] = poll
		}
		var sent *posta.SentResult
		if platform == "telegram" && poll == nil {
			sent, err = w.TelegramPublishPrepared(w.Ctx, ch, text, prepared, progress)
		} else {
			sent, err = w.OutboxSendPost(w.Ctx, w.ID, post, ch)
		}
		if err != nil {
			return nil, fail(422, err.Error())
		}
		shown := sent.MediaKept
		if shown == nil {
			shown = prepared
		}
		kind, dur := "", 0
		if len(shown) > 0 {
			kind, dur = shown[0].Kind, shown[0].Duration
		}
		skipped := sent.Skipped
		if skipped == nil {
			skipped = preSkipped
		}
		w.TelegramLogPublished(jx.Merge(posta.TelegramPostSummary(text, len(shown)), jx.M{"at": now(), "scheduled_for": 0, "url": sent.URL, "status": "ok", "error": "",
			"media_src": posta.MediaSourcesOf(posta.PreparedMaps(shown)), "channel": ch.ID, "channel_title": ch.ChatTitle, "kind": kind, "duration": dur,
			"thumb_id": w.PostThumbMake(posta.PreparedMaps(shown)), "skipped": orStrs(skipped)}))
		response = jx.M{"scheduled": false, "url": sent.URL, "messages": len(sent.MessageIDs), "skipped": orStrs(skipped)}
	}
	w.BillingPostUsed()
	if recordKey != "" {
		response["marked"] = nilIfEmpty(w.ContentMarkPosted(recordKey))
	}
	return ok(response), nil
}

func orStrs(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func telegramRetry(c *Ctx) (any, error) {
	id := c.Str("id")
	if !postIDRe.MatchString(id) {
		return nil, fail(400, "Некорректный номер поста.")
	}
	found := false
	c.U.OutboxUpdate(func(q []jx.M) []jx.M {
		for _, e := range q {
			if jx.Str(e, "id") == id && jx.Str(e, "status") == "failed" {
				found = true
				e["status"], e["attempts"], e["error"], e["next_try_at"] = "queued", 0, "", 0
				e["publish_at"] = max(jx.Int(e, "publish_at"), now()+60)
			}
		}
		return q
	})
	if !found {
		return nil, fail(404, "Этого поста нет среди неудачных.")
	}
	c.U.KickPublisher()
	return ok(nil), nil
}

func telegramReschedule(c *Ctx) (any, error) {
	u := c.U
	id := c.Str("id")
	if !postIDRe.MatchString(id) {
		return nil, fail(400, "Некорректный номер поста.")
	}
	queue := u.Outbox()
	var post jx.M
	for _, e := range queue {
		if jx.Str(e, "id") == id {
			post = e
		}
	}
	if post == nil {
		return nil, fail(404, "Такого поста нет.")
	}
	if jx.Str(post, "status") != "queued" {
		return nil, fail(409, "Перенести можно только пост, который ждёт публикации.")
	}
	group := jx.Str(post, "group")
	var members []jx.M
	for _, e := range queue {
		if (group != "" && jx.Str(e, "group") == group && jx.Str(e, "status") == "queued") || (group == "" && jx.Str(e, "id") == id) {
			members = append(members, e)
		}
	}
	type move struct {
		at   int64
		slot *int64
	}
	plan := map[string]move{}
	if jx.Bool(c.In, "slot") {
		for _, m := range members {
			ch := u.TelegramConnection(jx.Str(m, "channel"))
			if ch == nil {
				ch = u.Channel(jx.Str(m, "channel"))
			}
			if ch == nil {
				return nil, fail(404, "Канал не найден: обновите страницу.")
			}
			s := c.A.ChannelNextSlot(ch, now(), u.Outbox())
			if s == nil {
				return nil, fail(422, "В расписании канала нет свободного слота в ближайшие 60 дней.")
			}
			sa := jx.Int(s, "slot_at")
			plan[jx.Str(m, "id")] = move{jx.Int(s, "publish_at"), &sa}
		}
	} else {
		at := jx.Int(c.In, "publish_at")
		if err := checkPublishTime(c, at); err != nil {
			return nil, err
		}
		for _, m := range members {
			plan[jx.Str(m, "id")] = move{at, nil}
		}
	}
	if d := u.BillingPostDenied(true); d != "" {
		return nil, fail(429, d)
	}
	u.OutboxUpdate(func(q []jx.M) []jx.M {
		for _, e := range q {
			if m, has := plan[jx.Str(e, "id")]; has && jx.Str(e, "status") == "queued" {
				e["publish_at"], e["slot"] = m.at, m.slot != nil
				if m.slot != nil {
					e["slot_at"] = *m.slot
				} else {
					e["slot_at"] = nil
				}
			}
		}
		return q
	})
	return ok(jx.M{"publish_at": plan[id].at, "slot": plan[id].slot != nil, "moved": len(plan)}), nil
}

func telegramQueue(c *Ctx) (any, error) {
	u := c.U
	bot := u.TelegramConnection("")
	u.TelegramSettleNativePosts()
	scheduled := []jx.M{}
	for _, e := range u.Outbox() {
		scheduled = append(scheduled, jx.Merge(jx.Merge(posta.TelegramPostSummary(jx.Str(e, "text"), len(jx.List(e, "media"))), u.TelegramChannelFields(jx.Str(e, "channel"))),
			jx.M{"id": e["id"], "publish_at": jx.Int(e, "publish_at"), "status": e["status"], "attempts": jx.Int(e, "attempts"), "error": jx.Str(e, "error")}))
	}
	sort.SliceStable(scheduled, func(i, j int) bool { return jx.Int(scheduled[i], "publish_at") < jx.Int(scheduled[j], "publish_at") })
	var channel any
	if bot != nil {
		channel = jx.M{"title": bot.ChatTitle, "username": bot.ChatUsername}
	}
	return jx.M{"connected": bot != nil, "native_account": u.TelegramUserConnected(), "channel": channel, "channels": channelsPublic(c), "scheduled": scheduled,
		"published": orMaps(u.Published(10))}, nil
}

func orMaps(l []jx.M) []jx.M {
	if l == nil {
		return []jx.M{}
	}
	return l
}

func thumbFields(e jx.M) jx.M {
	if id := jx.Str(e, "thumb_id"); id != "" {
		return jx.M{"thumb": true, "thumb_url": "index.php?" + url.Values{"action": {"post_thumb"}, "id": {id}}.Encode()}
	}
	media := jx.Maps(e, "media")
	if jx.Str(e, "status") != "telegram" && jx.Str(e, "id") != "" && len(media) > 0 && jx.Str(media[0], "kind") == "photo" {
		return jx.M{"thumb": true, "thumb_url": "index.php?" + url.Values{"action": {"outbox_media"}, "id": {jx.Str(e, "id")}}.Encode()}
	}
	return jx.M{"thumb": false, "thumb_url": ""}
}

func calendar(c *Ctx) (any, error) {
	u := c.U
	bot := u.TelegramConnection("")
	u.TelegramSettleNativePosts()
	published := u.CalendarPublishedRecent()
	hashes := map[string]string{}
	var channels []jx.M
	byID := map[string]jx.M{}
	chans := u.TelegramChannels()
	for i, ch := range channelsPublic(c) {
		ch["reach"] = c.A.ChannelReachCached(chans[i])
		channels = append(channels, ch)
		byID[jx.Str(ch, "id")] = ch
	}
	posts := []jx.M{}
	for _, e := range u.Outbox() {
		media := jx.Maps(e, "media")
		kind, dur, src := "", int64(0), ""
		if len(media) > 0 {
			kind, dur, src = jx.Str(media[0], "kind"), jx.Int(media[0], "duration"), jx.Str(media[0], "src")
		}
		var forecast any
		if ch := byID[jx.Str(e, "channel")]; ch != nil {
			if r, isReach := ch["reach"].(*posta.Reach); isReach {
				if f := posta.ChannelForecast(r, jx.Int(e, "publish_at"), jx.Str(jx.Map(ch, "schedule"), "tz")); f != nil {
					forecast = *f
				}
			}
		}
		p := jx.Merge(jx.Merge(posta.TelegramPostSummary(jx.Str(e, "text"), len(media)), u.TelegramChannelFields(jx.Str(e, "channel"))), jx.M{"id": e["id"],
			"status": e["status"], "error": jx.Str(e, "error"), "full_text": trimRunes(strings.TrimSpace(jx.Str(e, "text")), 600), "article": e["article"],
			"group": jx.Str(e, "group"), "kind": kind, "duration": dur, "poll": jx.Has(e, "poll") && e["poll"] != nil, "at": jx.Int(e, "publish_at"), "forecast": forecast,
			"image_key": src, "duplicate": c.A.CalendarDuplicateOf(jx.M{"image": src, "text": jx.Str(e, "text")}, jx.Str(e, "channel"), published, hashes)})
		posts = append(posts, jx.Merge(p, thumbFields(e)))
	}
	sent := u.Published(200)
	var sentPosts []jx.M
	for _, e := range sent {
		at := jx.Int(e, "scheduled_for")
		if at == 0 {
			at = jx.Int(e, "at")
		}
		status := "sent"
		if s := jx.Str(e, "status"); s != "" && s != "ok" {
			status = "failed"
		}
		sentPosts = append(sentPosts, jx.Merge(jx.Merge(posta.TelegramPostSummary(jx.Str(e, "text"), int(jx.Int(e, "media"))), jx.M{"id": "", "at": at, "status": status,
			"error": jx.Str(e, "error"), "url": jx.Str(e, "url"), "channel": jx.Str(e, "channel"), "channel_title": jx.Str(e, "channel_title"), "full_text": jx.Str(e, "text"),
			"kind": jx.Str(e, "kind"), "duration": jx.Int(e, "duration"), "poll": false}), thumbFields(e)))
	}
	pending := false
	external := c.A.CalendarExternalPosts(chans, u.Published(500), &pending)
	all := append(append(external, sentPosts...), posts...)
	var hint any
	if h := jx.Map(u.Settings(), "hints"); h != nil {
		hint = h["calendar_v3"]
	}
	if channels == nil {
		channels = []jx.M{}
	}
	_ = bot
	return jx.M{"connected": bot != nil, "now": now(), "channels": channels, "posts": orMaps(all), "history_pending": pending, "hints_seen": hint}, nil
}

func trimRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

func calendarFillPlan(c *Ctx) (any, error) {
	from := jx.Int(c.In, "from")
	if from == 0 {
		from = now()
	}
	to := jx.Int(c.In, "to")
	if to == 0 {
		to = now() + 7*86400
	}
	if to <= from || to-from > 31*86400 {
		return nil, fail(400, "Период плана — не больше 31 дня.")
	}
	wanted := jx.Strs(c.In, "channels")
	var chans []*posta.Channel
	for _, ch := range c.U.TelegramChannels() {
		if len(wanted) == 0 || contains(wanted, ch.ID) {
			chans = append(chans, ch)
		}
	}
	plan := c.U.CalendarFillPlan(chans, from, to, jx.Bool(c.In, "include_rest"), c.U.ProposalItems(c.U.History()), c.U.Outbox())
	return ok(jx.M{"plan": plan}), nil
}

func telegramCancelOne(c *Ctx, id string) error {
	u := c.U
	var q jx.M
	for _, e := range u.Outbox() {
		if jx.Str(e, "id") == id {
			q = e
		}
	}
	switch jx.Str(q, "status") {
	case "sending":
		return fail(409, "Пост уже отправляется — отменить нельзя.")
	case "queued", "failed":
		if !u.OutboxRemovePost(id) {
			return fail(404, "Этого поста уже нет в очереди.")
		}
		return nil
	case "telegram":
		if ch := u.Channel(jx.Str(q, "channel")); ch != nil {
			var ids []int
			for _, v := range jx.List(q, "tg_ids") {
				ids = append(ids, int(jx.ToInt(v)))
			}
			if err := u.TelegramUserCancel(ch, ids); err != nil {
				return fail(502, "Не удалось убрать пост из отложенных в Telegram: "+err.Error()+" Удалите его в канале вручную («Отложенные»).")
			}
		}
	}
	if !u.TelegramUnqueuePost(id) {
		return fail(404, "Этого поста уже нет в очереди: возможно, он отправлен.")
	}
	return nil
}

func telegramCancel(c *Ctx) (any, error) {
	u := c.U
	id := c.Str("id")
	if !postIDRe.MatchString(id) {
		return nil, fail(400, "Некорректный номер поста.")
	}
	queue := u.Outbox()
	group := ""
	for _, e := range queue {
		if jx.Str(e, "id") == id {
			group = jx.Str(e, "group")
		}
	}
	ids := []string{id}
	if !jx.Bool(c.In, "only") && group != "" {
		ids = nil
		for _, e := range queue {
			if jx.Str(e, "group") == group {
				ids = append(ids, jx.Str(e, "id"))
			}
		}
	}
	var records []string
	for _, e := range queue {
		if contains(ids, jx.Str(e, "id")) && jx.Str(e, "record") != "" && !contains(records, jx.Str(e, "record")) {
			records = append(records, jx.Str(e, "record"))
		}
	}
	cancelled := 0
	errs := []jx.M{}
	for _, one := range ids {
		if err := telegramCancelOne(c, one); err != nil {
			if one == id {
				return nil, err
			}
			errs = append(errs, jx.M{"id": one, "error": err.Error()})
			continue
		}
		cancelled++
	}
	for _, key := range records {
		still := false
		for _, e := range u.Outbox() {
			if jx.Str(e, "record") == key {
				still = true
			}
		}
		if !still {
			u.ContentUnmarkPosted(key)
		}
	}
	return ok(jx.M{"cancelled": cancelled, "errors": errs}), nil
}

// ---- Панель модели ----

func modelStatus(c *Ctx) (any, error) {
	ctx, cancel := context.WithTimeout(c.R.Context(), 4*time.Second)
	defer cancel()
	ol := c.A.LLM.Ollama
	version := ol.Version(ctx)
	loaded := []jx.M{}
	if version != "" {
		for _, m := range ol.Loaded(ctx) {
			size, vram := m.Size, m.SizeVRAM
			pct := 0
			if size > 0 {
				pct = int(100 * vram / size)
			}
			loaded = append(loaded, jx.M{"name": m.Name, "size_mb": size >> 20, "vram_mb": vram >> 20, "gpu_percent": pct, "context": m.Context, "expires_at": m.ExpiresAt})
		}
	}
	s := c.A.LLMSettings()
	var ver any
	if version != "" {
		ver = version
	}
	return jx.M{"ollama": version != "", "version": ver, "loaded": loaded, "roles": jx.M{"prescreen": s.Roles["prescreen"].Primary.Model, "analysis": s.Roles["analysis"].Primary.Model},
		"gpu": nil, "parser_running": c.A.ParserRunning(c.U.ID), "llm": jx.M{"preset": s.Preset, "spent_today": c.A.LLMSpentToday(), "free_used": c.A.LLM.FreeUsed()}}, nil
}

func modelControl(c *Ctx) (any, error) {
	started := time.Now()
	ctx := c.R.Context()
	ol := c.A.LLM.Ollama
	s := c.A.LLMSettings()
	var loaded []string
	for _, m := range ol.Loaded(ctx) {
		loaded = append(loaded, m.Name)
	}
	var msg string
	switch c.Str("op") {
	case "load":
		rc, known := s.Roles[llmRole(c.Str("role"))]
		if !known || rc.Primary.Provider != "ollama" {
			return nil, fail(400, "Эта роль работает не на Ollama.")
		}
		if err := ol.Load(ctx, rc.Primary.Model); err != nil {
			return nil, fail(502, err.Error())
		}
		msg = "Загружена " + rc.Primary.Model
	case "unload":
		m := c.Str("model")
		if !contains(loaded, m) {
			return nil, fail(400, "Эта модель сейчас не загружена.")
		}
		ol.Unload(ctx, m)
		msg = "Выгружена " + m
	case "unload_all", "restart":
		for _, m := range loaded {
			ol.Unload(ctx, m)
		}
		msg = "Загруженных моделей не было"
		if len(loaded) > 0 {
			msg = "Выгружены: " + strings.Join(loaded, ", ")
		}
		if c.Str("op") == "restart" {
			target := s.Roles["prescreen"].Primary.Model
			if len(loaded) > 0 {
				target = loaded[0]
			}
			if err := ol.Load(ctx, target); err != nil {
				return nil, fail(502, err.Error())
			}
			msg = "Перезапущена " + target
		}
	default:
		return nil, fail(400, "Неизвестное действие.")
	}
	return ok(jx.M{"message": msg + " за " + strconv.FormatFloat(time.Since(started).Seconds(), 'f', 1, 64) + " с."}), nil
}

// ---- Тариф ----

func billingInfo(c *Ctx) (any, error) {
	u := c.U
	ctx := c.R.Context()
	limits := u.BillingLimits()
	plans := []jx.M{}
	once := u.BillingOnceAvailable()
	for _, p := range config.BillingPlans {
		plans = append(plans, jx.M{"id": p.Key, "title": p.Title, "tag": p.Tag, "price": p.Price, "channels": p.Channels, "posts_per_day": p.PostsPerDay, "schedule": p.Schedule,
			"once": p.Once, "posts_per_hour": posta.BillingHourLimit(p.PostsPerDay), "days": posta.PlanDays(p), "available": !p.Once || once})
	}
	code, _ := u.ReferralCode()
	var refBy string
	c.A.DB.Pool.QueryRow(ctx, `SELECT referred_by FROM users WHERE id = $1`, u.ID).Scan(&refBy)
	ref := jx.Merge(u.ReferralStats(), jx.M{"code": code, "percent": config.ReferralPercent, "bound": refBy != ""})
	cu := config.BillingCustom
	return jx.M{"balance": posta.KopToRub(c.A.BalanceKop(ctx, c.A.DB.Pool, u.ID)), "plan": limits, "enforce": c.A.TBool("billing_enforce"), "period_days": config.BillingPeriodDays,
		"usage": jx.M{"hour": u.BillingPostsSince(now() - 3600), "day": u.BillingPostsSince(now() - 86400)}, "plans": plans,
		"custom": jx.M{"base": cu.Base, "channel": cu.Channel, "posts_step": cu.PostsStep, "posts_step_price": cu.PostsStepPrice, "max_channels": cu.MaxChannels,
			"max_posts_per_day": cu.MaxPostsPerDay, "hour_share": config.BillingHourShare}, "referral": ref, "ledger": u.BillingPublic()["ledger"]}, nil
}

// ---- API: пост в подборку по токену ----

var bearerRe = regexp.MustCompile(`(?i)^Bearer\s+(\S+)$`)

func apiAddPost(c *Ctx) func() (any, error) {
	return func() (any, error) {
		if c.R.Method != "POST" {
			return nil, fail(405, "Метод не поддерживается.")
		}
		m := bearerRe.FindStringSubmatch(strings.TrimSpace(c.R.Header.Get("Authorization")))
		uid := ""
		if m != nil {
			uid = c.A.APITokenUser(c.R.Context(), m[1])
		}
		if uid == "" {
			return nil, fail(401, "Неверный токен.")
		}
		ctx, cancel := context.WithTimeout(context.WithoutCancel(c.R.Context()), time.Minute)
		defer cancel()
		u := c.A.User(ctx, uid, nil)
		if !u.APIRateTake() {
			return nil, fail(429, "Слишком много запросов: не больше 60 в час.")
		}
		var in jx.M
		body, _ := io.ReadAll(io.LimitReader(c.R.Body, 1<<20))
		if json.Unmarshal(body, &in) != nil || in == nil {
			return nil, fail(400, "Тело запроса должно быть JSON-объектом.")
		}
		r, err := u.APIAddPost(in)
		if err != nil {
			return nil, err
		}
		return ok(r), nil
	}
}

func llmRole(s string) llm.Role { return llm.Role(s) }
