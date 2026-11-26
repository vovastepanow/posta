package posta

import (
	"testing"
	"strings"

	"posta/internal/jx"
)

func TestRSSOrderedMixedMedia(t *testing.T) {
	raw := []byte(`<rss xmlns:media="http://search.yahoo.com/mrss/"><channel><item><guid>mixed</guid><title>Example</title><media:content url="https://example.org/a.jpg" type="image/jpeg"/><enclosure url="https://example.org/a.mp4" type="video/mp4"/><media:thumbnail url="https://example.org/cover.jpg"/><media:content url="https://example.org/b.webm" type="video/webm"/><enclosure url="https://example.org/a.jpg" type="image/jpeg"/></item></channel></rss>`)
	posts,err:=RSSParse(raw,10,"","https://example.org/feed",false,false,0)
	if err!=nil||len(posts)!=1{t.Fatal(posts,err)}
	m:=jx.Maps(posts[0],"gallery")
	if len(m)!=3||jx.Str(m[0],"image")!="https://example.org/a.jpg"||jx.Str(m[1],"video")!="https://example.org/a.mp4"||jx.Str(m[1],"image")!="https://example.org/cover.jpg"||jx.Str(m[2],"video")!="https://example.org/b.webm"{t.Fatalf("media lost: %v",m)}
}
func TestRSSAnonymousTextRecordsDoNotCollide(t *testing.T){
	raw:=[]byte(`<rss><channel><item><title>First text</title></item><item><title>Second text</title></item></channel></rss>`)
	posts,err:=RSSParse(raw,10,"","https://example.org/feed",true,true,0)
	if err!=nil||len(posts)!=2||jx.Str(posts[0],"post_id")==jx.Str(posts[1],"post_id"){t.Fatalf("identity collision: %v %v",posts,err)}
}
func TestRSSUnsupportedVideoNotComplete(t *testing.T){
	raw:=[]byte(`<rss><channel><item><guid>clip</guid><title>Video</title><enclosure url="https://example.org/v.mov" type="video/quicktime"/><enclosure url="https://example.org/c.jpg" type="image/jpeg"/></item></channel></rss>`)
	posts,err:=RSSParse(raw,1,"","https://example.org/feed",true,true,0)
	if err!=nil||len(posts)!=1||!jx.Bool(posts[0],"media_incomplete"){t.Fatalf("incomplete video accepted: %v %v",posts,err)}
	if _,_,ok:=publicClassificationInput(posts[0]);ok{t.Fatal("incomplete post classified")}
}
func TestRSSRejectsCredentialMediaURL(t *testing.T){
	if raw:=rssHTTPS("https://user:pass@example.org/a.jpg","");raw!=""{t.Fatal(strings.Split(raw,"@")[1])}
}
