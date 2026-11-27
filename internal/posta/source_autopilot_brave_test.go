package posta

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestAutopilotBraveCooldownAvoidsNetwork(t *testing.T) {
	bravePause.Lock()
	old := bravePause.until
	bravePause.until = time.Now().Add(time.Hour)
	bravePause.Unlock()
	t.Cleanup(func() { bravePause.Lock(); bravePause.until = old; bravePause.Unlock() })
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error { calls++; return errors.New("unexpected fetch") })
	u := (&App{}).User(ctx, "", nil)
	r := u.autopilotBraveSearch("site:youtube.com science")
	if r.Status != 429 || calls != 0 {
		t.Fatalf("status=%d requests=%d", r.Status, calls)
	}
}

func TestAutopilotBraveRateLimitHonorsRetryAfter(t *testing.T) {
	bravePause.Lock()
	old := bravePause.until
	bravePause.until = time.Time{}
	bravePause.Unlock()
	t.Cleanup(func() { bravePause.Lock(); bravePause.until = old; bravePause.Unlock() })
	u := (&App{}).User(context.Background(), "", nil)
	calls := 0
	fetch := func(string, int64, time.Duration, string) FetchResultURL {
		calls++
		return FetchResultURL{FetchResult: FetchResult{Status: 429, RetryAfter: "172800"}}
	}
	start := time.Now()
	u.autopilotBraveSearchWith("site:youtube.com cats", fetch)
	bravePause.Lock()
	until := bravePause.until
	bravePause.Unlock()
	if until.Before(start.Add(48 * time.Hour)) {
		t.Fatalf("Retry-After ignored: %v", until)
	}
	u.autopilotBraveSearchWith("site:vk.com cats", fetch)
	if calls != 1 {
		t.Fatalf("cooldown made %d requests", calls)
	}
}

func TestAutopilotBraveRetryDelay(t *testing.T) {
	at := time.Unix(1800000000, 0).UTC()
	if got := autopilotBraveRetryAt(at, ""); !got.Equal(at.Add(34 * time.Minute)) {
		t.Fatalf("default cooldown: %v", got)
	}
	until := at.Add(2 * time.Hour)
	if got := autopilotBraveRetryAt(at, until.Format(http.TimeFormat)); got.Before(until) {
		t.Fatalf("date Retry-After ignored: %v", got)
	}
}

func TestAutopilotSearchLinksUseActualPlatformDestinations(t *testing.T) {
	page := `<html>
<script>const data = "https://t.me/scriptnoise";</script>
<!-- https://t.me/commentnoise -->
<p>https://t.me/snippetnoise</p>
<a href="https://evil.example/t.me/pathnoise">Fake path</a>
<a href="https://search.brave.com/redirect?u=https://t.me/querynoise">Tracker</a>
<a href="https://t.me.evil.example/fakechannel">Fake host</a>
<a href="https://t.me/s/garden_digest/123">Real post</a>
<a href="https://telegram.me/garden_digest">Same publisher</a>
<a href="https://t.me/garden_digest/invalid">Invalid post path</a>
<a href="https://vk.com/wall-123_456">Real community post</a>
<a href="https://vk.com/wall123_456">Personal wall</a>
<a href="https://vk.com/video-123_456">Video route</a>
<a href="https://vk.com/videomaker">Real publisher</a>
<a href="https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv/videos">Real channel</a>
<a href="https://www.youtube.com/channel/UCA bcdefghijklmnopqrstuv">Broken channel</a>
<a href="https://www.youtube.com/@garden_creator">Handle</a>
<a href="https://www.youtube.com/watch?v=123&other=https://youtube.com/channel/UCabcdefghijklmnopqrstuv">Not a channel result</a>
<a href="https://www.tiktok.com/@gardencreator">Real TikTok account</a>
</html>`
	r := FetchResultURL{URL: "https://search.brave.com/search", FetchResult: FetchResult{OK: true, Bytes: []byte(page)}}
	for _, tc := range []struct {
		platform string
		want []string
	}{
		{PlatformTelegram, []string{"garden_digest"}},
		{PlatformVK, []string{"club123", "videomaker"}},
		{PlatformRSS, []string{YouTubeChannelFeedURL("UCabcdefghijklmnopqrstuv"), "https://www.youtube.com/@garden_creator"}},
		{PlatformTikTok, []string{"gardencreator"}},
	} {
		got := autopilotSearchLinkedCandidates(r, tc.platform)
		if len(got) != len(tc.want) {
			t.Fatalf("%s: got %+v, want %v", tc.platform, got, tc.want)
		}
		for i, c := range got {
			if c.ID != tc.want[i] {
				t.Fatalf("%s: got %q, want %q", tc.platform, c.ID, tc.want[i])
			}
		}
	}
	r.URL = "https://evil.example/search"
	if got := autopilotSearchLinkedCandidates(r, PlatformTelegram); len(got) != 0 {
		t.Fatal("untrusted search response accepted")
	}
}

func TestAutopilotSearchLinksPreserveValidatedBingFallback(t *testing.T) {
	r := FetchResultURL{URL: "https://www.bing.com/search", FetchResult: FetchResult{OK: true,
		Bytes: []byte("https://www.tiktok.com/@cats/video/123456789\nhttps://www.tiktok.com/@cats\nhttps://evil.example/@unrelated")}}
	got := autopilotSearchLinkedCandidates(r, PlatformTikTok)
	if len(got) != 1 || got[0].ID != "cats" {
		t.Fatalf("Bing fallback lost or polluted: %+v", got)
	}
}

func TestAutopilotPublisherSearchUsesBingWhenBraveUnavailable(t *testing.T) {
	brave := func(string) FetchResultURL {
		return FetchResultURL{FetchResult: FetchResult{Status: 429}}
	}
	calls := 0
	bing := func(raw string, limit int64, _ time.Duration, _ string) FetchResultURL {
		calls++
		if !strings.HasPrefix(raw, "https://www.bing.com/search?") || limit != rssMaxBytes {
			t.Fatal("unbounded or untrusted fallback")
		}
		body := `<rss><channel>
<item><link>https://t.me/s/camera_digest/123</link></item>
<item><link>https://t.me.evil.example/fakechannel</link></item>
<item><link>https://example.org/unrelated</link></item>
<item><link>https://www.youtube.com/channel/UCabcdefghijklmnopqrstuv</link></item>
</channel></rss>`
		return FetchResultURL{URL: raw, FetchResult: FetchResult{OK: true, Bytes: []byte(body)}}
	}
	for _, tc := range []struct{ platform, want string }{
		{PlatformTelegram, "camera_digest"},
		{PlatformRSS, YouTubeChannelFeedURL("UCabcdefghijklmnopqrstuv")},
	} {
		got := autopilotPublisherSearchWith("camera lenses", tc.platform, brave, bing)
		candidates := autopilotSearchLinkedCandidates(got, tc.platform)
		if !autopilotPublisherSearchResponseValid(got) || len(candidates) != 1 || candidates[0].ID != tc.want {
			t.Fatalf("fallback lost real result or accepted unrelated host: %+v", candidates)
		}
	}
	if calls != 2 {
		t.Fatalf("fallback calls=%d", calls)
	}
	healthy := func(string) FetchResultURL {
		return FetchResultURL{URL: "https://search.brave.com/search", FetchResult: FetchResult{
			OK: true, Bytes: []byte(`<a href="https://t.me/camera_digest">Camera tutorials</a>`),
		}}
	}
	autopilotPublisherSearchWith("camera lenses", PlatformTelegram, healthy, bing)
	if calls != 2 {
		t.Fatal("healthy primary search caused an extra fallback request")
	}
}

func TestAutopilotPublisherSearchBingHTMLFallback(t *testing.T) {
	brave := func(string) FetchResultURL {
		return FetchResultURL{FetchResult: FetchResult{Status: 429}}
	}
	calls := 0
	bing := func(raw string, _ int64, _ time.Duration, _ string) FetchResultURL {
		calls++
		body := `<rss><channel><item><link>https://example.org/unrelated</link></item></channel></rss>`
		if !strings.Contains(raw, "format=rss") {
			body = `<html><body>
<a href="https://t.me/navigation_noise">Navigation</a>
<ul><li class="b_algo"><h2><a href="https://t.me/s/real_channel/12">Real channel</a></h2>
<a href="https://t.me/snippet_noise">Not a result title</a></li>
<li class="b_ad"><h2><a href="https://t.me/advert_noise">Ad</a></h2></li>
<li class="b_algo"><h2><a href="https://t.me.evil.org/fake_channel">Fake</a></h2></li></ul>
</body></html>`
		}
		return FetchResultURL{URL: raw, FetchResult: FetchResult{OK: true, Status: 200, Bytes: []byte(body)}}
	}
	result := autopilotPublisherSearchWith("camera lenses", PlatformTelegram, brave, bing)
	got := autopilotSearchLinkedCandidates(result, PlatformTelegram)
	if calls != 2 || !autopilotPublisherSearchResponseValid(result) || len(got) != 1 || got[0].ID != "real_channel" {
		t.Fatalf("fallback calls=%d candidates=%+v", calls, got)
	}
	calls = 0
	blocked := func(raw string, _ int64, _ time.Duration, _ string) FetchResultURL {
		calls++
		return FetchResultURL{URL: raw, FetchResult: FetchResult{Status: 429}}
	}
	autopilotPublisherSearchWith("camera lenses", PlatformTelegram, brave, blocked)
	if calls != 1 {
		t.Fatal("blocked provider retried")
	}
}

func TestAutopilotPublisherSearchStatusDistinguishesOffsiteResults(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{`<rss><channel><item><link>https://example.org/cats</link></item></channel></rss>`, "unavailable"},
		{`<rss><channel></channel></rss>`, "empty"},
		{`<rss><channel><item><link>https://t.me/real_channel</link></item></channel></rss>`, "ok"},
		{`<html><li class="b_algo"><h2><a href="https://example.org/cats">Cats</a></h2></li></html>`, "unavailable"},
	} {
		r := FetchResultURL{URL: "https://www.bing.com/search", FetchResult: FetchResult{OK: true, Status: 200, Bytes: []byte(tc.body)}}
		if got := autopilotPublisherSearchStatus(r, PlatformTelegram); got != tc.want {
			t.Fatalf("status=%s want=%s body=%s", got, tc.want, tc.body)
		}
	}
}
