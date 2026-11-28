package posta

import (
	"os"
	"strings"
	"testing"

	"posta/internal/jx"
)

// Поиск по запросу: отбор по словам, дубли, один пост с источника, издание новости, разбор vc.ru.

func noSkip(jx.M) bool { return false }

func TestQueryPlanFallback(t *testing.T) {
	p := queryPlanFallback("Тюмень")
	if len(p.RU) != 1 || p.RU[0] != "Тюмень" {
		t.Fatalf("ru: %v", p.RU)
	}
	if len(p.Hashtags) != 1 || p.Hashtags[0] != "#Тюмень" {
		t.Fatalf("hashtags: %v", p.Hashtags)
	}
	if len(p.Must) != 1 || p.Must[0] != "тюмень" {
		t.Fatalf("must: %v", p.Must)
	}
}

func TestOsnovaPostContainsFullArticle(t *testing.T) {
	body := strings.Repeat("Текст статьи. ", 500) + "Конец статьи"
	d := jx.M{"url": "https://vc.ru/test/123456-example", "title": "Заголовок", "blocks": []jx.M{
		{"type": "text", "data": jx.M{"text": "<p>" + body + "</p>"}},
	}}
	p := osnovaSearchPost(d, "vc.ru", "")
	if p == nil || !strings.Contains(jx.Str(p, "text"), "Конец статьи") || !strings.Contains(jx.Str(p, "text"), "Заголовок") {
		t.Fatal("текст поста должен содержать заголовок и статью целиком")
	}
}

func TestQueryLex(t *testing.T) {
	plan := QueryPlan{RU: []string{"шрек фильм"}, Must: []string{"шрек"}}
	cases := []struct {
		text string
		want float64
	}{
		{"Шрек вернулся в кино", 1},
		{"Новый фильм про осла", 0},
		{"Про шреков и болото", 1},
	}
	for _, c := range cases {
		if got := queryLex(jx.M{"text": c.text}, plan); got != c.want {
			t.Errorf("%q: want %v, got %v", c.text, c.want, got)
		}
	}
	// часть обязательных слов даёт 0.6, целая формулировка без must тоже
	both := QueryPlan{Must: []string{"тюмень", "экспедиция"}}
	if got := queryLex(jx.M{"text": "Тюмень сегодня"}, both); got != 0.6 {
		t.Errorf("часть must: want 0.6, got %v", got)
	}
	if got := queryLex(jx.M{"text": "Тюменский мороз"}, QueryPlan{Must: []string{"тюмень"}}); got != 1 {
		t.Errorf("основа слова: want 1, got %v", got)
	}
}

func TestRankQuerySearchOnePerSourceAndDedupe(t *testing.T) {
	now64 := now()
	plan := QueryPlan{Must: []string{"тюмень"}}
	mk := func(src, text, img string, age int64) jx.M {
		return jx.M{"source_key": src, "text": text, "image": img, "posted_at": now64 - age, "url": src + text}
	}
	by := map[string][]jx.M{
		PlatformTelegram: {
			mk("telegram:a", "Тюмень утром", "1.jpg", 300),
			mk("telegram:a", "Тюмень вечером", "2.jpg", 100),
			mk("telegram:a", "Ничего про город", "3.jpg", 50), // не про запрос — отсев
		},
		PlatformRSS: {
			mk("rss:b", "Тюмень новости", "4.jpg", 200),
			mk("rss:c", "Тюмень утром", "1.jpg", 900), // та же картинка, что у telegram:a — оставляем раньше опубликованный
			mk("rss:d", "Реклама: тюмень бесплатно подписывайтесь", "5.jpg", 10),
		},
	}
	out := rankQuerySearch(by, plan, 30, noSkip)
	srcs := map[string]int{}
	total := 0
	for _, posts := range out {
		for _, p := range posts {
			srcs[ContentSourceKey(p)]++
			total++
		}
	}
	for src, n := range srcs {
		if n > 1 {
			t.Errorf("источник %s: %d постов, want 1", src, n)
		}
	}
	if srcs["telegram:a"] != 1 || srcs["rss:b"] != 1 {
		t.Errorf("источники: %v", srcs)
	}
	// дубль картинки: остаётся более ранняя публикация (rss:c), а не «Тюмень утром» из telegram:a
	if srcs["rss:c"] != 1 {
		t.Errorf("дубль картинки: должна остаться ранняя публикация rss:c: %v", srcs)
	}
	for _, posts := range out {
		for _, p := range posts {
			if jx.Str(p, "url") == "telegram:aТюмень утром" {
				t.Errorf("дубль картинки из telegram:a не должен попасть в очередь")
			}
		}
	}
	if srcs["rss:d"] != 0 {
		t.Errorf("реклама должна отсеяться: %v", srcs)
	}
	if total != 3 {
		t.Errorf("всего постов: want 3 (по одному с telegram:a, rss:b, rss:c), got %d", total)
	}
}

func TestRankQuerySearchPlatformCap(t *testing.T) {
	plan := QueryPlan{Must: []string{"шрек"}}
	var posts []jx.M
	for i := 0; i < 80; i++ {
		src := "rss:s" + string(rune('a'+i%26)) + string(rune('a'+i/26))
		posts = append(posts, jx.M{"source_key": src, "text": "шрек", "image": "i" + src, "posted_at": now() - 60, "url": src})
	}
	out := rankQuerySearch(map[string][]jx.M{PlatformRSS: posts}, plan, 30, noSkip)
	if n := len(out[PlatformRSS]); n != int(querySearchPlatformCap*querySearchQueueMax) {
		t.Errorf("доля одной площадки: want %d, got %d", int(querySearchPlatformCap*querySearchQueueMax), n)
	}
}

func TestGoogleNewsPublishers(t *testing.T) {
	data, err := os.ReadFile("../../tools/fixtures/gnews_rss.xml")
	if err != nil {
		t.Skip("нет фикстуры:", err)
	}
	m := googleNewsPublishers(rssLoad(data))
	if len(m) == 0 {
		t.Fatal("издания не найдены")
	}
	for link, host := range m {
		if host == "" || host == "news.google.com" {
			t.Errorf("ссылка %s: издание %q — нужен домен издания, не Google", link, host)
		}
	}
}

func TestOsnovaSearchPost(t *testing.T) {
	raw, err := os.ReadFile("../../tools/fixtures/vc_search.json")
	if err != nil {
		t.Skip("нет фикстуры:", err)
	}
	items := jx.Maps(jx.Map(jx.Unmarshal(raw), "result"), "items")
	if len(items) == 0 {
		t.Fatal("в фикстуре нет записей")
	}
	p := osnovaSearchPost(jx.Map(items[0], "data"), "vc.ru", "тюмень")
	if p == nil {
		t.Fatal("запись не разобрана")
	}
	if jx.Str(p, "url") == "" || jx.Str(p, "text") == "" || jx.Str(p, "source_key") != "rss:vc.ru" {
		t.Errorf("поля: url=%q source_key=%q text=%q", jx.Str(p, "url"), jx.Str(p, "source_key"), jx.Str(p, "text"))
	}
	author := PostSearchAuthor(p)
	if author == nil {
		t.Fatal("real article lost its publisher")
	}
	source, ok := autopilotCandidateID(*author)
	if !ok || !autopilotPostMatchesSource(source, p) {
		t.Fatalf("real API article did not match publisher: %q", source)
	}
}

func TestQueryScoreCacheDependsOnExactEvidenceAndRoute(t *testing.T) {
	base := queryScoreCacheKey("match", "owner", "prompt", "model", "title", "body")
	for _, key := range []string{
		queryScoreCacheKey("match", "other", "prompt", "model", "title", "body"),
		queryScoreCacheKey("match", "owner", "changed prompt", "model", "title", "body"),
		queryScoreCacheKey("match", "owner", "prompt", "other model", "title", "body"),
		queryScoreCacheKey("match", "owner", "prompt", "model", "changed title", "body"),
		queryScoreCacheKey("match", "owner", "prompt", "model", "title", "changed body"),
		queryScoreCacheKey("plan", "owner", "prompt", "model", "title", "body"),
	} {
		if key == base {
			t.Fatal("query cache reused a different context or evidence")
		}
	}
}

func TestQueryRerankProtectedContentIsNotSentToOptionalCloudModel(t *testing.T) {
	for _, item := range []jx.M{
		{"source_key": "own:123", "text": "private text"},
		{"source_key": "source:123", "text": "owner source"},
		{"source_key": "rss:public", "source_sensitive": true},
		{"nsfw": true},
		{"fetched_via": "telegram_mtproto"},
		{"attachment_recovered": true},
		{"safety": []string{"adult"}},
	} {
		if !queryRerankProtected(item) {
			t.Errorf("protected input classified as public: %v", item)
		}
	}
	if queryRerankProtected(jx.M{"source_key": "rss:public", "text": "ordinary news"}) {
		t.Fatal("ordinary public result lost optional reranking")
	}
}

func TestQueryRerankHintIsBoundedAndNotPersisted(t *testing.T) {
	item := jx.M{"text": "SMM case"}
	high := withQueryRerankHint(item, "SMM", 100)
	low := withQueryRerankHint(item, "SMM", 0)
	if queryRerankMultiplier(high, "smm") != 1.25 || queryRerankMultiplier(low, "smm") != 0.75 || queryRerankMultiplier(item, "smm") != 1 {
		t.Fatal("query hint must be bounded and neutral without evidence")
	}
	if queryRerankMultiplier(high, "other") != 1 || queryRerankMultiplier(high, "") != 1 || item["_query_rank_score"] != nil {
		t.Fatal("query hint leaked to a different request or mutated input")
	}
	high["_query_rank_score"] = 101
	if queryRerankMultiplier(high, "smm") != 1 {
		t.Fatal("invalid score influenced ranking")
	}
	record := analysisRecord(jx.M{}, low, "SMM", "text")
	if record["_query_rank_for"] != nil || record["_query_rank_score"] != nil {
		t.Fatal("transient search score was persisted in the record")
	}
}

func TestInventoryRankIsTransientAndQueryScoped(t *testing.T) {
	item := jx.M{"text": "Продвижение соцсетей"}
	ranked := withInventoryRank(item, "SMM", 1)
	if !inventoryPriority(ranked, "smm") || inventoryPriority(ranked, "other") || inventoryPriority(item, "smm") {
		t.Fatal("index rank leaked outside its query")
	}
	if inventoryPriority(withInventoryRank(item, "SMM", 61), "smm") || inventoryRank(withInventoryRank(item, "SMM", 241), "smm") != 0 {
		t.Fatal("unbounded rank")
	}
	record := analysisRecord(jx.M{}, ranked, "SMM", "text")
	if record["_inventory_rank"] != nil || record["_inventory_rank_for"] != nil {
		t.Fatal("retrieval rank persisted in editorial record")
	}
}
