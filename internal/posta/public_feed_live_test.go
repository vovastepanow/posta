package posta

import (
	"context"
	"os"
	"testing"
	"time"

	"posta/internal/jx"
)

// Opt-in, read-only test of the production fetch and parse path. Do not log
// article bodies or upload them as artifacts. Network failure is separately
// reported from a successful response that cannot be parsed.
func TestPublicFeedLiveIntake(t *testing.T) {
	if os.Getenv("POSTA_FEED_LIVE") != "1" {
		t.Skip("requires POSTA_FEED_LIVE=1")
	}
	ctx,cancel:=context.WithTimeout(context.Background(),3*time.Minute)
	defer cancel()
	u:=(&App{}).User(ctx,"",nil)
	cases:=[]struct{name,format,url string}{
		{"BBC Technology","rss","https://feeds.bbci.co.uk/news/technology/rss.xml"},
		{"Hacker News","rss","https://hnrss.org/frontpage"},
		{"yt-dlp releases","atom","https://github.com/yt-dlp/yt-dlp/releases.atom"},
		{"Daring Fireball","json","https://daringfireball.net/feeds/json"},
		{"Manton blog","json","https://www.manton.org/feed.json"},
	}
	verified:=map[string]int{}
	for _,tc:=range cases{
		t.Run(tc.name,func(t *testing.T){
			r:=autopilotReadRSS(tc.url,u.RSSGet)
			if !r.OK||r.Status==206{
				t.Logf("UNAVAILABLE: format=%s HTTP=%d reason=%s",tc.format,r.Status,autopilotFeedReadReason(r))
				return
			}
			doc:=rssLoad(r.Bytes)
			if !rssIsFeed(doc){t.Fatalf("PARSE FAILURE: HTTP=%d bytes=%d",r.Status,len(r.Bytes))}
			posts,err:=RSSParse(r.Bytes,20,"",r.URL,true,true,0)
			if err!=nil{t.Fatal(err)}
			raw,usable:=len(rssItems(doc)),0
			for _,p:=range posts{
				if tc.format=="json"&&jx.Str(p,"feed_format")!="json_feed"{t.Fatal("JSON provenance lost")}
				if tc.format=="atom"&&doc.N!="feed"{t.Fatal("not Atom")}
				if jx.Str(p,"post_id")!=""&&jx.Str(p,"url")!=""&&AnalysisText(p)!=""{usable++}
			}
			t.Logf("OBSERVED: format=%s HTTP=%d records=%d parsed=%d usable=%d",tc.format,r.Status,raw,len(posts),usable)
			if raw==0||usable==0{t.Fatal("no usable records")}
			verified[tc.format]++
		})
	}
	for _,format:=range []string{"rss","atom","json"}{
		if verified[format]==0{t.Errorf("No live %s feed verified",format)}
	}
}
