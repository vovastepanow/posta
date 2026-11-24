package posta

import "testing"

func TestAutopilotYouTubeRecoveryRequiresOfficialExactIdentity(t *testing.T) {
	const id = "UCAbCdEfghijklmnopqrstuv"
	feed:=YouTubeChannelFeedURL(id)
	old:="rss:https://www.youtube.com/feeds/videos.xml?channel_id=ucabcdefghijklmnopqrstuv"
	if got:=autopilotYouTubeRecoveryFeed(old,[]string{id});got!=feed {
		t.Fatalf("repair lead=%q want %q",got,feed)
	}
	if got:=autopilotYouTubeRecoveryFeed(old,[]string{id,"UCabcdefghijklmnopqrstuv"});got!="" {
		t.Fatal("ambiguous lowercase identity guessed")
	}
	r:=FetchResultURL{URL:feed,FetchResult:FetchResult{OK:true,Bytes:[]byte(`<feed xmlns="http://www.w3.org/2005/Atom" xmlns:yt="http://www.youtube.com/xml/schemas/2015"><yt:channelId>`+id+`</yt:channelId><entry><yt:channelId>`+id+`</yt:channelId></entry></feed>`)}}
	if !autopilotVerifiedYouTubeIdentity(feed,r) {
		t.Fatal("official feed identity rejected")
	}
	r.Bytes=[]byte(`<feed xmlns="http://www.w3.org/2005/Atom" xmlns:yt="http://www.youtube.com/xml/schemas/2015"><yt:channelId>`+id[2:]+`</yt:channelId><entry><yt:channelId>`+id+`</yt:channelId></entry></feed>`)
	if !autopilotVerifiedYouTubeIdentity(feed,r) {
		t.Fatal("real feed root without UC prefix rejected")
	}
	r.Bytes=[]byte(`<feed xmlns="http://www.w3.org/2005/Atom" xmlns:yt="http://www.youtube.com/xml/schemas/2015"><yt:channelId>`+id[2:]+`</yt:channelId><entry><yt:channelId>UCabcdefghijklmnopqrstuv</yt:channelId></entry></feed>`)
	if autopilotVerifiedYouTubeIdentity(feed,r) {
		t.Fatal("wrong case entry accepted")
	}
	r.URL="https://example.org/feed"
	if autopilotVerifiedYouTubeIdentity(feed,r) {
		t.Fatal("foreign redirect accepted")
	}
	r.URL=feed
	r.Bytes=[]byte(`<feed><title>Unrelated</title></feed>`)
	if autopilotVerifiedYouTubeIdentity(feed,r) {
		t.Fatal("unverified XML accepted")
	}
}
