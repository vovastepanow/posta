package posta
import("testing";"strings";"posta/internal/jx")
func TestPublishStyledHTML(t *testing.T){
 got:=publishStyledHTML("**Bold** __italic__ ++u++ ~~s~~ \x60code\x60 [link](https://example.com?a=1&b=2) <script>")
 for _,want:=range []string{"<b>Bold</b>","<i>italic</i>","<u>u</u>","<s>s</s>","<code>code</code>","<a href=\"https://example.com?a=1&amp;b=2\">link</a>","&lt;script&gt;"}{if !strings.Contains(got,want){t.Fatal(got,want)}}
 if strings.Contains(publishStyledHTML("[x](https://a:b@example.com)"),"<a "){t.Fatal("credential-bearing link accepted")}
 if _,err:=PublishExtrasCheck("vk","x",nil,nil,jx.M{"styled_text":true});err==nil{t.Fatal("unsupported formatting")}
}
