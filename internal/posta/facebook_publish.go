package posta

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"

	"posta/internal/jx"
)

func metaGraphBase(a *App)(string,error){
	v:=a.NetworkAppKey("META_GRAPH_API_VERSION")
	if v==""{v="v24.0"}
	if !metaVersion.MatchString(v){return "",Bad("Meta: неверная версия Graph API.")}
	return "https://graph.facebook.com/"+v,nil
}

// Provider error payloads can contain secrets or signed URLs. Expose only
// the code and status rather than the untrusted remote message.
func metaPublishExpect(r *netResp,err error,what string)(jx.M,error){
	if err!=nil{return nil,err}
	if r==nil{return nil,uncertainPublish(what+": пустой ответ API.")}
	if r.Status>=500||r.Status==408{return nil,uncertainPublish(fmt.Sprintf("%s: HTTP %d.",what,r.Status))}
	if e:=jx.Map(r.Data,"error");e!=nil{
		if r.Status==429 || jx.Int(e,"code")==4 || jx.Int(e,"code")==17 || jx.Int(e,"code")==32 || jx.Int(e,"code")==613 {
			return nil,fmt.Errorf("%s: временный лимит Meta (код %d).",what,jx.Int(e,"code"))
		}
		return nil,fmt.Errorf("%s: Meta отклонила запрос (код %d): %w",what,jx.Int(e,"code"),ErrNetworkRejected)
	}
	if r.Status!=200&&r.Status!=201{
		_,err:=networkExpect(&netResp{Status:r.Status},nil,[]int{200,201},what);return nil,err
	}
	return orEmpty(r.Data),nil
}

func facebookConnect(a *App,ctx context.Context,in map[string]string)(*NetResource,error){
	base,err:=metaGraphBase(a);if err!=nil{return nil,err}
	return facebookConnectWith(ctx,base,in,networkHTTP)
}

func facebookConnectWith(ctx context.Context,base string,in map[string]string,request networkRequestFunc)(*NetResource,error){
	token,id:=strings.TrimSpace(in["access_token"]),strings.TrimSpace(in["page_id"])
	if token==""||!metaDigits.MatchString(id){return nil,Bad("Укажите ID страницы Facebook и Page access token.")}
	r,err:=request(ctx,http.MethodGet,base+"/me?fields=id,name,category",bearer(token),nil)
	page,err:=metaPublishExpect(r,err,"Facebook: проверка страницы")
	if err!=nil{return nil,err}
	if jx.Str(page,"id")!=id||jx.Str(page,"name")==""||jx.Str(page,"category")==""{
		return nil,Bad("Токен не подтверждает выбранную страницу Facebook.")
	}
	return &NetResource{Ref:id,Title:jx.Str(page,"name"),Secrets:map[string]string{"access_token":token},Meta:jx.M{}},nil
}

var facebookPostIDPattern=regexp.MustCompile("^[0-9]{5,25}_[0-9]{5,25}$")

func facebookPublish(a *App,ctx context.Context,c *Channel,p NetPost)(*NetSent,error){
	base,err:=metaGraphBase(a);if err!=nil{return nil,err}
	return facebookPublishWith(ctx,base,c,p,networkHTTP)
}

func facebookPublishWith(ctx context.Context,base string,c *Channel,p NetPost,request networkRequestFunc)(*NetSent,error){
	if !metaDigits.MatchString(c.Ref)||c.Secrets["access_token"]==""{return nil,Bad("Facebook: страница не подключена.")}
	if len(p.Media)>10||runeLen(p.Text)>63206{return nil,Bad("Facebook: превышен лимит текста или вложений.")}
	if strings.TrimSpace(p.Text)==""&&len(p.Media)==0{return nil,Bad("Facebook: пустой пост.")}
	auth:=bearer(c.Secrets["access_token"])
	for _,media:=range p.Media{
		if media.Kind=="video"{
			if len(p.Media)!=1{return nil,Bad("Facebook: допустим фотоальбом либо одно видео, без смешивания.")}
			return facebookVideoPublishWith(ctx,base,c,p.Text,media.Path,request)
		}
		if media.Kind!="photo"{return nil,Bad("Facebook: неподдерживаемое вложение.")}
	}
	var attached []jx.M
	for _,f:=range p.Media{
		if f.Kind!="photo"{return nil,Bad("Facebook: пока поддерживаются фотографии.")}
		body,ct,cleanup,err:=networkMultipartFile(map[string]string{"published":"false"},nil,map[string]string{"source":f.Path})
		if err!=nil{return nil,err}
		r,err:=request(ctx,http.MethodPost,base+"/"+c.Ref+"/photos",withCT(auth,ct),body)
		cleanup()
		data,err:=metaPublishExpect(r,err,"Facebook: загрузка фото")
		if err!=nil{return nil,networkUploadError(err,"Facebook не принял фотографию")}
		id:=jx.Str(data,"id")
		if !metaDigits.MatchString(id){return nil,Bad("Facebook не подтвердил загрузку фотографии.")}
		attached=append(attached,jx.M{"media_fbid":id})
	}
	params:=url.Values{"message":{p.Text}}
	for i,f:=range attached{params.Set(fmt.Sprintf("attached_media[%d]",i),string(jx.Marshal(f)))}
	r,err:=request(ctx,http.MethodPost,base+"/"+c.Ref+"/feed",withCT(auth,ctForm),formBody(params))
	data,err:=metaPublishExpect(r,err,"Facebook: публикация")
	if err!=nil{return nil,err}
	id:=jx.Str(data,"id")
	if !facebookPostIDPattern.MatchString(id)||!strings.HasPrefix(id,c.Ref+"_"){return nil,uncertainPublish("Facebook не вернул ID поста выбранной страницы.")}
	return &NetSent{URL:"https://www.facebook.com/"+id,MessageIDs:[]string{id}},nil
}

func facebookVideoPublishWith(ctx context.Context,base string,c *Channel,description,path string,request networkRequestFunc)(*NetSent,error){
	info,err:=os.Stat(path)
	if err!=nil||!info.Mode().IsRegular()||info.Size()<=0||info.Size()>200_000_000||sniffMime(path)!="video/mp4"{return nil,Bad("Facebook: требуется подготовленный MP4 до 200 МБ.")}
	u,err:=url.Parse(base)
	if err!=nil||u.Scheme!="https"||u.Host!="graph.facebook.com"||u.User!=nil||u.RawQuery!=""||u.Fragment!=""||!metaVersion.MatchString(strings.TrimPrefix(u.Path,"/")){return nil,Bad("Facebook: неверный адрес API.")}
	u.Host="graph-video.facebook.com"
	body,ct,cleanup,err:=networkMultipartFile(map[string]string{"description":description,"published":"true"},nil,map[string]string{"source":path})
	if err!=nil{return nil,err};defer cleanup()
	r,err:=request(ctx,http.MethodPost,u.String()+"/"+c.Ref+"/videos",withCT(bearer(c.Secrets["access_token"]),ct),body)
	data,err:=metaPublishExpect(r,err,"Facebook: публикация видео")
	if err!=nil{return nil,err}
	id:=jx.Str(data,"id")
	if !metaDigits.MatchString(id){return nil,uncertainPublish("Facebook не вернул ID загруженного видео.")}
	return &NetSent{URL:"https://www.facebook.com/"+c.Ref+"/videos/"+id+"/",MessageIDs:[]string{id}},nil
}
