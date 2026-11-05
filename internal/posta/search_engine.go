package posta

import (
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"posta/internal/jx"
)

// SearchBackend — одна поисковая площадка: посты по одной формулировке, страница page (с 1). Platform — общая очередь:
// площадки с одним Platform идут по очереди (один аккаунт или один лимит), разные — параллельно.
type SearchBackend struct {
	Name     string
	Platform string
	Tunable  string // bool-переключатель в админке
	Terms    func(p QueryPlan) []string
	Search   func(u *U, q string, page int) ([]jx.M, bool, error)
	Paged    bool // площадка отдаёт страницы: номер страницы — по кругу от прохода
}

// searchPageRotate — сколько страниц по кругу листает площадка с пагинацией за проходы.
const searchPageRotate = 5

// searchTerms — первые n строк списка формулировок.
func searchTerms(list []string, n int) []string {
	return list[:min(len(list), n)]
}

// searchBackends — все площадки поиска по запросу. Встроенные поиски оборачиваются тем же интерфейсом.
func searchBackends() []SearchBackend {
	ru := func(n int) func(QueryPlan) []string {
		return func(p QueryPlan) []string { return p.RU }
	}
	en := func(n int) func(QueryPlan) []string {
		return func(p QueryPlan) []string { return p.EN }
	}
	tags := func(n int) func(QueryPlan) []string {
		return func(p QueryPlan) []string { return p.Hashtags }
	}
	// once — площадка без страниц: одна выдача на формулировку
	once := func(f func(u *U, q string) []jx.M) func(u *U, q string, page int) ([]jx.M, bool, error) {
		return func(u *U, q string, page int) ([]jx.M, bool, error) {
			return f(u, q), false, nil
		}
	}
	return []SearchBackend{
		{"Telegram", PlatformTelegram, "search_telegram", func(p QueryPlan) []string { return append(append([]string{}, p.Hashtags...), p.RU...) },
			once(func(u *U, q string) []jx.M { return u.PostSearchTelegram(q, PostSearchLimit) }), false},
		{"VK", PlatformVK, "search_vk", ru(2), once(func(u *U, q string) []jx.M { return u.PostSearchVK(q, PostSearchLimit) }), false},
		{"YouTube", PlatformRSS, "search_youtube", func(p QueryPlan) []string { return append(append([]string{}, p.RU...), p.EN...) },
			once(func(u *U, q string) []jx.M { return u.PostSearchYouTube(q, PostSearchLimit) }), false},
		{"Новости", PlatformRSS, "search_news", ru(1), once(func(u *U, q string) []jx.M { return u.queryNewsRSS(q) }), false},
		{"vc/dtf", PlatformRSS, "search_vc", ru(1), once(func(u *U, q string) []jx.M { return u.queryOsnovaSearch(q) }), false},
		{"TikTok", PlatformTikTok, "search_tiktok", tags(1), once(func(u *U, q string) []jx.M { return u.PostSearchTikTok(q, PostSearchLimit) }), false},
		{"Rutube", PlatformRutube, "search_rutube", ru(1), func(u *U, q string, page int) ([]jx.M, bool, error) { return u.SearchRutube(q, page) }, true},
		{"9GAG", Platform9GAG, "search_9gag", en(1), func(u *U, q string, page int) ([]jx.M, bool, error) { return u.SearchNineGAG(q, page) }, false},
		{"Lemmy", PlatformLemmy, "search_lemmy", en(1), func(u *U, q string, page int) ([]jx.M, bool, error) { return u.SearchLemmy(q, page) }, false},
		{"Mastodon", PlatformMastodon, "search_mastodon", tags(1), func(u *U, q string, page int) ([]jx.M, bool, error) { return u.SearchMastodon(q, page) }, false},
		{"Joyreactor", PlatformJoyreactor, "search_joyreactor", ru(1), func(u *U, q string, page int) ([]jx.M, bool, error) { return u.SearchJoyreactor(q, page) }, false},
	}
}

// searchBackendCall — один вызов площадки с кэшем на час; паника или ошибка — пусто, остальные площадки идут дальше.
func (u *U) searchBackendCall(b SearchBackend, q string, page int) (posts []jx.M) {
	defer func() {
		if r := recover(); r != nil {
			u.Log("Поиск «%s»: %s — ошибка, пропускаю: %v", q, b.Name, r)
			posts = nil
		}
	}()
	key := "search_engine:" + sha1Short(b.Name+"|"+q+"|"+strconv.Itoa(page), 20)
	var cached []jx.M
	if u.CacheGet(u.Ctx, key, &cached) {
		return cached
	}
	posts, _, err := b.Search(u, q, page)
	if err != nil {
		u.Log("Поиск «%s»: %s — %s", q, b.Name, err)
		return nil
	}
	// пустой ответ не кэшируем: временный сбой площадки не должен держаться час
	if len(posts) > 0 {
		u.CacheSet(u.Ctx, key, posts, postSearchCacheTTL)
	}
	return posts
}

// SearchEngineRun — опрос включённых площадок по формулировкам плана. off — сдвиг по кругу (страница и формулировка).
// Бюджет budget на весь опрос: то, что не успело прийти, пропускается, остальные результаты идут.
func (u *U) SearchEngineRun(plan QueryPlan, off int, budget time.Duration) (map[string][]jx.M, map[string]int) {
	return u.searchEngineRun(plan, off, budget, nil)
}

// searchEngineRun can restrict a topic pass to the platforms absent from the
// built-in Telegram/VK/YouTube search.
func (u *U) searchEngineRun(plan QueryPlan, off int, budget time.Duration, only map[string]bool) (map[string][]jx.M, map[string]int) {
	groups := map[string][]SearchBackend{}
	var order []string
	for _, b := range searchBackends() {
		if !u.TBool(b.Tunable) || (only != nil && !only[b.Name]) {
			continue
		}
		// YouTube, news and vc/dtf share a result category, not a rate limit.
		// Run them independently so a slow YouTube lookup cannot starve RSS.
		group := b.Platform
		if b.Platform == PlatformRSS {
			group = b.Name
		}
		if _, ok := groups[group]; !ok {
			order = append(order, group)
		}
		groups[group] = append(groups[group], b)
	}
	type result struct {
		plat, name string
		posts      []jx.M
	}
	deadline := time.Now().Add(budget)
	results := make(chan result, 256)
	var wg sync.WaitGroup
	for _, plat := range order {
		wg.Add(1)
		go func(list []SearchBackend) {
			defer wg.Done()
			for _, b := range list {
				terms := DiscoveryRotate(b.Terms(plan), off)
				page := 1
				if b.Paged {
					page = 1 + off%searchPageRotate
				}
				for _, q := range searchTerms(terms, 2) {
					if time.Now().After(deadline) || !u.Alive() {
						return
					}
					results <- result{b.Platform, b.Name, u.searchBackendCall(b, q, page)}
				}
			}
		}(groups[plat])
	}
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	got := map[string][]jx.M{}
	counts := map[string]int{}
	timedOut := false
	for waiting := true; waiting; {
		select {
		case r := <-results:
			got[r.plat] = append(got[r.plat], r.posts...)
			counts[r.name] += len(r.posts)
		case <-done:
			waiting = false
		case <-time.After(time.Until(deadline)):
			timedOut, waiting = true, false
		}
	}
	// уже лежащее в канале после выхода из цикла — тоже в выдачу (поздних ответов не ждём)
	for len(results) > 0 {
		r := <-results
		got[r.plat] = append(got[r.plat], r.posts...)
		counts[r.name] += len(r.posts)
	}
	if timedOut {
		u.Log("Поиск: истёк общий бюджет; поздние ответы площадок пропущены.")
	}
	u.Log("%s", searchCountsLine(plan, counts))
	return got, counts
}

// searchCountsLine — строка по площадкам для журнала прохода.
func searchCountsLine(plan QueryPlan, counts map[string]int) string {
	line := "Поиск:"
	for _, b := range searchBackends() {
		if n, ok := counts[b.Name]; ok {
			line += fmt.Sprintf(" %s %d ·", b.Name, n)
		}
	}
	return trimRunes(line, 900)
}

// SearchTest — проверка поиска без записи в пул и без полного анализа: план, опрос площадок, отбор по словам.
// Для cmd/posta «search-test»: печатает число постов по площадкам и топ-20 отобранных.
func (u *U) SearchTest(query, topic string) string {
	plan := u.QuerySearchPlan(query, topic)
	got, counts := u.SearchEngineRun(plan, 0, time.Duration(u.TInt("query_search_seconds"))*time.Second)
	ranked := rankQuerySearch(got, plan, u.TInt("content_max_age"), func(it jx.M) bool { return u.UAItemBlocked(it) != "" })
	ranked = u.queryRerank(query, plan, ranked)
	var all []jx.M
	for _, plat := range []string{PlatformTelegram, PlatformVK, PlatformRSS, PlatformTikTok, PlatformRutube, Platform9GAG, PlatformLemmy, PlatformMastodon, PlatformJoyreactor} {
		all = append(all, ranked[plat]...)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "план: ru %v, en %v, хэштеги %v, обязательные %v\n", plan.RU, plan.EN, plan.Hashtags, plan.Must)
	for _, name := range []string{"Telegram", "Telegram каналы", "VK", "YouTube", "Новости", "vc/dtf", "TikTok", "Rutube", "9GAG", "Lemmy", "Mastodon", "Joyreactor"} {
		if n, ok := counts[name]; ok {
			fmt.Fprintf(&b, "  %-16s %d\n", name, n)
		}
	}
	fmt.Fprintf(&b, "отобрано по словам: %d, из них площадок: %d\n", len(all), len(ranked))
	for i, it := range all[:min(len(all), 20)] {
		fmt.Fprintf(&b, "%2d. [%s] %s — %s\n", i+1, jx.Str(it, "source"), trimRunes(jx.Str(it, "title"), 80), jx.Str(it, "url"))
	}
	return b.String()
}
