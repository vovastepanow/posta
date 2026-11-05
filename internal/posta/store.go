package posta

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/secret"
)

func randHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// ensureUser — строка пользователя есть (анонимный профиль пишется в базу при первом осмысленном действии).
func ensureUser(ctx context.Context, q db.Querier, uid string) error {
	_, err := q.Exec(ctx, `INSERT INTO users (id, created_at) VALUES ($1, $2) ON CONFLICT (id) DO NOTHING`, uid, now())
	return err
}

// UserExists — есть ли профиль в базе.
func (a *App) UserExists(ctx context.Context, uid string) bool {
	ok, _ := db.Exists(ctx, a.DB.Pool, `SELECT 1 FROM users WHERE id = $1`, uid)
	return ok
}

// ---- Настройки ----

var ContentFilters = []string{"fresh_only", "active_sources_only", "video_only", "media_only", "media_no_text", "auto_translate"}

func DefaultSettings() jx.M {
	return jx.M{"topic": config.DefaultTopic, "topic2": "", "query": "", "fresh_only": false, "active_sources_only": true, "video_only": false,
		"media_only": false, "media_no_text": false, "auto_translate": true}
}

func NormalizeTopic(topic string) string {
	return trimRunes(collapseSpaces(topic), config.TopicMaxLength)
}

// NormalizeQuery — уточнение к теме: без кавычек и «+», пробелы схлопнуты.
func NormalizeQuery(q string) string {
	for _, c := range []string{`"`, "«", "»", "+", "?"} {
		q = strings.ReplaceAll(q, c, " ")
	}
	return trimRunes(collapseSpaces(q), config.TopicQueryMaxLen)
}

func (a *App) settingsRaw(ctx context.Context, uid string) jx.M {
	var raw []byte
	found, _ := db.Value(ctx, a.DB.Pool, &raw, `SELECT settings FROM users WHERE id = $1`, uid)
	if m := jx.Unmarshal(raw); found && m != nil {
		return m
	}
	return DefaultSettings()
}

// Settings — настройки с нормализацией (бывший load_settings).
func (u *U) Settings() jx.M {
	base := DefaultSettings()
	// значения по умолчанию для всех пользователей задаются в админке
	if t := u.TStr("default_topic"); t != "" {
		base["topic"] = t
	}
	base["fresh_only"], base["auto_translate"] = u.TBool("default_fresh_only"), u.TBool("default_auto_translate")
	s := jx.Merge(base, u.settingsRaw(u.Ctx, u.ID))
	s["topic"] = NormalizeTopic(jx.Str(s, "topic"))
	if s["topic"] == "" {
		s["topic"] = base["topic"]
	}
	s["topic2"] = NormalizeTopic(jx.Str(s, "topic2"))
	if TopicNorm(jx.Str(s, "topic2")) == TopicNorm(jx.Str(s, "topic")) {
		s["topic2"] = ""
	}
	s["query"] = NormalizeQuery(jx.Str(s, "query"))
	for _, k := range ContentFilters {
		s[k] = jx.Bool(s, k)
	}
	return s
}

// SaveSettings — изменения поверх текущих.
func (u *U) SaveSettings(changes jx.M) error {
	s := jx.Merge(u.Settings(), changes)
	return u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		_, err := tx.Exec(u.Ctx, `UPDATE users SET settings = $1 WHERE id = $2`, db.JSON(s), u.ID)
		return err
	})
}

// CurrentTopic — тема каноническим названием из реестра («мемы» → «Мемы»).
func (u *U) CurrentTopic() string {
	t := jx.Str(u.Settings(), "topic")
	if e := u.TopicLookup(t); e != nil {
		return e.Title
	}
	return t
}

func (u *U) CurrentTopic2() string {
	t := jx.Str(u.Settings(), "topic2")
	if t == "" {
		return ""
	}
	if e := u.TopicLookup(t); e != nil {
		return e.Title
	}
	return t
}

func (u *U) CurrentQuery() string {
	if u == nil || u.ID == "" {
		return ""
	}
	return jx.Str(u.Settings(), "query")
}

// SearchTopicKey — ключ оценок, пула и кэша: «первая + вторая», с уточнением — «тема ? запрос». Источники — от первой.
func (u *U) SearchTopicKey() string {
	key := u.CurrentTopic()
	if second := u.CurrentTopic2(); second != "" {
		key += " + " + second
	}
	if q := u.CurrentQuery(); q != "" {
		key += " ? " + q
	}
	return key
}

func (u *U) ContentFilterValues() jx.M {
	s := u.Settings()
	out := jx.M{}
	for _, k := range ContentFilters {
		out[k] = s[k]
	}
	return out
}

func ContentMaxAgeDays(dvach bool) int {
	if dvach {
		return config.DvachMaxAgeDays
	}
	return config.ContentMaxAgeDays
}

// ---- Отметки сверки ----

func (u *U) CheckedAt(what string) int64 {
	col := "proposals_checked_at"
	if what == "threads" {
		col = "threads_checked_at"
	}
	var t int64
	db.Value(u.Ctx, u.DB.Pool, &t, `SELECT `+col+` FROM users WHERE id = $1`, u.ID)
	return t
}

func (u *U) MarkChecked(what string) {
	col := "proposals_checked_at"
	if what == "threads" {
		col = "threads_checked_at"
	}
	u.DB.Pool.Exec(u.Ctx, `UPDATE users SET `+col+` = $1 WHERE id = $2`, now(), u.ID)
}

// ---- Пул ----

func decodeRows(rows pgx.Rows) ([]jx.M, error) {
	var out []jx.M
	defer rows.Close()
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return out, err
		}
		if m := jx.Unmarshal(raw); m != nil {
			out = append(out, m)
		} else {
			out = append(out, jx.M{})
		}
	}
	return out, rows.Err()
}

func (a *App) poolLoadChecked(ctx context.Context, q db.Querier, uid string) ([]jx.M, error) {
	rows, err := q.Query(ctx, `SELECT data FROM pool_items WHERE user_id = $1 ORDER BY position`, uid)
	if err != nil {
		return nil, err
	}
	return decodeRows(rows)
}

func (a *App) poolLoad(ctx context.Context, q db.Querier, uid string) []jx.M {
	list, _ := a.poolLoadChecked(ctx, q, uid)
	return list
}

// Pool — пул пользователя по порядку показа.
func (u *U) Pool() []jx.M { return u.poolLoad(u.Ctx, u.DB.Pool, u.ID) }

// PoolUpdate — заменить пул целиком: fn получает текущий и возвращает новый (nil — не менять). Возвращает итог.
func (u *U) PoolUpdate(fn func(pool []jx.M) []jx.M) ([]jx.M, error) {
	lock := u.userLock(u.ID)
	lock.Lock()
	defer lock.Unlock()
	var result []jx.M
	err := u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		cur, err := u.poolLoadChecked(u.Ctx, tx, u.ID)
		if err != nil {
			return err
		}
		next := fn(cur)
		if next == nil {
			result = cur
			return nil
		}
		result = next
		if _, err := tx.Exec(u.Ctx, `DELETE FROM pool_items WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		rows := make([][]any, 0, len(next))
		for i, item := range next {
			rows = append(rows, []any{u.ID, i, ContentImageKey(item), jx.Str(item, "topic"), db.JSON(item)})
		}
		_, err = tx.CopyFrom(u.Ctx, pgx.Identifier{"pool_items"}, []string{"user_id", "position", "image_key", "topic", "data"}, pgx.CopyFromRows(rows))
		return err
	})
	return result, err
}

// ---- История оценок ----

type History map[string][]jx.M

func EmptyHistory() History {
	h := History{}
	for _, c := range config.HistoryCollections {
		h[c] = []jx.M{}
	}
	return h
}

// All — записи всех коллекций подряд.
func (h History) All() []jx.M {
	var out []jx.M
	for _, c := range config.HistoryCollections {
		out = append(out, h[c]...)
	}
	return out
}

func (a *App) historyLoad(ctx context.Context, q db.Querier, uid string) History {
	h := EmptyHistory()
	rows, err := q.Query(ctx, `SELECT collection, data FROM history_items WHERE user_id = $1 ORDER BY collection, position`, uid)
	if err != nil {
		return h
	}
	defer rows.Close()
	for rows.Next() {
		var coll string
		var raw []byte
		if rows.Scan(&coll, &raw) != nil {
			continue
		}
		if m := jx.Unmarshal(raw); m != nil {
			h[coll] = append(h[coll], m)
		}
	}
	return h
}

func (u *U) History() History { return u.historyLoad(u.Ctx, u.DB.Pool, u.ID) }

func (a *App) HistoryOf(ctx context.Context, uid string) History {
	return a.historyLoad(ctx, a.DB.Pool, uid)
}

// HistoryUpdate — заменить историю целиком (fn возвращает nil — не менять).
func (u *U) HistoryUpdate(fn func(h History) History) (History, error) {
	return u.App.historyUpdate(u.Ctx, u.ID, fn)
}

func (a *App) historyUpdate(ctx context.Context, uid string, fn func(h History) History) (History, error) {
	lock := a.userLock(uid)
	lock.Lock()
	defer lock.Unlock()
	var result History
	err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
		if err := ensureUser(ctx, tx, uid); err != nil {
			return err
		}
		cur := a.historyLoad(ctx, tx, uid)
		next := fn(cur)
		if next == nil {
			result = cur
			return nil
		}
		result = next
		if _, err := tx.Exec(ctx, `DELETE FROM history_items WHERE user_id = $1`, uid); err != nil {
			return err
		}
		var rows [][]any
		for _, coll := range config.HistoryCollections {
			for i, item := range next[coll] {
				rows = append(rows, []any{uid, coll, i, ContentImageKey(item), jx.Bool(item, "proposal_hidden"), db.JSON(item)})
			}
		}
		_, err := tx.CopyFrom(ctx, pgx.Identifier{"history_items"}, []string{"user_id", "collection", "position", "image_key", "proposal_hidden", "data"}, pgx.CopyFromRows(rows))
		return err
	})
	return result, err
}

// HistoryAppend — одна запись в историю (голос). Картинка уже есть в истории — остаётся только последняя оценка.
func (u *U) HistoryAppend(collection string, record jx.M) error {
	lock := u.userLock(u.ID)
	lock.Lock()
	defer lock.Unlock()
	err := u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		var pos int
		tx.QueryRow(u.Ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM history_items WHERE user_id = $1 AND collection = $2`, u.ID, collection).Scan(&pos)
		key := ContentImageKey(record)
		if key != "" {
			if _, err := tx.Exec(u.Ctx, `DELETE FROM history_items WHERE user_id = $1 AND image_key = $2`, u.ID, key); err != nil {
				return err
			}
		}
		_, err := tx.Exec(u.Ctx, `INSERT INTO history_items (user_id, collection, position, image_key, proposal_hidden, data) VALUES ($1, $2, $3, $4, $5, $6)`,
			u.ID, collection, pos, key, jx.Bool(record, "proposal_hidden"), db.JSON(record))
		return err
	})
	// Лайк, «не знаю» (пол-лайка), дизлайк и жалоба «ошибка подбора» сдвигают индекс качества источника
	if err == nil && (collection == "liked" || collection == "disliked" || collection == "undecided" || (collection == "complaints" && jx.Str(record, "complaint_reason") == "match_error")) {
		u.SourceQualityUpdate(u.Ctx, record)
	}
	return err
}

// AllUsersPoolItems — записи пулов и историй всех пользователей: их медиа нельзя удалять при уборке кэша.
func (a *App) AllUsersPoolItems(ctx context.Context) []jx.M {
	var out []jx.M
	for _, sql := range []string{`SELECT data FROM pool_items`, `SELECT data FROM history_items`} {
		if rows, err := a.DB.Pool.Query(ctx, sql); err == nil {
			list, _ := decodeRows(rows)
			out = append(out, list...)
		}
	}
	return out
}

// ---- Подключения (VK и прочие ключи пользователя) ----

func (u *U) Connections() jx.M {
	var raw []byte
	db.Value(u.Ctx, u.DB.Pool, &raw, `SELECT connections FROM users WHERE id = $1`, u.ID)
	if m := jx.Unmarshal(raw); m != nil {
		return m
	}
	return jx.M{}
}

func (u *U) ConnectionsUpdate(fn func(c jx.M) jx.M) (jx.M, error) {
	var result jx.M
	err := u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		var raw []byte
		tx.QueryRow(u.Ctx, `SELECT connections FROM users WHERE id = $1 FOR UPDATE`, u.ID).Scan(&raw)
		cur := jx.Unmarshal(raw)
		if cur == nil {
			cur = jx.M{}
		}
		next := fn(cur)
		if next == nil {
			result = cur
			return nil
		}
		result = next
		_, err := tx.Exec(u.Ctx, `UPDATE users SET connections = $1 WHERE id = $2`, db.JSON(next), u.ID)
		return err
	})
	return result, err
}

// ---- Каналы (ресурсы соцсетей) ----

var ChannelHashtagModes = []string{"off", "rubric", "full"}

// Channel — ресурс соцсети пользователя. Secrets — расшифрованные секреты (наружу не отдавать).
type Channel struct {
	ID           string            `json:"id"`
	UserID       string            `json:"user_id"`
	Platform     string            `json:"platform"`
	Ref          string            `json:"ref"`
	Meta         jx.M              `json:"meta"`
	Position     int               `json:"position"`
	BotID        int64             `json:"bot_id"`
	BotUsername  string            `json:"bot_username"`
	ChatID       int64             `json:"chat_id"`
	ChatTitle    string            `json:"chat_title"`
	ChatUsername string            `json:"chat_username"`
	ChatType     string            `json:"chat_type"`
	ConnectedAt  int64             `json:"connected_at"`
	Schedule     jx.M              `json:"schedule"`
	Style        jx.M              `json:"style"`
	Hashtags     string            `json:"hashtags"`
	Secrets      map[string]string `json:"-"`
	Token        string            `json:"-"` // токен бота Telegram
}

const channelCols = `id, user_id, platform, ref, secrets_enc, meta, position, token_enc, bot_id, bot_username, chat_id, chat_title, chat_username, chat_type, connected_at, schedule, style, hashtags`

func (a *App) scanChannel(row pgx.Row) (*Channel, error) {
	var c Channel
	var secretsEnc, tokenEnc string
	var meta, sched, style []byte
	err := row.Scan(&c.ID, &c.UserID, &c.Platform, &c.Ref, &secretsEnc, &meta, &c.Position, &tokenEnc, &c.BotID, &c.BotUsername, &c.ChatID,
		&c.ChatTitle, &c.ChatUsername, &c.ChatType, &c.ConnectedAt, &sched, &style, &c.Hashtags)
	if err != nil {
		return nil, err
	}
	c.Meta, c.Schedule, c.Style = orEmpty(jx.Unmarshal(meta)), orEmpty(jx.Unmarshal(sched)), orEmpty(jx.Unmarshal(style))
	if !contains(ChannelHashtagModes, c.Hashtags) {
		c.Hashtags = "off"
	}
	c.Secrets = map[string]string{}
	if plain, ok := a.Box.Decrypt(secretsEnc, secret.ChannelAAD(c.UserID, c.ID, c.Platform, "secrets")); ok && secretsEnc != "" {
		var m map[string]any
		if json.Unmarshal([]byte(plain), &m) == nil {
			for k, v := range m {
				c.Secrets[k] = jx.ToStr(v)
			}
		}
	} else if tok, ok := a.Box.Decrypt(tokenEnc, secret.ChannelAAD(c.UserID, c.ID, c.Platform, "token")); ok && tok != "" {
		c.Secrets["token"] = tok
	}
	if c.Platform == "telegram" {
		c.Token = c.Secrets["token"]
	}
	return &c, nil
}

func orEmpty(m jx.M) jx.M {
	if m == nil {
		return jx.M{}
	}
	return m
}

func (u *U) Channels() []*Channel { return u.App.ChannelsOf(u.Ctx, u.ID) }

func (a *App) ChannelsOf(ctx context.Context, uid string) []*Channel {
	rows, err := a.DB.Pool.Query(ctx, `SELECT `+channelCols+` FROM channels WHERE user_id = $1 ORDER BY position, connected_at`, uid)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Channel
	for rows.Next() {
		if c, err := a.scanChannel(rows); err == nil {
			out = append(out, c)
		}
	}
	return out
}

// TelegramChannels — только каналы Telegram.
func (u *U) TelegramChannels() []*Channel {
	var out []*Channel
	for _, c := range u.Channels() {
		if c.Platform == "telegram" {
			out = append(out, c)
		}
	}
	return out
}

// Channel — канал пользователя по id (чужой — nil).
func (u *U) Channel(id string) *Channel {
	c, err := u.scanChannel(u.DB.Pool.QueryRow(u.Ctx, `SELECT `+channelCols+` FROM channels WHERE id = $1 AND user_id = $2`, id, u.ID))
	if err != nil {
		return nil
	}
	return c
}

// ChannelSave — добавить или обновить (по сети и ref) ресурс; секреты хранятся зашифрованными с привязкой к записи.
func (u *U) ChannelSave(c *Channel) (string, error) {
	if c.Platform == "" {
		c.Platform = "telegram"
	}
	if c.Platform == "telegram" {
		c.Ref = strconv.FormatInt(c.ChatID, 10)
	}
	if c.Secrets == nil {
		c.Secrets = map[string]string{}
	}
	if c.Token != "" {
		c.Secrets["token"] = c.Token
	}
	if c.ConnectedAt == 0 {
		c.ConnectedAt = now()
	}
	var id string
	err := u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		found, err := db.Value(u.Ctx, tx, &id, `SELECT id FROM channels WHERE user_id = $1 AND platform = $2 AND ref = $3`, u.ID, c.Platform, c.Ref)
		if err != nil {
			return err
		}
		if !found {
			id = "c" + randHex(5)
		}
		secrets, _ := json.Marshal(c.Secrets)
		secretsEnc := u.Box.Encrypt(string(secrets), secret.ChannelAAD(u.ID, id, c.Platform, "secrets"))
		tokenEnc := u.Box.Encrypt(c.Token, secret.ChannelAAD(u.ID, id, c.Platform, "token"))
		if found {
			_, err = tx.Exec(u.Ctx, `UPDATE channels SET platform=$1, ref=$2, secrets_enc=$3, meta=$4, token_enc=$5, bot_id=$6, bot_username=$7, chat_id=$8,
				chat_title=$9, chat_username=$10, chat_type=$11, connected_at=$12 WHERE id=$13`,
				c.Platform, c.Ref, secretsEnc, db.JSON(orEmpty(c.Meta)), tokenEnc, c.BotID, c.BotUsername, c.ChatID, c.ChatTitle, c.ChatUsername, c.ChatType, c.ConnectedAt, id)
			return err
		}
		pos := c.Position
		if pos < 0 {
			tx.QueryRow(u.Ctx, `SELECT COALESCE(MAX(position), -1) + 1 FROM channels WHERE user_id = $1`, u.ID).Scan(&pos)
		}
		_, err = tx.Exec(u.Ctx, `INSERT INTO channels (id, user_id, position, platform, ref, secrets_enc, meta, token_enc, bot_id, bot_username, chat_id, chat_title,
			chat_username, chat_type, connected_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			id, u.ID, pos, c.Platform, c.Ref, secretsEnc, db.JSON(orEmpty(c.Meta)), tokenEnc, c.BotID, c.BotUsername, c.ChatID, c.ChatTitle, c.ChatUsername, c.ChatType, c.ConnectedAt)
		return err
	})
	return id, err
}

// ChannelUpdateField — сохранить расписание, стиль или режим хэштегов канала.
func (u *U) ChannelUpdateField(id, field string, value any) error {
	switch field {
	case "schedule", "style", "meta":
		_, err := u.DB.Pool.Exec(u.Ctx, `UPDATE channels SET `+field+` = $1 WHERE id = $2 AND user_id = $3`, db.JSON(value), id, u.ID)
		return err
	case "hashtags":
		_, err := u.DB.Pool.Exec(u.Ctx, `UPDATE channels SET hashtags = $1 WHERE id = $2 AND user_id = $3`, value, id, u.ID)
		return err
	}
	return fmt.Errorf("поле канала %s не меняется", field)
}

// ChannelSecretsSave — перезаписать секреты (обновлённый OAuth-токен).
func (u *U) ChannelSecretsSave(c *Channel) error {
	b, _ := json.Marshal(c.Secrets)
	_, err := u.DB.Pool.Exec(u.Ctx, `UPDATE channels SET secrets_enc = $1 WHERE id = $2 AND user_id = $3`,
		u.Box.Encrypt(string(b), secret.ChannelAAD(u.ID, c.ID, c.Platform, "secrets")), c.ID, u.ID)
	return err
}

func (u *U) ChannelDelete(id string) bool {
	n, _ := db.Exec(u.Ctx, u.DB.Pool, `DELETE FROM channels WHERE id = $1 AND user_id = $2`, id, u.ID)
	return n > 0
}

// ChannelMakeDefault — канал первым (по умолчанию), остальные сохраняют порядок.
func (u *U) ChannelMakeDefault(id string) error {
	return u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		rows, err := tx.Query(u.Ctx, `SELECT id FROM channels WHERE user_id = $1 ORDER BY position, connected_at`, u.ID)
		if err != nil {
			return err
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			return err
		}
		if !contains(ids, id) {
			return Bad("Такого канала нет.")
		}
		order := []string{id}
		for _, x := range ids {
			if x != id {
				order = append(order, x)
			}
		}
		for i, x := range order {
			if _, err := tx.Exec(u.Ctx, `UPDATE channels SET position = $1 WHERE id = $2`, i, x); err != nil {
				return err
			}
		}
		return nil
	})
}

// channelMoveToUser — передать ресурс другому профилю (слияние): секреты перешифровываются под нового владельца.
func (a *App) channelMoveToUser(ctx context.Context, q db.Querier, id, from, into string, position int) error {
	var platform, secretsEnc, tokenEnc string
	if err := q.QueryRow(ctx, `SELECT platform, secrets_enc, token_enc FROM channels WHERE id = $1 AND user_id = $2`, id, from).Scan(&platform, &secretsEnc, &tokenEnc); err != nil {
		return nil
	}
	reseal := func(enc, field string) string {
		if enc == "" {
			return ""
		}
		if plain, ok := a.Box.Decrypt(enc, secret.ChannelAAD(from, id, platform, field)); ok {
			return a.Box.Encrypt(plain, secret.ChannelAAD(into, id, platform, field))
		}
		return enc
	}
	_, err := q.Exec(ctx, `UPDATE channels SET user_id = $1, position = $2, secrets_enc = $3, token_enc = $4 WHERE id = $5`,
		into, position, reseal(secretsEnc, "secrets"), reseal(tokenEnc, "token"), id)
	return err
}

// ---- Очередь публикаций ----

func (u *U) Outbox() []jx.M {
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT data FROM outbox WHERE user_id = $1 ORDER BY publish_at`, u.ID)
	if err != nil {
		return nil
	}
	list, _ := decodeRows(rows)
	return list
}

// OutboxUpdate — заменить очередь пользователя целиком (fn → nil — не менять).
func (u *U) OutboxUpdate(fn func(list []jx.M) []jx.M) ([]jx.M, error) {
	var result []jx.M
	err := u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		tx.Exec(u.Ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, u.ID)
		rows, err := tx.Query(u.Ctx, `SELECT data FROM outbox WHERE user_id = $1 ORDER BY publish_at`, u.ID)
		if err != nil {
			return err
		}
		cur, _ := decodeRows(rows)
		next := fn(cur)
		if next == nil {
			result = cur
			return nil
		}
		result = next
		if _, err := tx.Exec(u.Ctx, `DELETE FROM outbox WHERE user_id = $1`, u.ID); err != nil {
			return err
		}
		for _, item := range next {
			id := jx.Str(item, "id")
			if id == "" {
				id = randHex(8)
				item["id"] = id
			}
			at := jx.Int(item, "publish_at")
			if at == 0 {
				at = now()
			}
			status := jx.Str(item, "status")
			if status == "" {
				status = "pending"
			}
			if _, err := tx.Exec(u.Ctx, `INSERT INTO outbox (id, user_id, publish_at, status, data) VALUES ($1, $2, $3, $4, $5)`, id, u.ID, at, status, db.JSON(item)); err != nil {
				return err
			}
		}
		return nil
	})
	return result, err
}

// ---- Журнал опубликованного ----

func (u *U) Published(limit int) []jx.M {
	sql := `SELECT data FROM published WHERE user_id = $1 ORDER BY id DESC`
	if limit > 0 {
		sql += " LIMIT " + strconv.Itoa(limit)
	}
	rows, err := u.DB.Pool.Query(u.Ctx, sql, u.ID)
	if err != nil {
		return nil
	}
	list, _ := decodeRows(rows)
	return list
}

// PublishedAdd — запись в журнал; храним последние TelegramPublishedLogLimit.
func (u *U) PublishedAdd(entry jx.M) error {
	at := jx.Int(entry, "at")
	if at == 0 {
		at = now()
	}
	return u.DB.Tx(u.Ctx, func(tx pgx.Tx) error {
		if err := ensureUser(u.Ctx, tx, u.ID); err != nil {
			return err
		}
		if _, err := tx.Exec(u.Ctx, `INSERT INTO published (user_id, at, data) VALUES ($1, $2, $3)`, u.ID, at, db.JSON(entry)); err != nil {
			return err
		}
		_, err := tx.Exec(u.Ctx, `DELETE FROM published WHERE user_id = $1 AND id NOT IN (SELECT id FROM published WHERE user_id = $1 ORDER BY id DESC LIMIT $2)`,
			u.ID, config.TelegramPublishedLogLimit)
		return err
	})
}

// ---- Мелочи ----

func contains[T comparable](list []T, v T) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
