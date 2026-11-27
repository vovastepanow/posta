package posta

import "testing"

func TestRecommendationSourceKeyYouTubeVariants(t *testing.T) {
	videos := "rss:https://www.youtube.com/feeds/videos.xml?channel_id=UCabcdefghijklmnopqrstuv"
	shorts := "rss:https://www.youtube.com/feeds/videos.xml?playlist_id=UUSHabcdefghijklmnopqrstuv"
	if recommendationSourceKey(videos) != recommendationSourceKey(shorts) {
		t.Fatalf("channel variants differ: %s %s", recommendationSourceKey(videos), recommendationSourceKey(shorts))
	}
	if recommendationSourceKey("telegram:One") == recommendationSourceKey("telegram:Two") {
		t.Fatal("different sources collapsed")
	}
}
