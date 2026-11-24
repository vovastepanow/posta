package posta

import (
	"context"
	"testing"
	"time"
)

func TestAutopilotNativeYouTubeSearchOnlyMainResults(t *testing.T) {
	page := `<html><script>var ytInitialData = {
		"contents":{"twoColumnSearchResultsRenderer":{
			"primaryContents":{"sectionListRenderer":{"contents":[
				{"itemSectionRenderer":{"contents":[
					{"videoRenderer":{"videoId":"abcdefghijk","ownerText":{"runs":[{"navigationEndpoint":{"browseEndpoint":{"browseId":"UCabcdefghijklmnopqrstuv"}}}]}}},
					{"channelRenderer":{"channelId":"UCabcdefghijklmnopqrstuv"}},
					{"channelRenderer":{"channelId":"UCABCDEFGHIJKLMNOPQRSTUV"}},
					{"videoRenderer":{"videoId":"invalid","ownerText":{"runs":[{"navigationEndpoint":{"browseEndpoint":{"browseId":"UC1111111111111111111111"}}}]}}},
					{"promotedSparklesWebRenderer":{"channelRenderer":{"channelId":"UC2222222222222222222222"}}},
					{"videoRenderer":{"videoId":"abcdefghijk","ownerText":{"runs":[{"navigationEndpoint":{"browseEndpoint":{"browseId":"UC3333333333333333333333"}}},{"text":"second author"}]}}}
				]}}
			]}},
			"secondaryContents":{"channelRenderer":{"channelId":"UC4444444444444444444444"}}
		}}
	};</script></html>`
	r := FetchResultURL{URL: "https://www.youtube.com/results?search_query=astronomy", FetchResult: FetchResult{OK: true, Bytes: []byte(page)}}
	got, err := autopilotYouTubeSearchCandidates(r)
	if err != nil || len(got) != 2 {
		t.Fatalf("search results: %+v %v", got, err)
	}
	for i, id := range []string{"UCabcdefghijklmnopqrstuv", "UCABCDEFGHIJKLMNOPQRSTUV"} {
		if got[i].ID != YouTubeChannelFeedURL(id) {
			t.Fatalf("wrong canonical channel %q", got[i].ID)
		}
	}
	r.URL = "https://consent.youtube.com/results"
	if _, err := autopilotYouTubeSearchCandidates(r); err == nil {
		t.Fatal("consent page was accepted")
	}
	r.URL = "https://www.youtube.com/results"
	r.Bytes = []byte(`<script>var ytInitialData = {"contents":{}};</script>`)
	if _, err := autopilotYouTubeSearchCandidates(r); err == nil {
		t.Fatal("unknown search layout was treated as an empty result")
	}
}

func TestAutopilotNativeYouTubeSearchHonorsRateLimit(t *testing.T) {
	autopilotNativeYouTubePause.Lock()
	old := autopilotNativeYouTubePause.until
	autopilotNativeYouTubePause.until = time.Time{}
	autopilotNativeYouTubePause.Unlock()
	t.Cleanup(func() {
		autopilotNativeYouTubePause.Lock()
		autopilotNativeYouTubePause.until = old
		autopilotNativeYouTubePause.Unlock()
	})
	u := (&App{}).User(context.Background(), "", nil)
	calls := 0
	fetch := func(string, int64, time.Duration, string) FetchResultURL {
		calls++
		return FetchResultURL{FetchResult: FetchResult{Status: 429, RetryAfter: "7200"}}
	}
	if _, err := u.autopilotNativeYouTubeSearchWith("cats", fetch); err == nil {
		t.Fatal("rate limit treated as a successful search")
	}
	if _, err := u.autopilotNativeYouTubeSearchWith("photography", fetch); err == nil || calls != 1 {
		t.Fatalf("rate-limit cooldown not enforced: calls=%d err=%v", calls, err)
	}
}
