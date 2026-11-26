package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"

	"posta/internal/jx"
)

var tumblrBlogPattern=regexp.MustCompile("^[a-zA-Z0-9][a-zA-Z0-9-]{0,63}\\.tumblr\\.com$")
var tumblrPostIDPattern=regexp.MustCompile("^[1-9][0-9]{0,19}$")

func tumblrContent(p NetPost)(jx.M,map[string]string,error){
	var blocks []jx.M
	if runeLen(p.Text)>4096||len(p.Media)>10{return nil,nil,Bad("Tumblr: превышен лимит текста или вложений.")}
	if strings.TrimSpace(p.Text)!=""{blocks=append(blocks,jx.M{"type":"text","text":p.Text})}
	files:=map[string]string{};videos:=0
	for i,f:=range p.Media{
		id:=fmt.Sprintf("media-%02d",i)
		mime:=sniffMime(f.Path)
		media:=jx.M{"identifier":id,"type":mime}
		if f.Width>0 && f.Height>0{media["width"],media["height"]=f.Width,f.Height}
		switch f.Kind{
		case "photo","animation":
			if mime!="image/jpeg"&&mime!="image/png"&&mime!="image/gif"&&mime!="image/webp"{return nil,nil,Bad("Tumblr: неподдерживаемый формат изображения.")}
			blocks=append(blocks,jx.M{"type":"image","media":[]jx.M{media}})
		case "video":
			videos++
			if videos>1||f.Duration<=0||f.Duration>600||mime!="video/mp4"{return nil,nil,Bad("Tumblr: требуется одно MP4-видео длительностью до 10 минут.")}
			blocks=append(blocks,jx.M{"type":"video","provider":"tumblr","media":media})
		default:return nil,nil,Bad("Tumblr: неподдерживаемое вложение.")
		}
		files[id]=f.Path
	}
	if len(blocks)==0{return nil,nil,Bad("Tumblr: пустой пост.")}
	return jx.M{"content":blocks,"state":"published"},files,nil
}

// Avoid float conversion of a 64-bit provider ID.
func tumblrReceiptID(r *netResp)string{
	var result struct{Response struct{ID json.RawMessage `json:"id"`;IDString string `json:"id_string"`} `json:"response"`}
	if json.Unmarshal(r.Raw,&result)==nil{
		if tumblrPostIDPattern.MatchString(result.Response.IDString){return result.Response.IDString}
		id:=string(result.Response.ID);var quoted string
		if json.Unmarshal(result.Response.ID,&quoted)==nil{id=quoted}
		if tumblrPostIDPattern.MatchString(id){return id}
	}
	return ""
}

func tumblrPublishWith(ctx context.Context,c *Channel,p NetPost,request networkRequestFunc)(*NetSent,error){
	blog:=jx.Str(c.Meta,"blog")
	if !tumblrBlogPattern.MatchString(blog){return nil,Bad("Tumblr: блог не определён.")}
	payload,files,err:=tumblrContent(p)
	if err!=nil{return nil,err}
	var body io.Reader=jsonBody(payload);ct:=ctJSON
	if len(files)>0{
		f,contentType,cleanup,err:=networkMultipartFile(nil,map[string][]byte{"json":jx.Marshal(payload)},files)
		if err!=nil{return nil,err};defer cleanup();body,ct=f,contentType
	}
	r,err:=request(ctx,http.MethodPost,"https://api.tumblr.com/v2/blog/"+blog+"/posts",withCT(bearer(c.Secrets["access_token"]),ct),body)
	if _,err:=networkExpect(r,err,[]int{201},"Tumblr не принял пост");err!=nil{return nil,err}
	id:=tumblrReceiptID(r)
	if id==""{return nil,uncertainPublish("Tumblr не вернул ID принятой записи.")}
	return &NetSent{URL:"https://"+blog+"/post/"+id,MessageIDs:[]string{id}},nil
}
