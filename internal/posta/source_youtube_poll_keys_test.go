package posta

import (
	"crypto/sha1"
	"encoding/hex"
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestYouTubePollAndUpdateKeysPreserveCase(t *testing.T) {
	a := YouTubeChannelFeedURL("UCabcdefghijklmnopqrstuv")
	b := YouTubeChannelFeedURL("UCAbcdefghijklmnopqrstuv")
	alias := strings.Replace(a, "www.youtube.com", "YOUTUBE.COM", 1)
	shorts := RSSYouTubeShortsFeed("UCabcdefghijklmnopqrstuv")
	shortsB := RSSYouTubeShortsFeed("UCAbcdefghijklmnopqrstuv")
	cases := map[string]func(string) string{
		"poll":           func(id string) string { return sourcePollMapKey(PlatformRSS, id) },
		"personal-cache": func(id string) string { return personalSourcePollKey("user", PlatformRSS, id) },
		"catalog-update": func(id string) string { return catalogSourceUpdateKey(CatalogKind(PlatformRSS), PlatformRSS, id) },
	}
	for name, key := range cases {
		t.Run(name, func(t *testing.T) {
			if key(a) == key(b) || key(shorts) == key(shortsB) {
				t.Fatal("case-sensitive IDs collided")
			}
			if key(a) != key(alias) {
				t.Fatal("host aliases did not canonicalize")
			}
			if key(a) == key(shorts) {
				t.Fatal("videos and shorts collided")
			}
			if key(a) == key(strings.ToLower(a)) {
				t.Fatal("legacy invalid lowercase identity matched valid channel")
			}
		})
	}
	if personalSourcePollKey("a", PlatformRSS, a) == personalSourcePollKey("b", PlatformRSS, a) {
		t.Fatal("personal cache leaked across users")
	}
	old := sha1.Sum([]byte(PlatformRSS + "|" + strings.ToLower(a)))
	if SourcePollCacheKey(PlatformRSS, a) == "source_posts:"+hex.EncodeToString(old[:]) {
		t.Fatal("reused ambiguous old cache")
	}
}

func TestNonYouTubePollKeysRemainCompatible(t *testing.T) {
	for _, tc := range []struct{ platform, id string }{
		{PlatformTelegram, "ExampleChannel"},
		{PlatformRSS, "https://Example.org/Feed.xml"},
	} {
		old := sha1.Sum([]byte(tc.platform + "|" + strings.ToLower(tc.id)))
		if SourcePollCacheKey(tc.platform, tc.id) != "source_posts:"+hex.EncodeToString(old[:]) {
			t.Fatal("unrelated cache format changed")
		}
		if sourcePollMapKey(tc.platform, tc.id) != tc.platform+"|"+strings.ToLower(tc.id) {
			t.Fatal("unrelated poll key changed")
		}
	}
}

func TestYouTubeCatalogBatchUpdateDoesNotOverwriteCaseVariant(t *testing.T) {
	a := YouTubeChannelFeedURL("UCabcdefghijklmnopqrstuv")
	b := YouTubeChannelFeedURL("UCAbcdefghijklmnopqrstuv")
	d := &SourceDB{RSS: []jx.M{{"id": a, "name": "A"}, {"id": b, "name": "B"}}}
	key := catalogSourceUpdateKey(CatalogKind(PlatformRSS), PlatformRSS, b)
	if !applyCatalogEntryUpdates(d, map[string]jx.M{key: {"id": b, "name": "Updated B"}}) {
		t.Fatal("expected catalog update")
	}
	if len(d.RSS) != 2 || jx.Str(d.RSS[0], "id") != a || jx.Str(d.RSS[0], "name") != "A" ||
		jx.Str(d.RSS[1], "id") != b || jx.Str(d.RSS[1], "name") != "Updated B" {
		t.Fatalf("updating one channel affected another: %#v", d.RSS)
	}
}
