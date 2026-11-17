package posta

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestAutopilotTikTokBingFallback(t *testing.T) {
	calls := 0
	brave := func(string) FetchResultURL { calls++; return FetchResultURL{FetchResult: FetchResult{Status: 429}} }
	fetch := func(raw string, max int64, _ time.Duration, _ string) FetchResultURL {
		calls++
		if !strings.HasPrefix(raw, "https://www.bing.com/search?") || max != rssMaxBytes {
			t.Fatal("unbounded search")
		}
		feed := `<rss><channel><item><link>https://www.tiktok.com/@cats/video/123456789?track=1</link></item><item><link>https://evil.test/@cats/video/123456789</link></item><item><link>https://www.tiktok.com.evil.test/@cats/video/123456789</link></item><item><link>https://www.tiktok.com/@other</link></item></channel></rss>`
		return FetchResultURL{URL: "https://www.bing.com/search?q=test", FetchResult: FetchResult{OK: true, Bytes: []byte(feed)}}
	}
	r := autopilotTikTokSearchWith("site:tiktok.com cats", brave, fetch)
	if calls != 2 || !autopilotTikTokSearchResponseValid(r) || string(r.Bytes) != "https://www.tiktok.com/@cats/video/123456789\nhttps://www.tiktok.com/@other" {
		t.Fatalf("fallback: calls=%d result=%q", calls, r.Bytes)
	}
}

func TestAutopilotTikTokBingWrappedLinks(t *testing.T) {
	wrap := func(target string) string {
		return "https://www.bing.com/ck/a?u=" + url.QueryEscape("a1"+base64.RawURLEncoding.EncodeToString([]byte(target)))
	}
	valid := wrap("https://www.tiktok.com/@cats/video/123456789?track=1")
	bad := wrap("https://evil.test/@cats/video/123456789")
	feed := `<rss><channel><item><link>` + valid + `</link></item><item><link>` + bad + `</link></item></channel></rss>`
	r := FetchResultURL{URL: "https://www.bing.com/search", FetchResult: FetchResult{OK: true, Bytes: []byte(feed)}}
	links := autopilotTikTokBingLinks(r)
	if len(links) != 1 || links[0] != "https://www.tiktok.com/@cats/video/123456789" {
		t.Fatalf("wrapped links: %v", links)
	}
	if got := autopilotTikTokUnwrapBing("https://www.bing.com/ck/a?u=a1invalid%25"); got != "" {
		t.Fatal("invalid encoding accepted")
	}
}
func TestAutopilotTikTokBingDoesNotClaimUnrelatedResults(t *testing.T) {
	r := FetchResultURL{URL: "https://www.bing.com/search", FetchResult: FetchResult{OK: true, Bytes: []byte(`<rss><channel><item><link>https://example.com/</link></item></channel></rss>`)}}
	if got := autopilotTikTokBingLinks(r); len(got) != 0 {
		t.Fatal("unrelated results accepted")
	}
	r.URL = "https://evil.test/search"
	if got := autopilotTikTokBingLinks(r); len(got) != 0 {
		t.Fatal("untrusted search accepted")
	}
}

func TestAutopilotTikTokLiveSearch(t *testing.T) {
	if os.Getenv("POSTA_TEST_TIKTOK_SEARCH_LIVE") != "1" {
		t.Skip("set POSTA_TEST_TIKTOK_SEARCH_LIVE=1")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	ctx = contextWithAutopilotLease(ctx, &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error {
		calls++
		if calls > 6 {
			return errors.New("test request budget exhausted")
		}
		return nil
	})
	u := (&App{}).User(ctx, "", nil)
	r := u.autopilotTikTokSearch("site:tiktok.com cats")
	if !autopilotTikTokSearchResponseValid(r) {
		t.Fatalf("search unavailable after %d budgeted requests (HTTP %d)", calls, r.Status)
	}
	candidates := SourceLinkedSources(string(r.Bytes))
	n := 0
	for _, c := range candidates {
		if c.Platform == PlatformTikTok {
			n++
		}
	}
	if n == 0 {
		t.Fatalf("no TikTok candidates after %d budgeted requests", calls)
	}
	t.Logf("candidates=%d budgeted requests=%d", n, calls)
}

func TestAutopilotTikTokBingCooldownAvoidsNetwork(t *testing.T) {
	autopilotTikTokBingPause.Lock()
	old := autopilotTikTokBingPause.until
	autopilotTikTokBingPause.until = time.Now().Add(time.Hour)
	autopilotTikTokBingPause.Unlock()
	t.Cleanup(func() {
		autopilotTikTokBingPause.Lock()
		autopilotTikTokBingPause.until = old
		autopilotTikTokBingPause.Unlock()
	})
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error { calls++; return errors.New("unexpected request") })
	result := (&App{}).User(ctx, "", nil).autopilotTikTokBingFetch("https://www.bing.com/search", rssMaxBytes, time.Second, BrowserUA)
	if result.Status != 429 || calls != 0 {
		t.Fatalf("cooldown status=%d requests=%d", result.Status, calls)
	}
}

func TestAutopilotTikTokEmptyBraveFallsBack(t *testing.T) {
	calls := 0
	brave := func(string) FetchResultURL {
		return FetchResultURL{URL: "https://search.brave.com/search", FetchResult: FetchResult{OK: true, Bytes: []byte("<html>no indexed links</html>")}}
	}
	bing := func(string, int64, time.Duration, string) FetchResultURL {
		calls++
		return FetchResultURL{URL: "https://www.bing.com/search", FetchResult: FetchResult{OK: true, Bytes: []byte(`<rss><channel><item><link>https://www.tiktok.com/@cats/video/123456789</link></item></channel></rss>`)}}
	}
	r := autopilotTikTokSearchWith("site:tiktok.com/@cats/video/", brave, bing)
	if calls != 1 || !strings.Contains(string(r.Bytes), "/video/123456789") {
		t.Fatal("empty Brave response prevented fallback")
	}
	calls = 0
	linked := func(string) FetchResultURL {
		return FetchResultURL{URL: "https://search.brave.com/search", FetchResult: FetchResult{OK: true, Bytes: []byte("https://www.tiktok.com/@cats/video/123456789")}}
	}
	autopilotTikTokSearchWith("site:tiktok.com/@cats/video/", linked, bing)
	if calls != 0 {
		t.Fatal("unnecessary fallback request")
	}
}
