package posta

import "testing"

func TestDiscoveryMatchesPlatform(t *testing.T) {
	cases := []struct {
		platform, source, id string
		want                 bool
	}{
		{"telegram", PlatformTelegram, "channel", true},
		{"vk", PlatformTelegram, "channel", false},
		{"youtube", PlatformRSS, "https://www.youtube.com/feeds/videos.xml?channel_id=UC123", true},
		{"reddit", PlatformRSS, "https://www.reddit.com/r/golang/", true},
		{"x", PlatformRSS, "https://twitter.com/example", true},
		{"rss", PlatformRSS, "https://example.com/feed", true},
		{"rss", PlatformRSS, "https://www.reddit.com/r/golang/", false},
		{"", PlatformTelegram, "channel", true},
		{"invalid", PlatformTelegram, "channel", false},
	}
	for _, tc := range cases {
		if got := discoveryMatchesPlatform(DiscoveryCandidate{Platform: tc.source, ID: tc.id}, tc.platform); got != tc.want {
			t.Errorf("platform %q source %q: got %v", tc.platform, tc.id, got)
		}
	}
}

func TestDiscoveryParkedCandidatesRespectPlatform(t *testing.T) {
	tg := DiscoveryCandidate{Platform: PlatformTelegram, ID: "channel"}
	x := DiscoveryCandidate{Platform: PlatformRSS, ID: "https://x.com/example"}
	ready, other := discoveryPartitionParked([]DiscoveryCandidate{tg, x}, "x")
	if len(ready) != 1 || ready[0].ID != x.ID || len(other) != 1 || other[0].ID != tg.ID {
		t.Fatalf("wrong partition: ready=%v other=%v", ready, other)
	}
	ready, other = discoveryPartitionParked(other, "x")
	if len(ready) != 0 || len(other) != 1 {
		t.Fatalf("other platform requeued: %v %v", ready, other)
	}
}
