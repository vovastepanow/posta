package posta

import (
	"encoding/json"
	"net/url"
	"strings"
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

func TestAutopilotBlueskyAuthorFallbackRequiresOwnOriginalPosts(t *testing.T) {
	at := time.Now()
	did := "did:plc:abcdefghijklmnopqrstuvwx"
	other := "did:plc:zyxwvutsrqponmlkjihgfedc"
	record := func(owner,key string) jx.M {
		return jx.M{"uri":"at://"+owner+"/app.bsky.feed.post/"+key,"author":jx.M{"did":owner},"record":jx.M{"$type":"app.bsky.feed.post","text":"Detailed telescope alignment procedure for astronomers.","createdAt":at.Format(time.RFC3339Nano)}}
	}
	feed := jx.M{"feed":[]jx.M{
		{"post":record(did,"own")},
		{"post":record(other,"foreign")},
		{"post":record(did,"repost"),"reason":jx.M{"$type":"app.bsky.feed.defs#reasonRepost"}},
		{"post":record(did,"reply"),"reply":jx.M{}},
	}}
	calls:=0
	got,err:=autopilotBlueskyAuthorFallback("astronomy",at.Unix(),func(raw string)([]byte,error){
		calls++
		p,err:=url.Parse(raw)
		if err!=nil || p.Host!="public.api.bsky.app" {
			t.Fatalf("unexpected endpoint %q",raw)
		}
		if strings.HasSuffix(p.Path,"searchActors") {
			return json.Marshal(jx.M{"actors":[]jx.M{{"did":did},{"did":did}}})
		}
		if p.Query().Get("actor")!=did || p.Query().Get("filter")!="posts_no_replies" {
			t.Fatalf("unexpected author query %s",raw)
		}
		return json.Marshal(feed)
	})
	if err!=nil || calls!=2 || len(got)!=1 || !strings.HasSuffix(jx.Str(got[0],"url"),"/own") || !jx.Bool(got[0],"text_post") {
		t.Fatalf("calls=%d posts=%+v err=%v",calls,got,err)
	}
}
