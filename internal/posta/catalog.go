package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/jackc/pgx/v5"

	"posta/internal/db"
	"posta/internal/jx"
)

// Площадки (как в записях и интерфейсе).
const (
	PlatformDvach    = "Двач"
	PlatformTelegram = "Telegram"
	PlatformVK       = "ВКонтакте"
	PlatformRSS      = "RSS"
	PlatformOK       = "Одноклассники"
	PlatformTikTok   = "TikTok"
	Platform4chan    = "4chan"
)

// CatalogKinds — площадка → вид записей каталога (порядок обхода важен для сборщика).
var CatalogKinds = []struct{ Platform, Kind string }{
	{PlatformTelegram, "telegram_channels"}, {PlatformVK, "vk_publics"}, {PlatformRSS, "rss_feeds"}, {PlatformOK, "ok_groups"}, {PlatformTikTok, "tiktok_accounts"},
}

func CatalogKind(platform string) string {
	for _, c := range CatalogKinds {
		if c.Platform == platform {
			return c.Kind
		}
	}
	return ""
}

// SourceDB — каталог источников, находки и голоса подбора, реестр тем (бывший load_source_db()).
type SourceDB struct {
	Boards           []string            `json:"2ch_boards"`
	Telegram         []jx.M              `json:"telegram_channels"`
	VK               []jx.M              `json:"vk_publics"`
	RSS              []jx.M              `json:"rss_feeds"`
	OK               []jx.M              `json:"ok_groups"`
	TikTok           []jx.M              `json:"tiktok_accounts"`
	Votes            []jx.M              `json:"source_votes"`
	Suggestions      []jx.M              `json:"source_suggestions"`
	DvachTopicBoards map[string][]string `json:"dvach_topic_boards"`
	Discovery        jx.M                `json:"discovery"`
	Topics           []*Topic            `json:"topics"`
}

// List — записи каталога вида kind (telegram_channels…), указатель на срез: Update может менять.
func (d *SourceDB) List(kind string) *[]jx.M {
	switch kind {
	case "telegram_channels":
		return &d.Telegram
	case "vk_publics":
		return &d.VK
	case "rss_feeds":
		return &d.RSS
	case "ok_groups":
		return &d.OK
	case "tiktok_accounts":
		return &d.TikTok
	}
	return nil
}

func (d *SourceDB) Clone() *SourceDB {
	var c SourceDB
	json.Unmarshal(jx.Marshal(d), &c)
	if c.DvachTopicBoards == nil {
		c.DvachTopicBoards = map[string][]string{}
	}
	if c.Discovery == nil {
		c.Discovery = jx.M{}
	}
	return &c
}

func defaultCatalog() *SourceDB {
	tag := func(list [][2]string) []jx.M {
		out := []jx.M{}
		for _, e := range list {
			out = append(out, jx.M{"id": e[0], "name": e[1], "topics": []any{"мемы"}})
		}
		return out
	}
	return &SourceDB{
		Boards: []string{"b", "me", "pr", "r", "cc", "a", "mu", "d", "s"},
		VK: tag([][2]string{{"mrzlk", "MDK"}, {"leprum", "Лепрекон"}, {"sci_one", "Умный Юмор"}, {"british_humor", "БРИТИШ ЮМОР"},
			{"bessm_humor", "ОВСЯНКА, СЭР!"}, {"memes_2024", "Мемы на каждый день"}}),
		Telegram: tag([][2]string{{"memesdaily", "Мемы каждый день"}, {"this_is_not_funny", "Это не смешно"}, {"humorhub", "Humor Hub"},
			{"trashmemes", "Trash memes"}, {"russmemes", "Русские мемы"}, {"memechi", "Meme Chi"}}),
		RSS: []jx.M{}, OK: []jx.M{}, TikTok: []jx.M{}, Votes: []jx.M{}, Suggestions: []jx.M{},
		DvachTopicBoards: map[string][]string{}, Discovery: jx.M{"topic": "", "last_run": 0, "requested": false},
	}
}

// Catalog — каталог в памяти: читатели берут неизменяемый снимок, писатели меняют копию под мьютексом,
// изменения сохраняются в Postgres построчно.
type Catalog struct {
	app *App
	// wmu — записи по очереди (fn из UpdateCatalog); mu — только снимок и индексы, держится коротко,
	// поэтому чтение тем (TopicSame и др.) внутри fn не блокирует сам себя
	wmu sync.Mutex
	mu  sync.Mutex
	cur *SourceDB
	// индексы реестра тем (пересобираются при каждом изменении)
	idx *topicIndex
}

// Catalog — текущий снимок каталога. Не менять: изменения — только через UpdateCatalog.
func (a *App) Catalog() *SourceDB {
	a.catalog.mu.Lock()
	defer a.catalog.mu.Unlock()
	return a.catalog.cur
}

// LoadCatalog читает каталог из базы при запуске.
func (a *App) LoadCatalog(ctx context.Context) error {
	d := defaultCatalog()
	kinds := map[string]bool{}
	rows, err := a.DB.Pool.Query(ctx, `SELECT kind, source_id, data FROM catalog_sources ORDER BY kind, position`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var kind, id string
		var raw []byte
		if rows.Scan(&kind, &id, &raw) != nil {
			continue
		}
		if !kinds[kind] {
			kinds[kind] = true
			if kind == "2ch_boards" {
				d.Boards = nil
			} else if l := d.List(kind); l != nil {
				*l = nil
			}
		}
		if kind == "2ch_boards" {
			d.Boards = append(d.Boards, id)
		} else if l := d.List(kind); l != nil {
			if m := jx.Unmarshal(raw); m != nil {
				*l = append(*l, m)
			}
		}
	}
	rows.Close()
	for _, k := range []string{"telegram_channels", "vk_publics", "rss_feeds", "ok_groups", "tiktok_accounts"} {
		if l := d.List(k); *l == nil {
			*l = []jx.M{}
		}
	}
	for _, t := range []struct {
		table string
		dst   *[]jx.M
	}{{"source_votes", &d.Votes}, {"source_suggestions", &d.Suggestions}} {
		rows, err := a.DB.Pool.Query(ctx, `SELECT data FROM `+t.table+` ORDER BY position`)
		if err != nil {
			return err
		}
		list, _ := decodeRows(rows)
		*t.dst = []jx.M{}
		for _, m := range list {
			if jx.Str(m, "source_id") != "" {
				*t.dst = append(*t.dst, m)
			}
		}
	}
	if raw := a.KVRaw(ctx, "dvach_topic_boards"); raw != nil {
		json.Unmarshal(raw, &d.DvachTopicBoards)
	}
	if raw := a.KVRaw(ctx, "discovery"); raw != nil {
		if m := jx.Unmarshal(raw); m != nil {
			d.Discovery = jx.Defaults(m, d.Discovery)
		}
	}
	rows, err = a.DB.Pool.Query(ctx, `SELECT data FROM topics ORDER BY position`)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		if rows.Scan(&raw) != nil {
			continue
		}
		var t Topic
		if json.Unmarshal(raw, &t) == nil {
			if n := NormalizeTopicEntry(&t); n != nil {
				d.Topics = append(d.Topics, n)
			}
		}
	}
	rows.Close()
	d.Topics = topicRegistryFromDB(d)
	a.catalog = &Catalog{app: a, cur: d, idx: buildTopicIndex(d.Topics)}
	return nil
}

// UpdateCatalog — изменить каталог: fn меняет копию и возвращает true, если есть что сохранять.
func (a *App) UpdateCatalog(ctx context.Context, fn func(d *SourceDB) bool) (*SourceDB, error) {
	c := a.catalog
	c.wmu.Lock()
	defer c.wmu.Unlock()
	c.mu.Lock()
	cur := c.cur
	c.mu.Unlock()
	next := cur.Clone()
	if !fn(next) {
		return cur, nil
	}
	next.Topics = topicRegistryFromDB(next)
	if err := a.persistCatalog(ctx, cur, next); err != nil {
		return cur, fmt.Errorf("каталог не сохранён: %w", err)
	}
	c.mu.Lock()
	c.cur = next
	c.idx = buildTopicIndex(next.Topics)
	c.mu.Unlock()
	return next, nil
}

// persistCatalog пишет только изменившиеся строки.
func (a *App) persistCatalog(ctx context.Context, old, next *SourceDB) error {
	return a.DB.Tx(ctx, func(tx pgx.Tx) error {
		// источники: (kind, source_id) → (position, data)
		type row struct {
			pos  int
			data string
		}
		collect := func(d *SourceDB) map[[2]string]row {
			out := map[[2]string]row{}
			for i, b := range d.Boards {
				out[[2]string{"2ch_boards", b}] = row{i, string(jx.Marshal(b))}
			}
			for _, k := range []string{"telegram_channels", "vk_publics", "rss_feeds", "ok_groups", "tiktok_accounts"} {
				for i, e := range *d.List(k) {
					id := jx.Str(e, "id")
					if id == "" {
						id = fmt.Sprint(i)
					}
					out[[2]string{k, id}] = row{i, string(jx.Marshal(e))}
				}
			}
			return out
		}
		was, now := collect(old), collect(next)
		for k := range was {
			if _, ok := now[k]; !ok {
				if _, err := tx.Exec(ctx, `DELETE FROM catalog_sources WHERE kind = $1 AND source_id = $2`, k[0], k[1]); err != nil {
					return err
				}
			}
		}
		for k, r := range now {
			if w, ok := was[k]; ok && w == r {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO catalog_sources (kind, source_id, position, data) VALUES ($1, $2, $3, $4)
				ON CONFLICT (kind, source_id) DO UPDATE SET position = EXCLUDED.position, data = EXCLUDED.data`, k[0], k[1], r.pos, []byte(r.data)); err != nil {
				return err
			}
		}
		// голоса и находки: список целиком, если поменялся
		for _, t := range []struct {
			table     string
			old, next []jx.M
		}{{"source_votes", old.Votes, next.Votes}, {"source_suggestions", old.Suggestions, next.Suggestions}} {
			if string(jx.Marshal(t.old)) == string(jx.Marshal(t.next)) {
				continue
			}
			if _, err := tx.Exec(ctx, `DELETE FROM `+t.table); err != nil {
				return err
			}
			rows := make([][]any, 0, len(t.next))
			for i, e := range t.next {
				rows = append(rows, []any{jx.Str(e, "source_id"), i, db.JSON(e)})
			}
			if _, err := tx.CopyFrom(ctx, pgx.Identifier{t.table}, []string{"source_id", "position", "data"}, pgx.CopyFromRows(rows)); err != nil {
				return err
			}
		}
		// темы
		oldT := map[string]string{}
		for i, t := range old.Topics {
			oldT[t.ID] = fmt.Sprint(i) + "|" + string(jx.Marshal(t))
		}
		seen := map[string]bool{}
		for i, t := range next.Topics {
			seen[t.ID] = true
			if oldT[t.ID] == fmt.Sprint(i)+"|"+string(jx.Marshal(t)) {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO topics (id, position, data) VALUES ($1, $2, $3)
				ON CONFLICT (id) DO UPDATE SET position = EXCLUDED.position, data = EXCLUDED.data`, t.ID, i, db.JSON(t)); err != nil {
				return err
			}
		}
		for id := range oldT {
			if !seen[id] {
				if _, err := tx.Exec(ctx, `DELETE FROM topics WHERE id = $1`, id); err != nil {
					return err
				}
			}
		}
		for key, pair := range map[string][2]any{"dvach_topic_boards": {old.DvachTopicBoards, next.DvachTopicBoards}, "discovery": {old.Discovery, next.Discovery}} {
			if string(jx.Marshal(pair[0])) == string(jx.Marshal(pair[1])) {
				continue
			}
			if _, err := tx.Exec(ctx, `INSERT INTO kv (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now()`, key, db.JSON(pair[1])); err != nil {
				return err
			}
		}
		return nil
	})
}
