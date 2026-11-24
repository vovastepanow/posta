package posta

import (
	"encoding/json"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestAutopilotBlueskyTextSearchRejectsUnverifiedIdentityAndMedia(t *testing.T) {
	at := time.Now()
	did := "did:plc:abcdefghijklmnopqrstuvwx"
	other := "did:plc:zyxwvutsrqponmlkjihgfedc"
	row := func(key string) jx.M {
		return jx.M{"uri": "at://" + did + "/app.bsky.feed.post/" + key,
			"author": jx.M{"did": did, "displayName": "Astronomy notes"},
			"record": jx.M{"$type": "app.bsky.feed.post", "text": "A detailed explanation of the telescope alignment procedure.", "createdAt": at.Format(time.RFC3339Nano)}}
	}
	good := row("abc123")
	forged := row("forged")
	forged["uri"] = "at://" + other + "/app.bsky.feed.post/forged"
	media := row("media")
	media["embed"] = jx.M{"$type": "app.bsky.embed.images#view"}
	labeled := row("labeled")
	labeled["labels"] = []jx.M{{"val": "sexual"}}
	old := row("old")
	jx.Map(old, "record")["createdAt"] = at.Add(-72 * time.Hour).Format(time.RFC3339Nano)
	body, err := json.Marshal(jx.M{"posts": []jx.M{good, forged, media, labeled, old, good}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := autopilotBlueskyTextPosts(body, at.Unix())
	if err != nil || len(got) != 1 {
		t.Fatalf("safe public posts: %+v %v", got, err)
	}
	source := "rss:https://bsky.app/profile/" + did + "/rss"
	if !autopilotPostMatchesSource(source, got[0]) {
		t.Fatal("valid post lost source attribution")
	}
	got[0]["url"] = "https://bsky.app/profile/" + other + "/post/abc123"
	if autopilotPostMatchesSource(source, got[0]) {
		t.Fatal("same host but different author accepted")
	}
	if _, err := autopilotBlueskyTextPosts([]byte(`{"error":"unknown"}`), at.Unix()); err == nil {
		t.Fatal("invalid API response accepted")
	}
}
