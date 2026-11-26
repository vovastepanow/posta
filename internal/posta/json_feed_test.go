package posta

import (
	"strings"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestJSONFeedThroughExistingRSSPipeline(t *testing.T){
	data:=[]byte(`{"version":"https://jsonfeed.org/version/1.1","title":"Example","authors":[{"name":"Editor"}],"items":[{"id":"stable-1","url":"https://example.org/one","content_text":"Text with <literal> and & sign","date_published":"2026-11-25T12:00:00Z","image":"https://example.org/cover.jpg","attachments":[{"url":"https://example.org/clip.webm","mime_type":"video/webm","title":"Clip"},{"url":"https://example.org/clip.mp4","mime_type":"video/mp4","title":"Clip"},{"url":"https://example.org/photo.jpg","mime_type":"image/jpeg"}]}]}`)
	p,err:=RSSParse(data,10,"","https://example.org/feed.json",false,false,0)
	if err!=nil||len(p)!=1{t.Fatal(p,err)}
	if jx.Str(p[0],"feed_format")!="json_feed"||jx.Str(p[0],"author_name")!="Editor"||!strings.Contains(jx.Str(p[0],"text"),"<literal> and & sign"){t.Fatal(p)}
	m:=jx.Maps(p[0],"gallery")
	if len(m)!=2||jx.Str(m[0],"video")!="https://example.org/clip.mp4"||jx.Str(m[0],"image")!="https://example.org/cover.jpg"||jx.Str(m[1],"image")!="https://example.org/photo.jpg"{t.Fatal(m)}
}
func TestJSONFeedRejectsInvalidOrDuplicateItems(t *testing.T){
	for _,raw:=range []string{
		`{"title":"error","items":[]}`,
		`{"version":"https://jsonfeed.org/version/1","title":"x"}`,
		`{"version":"https://jsonfeed.org/version/1","title":"x","items":[{"id":"a","content_text":"a"},{"id":"a","content_text":"b"}]}`,
		`{"version":"https://jsonfeed.org/version/1","title":"x","items":[{"id":"a"}]}`,
	}{
		if rssIsFeed(rssLoad([]byte(raw))){t.Fatal(raw)}
	}
	if !rssIsFeed(rssLoad([]byte(`{"version":"https://jsonfeed.org/version/1","title":"Empty","items":[]}`))){t.Fatal("valid empty feed rejected")}
}
func TestJSONFeedAlternateDiscovery(t *testing.T){
	r:=FetchResultURL{URL:"https://example.org/",FetchResult:FetchResult{OK:true,Bytes:[]byte(`<link rel="alternate" type="application/feed+json" href="/feed.json">`)}}
	if got:=autopilotRSSAlternate(r);got!="https://example.org/feed.json"{t.Fatal(got)}
	got,err:=rssDiscover(r.URL,func(string,int64,time.Duration,string)FetchResultURL{return r})
	if err!=nil||got!="https://example.org/feed.json"{t.Fatal(got,err)}
}
