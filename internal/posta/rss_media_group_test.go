package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestRSSMediaGroupSelectsOneRepresentationAndItsPoster(t *testing.T){
	raw:=[]byte(`<rss xmlns:media="http://search.yahoo.com/mrss/"><channel><item><title>Two clips</title><guid>two</guid><media:group><media:content url="https://example.org/a-small.mp4" type="video/mp4"/><media:content url="https://example.org/a-big.mp4" type="video/mp4" isDefault="true"><media:thumbnail url="https://example.org/a.jpg"/></media:content></media:group><media:group><media:content url="https://example.org/b.mov" type="video/quicktime" isDefault="true"/><media:content url="https://example.org/b.mp4" type="video/mp4"/><media:thumbnail url="https://example.org/b.jpg"/></media:group></item></channel></rss>`)
	posts,err:=RSSParse(raw,1,"","https://example.org/feed",false,false,0)
	if err!=nil||len(posts)!=1{t.Fatal(posts,err)}
	m:=jx.Maps(posts[0],"gallery")
	if len(m)!=2||jx.Str(m[0],"video")!="https://example.org/a-big.mp4"||jx.Str(m[0],"image")!="https://example.org/a.jpg"||jx.Str(m[1],"video")!="https://example.org/b.mp4"||jx.Str(m[1],"image")!="https://example.org/b.jpg"{t.Fatalf("wrong renditions: %v",m)}
}

func TestRSSIncompleteVideoWithoutCoverIsNotText(t *testing.T){
	raw:=[]byte(`<rss><channel><item><title>Unresolved video</title><guid>clip</guid><enclosure url="https://example.org/v.mov" type="video/quicktime"/></item></channel></rss>`)
	p,err:=RSSParse(raw,1,"","https://example.org/feed",false,true,0)
	if err!=nil||len(p)!=1||!jx.Bool(p[0],"media_incomplete")||jx.Bool(p[0],"text_post"){t.Fatal(p,err)}
}
