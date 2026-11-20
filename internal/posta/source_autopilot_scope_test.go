package posta

import (
	"context"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestAutopilotDiscoveryKeysSeparateScopesAndProfiles(t *testing.T) {
	makeUser := func(scope string) *U {
		return &U{Ctx: contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: scope})}
	}
	a, b := makeUser("user:a"), makeUser("user:b")
	if a.discoveryProgressKey("C++") == b.discoveryProgressKey("C++") || a.discoveryProgressKey("C++") == a.discoveryProgressKey("C") {
		t.Fatal("progress keys merged scopes or profiles")
	}
	if a.discoveryScopedKey("discovery_deferred") == b.discoveryScopedKey("discovery_deferred") || a.discoveryScopedTopic("C++") == a.discoveryScopedTopic("C") {
		t.Fatal("deferred candidates merged scopes or profiles")
	}
	legacy := &U{Ctx: context.Background()}
	if legacy.discoveryScopedKey("discovery_deferred") != "discovery_deferred" || legacy.discoveryScopedTopic("Тема") != TopicNorm("Тема") {
		t.Fatal("legacy discovery keys changed")
	}
}

func TestAutopilotFreshRSSSamplesRejectsUnknownDatesAndDuplicates(t *testing.T) {
	at := time.Now().Unix()
	posts := []jx.M{
		{"url": "https://example.org/one", "posted_at": at - 60},
		{"url": "https://example.org/one?utm_source=copy", "posted_at": at - 60},
		{"url": "https://example.org/no-date"},
		{"url": "https://example.org/old", "posted_at": at - 90*86400},
		{"url": "https://example.org/future", "posted_at": at + 3600},
		{"source_url": "https://example.org/feed", "posted_at": at - 60},
	}
	got := autopilotFreshRSSSamples(posts, at)
	if len(got) != 1 {
		t.Fatalf("fresh unique samples: %+v", got)
	}
}

func TestAutopilotSourceQualityAcceptsRareHistoricalPosts(t *testing.T) {
	at := time.Now().Unix()
	posts := []jx.M{
		{"url": "https://example.org/quality-a", "posted_at": at - 46*86400},
		{"url": "https://example.org/quality-b", "posted_at": at - 150*86400},
		{"url": "https://example.org/quality-duplicate", "posted_at": at - 500*86400},
		{"url": "https://example.org/quality-nodate"},
	}
	if got := autopilotFreshRSSSamples(posts, at); len(got) != 0 {
		t.Fatalf("old posts incorrectly treated as live content: %+v", got)
	}
	quality := autopilotSourceQualitySamples(posts, at)
	if len(quality) != 2 {
		t.Fatalf("rare publisher must retain its dated history for source review: %+v", quality)
	}
}

func TestAutopilotFreshRSSSamplesSpreadAcrossTime(t *testing.T) {
	at := time.Now().Unix()
	posts := []jx.M{
		{"url": "https://example.org/old", "posted_at": at - 300},
		{"url": "https://example.org/middle", "posted_at": at - 200},
		{"url": "https://example.org/new", "posted_at": at - 100},
	}
	samples := autopilotFreshRSSSamples(posts, at)
	if len(samples) != 3 || jx.Str(samples[0], "url") != "https://example.org/new" || jx.Str(samples[1], "url") != "https://example.org/old" {
		t.Fatalf("samples not spread across time: %+v", samples)
	}
}

func TestAutopilotPublicPostURLRejectsCredentials(t *testing.T) {
	for _, tc := range []struct {
		raw         string
		media, want bool
	}{
		{"https://example.org/post?utm_source=rss", false, true},
		{"https://example.org/post?access_token=secret", false, false},
		{"https://user:pass@example.org/post", false, false},
		{"https://example.org/image.jpg?signature=secret", true, false},
		{"http://example.org/image.jpg", true, false},
		{"https://example.org/image.jpg", true, true},
	} {
		if got := autopilotPublicPostURL(tc.raw, tc.media); got != tc.want {
			t.Errorf("%q media=%v: got %v", tc.raw, tc.media, got)
		}
	}
}

func TestAutopilotSharedDiscoveryRejectsPrivateScope(t *testing.T) {
	for _, tc := range []struct {
		job  *autopilotJob
		user string
		want bool
	}{
		{&autopilotJob{Scope: "public"}, "", true},
		{&autopilotJob{Scope: "public"}, "person", false},
		{&autopilotJob{Scope: "user:person"}, "person", false},
		{nil, "", false},
	} {
		if got := autopilotMayUseSharedDiscovery(tc.job, tc.user); got != tc.want {
			t.Fatalf("job=%+v user=%q got %v", tc.job, tc.user, got)
		}
	}
}

func TestAutopilotRSSIdentityPreservesPathCase(t *testing.T) {
	a, ok := autopilotPublicRSSID("https://EXAMPLE.org/Feed/A")
	if !ok {
		t.Fatal("public RSS rejected")
	}
	b, ok := autopilotPublicRSSID("https://example.org/feed/a")
	if !ok || a == b {
		t.Fatalf("case-sensitive feeds merged: %q %q", a, b)
	}
	c, ok := autopilotPublicRSSID("https://example.org/Feed/A")
	if !ok || a != c {
		t.Fatalf("host aliases not normalized: %q %q", a, c)
	}
	if _, ok := autopilotPublicRSSID("https://example.org/feed?token=secret"); ok {
		t.Fatal("query-bearing feed accepted")
	}
}

func TestAutopilotPublicSiteAdaptersUseCanonicalSafeIDs(t *testing.T) {
	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{"https://api.vc.ru/v2.10/timeline?sorting=new&subsitesids=199123",
			"rss:https://api.vc.ru/v2.10/timeline?sorting=new&subsitesids=199123", true},
		{"https://api.vc.ru/v2.10/timeline?sorting=new&subsitesIds=199123",
			"rss:https://api.vc.ru/v2.10/timeline?sorting=new&subsitesids=199123", true},
		{"https://habr.com/ru/rss/hubs/business_models/articles/all/?fl=ru",
			"rss:https://habr.com/ru/rss/hubs/business_models/articles/all/", true},
		{"https://pikabu.ru/community/lnk", "rss:https://pikabu.ru/community/lnk", true},
		{"https://api.vc.ru/v2.10/timeline?sorting=new&subsitesids=199123&token=secret", "", false},
		{"https://api.vc.ru.evil.example/v2.10/timeline?sorting=new&subsitesids=199123", "", false},
		{"https://habr.com/ru/rss/hubs/business_models/articles/all/?fl=ru&key=secret", "", false},
		{"https://pikabu.ru/community/lnk?access_token=secret", "", false},
		{"https://www.youtube.com/feeds/videos.xml?channel_id=ucabcdefghijklmnopqrstuv", "", false},
	}
	for _, tc := range cases {
		id, ok := autopilotPublicRSSID(tc.input)
		if ok != tc.ok || id != tc.want {
			t.Errorf("public %q got (%q,%v), want (%q,%v)", tc.input, id, ok, tc.want, tc.ok)
		}
		if tc.ok {
			platform, _, _, valid := autopilotSourceParts(id)
			if !valid || platform != PlatformRSS || SourceIDFor(platform, id[4:]) != id {
				t.Errorf("valid source not accepted for normal review: %q", id)
			}
		}
	}
	vc := "https://api.vc.ru/v2.10/timeline?sorting=new&subsitesids=199123"
	if host, id := osnovaTimelineSource(vc); host != "vc.ru" || id != 199123 {
		t.Fatalf("legacy osnova community not recognized: %s/%d", host, id)
	}
	if !sharedSourceEligible(PlatformRSS, jx.M{"id": vc}) {
		t.Fatal("recognized public vc.ru community excluded from shared collector")
	}
}
