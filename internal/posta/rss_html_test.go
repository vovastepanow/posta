package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestRSSInlineHTMLMediaAndParagraphs(t *testing.T){
	raw:=[]byte(`<rss><channel><item><title>Title</title><guid>story</guid><link>https://example.org/posts/story</link><description><![CDATA[<p>First <b>paragraph.</b></p><script>tracking-code</script><p>Second paragraph.</p><img width="1" height="1" src="/track.gif"><img src="data:image/gif;base64,x" data-src="/a.jpg"><video poster="/cover.jpg"><source src="/clip.webm" type="video/webm"><source src="/clip.mp4" type="video/mp4"></video><img src="/b.png">]]></description></item></channel></rss>`)
	p,err:=RSSParse(raw,1,"","https://example.org/feed",false,false,0)
	if err!=nil||len(p)!=1{t.Fatal(p,err)}
	text:=jx.Str(p[0],"text")
	if !strings.Contains(text,"First paragraph.")||!strings.Contains(text,"\n\nSecond paragraph.")||strings.Contains(text,"tracking-code"){t.Fatal(text)}
	m:=jx.Maps(p[0],"gallery")
	if len(m)!=3||jx.Str(m[0],"image")!="https://example.org/a.jpg"||jx.Str(m[1],"video")!="https://example.org/clip.webm"||jx.Str(m[1],"image")!="https://example.org/cover.jpg"||jx.Str(m[2],"image")!="https://example.org/b.png"{t.Fatal(m)}
}

func TestRSSExplicitEnclosureIsNotDuplicatedByHTML(t *testing.T){
	raw:=[]byte(`<rss><channel><item><title>Title</title><guid>story</guid><enclosure url="https://example.org/clip.mp4" type="video/mp4"/><description><![CDATA[<video src="https://example.org/clip.mp4"></video>]]></description></item></channel></rss>`)
	p,err:=RSSParse(raw,1,"","https://example.org/feed",false,false,0)
	if err!=nil||len(p)!=1||jx.Str(p[0],"video")!="https://example.org/clip.mp4"||len(jx.Maps(p[0],"gallery"))!=0{t.Fatal(p,err)}
}

func TestRSSDoesNotPromoteTrackingPixel(t *testing.T) {
	data := []byte(`<rss version="2.0"><channel><title>Feed</title><item><title>Text post</title><guid>pixel</guid><description><![CDATA[<p>Real text</p><img width="1" height="1" src="https://example.org/pixel.gif"><script>var template = '<img src="https://example.org/fake.jpg">';</script>]]></description></item></channel></rss>`)
	posts, err := RSSParse(data, 5, "Feed", "https://example.org/rss", false, true, 1)
	if err != nil || len(posts) != 1 { t.Fatalf("posts=%v err=%v",posts,err) }
	if jx.Str(posts[0],"image") != "" || PostItemHasMedia(posts[0]) || !jx.Bool(posts[0],"text_post") {
		t.Fatalf("tracking pixel was promoted: %v",posts[0])
	}
}
