package posta

import (
	"context"
	"strings"

	"posta/internal/jx"
)

// The seed is deliberately explicit: feeds are not added again automatically
// after an owner removes or rejects them. Existing catalog entries are untouched.
var thematicRSSSeeds = []struct {
	URL, Name string
	Topics    []string
}{
	{"https://72.ru/text/rss.xml", "72.ru — Тюмень", []string{"Города и архитектура"}},
	{"https://rssexport.rbc.ru/rbcnews/news/30/full.rss", "РБК — новости и экономика", []string{"Бизнес", "Деньги и инвестиции", "Новости"}},
	{"https://habr.com/ru/rss/all/all/?fl=ru", "Хабр — технологии", []string{"Технологии"}},
	{"https://habr.com/ru/rss/hubs/cryptocurrency/articles/all/?fl=ru", "Хабр — криптовалюты", []string{"Криптовалюта"}},
	{"https://www.coindesk.com/arc/outboundfeeds/rss/", "CoinDesk — криптовалюты", []string{"Криптовалюта"}},
	{"https://feeds.bbci.co.uk/sport/rss.xml", "BBC Sport", []string{"Спорт"}},
	{"https://news.google.com/rss/search?q=%D1%81%D0%BF%D0%BE%D1%80%D1%82&hl=ru&gl=RU&ceid=RU:ru", "Google Новости — спорт", []string{"Спорт"}},
}

func mergeThematicRSS(existing []jx.M, blocked map[string]bool) ([]jx.M, []string) {
	seen := map[string]bool{}
	for _, source := range existing {
		seen[strings.ToLower(jx.Str(source, "id"))] = true
	}
	result := append([]jx.M{}, existing...)
	var added []string
	for _, seed := range thematicRSSSeeds {
		key := strings.ToLower(seed.URL)
		if seen[key] || blocked[SourceIDFor(PlatformRSS, seed.URL)] {
			continue
		}
		result = append(result, jx.M{"id": seed.URL, "name": seed.Name, "topics": seed.Topics})
		seen[key] = true
		added = append(added, seed.URL)
	}
	return result, added
}

// SeedThematicRSS adds missing curated feeds once when explicitly invoked.
// Previously checked/rejected sources and existing entries are never overwritten.
func (a *App) SeedThematicRSS(ctx context.Context) ([]string, error) {
	blocked := map[string]bool{}
	rows, err := a.DB.Pool.Query(ctx, `SELECT source_id FROM source_registry`)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		blocked[id] = true
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	var added []string
	_, err = a.UpdateCatalog(ctx, func(d *SourceDB) bool {
		d.RSS, added = mergeThematicRSS(d.RSS, blocked)
		return len(added) > 0
	})
	if err != nil {
		return nil, err
	}
	u := a.User(ctx, "", nil)
	for _, id := range added {
		if err := u.registryUpsert(registryWrite{SourceID: SourceIDFor(PlatformRSS, id), Platform: PlatformRSS, URL: id, Status: RegStatusCatalog, Reason: "тематическая RSS-лента"}); err != nil {
			return added, err
		}
	}
	return added, nil
}

// EnsureThematicRSS runs the additive seed once; later user removals persist.
func (a *App) EnsureThematicRSS(ctx context.Context) ([]string, error) {
	const flag = "thematic_rss_seed_v1"
	if KV(a, ctx, flag, false) {
		return nil, nil
	}
	added, err := a.SeedThematicRSS(ctx)
	if err != nil {
		return added, err
	}
	if err := a.KVSet(ctx, flag, true, 0); err != nil {
		return added, err
	}
	return added, nil
}
