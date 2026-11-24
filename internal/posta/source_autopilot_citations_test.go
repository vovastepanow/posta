package posta

import (
	"reflect"
	"testing"
)

func TestAutopilotCitationSources(t *testing.T) {
	channel := "UCcJ7z3yGO0V-X_P1qVuwQGg"
	text := "https://t.me/OwnChannel/123 https://t.me/s/MarketingDigest/42. https://t.me/marketingdigest https://t.me/+private https://t.me/share https://t.me/evilbot https://vk.com/public123 https://www.youtube.com/channel/" + channel + " https://youtube.com/watch?v=video https://t.me.evil.test/other https://t.me/nextchannel?secret=1"
	want := []string{"telegram:marketingdigest", "vk:public123", "rss:" + YouTubeChannelFeedURL(channel)}
	if got := autopilotCitationSources(text, "telegram:ownchannel", true, true, true); !reflect.DeepEqual(got, want) {
		t.Fatalf("citation candidates = %v, want %v", got, want)
	}
	if got := autopilotCitationSources(text, "", false, false, false); len(got) != 0 {
		t.Fatalf("disabled platforms discovered: %v", got)
	}
	for _, raw := range []string{"https://t.me:443/channelname", "https://user@t.me/channelname", "https://t.me/channelname#private", "https://t.me/c/123/45", "https://t.me/joinchat/abcdef", "https://vk.com/id123", "https://t.me/%63hannelname", "https://youtube.com/channel/not_a_channel"} {
		if got := autopilotCitationSource(raw, true, true, true); got != "" {
			t.Errorf("unsafe or ambiguous citation %q -> %q", raw, got)
		}
	}
}
