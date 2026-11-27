package posta

import (
	"testing"
	"time"
)

func TestAutopilotReadRSSUsesBoundedSamePublisherAlternate(t *testing.T) {
	const feed = "https://example.org/news"
	calls := 0
	got := autopilotReadRSS(feed, func(raw string, limit int64, _ time.Duration, _ string) FetchResultURL {
		calls++
		switch calls {
		case 1:
			if raw != feed || limit != rssMaxBytes {
				t.Fatal("unexpected initial fetch")
			}
			return FetchResultURL{URL: raw, FetchResult: FetchResult{Status: 200, TooBig: true}}
		case 2:
			if raw != feed || limit != 5<<20 {
				t.Fatal("wrong size fallback")
			}
			return FetchResultURL{URL: raw, FetchResult: FetchResult{OK: true, Bytes: []byte(`<link rel="alternate" type="application/rss+xml" href="https://evil.example/feed"><link rel="alternate" type="application/atom+xml" href="/atom.xml">`)}}
		case 3:
			if raw != "https://example.org/atom.xml" {
				t.Fatalf("incorrect alternate %q", raw)
			}
			return FetchResultURL{URL: raw, FetchResult: FetchResult{OK: true, Bytes: []byte(`<feed xmlns="http://www.w3.org/2005/Atom"><title>Example</title></feed>`)}}
		default:
			t.Fatal("unbounded requests")
			return FetchResultURL{}
		}
	})
	if !got.OK || calls != 3 || !rssIsFeed(rssLoad(got.Bytes)) {
		t.Fatalf("alternate not recovered: %+v, calls=%d", got, calls)
	}
}

func TestAutopilotReadRSSDoesNotRetryRestrictedAccess(t *testing.T) {
	calls := 0
	r := autopilotReadRSS("https://example.org/feed", func(raw string, _ int64, _ time.Duration, _ string) FetchResultURL {
		calls++
		return FetchResultURL{URL: raw, FetchResult: FetchResult{Status: 403}}
	})
	if calls != 1 || autopilotFeedReadReason(r) != "feed_access_restricted" {
		t.Fatalf("restricted request retried or misclassified: %+v, calls=%d", r, calls)
	}
}

func TestRSSLoadDecodesDeclaredWindows1251(t *testing.T) {
	body := append([]byte(`<?xml version="1.0" encoding="windows-1251"?><rss><channel><title>`), 0xcf, 0xf0, 0xe8, 0xe2, 0xe5, 0xf2)
	body = append(body, []byte(`</title></channel></rss>`)...)
	doc := rssLoad(body)
	if !rssIsFeed(doc) || doc.kid("channel").kid("title").text() != "Привет" {
		t.Fatalf("legacy feed not decoded: %+v", doc)
	}
}

func TestAutopilotParseReviewListingKeepsFailuresIndependent(t *testing.T) {
	const feed = "https://example.org/feed"
	listing := func(body string) FetchResultURL {
		return FetchResultURL{URL: feed, FetchResult: FetchResult{OK: true, Bytes: []byte(body)}}
	}
	if _, reason := autopilotParseReviewListing(PlatformTikTok, "example", "tiktok:example", listing("{")); reason != "feed_parse_failed" {
		t.Fatalf("malformed JSON: %q", reason)
	}
	if _, reason := autopilotParseReviewListing(PlatformRSS, feed, feed, listing("<html><body>not a feed</body></html>")); reason != "unrecognized_feed" {
		t.Fatalf("unrecognized listing: %q", reason)
	}
	if posts, reason := autopilotParseReviewListing(PlatformTikTok, "example", "tiktok:example", listing(`[{"text":"example"}]`)); reason != "" || len(posts) != 1 || posts[0]["text"] != "example" {
		t.Fatalf("valid JSON listing: %v, %q", posts, reason)
	}
	if posts, reason := autopilotParseReviewListing(PlatformRSS, feed, feed, listing(`<feed xmlns="http://www.w3.org/2005/Atom"><title>Example</title></feed>`)); reason != "" || len(posts) != 0 {
		t.Fatalf("valid empty feed: %v, %q", posts, reason)
	}
}
