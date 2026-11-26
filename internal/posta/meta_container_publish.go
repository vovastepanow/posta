package posta

import (
 "context"
 "fmt"
 "net/http"
 "net/url"
 "strings"
 "strconv"
 "math"
 "time"

 "posta/internal/jx"
)

type metaContainerSpec struct {
 name,base,create,publish,status,caption string
 textOnly bool
 textLimit int
}

func metaContainerPublish(ctx context.Context,c *Channel,p NetPost,s metaContainerSpec,request networkRequestFunc,lease mediaLeaseFunc,wait func(context.Context)error)(*NetSent,error){
 if !metaDigits.MatchString(c.Ref)||c.Secrets["access_token"]==""{return nil,Bad("%s: аккаунт не подключён.",s.name)}
 var poll jx.M
 if p.Poll!=nil{
  if !s.textOnly||len(p.Media)>0{return nil,Bad("%s: опросы доступны только в текстовых постах Threads.",s.name)}
  var err error;poll,err=NetworkPollCheck("threads",p.Poll);if err!=nil{return nil,err}
  p.Text=jx.Str(poll,"question")
 }
 if runeLen(p.Text)>s.textLimit||len(p.Media)>10{return nil,Bad("%s: превышен лимит текста или вложений.",s.name)}
 if len(p.Media)==0&&(!s.textOnly||strings.TrimSpace(p.Text)==""){return nil,Bad("%s: требуется медиа или допустимый текст.",s.name)}
 platform:="instagram";if s.textOnly{platform="threads"}
 extra,err:=PublishExtrasCheck(platform,p.Text,PreparedKinds(p.Media),p.Poll,p.Extras);if err!=nil{return nil,err}
 if jx.Bool(extra,"ig_story")&&jx.Str(c.Meta,"account_type")!="BUSINESS"{return nil,Bad("Stories доступны бизнес-аккаунтам Instagram. Переподключите ресурс, чтобы проверить тип аккаунта.")}
 if v:=jx.Str(extra,"ig_cover_seconds");v!=""{seconds,_:=strconv.ParseFloat(v,64);if p.Media[0].Duration>0&&seconds>=float64(p.Media[0].Duration){return nil,Bad("Кадр обложки должен находиться внутри видео.")}}
 if err:=CheckVideoCovers(platform,p.Media,extra);err!=nil{return nil,err}
 // A single deadline bounds processing of the entire carousel, rather than
 // allowing each child to extend the delivery indefinitely.
 ctx,cancel:=context.WithTimeout(ctx,4*time.Minute);defer cancel()
 auth:=bearer(c.Secrets["access_token"])
 var urls []string
 for _,f:=range p.Media{
  if f.Kind!="photo"&&f.Kind!="video"{return nil,Bad("%s: неподдерживаемый тип вложения.",s.name)}
  if !s.textOnly&&f.Kind=="photo"&&sniffMime(f.Path)!="image/jpeg"{return nil,Bad("Instagram: требуется JPEG.")}
  // Instagram crops carousel media to the first item aspect ratio; do not
  // submit mixed aspect ratios without an explicit user transformation.
  if !s.textOnly&&len(p.Media)>1 {
   first:=p.Media[0]
   if first.Width<=0||first.Height<=0||f.Width<=0||f.Height<=0{return nil,Bad("Instagram: неизвестны размеры вложения карусели.")}
   a,b:=float64(first.Width)/float64(first.Height),float64(f.Width)/float64(f.Height)
   if a/b<0.98||a/b>1.02{return nil,Bad("Instagram: вложения карусели должны иметь одинаковые пропорции; обрезка отключена.")}
  }
  u,revoke,err:=lease(f);if err!=nil{return nil,err};defer revoke();urls=append(urls,u)
 }
 create:=func(form url.Values)(string,error){
  r,err:=request(ctx,http.MethodPost,s.base+"/"+c.Ref+"/"+s.create,withCT(auth,ctForm),formBody(form))
  data,err:=metaPublishExpect(r,err,s.name+": создание контейнера")
  if err!=nil{return "",networkUploadError(err,s.name+" не создал контейнер")}
  id:=jx.Str(data,"id");if !metaDigits.MatchString(id){return "",Bad("%s не подтвердил создание контейнера.",s.name)};return id,nil
 }
 ready:=func(id string)error{
  for i:=0;i<24;i++{
   r,err:=request(ctx,http.MethodGet,s.base+"/"+id+"?fields="+s.status,auth,nil)
   data,err:=metaPublishExpect(r,err,s.name+": обработка контейнера")
   if err!=nil{return networkUploadError(err,s.name+": не удалось проверить контейнер")}
   switch jx.Str(data,s.status){
   case "FINISHED":return nil
   case "ERROR","EXPIRED","PUBLISHED":return fmt.Errorf("%s: контейнер не готов к публикации (%s): %w",s.name,jx.Str(data,s.status),ErrNetworkRejected)
   case "IN_PROGRESS":if err:=wait(ctx);err!=nil{return err}
   default:return Bad("%s: неизвестный статус контейнера.",s.name)
   }
  }
  return fmt.Errorf("%s не завершил обработку медиа; публикация не отправлена",s.name)
 }
 form:=url.Values{s.caption:{p.Text},"media_type":{"TEXT"}}
 if poll!=nil{
  options:=jx.M{};keys:=[]string{"option_a","option_b","option_c","option_d"}
  for i,value:=range jx.Strs(poll,"options"){options[keys[i]]=value}
  form.Set("poll_attachment",string(jx.Marshal(options)))
 }
 if len(p.Media)>0{
  mediaForm:=func(i int)url.Values{
   if p.Media[i].Kind=="video"{
    kind:="VIDEO";if !s.textOnly&&len(p.Media)==1{kind="REELS"}
    return url.Values{"media_type":{kind},"video_url":{urls[i]}}
   }
   form:=url.Values{"image_url":{urls[i]}}
   if s.textOnly{form.Set("media_type","IMAGE")}
   return form
  }
  if len(p.Media)==1{form=mediaForm(0);form.Set(s.caption,p.Text)}else{
   var ids []string
   for i:=range p.Media{
    child:=mediaForm(i);child.Set("is_carousel_item","true")
    id,err:=create(child);if err!=nil{return nil,err}
    ids=append(ids,id)
   }
   for _,id:=range ids{if err:=ready(id);err!=nil{return nil,err}}
   form.Set("media_type","CAROUSEL");form.Set("children",strings.Join(ids,","))
  }
 }
 if len(p.Media)==1&&p.Media[0].CoverPath!=""{cover,revoke,err:=lease(Prepared{Kind:"photo",Path:p.Media[0].CoverPath});if err!=nil{return nil,err};defer revoke();form.Set("cover_url",cover)}
 if jx.Bool(extra,"ig_story"){form.Set("media_type","STORIES");form.Del(s.caption)}
 if v:=jx.Str(extra,"ig_cover_seconds");v!=""{seconds,_:=strconv.ParseFloat(v,64);form.Set("thumb_offset",strconv.FormatInt(int64(math.Round(seconds*1000)),10))}
 for _,key:=range []string{"alt_text","topic_tag","link_attachment"}{if v:=jx.Str(extra,key);v!=""{form.Set(key,v)}}
 if jx.Bool(extra,"spoiler"){form.Set("is_spoiler_media","true")}
 id,err:=create(form);if err!=nil{return nil,err}
 if len(p.Media)>0{if err=ready(id);err!=nil{return nil,err}}
 if ctx.Err()!=nil{return nil,ctx.Err()}
 r,err:=request(ctx,http.MethodPost,s.base+"/"+c.Ref+"/"+s.publish,withCT(auth,ctForm),formBody(url.Values{"creation_id":{id}}))
 data,err:=metaPublishExpect(r,err,s.name+": публикация");if err!=nil{return nil,err}
 postID:=jx.Str(data,"id");if !metaDigits.MatchString(postID){return nil,uncertainPublish(s.name+" не вернул ID опубликованного поста.")}
 return &NetSent{MessageIDs:[]string{postID}},nil
}
