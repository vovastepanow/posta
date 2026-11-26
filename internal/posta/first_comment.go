package posta

import ("regexp";"posta/internal/jx")

var maxCommentIDPattern=regexp.MustCompile(`^(mid[.])?[A-Za-z0-9_-]{1,256}$`)

// The parent receipt always remains successful even if the optional comment
// is rejected or uncertain. Never turn this into a retry of the parent post.
func firstCommentWarning(platform string)string{
 return platform+": пост опубликован, но первый комментарий не подтверждён. Проверьте комментарии; не публикуйте основной пост повторно."
}
func ChannelExtraKinds(c *Channel)[]string{
 if c==nil{return []string{}}
 list:=postExtraKinds(c.Platform)
 if c.Platform!="max"{return list}
 typ:=jx.Str(c.Meta,"chat_type")
 if typ==""||typ=="channel"{return list}
 out:=[]string{};for _,v:=range list{if v!="first_comment"{out=append(out,v)}}
 return out
}
