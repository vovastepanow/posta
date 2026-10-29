// Package importer — перенос данных из прежней базы SQLite (data/posta.sqlite) в Postgres.
// Переносится только владелец (остальные профили — следы тестов) и общие данные: каталог, темы, решения по источникам,
// радар лидов. Сырые ответы API (board_json, fourchan, youtube_posts…) не переносятся: это кэши.
// SQLite читается через sqlite3 -json (одноразовая миграция, без драйвера в бинарнике).
package importer

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jackc/pgx/v5"

	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/posta"
)

type Report struct {
	Lines []string
}

func (r *Report) add(format string, a ...any) { r.Lines = append(r.Lines, fmt.Sprintf(format, a...)) }

func query(file, sql string) ([]map[string]any, error) {
	out, err := exec.Command("sqlite3", "-readonly", "-json", file, sql).Output()
	if err != nil {
		return nil, fmt.Errorf("sqlite3: %w", err)
	}
	var rows []map[string]any
	if s := strings.TrimSpace(string(out)); s != "" {
		if err := json.Unmarshal([]byte(s), &rows); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

// kvKeep — какие ключи kv переносятся (остальное — кэши и служебные отметки PHP).
func kvKeep(key, owner string) bool {
	for _, p := range []string{"admin:", "telegram_limits", "telegram_app_keys", "telegram_login_bot", "vk_tokens", "hashtag_candidates",
		"fourchan_topic_boards", "fourchan_model_asked", "discovery_deferred", "hot_topics", "login_bot_offset", "tiktok_search_pause"} {
		if key == strings.TrimSuffix(p, ":") || strings.HasPrefix(key, p) {
			return true
		}
	}
	for _, p := range []string{"lead_radar:", "api_token:"} {
		if key == p+owner {
			return true
		}
	}
	return false
}

// unwrap — значение kv без обёртки PHP {"_kv_value": …}.
func unwrap(raw string) any {
	var v any
	if json.Unmarshal([]byte(raw), &v) != nil {
		return raw
	}
	if m, ok := v.(map[string]any); ok && len(m) == 1 {
		if inner, has := m["_kv_value"]; has {
			return inner
		}
	}
	return v
}

// migrateTags — метки анализа в записях: старые ключи таксономии → новые.
func migrateTags(data map[string]any) map[string]any {
	an := jx.Map(data, "analysis")
	if an == nil {
		return data
	}
	var tags []string
	for _, t := range jx.Strs(an, "tags") {
		if k := posta.MigrateTagKey(t); k != "" && !contains(tags, k) {
			tags = append(tags, k)
		}
	}
	if tags != nil {
		an["tags"] = tags
	}
	return data
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func jsonCol(v any) []byte {
	if s, ok := v.(string); ok && s != "" {
		var x any
		if json.Unmarshal([]byte(s), &x) == nil {
			return []byte(s)
		}
	}
	if v == nil {
		return []byte("{}")
	}
	return db.JSON(v)
}

func str(v any) string { return jx.ToStr(v) }
func num(v any) int64  { return jx.ToInt(v) }

func nullable(v any) any {
	if v == nil || jx.ToStr(v) == "" {
		return nil
	}
	return v
}

// Import — перенос в пустую базу Postgres. owner — id профиля владельца.
func Import(ctx context.Context, pg *db.DB, file, owner string) (*Report, error) {
	r := &Report{}
	var users int
	db.Value(ctx, pg.Pool, &users, `SELECT COUNT(*) FROM users`)
	if users > 0 {
		return nil, fmt.Errorf("в Postgres уже есть пользователи: импорт — только в пустую базу")
	}
	err := pg.Tx(ctx, func(tx pgx.Tx) error {
		ex := func(sql string, args ...any) error {
			_, err := tx.Exec(ctx, sql, args...)
			return err
		}
		// Владелец
		rows, err := query(file, fmt.Sprintf(`SELECT * FROM users WHERE id = '%s'`, owner))
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("в SQLite нет профиля %s", owner)
		}
		u := rows[0]
		if err := ex(`INSERT INTO users (id, created_at, last_seen, proposals_checked_at, threads_checked_at, settings, connections, ref_code, referred_by, telegram_id,
			tg_username, tg_name, adult_allowed, login, password_hash) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,'',$9,$10,$11,$12,$13,$14)`,
			owner, num(u["created_at"]), num(u["last_seen"]), num(u["proposals_checked_at"]), num(u["threads_checked_at"]), jsonCol(u["settings"]), jsonCol(u["connections"]),
			nullable(u["ref_code"]), nullable(u["telegram_id"]), str(u["tg_username"]), str(u["tg_name"]), num(u["adult_allowed"]) == 1, nullable(u["login"]),
			str(u["password_hash"])); err != nil {
			return fmt.Errorf("users: %w", err)
		}
		r.add("профиль владельца %s", owner)

		where := fmt.Sprintf(` WHERE user_id = '%s'`, owner)
		count := func(table string, n int) { r.add("%s: %d", table, n) }

		rows, _ = query(file, `SELECT * FROM history_items`+where+` ORDER BY collection, position`)
		for _, h := range rows {
			data := migrateTags(jx.Unmarshal([]byte(str(h["data"]))))
			if err := ex(`INSERT INTO history_items (user_id, collection, position, image_key, proposal_hidden, data) VALUES ($1,$2,$3,$4,$5,$6)`,
				owner, str(h["collection"]), num(h["position"]), str(h["image_key"]), num(h["proposal_hidden"]) == 1, db.JSON(data)); err != nil {
				return fmt.Errorf("history_items: %w", err)
			}
		}
		count("history_items", len(rows))

		rows, _ = query(file, `SELECT * FROM pool_items`+where+` ORDER BY position`)
		for _, p := range rows {
			data := migrateTags(jx.Unmarshal([]byte(str(p["data"]))))
			if err := ex(`INSERT INTO pool_items (user_id, position, image_key, topic, data) VALUES ($1,$2,$3,$4,$5)`,
				owner, num(p["position"]), str(p["image_key"]), posta.TopicNorm(jx.Str(data, "topic")), db.JSON(data)); err != nil {
				return fmt.Errorf("pool_items: %w", err)
			}
		}
		count("pool_items", len(rows))

		rows, _ = query(file, `SELECT * FROM outbox`+where)
		for _, o := range rows {
			if err := ex(`INSERT INTO outbox (id, user_id, publish_at, status, data) VALUES ($1,$2,$3,$4,$5)`, str(o["id"]), owner, num(o["publish_at"]), str(o["status"]), jsonCol(o["data"])); err != nil {
				return fmt.Errorf("outbox: %w", err)
			}
		}
		count("outbox", len(rows))

		rows, _ = query(file, `SELECT * FROM published`+where+` ORDER BY id`)
		for _, p := range rows {
			if err := ex(`INSERT INTO published (user_id, at, data) VALUES ($1,$2,$3)`, owner, num(p["at"]), jsonCol(p["data"])); err != nil {
				return fmt.Errorf("published: %w", err)
			}
		}
		count("published", len(rows))

		rows, _ = query(file, `SELECT * FROM channels`+where+` ORDER BY position`)
		for _, c := range rows {
			if err := ex(`INSERT INTO channels (id, user_id, platform, ref, secrets_enc, meta, position, token_enc, bot_id, bot_username, chat_id, chat_title, chat_username,
				chat_type, connected_at, schedule, style, hashtags) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)`,
				str(c["id"]), owner, str(c["platform"]), str(c["ref"]), str(c["secrets_enc"]), jsonCol(c["meta"]), num(c["position"]), str(c["token_enc"]), num(c["bot_id"]),
				str(c["bot_username"]), num(c["chat_id"]), str(c["chat_title"]), str(c["chat_username"]), str(c["chat_type"]), num(c["connected_at"]), jsonCol(c["schedule"]),
				jsonCol(c["style"]), str(c["hashtags"])); err != nil {
				return fmt.Errorf("channels: %w", err)
			}
		}
		count("channels", len(rows))

		rows, _ = query(file, `SELECT * FROM mirrors`+where)
		for _, m := range rows {
			if err := ex(`INSERT INTO mirrors (id, user_id, platform, source_id, name, channel_id, mode, rules, last_post_key, last_checked_at, created_at)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, str(m["id"]), owner, str(m["platform"]), str(m["source_id"]), str(m["name"]), nullable(m["channel_id"]),
				str(m["mode"]), jsonCol(m["rules"]), jsonCol(m["last_post_key"]), num(m["last_checked_at"]), num(m["created_at"])); err != nil {
				return fmt.Errorf("mirrors: %w", err)
			}
		}
		count("mirrors", len(rows))

		for _, t := range []struct{ name, cols string }{{"ledger", "at, amount, kind, note, related_user"}, {"post_usage", "at"}} {
			rows, _ = query(file, `SELECT `+t.cols+` FROM `+t.name+where)
			for _, row := range rows {
				cols := strings.Split(t.cols, ", ")
				args := []any{owner}
				ph := []string{"$1"}
				for i, c := range cols {
					args = append(args, row[c])
					ph = append(ph, fmt.Sprintf("$%d", i+2))
				}
				if err := ex(`INSERT INTO `+t.name+` (user_id, `+t.cols+`) VALUES (`+strings.Join(ph, ",")+`)`, args...); err != nil {
					return fmt.Errorf("%s: %w", t.name, err)
				}
			}
			count(t.name, len(rows))
		}
		rows, _ = query(file, `SELECT * FROM subscriptions`+where)
		for _, s := range rows {
			ex(`INSERT INTO subscriptions (user_id, plan, channels, posts_per_day, price, started_at, until) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
				owner, s["plan"], num(s["channels"]), num(s["posts_per_day"]), num(s["price"]), num(s["started_at"]), num(s["until"]))
		}
		rows, _ = query(file, `SELECT * FROM sessions`+where)
		for _, s := range rows {
			if err := ex(`INSERT INTO sessions (token_hash, user_id, created_at, last_seen) VALUES ($1,$2,$3,$4)`, str(s["token_hash"]), owner, num(s["created_at"]), num(s["last_seen"])); err != nil {
				return fmt.Errorf("sessions: %w", err)
			}
		}
		count("sessions (вход владельца сохраняется)", len(rows))

		// Общие данные
		rows, _ = query(file, `SELECT * FROM catalog_sources ORDER BY kind, position`)
		for _, c := range rows {
			if err := ex(`INSERT INTO catalog_sources (kind, source_id, position, data) VALUES ($1,$2,$3,$4) ON CONFLICT DO NOTHING`,
				str(c["kind"]), str(c["source_id"]), num(c["position"]), jsonCol(c["data"])); err != nil {
				return fmt.Errorf("catalog_sources: %w", err)
			}
		}
		count("catalog_sources", len(rows))
		for _, t := range []string{"source_votes", "source_suggestions"} {
			rows, _ = query(file, `SELECT * FROM `+t+` ORDER BY position`)
			for _, s := range rows {
				if err := ex(`INSERT INTO `+t+` (source_id, position, data) VALUES ($1,$2,$3)`, str(s["source_id"]), num(s["position"]), jsonCol(s["data"])); err != nil {
					return fmt.Errorf("%s: %w", t, err)
				}
			}
			count(t, len(rows))
		}
		rows, _ = query(file, `SELECT * FROM topics ORDER BY position`)
		for _, t := range rows {
			if err := ex(`INSERT INTO topics (id, position, data) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, str(t["id"]), num(t["position"]), jsonCol(t["data"])); err != nil {
				return fmt.Errorf("topics: %w", err)
			}
		}
		count("topics", len(rows))
		rows, _ = query(file, `SELECT * FROM source_verdicts`)
		for _, v := range rows {
			ex(`INSERT INTO source_verdicts (topic, source, passed, total, at) VALUES ($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`, str(v["topic"]), str(v["source"]),
				num(v["passed"]), num(v["total"]), num(v["at"]))
		}
		count("source_verdicts", len(rows))
		rows, _ = query(file, `SELECT * FROM screened_cache`)
		for _, s := range rows {
			ex(`INSERT INTO screened_cache (url, topic, source, score, phash, at, ver) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT DO NOTHING`,
				str(s["url"]), str(s["topic"]), str(s["source"]), num(s["score"]), str(s["phash"]), num(s["at"]), num(s["ver"]))
		}
		count("screened_cache", len(rows))

		// kv: без обёртки и без кэшей
		rows, _ = query(file, `SELECT key, value FROM kv WHERE key NOT LIKE 'board_json%' AND key NOT LIKE 'fourchan:%' AND key NOT LIKE 'youtube_posts%' AND key NOT LIKE 'thread_topic%' AND key NOT LIKE 'source_posts%' AND key NOT LIKE 'post_search%' AND key NOT LIKE 'article%' AND key NOT LIKE 'history%' AND key NOT LIKE 'video_preview%' AND key NOT LIKE 'translate%'`)
		kept := 0
		for _, k := range rows {
			key := str(k["key"])
			if !kvKeep(key, owner) {
				continue
			}
			if err := ex(`INSERT INTO kv (key, value) VALUES ($1,$2)`, key, db.JSON(unwrap(str(k["value"])))); err != nil {
				return fmt.Errorf("kv %s: %w", key, err)
			}
			kept++
		}
		r.add("kv: перенесено %d из %d (кэши ответов API отброшены)", kept, len(rows))

		// Радар лидов владельца
		rows, _ = query(file, `SELECT * FROM telegram_lead_queue`+where+` ORDER BY id`)
		ids := map[int64]int64{}
		for _, q := range rows {
			var id int64
			err := tx.QueryRow(ctx, `INSERT INTO telegram_lead_queue (user_id, chat_id, message, message_id, status, ollama_result, ollama_error, ollama_reason, created_at,
				processed_at, attempts, chat_ref, chat_title, chat_username, sender_id, sender_username, sender_name, message_date)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,COALESCE($9::timestamptz, now()),$10::timestamptz,$11,$12,$13,$14,$15,$16,$17,$18::timestamptz) ON CONFLICT DO NOTHING RETURNING id`,
				owner, num(q["chat_id"]), str(q["message"]), q["message_id"], str(q["status"]), q["ollama_result"], q["ollama_error"], q["ollama_reason"],
				sqliteTime(q["created_at"]), sqliteTime(q["processed_at"]), num(q["attempts"]), q["chat_ref"], q["chat_title"], q["chat_username"], q["sender_id"],
				q["sender_username"], q["sender_name"], sqliteTime(q["message_date"])).Scan(&id)
			if err == nil {
				ids[num(q["id"])] = id
			}
		}
		count("telegram_lead_queue", len(ids))
		rows, _ = query(file, `SELECT * FROM telegram_leads`+where+` ORDER BY id`)
		leads := 0
		for _, l := range rows {
			qid, has := ids[num(l["queue_id"])]
			if !has {
				continue
			}
			if err := ex(`INSERT INTO telegram_leads (queue_id, user_id, chat_id, message, message_id, lead_status, telegram_username, telegram_profile_url, confidence_score,
				created_at, replied_at, updated_at, chat_title, sender_id, sender_name, message_url, reason)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,COALESCE($10::timestamptz, now()),$11::timestamptz,COALESCE($12::timestamptz, now()),$13,$14,$15,$16,$17)`,
				qid, owner, num(l["chat_id"]), l["message"], l["message_id"], str(l["lead_status"]), l["telegram_username"], l["telegram_profile_url"],
				jx.ToFloat(l["confidence_score"]), sqliteTime(l["created_at"]), sqliteTime(l["replied_at"]), sqliteTime(l["updated_at"]), l["chat_title"], l["sender_id"],
				l["sender_name"], l["message_url"], l["reason"]); err != nil {
				return fmt.Errorf("telegram_leads: %w", err)
			}
			leads++
		}
		count("telegram_leads", leads)
		// Владелец видит админку и радар
		return ex(`INSERT INTO kv (key, value) VALUES ('admin:owners', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value`, db.JSON([]string{owner}))
	})
	return r, err
}

// sqliteTime — время SQLite («2026-10-25 14:03:11» в UTC или ISO 8601) для Postgres; пусто — nil.
func sqliteTime(v any) any {
	s := strings.TrimSpace(str(v))
	if s == "" {
		return nil
	}
	if len(s) == 19 && s[10] == ' ' {
		return s + "+00"
	}
	return s
}
