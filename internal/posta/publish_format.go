package posta

import (
 "html"
 "net/url"
 "regexp"
 "strings"
)

// Only a small explicit subset of the editor syntax becomes HTML. Raw
// input is always escaped; arbitrary HTML is never sent as markup.
var publishFormatToken=regexp.MustCompile(`\*\*[^*\n]+\*\*|__[^_\n]+__|\+\+[^+\n]+\+\+|~~[^~\n]+~~|\x60[^\x60\n]+\x60|\[[^\]\n]+\]\(https?://[^\s)]+\)`)
func publishStyledHTML(text string) string {
 var out strings.Builder
 pos:=0
 for _,idx:=range publishFormatToken.FindAllStringIndex(text,-1){
  out.WriteString(html.EscapeString(text[pos:idx[0]]))
  token:=text[idx[0]:idx[1]]
  tag:="";n:=2
  switch {
  case strings.HasPrefix(token,"**"):tag="b"
  case strings.HasPrefix(token,"__"):tag="i"
  case strings.HasPrefix(token,"++"):tag="u"
  case strings.HasPrefix(token,"~~"):tag="s"
  case strings.HasPrefix(token,"\x60"):tag="code";n=1
  case strings.HasPrefix(token,"["):
   split:=strings.Index(token,"](")
   raw:=token[split+2:len(token)-1]
   u,err:=url.Parse(raw)
   if err==nil&&u.Hostname()!=""&&u.User==nil&&(u.Scheme=="http"||u.Scheme=="https")&&len(raw)<=2048 {
    out.WriteString("<a href=\""+html.EscapeString(raw)+"\">"+html.EscapeString(token[1:split])+"</a>")
   }else{out.WriteString(html.EscapeString(token))}
  }
  if tag!=""{out.WriteString("<"+tag+">"+html.EscapeString(token[n:len(token)-n])+"</"+tag+">")}
  pos=idx[1]
 }
 out.WriteString(html.EscapeString(text[pos:]))
 return out.String()
}
