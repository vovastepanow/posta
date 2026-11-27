package posta

import (
	"context"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

// Мелкие операции, которые нужны обработчикам API.

func (a *App) ParserPhaseText(uid string) string { return a.parserPhaseOf(uid) }

func (u *U) SetAdultAllowed(on bool) error {
	_, err := u.DB.Pool.Exec(u.Ctx, `UPDATE users SET adult_allowed = $1 WHERE id = $2`, on, u.ID)
	return err
}

// VoteEvent — оценка записи для ClickHouse.
func VoteEvent(uid, topic, vote string, reason any, r jx.M) ch.Vote {
	an := jx.Map(r, "analysis")
	return ch.Vote{TS: time.Now(), UserID: uid, Topic: topic, Vote: vote, Reason: jx.ToStr(reason), SourceKey: jx.Str(r, "source_key"),
		Platform: ContentPlatform(r), Relevance: int(jx.Int(r, "relevance")), CardScore: int(jx.Int(r, "rating")), Format: jx.Str(an, "format"),
		Tags: jx.Strs(an, "tags"), ImageKey: ContentImageKey(r)}
}

var sqlViewTables = map[string]string{
	"analysis_cache":  `SELECT * FROM analysis_cache ORDER BY created_at DESC LIMIT 500`,
	"kv":              `SELECT key, value, expires_at, updated_at FROM kv ORDER BY key`,
	"outbox":          `SELECT * FROM outbox WHERE user_id = $1 ORDER BY publish_at`,
	"published":       `SELECT * FROM published WHERE user_id = $1 ORDER BY id DESC LIMIT 500`,
	"screened_cache":  `SELECT * FROM screened_cache ORDER BY 1 LIMIT 500`,
	"topics":          `SELECT * FROM topics ORDER BY 1`,
	"users":           `SELECT * FROM users WHERE id = $1`,
	"ledger":          `SELECT * FROM ledger WHERE user_id = $1 ORDER BY id DESC`,
	"subscriptions":   `SELECT * FROM subscriptions WHERE user_id = $1`,
	"post_usage":      `SELECT * FROM post_usage WHERE user_id = $1 ORDER BY at DESC`,
	"channels":        `SELECT * FROM channels WHERE user_id = $1 ORDER BY position`,
	"mirrors":         `SELECT * FROM mirrors WHERE user_id = $1 ORDER BY created_at`,
	"source_verdicts": `SELECT * FROM source_verdicts ORDER BY 1 LIMIT 500`,
}

// SQLTableRows — строки служебной таблицы для просмотра (только чтение; свои строки, если таблица по пользователям).
func (u *U) SQLTableRows(table string) ([]jx.M, error) {
	switch table {
	case "analysis_cache", "screened_cache", "kv", "source_verdicts":
		if !u.IsOwner(u.Ctx, u.ID) {
			return nil, Bad("Служебная таблица недоступна.")
		}
	}
	q, known := sqlViewTables[table]
	if !known {
		return nil, Bad("Неизвестная таблица.")
	}
	var args []any
	for _, t := range []string{"outbox", "published", "users", "ledger", "subscriptions", "post_usage", "channels", "mirrors"} {
		if t == table {
			args = []any{u.ID}
		}
	}
	rows, err := db.All(u.Ctx, u.DB.Pool, q, args...)
	if err != nil {
		return nil, err
	}
	out := make([]jx.M, 0, len(rows))
	for _, r := range rows {
		out = append(out, jx.Normalize(r).(map[string]any))
	}
	return out, nil
}

func (u *U) TopicFormatOverridden(topic string) bool { return u.topicFormatOverride(topic) != nil }

// TopicFormatSave — правка формата темы этим пользователем; reset или пустой формат — вернуть формат темы.
func (u *U) TopicFormatSave(topic string, in jx.M, reset bool) error {
	var f TopicFormat
	jsonRoundTrip(in, &f)
	clean := cleanFormat(&f)
	s := u.settingsRaw(u.Ctx, u.ID)
	formats := jx.Map(s, "topic_formats")
	if formats == nil {
		formats = jx.M{}
	}
	if reset || clean == nil {
		delete(formats, TopicNorm(topic))
	} else {
		formats[TopicNorm(topic)] = clean
	}
	return u.SaveSettings(jx.M{"topic_formats": formats})
}

// ArticleURLIsOwn — адрес из записей пользователя (пул или история): читалка не открывает чужие страницы.
func (u *U) ArticleURLIsOwn(link string) bool {
	if len(link) < 9 || link[:8] != "https://" {
		return false
	}
	for _, r := range append(u.Pool(), u.History().All()...) {
		if jx.Str(r, "url") == link {
			return true
		}
	}
	return false
}

var _ = config.DaySeconds

// SourceOverrideSet — включить или выключить источник для текущей темы у этого пользователя (общий каталог не трогаем).
func sourceExcludedByOverride(overrides jx.M, sourceID string) bool {
	if value := jx.Str(overrides, sourceID); value != "" {
		return value == "off"
	}
	for key, value := range overrides {
		if sourceIDMatchesLegacy(key, sourceID) && jx.ToStr(value) == "off" {
			return true
		}
	}
	return false
}

func (u *U) sourceOverridesForTopic(topic string) jx.M {
	settings := u.Settings()
	legacy := jx.Map(jx.Map(settings, "source_overrides"), TopicNorm(topic))
	exact := jx.Map(jx.Map(settings, "source_overrides_v2"), autopilotTopicKey(topic))
	combined := jx.M{}
	for id, value := range legacy {
		combined[id] = value
	}
	for id, value := range exact {
		combined[id] = value
	}
	return combined
}

func (u *U) SourceOverrideSet(sid string, on bool) error {
	topic := autopilotTopicKey(u.CurrentTopic())
	return u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		var raw []byte
		if err := tx.QueryRow(u.Ctx, `SELECT settings FROM users WHERE id=$1 FOR UPDATE`, u.ID).Scan(&raw); err != nil {
			return err
		}
		s := jx.Unmarshal(raw)
		if s == nil {
			s = jx.M{}
		}
		all := jx.Map(s, "source_overrides_v2")
		if all == nil {
			all = jx.M{}
		}
		m := jx.Map(all, topic)
		if m == nil {
			m = jx.M{}
		}
		m[sid] = map[bool]string{true: "on", false: "off"}[on]
		all[topic] = m
		s["source_overrides_v2"] = all
		_, err := tx.Exec(u.Ctx, `UPDATE users SET settings=$2 WHERE id=$1`, u.ID, db.JSON(s))
		return err
	})
}

func (u *U) BillingOnceAvailable() bool { return u.billingOnceAvailable(u.Ctx, u.DB.Pool, u.ID) }

func NetworkTextLimit(n *Network, withMedia bool) int { return n.textLimit(withMedia) }

// VKTokensPublic — подключён ли VK (без токенов).
func (a *App) VKTokensPublic(ctx context.Context) jx.M {
	t := a.vkTokens(ctx)
	return jx.M{"connected": jx.Str(t, "refresh_token") != "", "user_id": t["user_id"]}
}

// VKOAuthExchange — код VK ID → токены (OAuth 2 + PKCE).
func (a *App) VKOAuthToken(code, verifier, device, state string) (jx.M, error) {
	p := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "client_id": {a.Cfg.VKClientID},
		"device_id": {device}, "redirect_uri": {a.VKOAuthRedirectURI()}, "state": {state}}
	if a.Cfg.VKServiceToken != "" {
		p.Set("service_token", a.Cfg.VKServiceToken)
	}
	t, err := a.vkOAuthPost(p)
	if err != nil {
		return nil, err
	}
	if jx.Str(t, "access_token") == "" || jx.Str(t, "refresh_token") == "" {
		return nil, Bad("VK ID не вернул access_token и refresh_token.")
	}
	t["device_id"], t["expires_at"] = device, now()+max(0, jx.Int(t, "expires_in"))
	return t, nil
}

func (a *App) VKOAuthExchange(ctx context.Context, code, verifier, device, state string) error {
	t, err := a.VKOAuthToken(code, verifier, device, state)
	if err != nil {
		return err
	}
	a.SaveVKTokens(ctx, t)
	return nil
}
