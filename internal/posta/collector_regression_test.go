package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestPoolSelectionKeepsHiddenAndExpiredRecords(t *testing.T) {
	pool := []jx.M{
		{"image": "visible", "topic": "мемы", "posted_at": int64(2)},
		{"image": "hidden", "topic": "мемы", "posted_at": int64(1)},
		{"image": "other-topic", "topic": "города", "posted_at": int64(3)},
	}
	visible := selectPoolItems(pool, func(it jx.M) bool { return jx.Str(it, "image") == "visible" })
	later := selectPoolItems(pool, func(it jx.M) bool { return jx.Str(it, "image") != "visible" })
	if len(visible) != 1 || len(later) != 2 || len(pool) != 3 || jx.Str(pool[1], "image") != "hidden" {
		t.Fatalf("selection must not remove or reorder stored records: pool=%v visible=%v later=%v", pool, visible, later)
	}
}

func TestCandidatePlatformsIncludeAllSearchResults(t *testing.T) {
	by := map[string][]jx.M{PlatformRutube: nil, Platform9GAG: nil, PlatformLemmy: nil, PlatformMastodon: nil, PlatformJoyreactor: nil, "future": nil}
	found := map[string]bool{}
	for _, p := range candidatePlatformOrder(by) {
		found[p] = true
	}
	for p := range by {
		if !found[p] {
			t.Errorf("platform %q is lost before analysis", p)
		}
	}
}

func TestQuerySearchRetainsAlternativesFromSameSource(t *testing.T) {
	posts := []jx.M{
		{"source_key": "telegram:a", "text": "Тюмень новости", "image": "one", "url": "https://t.me/a/1", "posted_at": now()},
		{"source_key": "telegram:a", "text": "Тюмень погода", "image": "two", "url": "https://t.me/a/2", "posted_at": now()},
	}
	result := rankQuerySearch(map[string][]jx.M{PlatformTelegram: posts}, QueryPlan{Must: []string{"тюмень"}}, 30, noSkip)
	if len(result[PlatformTelegram]) != 2 {
		t.Fatalf("both posts must reach deduplication and analysis: %v", result)
	}
}

func TestCandidatePostKeyUsesPostURL(t *testing.T) {
	a := jx.M{"source_url": "https://example.com/channel", "url": "https://example.com/post/1"}
	b := jx.M{"source_url": "https://example.com/channel", "url": "https://example.com/post/2"}
	if CandidatePostKey(a) == CandidatePostKey(b) {
		t.Fatalf("distinct posts considered duplicates: %v %v", a, b)
	}
	p := searchPost(Platform9GAG, "9gag:1", "https://example.com/channel", "", "", "img", "", "https://example.com/post/1", now())
	if jx.Str(p, "source_url") != "https://example.com/post/1" {
		t.Fatalf("wrong source post URL: %v", p)
	}
}

func TestSourcePollFetchSetRespectsConfiguredLimit(t *testing.T) {
	entries := map[string]jx.M{"a": {}, "b": {}, "c": {}}
	got := SourcePollFetchSet(entries, map[string]bool{}, now(), 2)
	if len(got) != 2 {
		t.Fatalf("expected 2 uncached sources, got %v", got)
	}
}

func TestSearchBackendTermsRotateAcrossPasses(t *testing.T) {
	plan := QueryPlan{RU: []string{"one", "two", "three"}, EN: []string{"first", "second"}}
	for _, b := range searchBackends() {
		if b.Name == "VK" {
			terms := b.Terms(plan)
			if len(terms) != 3 || DiscoveryRotate(terms, 2)[0] != "three" {
				t.Fatalf("search terms cannot rotate: %v", terms)
			}
			return
		}
	}
	t.Fatal("VK backend missing")
}

func TestCandidatePostKeyKeepsPostIdentifiers(t *testing.T) {
	cases := []struct {
		name          string
		first, second jx.M
	}{
		{"YouTube videos", jx.M{"url": "https://www.youtube.com/watch?v=abcdefghijk"}, jx.M{"url": "https://www.youtube.com/watch?v=123456789ab"}},
		{"4chan posts", jx.M{"url": "https://boards.4chan.org/g/thread/100#p101", "post_id": "101"}, jx.M{"url": "https://boards.4chan.org/g/thread/100#p102", "post_id": "102"}},
		{"2ch posts", jx.M{"url": "https://2ch.hk/b/res/100.html#101", "post_id": "101"}, jx.M{"url": "https://2ch.hk/b/res/100.html#102", "post_id": "102"}},
		{"query IDs", jx.M{"url": "https://example.org/article?id=1"}, jx.M{"url": "https://example.org/article?id=2"}},
	}
	for _, tc := range cases {
		if CandidatePostKey(tc.first) == CandidatePostKey(tc.second) {
			t.Errorf("%s: different posts deduplicated", tc.name)
		}
	}
	first := jx.M{"url": "https://www.youtube.com/watch?v=abcdefghijk"}
	tracked := jx.M{"url": "https://www.youtube.com/watch?utm_source=test&v=abcdefghijk&t=10"}
	if CandidatePostKey(first) != CandidatePostKey(tracked) {
		t.Fatal("tracking parameters must not create a different YouTube post")
	}
}

func TestVideoFilterRecognizesDeferredVideos(t *testing.T) {
	for _, page := range []string{"https://www.youtube.com/watch?v=abcdefghijk", "https://www.tiktok.com/@example/video/123456789"} {
		post := jx.M{"video_via": "ytdlp", "video_page": page}
		if !PostItemHasVideo(post) || !poolMatchesDisplayFilters(post, jx.M{"video_only": true}, nil) {
			t.Errorf("deferred video filtered out: %s", page)
		}
		post["video_unavailable"] = true
		if PostItemHasVideo(post) {
			t.Errorf("unavailable video treated as playable: %s", page)
		}
	}
}

func TestPostSearchAdsDoNotDiscardFinancialNews(t *testing.T) {
	for _, text := range []string{"Акции компании выросли на бирже", "Сколько зарабатывают разработчики", "Котировки акций и доходность облигаций"} {
		if PostSearchIsAd(jx.M{"text": text}) {
			t.Errorf("financial news marked as an ad: %q", text)
		}
	}
	for _, text := range []string{"Скидка 50% по промокоду", "Подписывайтесь на наш канал", "Казино бесплатно"} {
		if !PostSearchIsAd(jx.M{"text": text}) {
			t.Errorf("obvious advertisement not detected: %q", text)
		}
	}
}
