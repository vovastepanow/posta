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
