package posta

import (
	"strings"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestRutubeFullPublicRecord(t *testing.T) {
	data := []byte(`{"results":[{"id":"4ceb9757e7eec396856b7c7b08b7565a","title":"Title","description":"First paragraph\n\nSecond paragraph","created_ts":"2026-11-24T15:00:00","publication_ts":"2026-11-25T12:00:00+03:00","author":{"id":42,"name":"Channel"}},{"id":"11111111111111111111111111111111","title":"Paid","is_paid":true,"author":{"id":42}},{"id":"22222222222222222222222222222222","title":"Wrong channel","author":{"id":43}}]}`)
	posts,err:=autopilotRutubeChannelPosts(data,"42")
	if err!=nil||len(posts)!=1{t.Fatal(posts,err)}
	p:=posts[0]
	if !strings.Contains(jx.Str(p,"text"),"First paragraph\n\nSecond paragraph") || jx.Int(p,"posted_at")!=time.Date(2026,11,25,9,0,0,0,time.UTC).Unix()||!PostItemHasVideo(p)||jx.Bool(p,"text_post"){t.Fatal(p)}
	if jx.Str(p,"image")!=""||jx.Str(p,"video_page")!="https://rutube.ru/video/4ceb9757e7eec396856b7c7b08b7565a/"{t.Fatal(p)}
}

func TestRutubeRejectsNonListing(t *testing.T) {
	if _,err:=RutubeParse([]byte(`{"detail":"restricted"}`));err==nil{t.Fatal("error mistaken for empty feed")}
	if p,err:=RutubeParse([]byte(`{"results":[]}`));err!=nil||len(p)!=0{t.Fatal(p,err)}
}
