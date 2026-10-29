package posta

import (
	"os"
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
}
