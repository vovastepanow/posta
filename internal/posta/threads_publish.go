package posta

import (
 "context"
 "fmt"
 "net/http"
 "net/url"
 "strings"
 "time"

 "posta/internal/jx"
)

const threadsPublishBase="https://graph.threads.com"

func threadsToken(data jx.M)(map[string]string,error){
 if jx.Str(data,"access_token")==""||jx.Int(data,"expires_in")<=0{return nil,Bad("Threads не вернул действующий долгосрочный токен.")}
 return map[string]string{"access_token":jx.Str(data,"access_token"),"expires_at":expiresAt(data)},nil
}

var threadsOAuth=&OAuthFlow{
 RefreshWithAccessToken:true,
 Authorize:func(a *App,state,redirect,verifier string)string{
  q:=url.Values{"client_id":{a.NetworkAppKey("THREADS_CLIENT_ID")},"redirect_uri":{redirect},"response_type":{"code"},"scope":{"threads_basic,threads_content_publish"},"state":{state}}
  return "https://www.threads.com/oauth/authorize?"+q.Encode()
 },
 Exchange:func(a *App,ctx context.Context,code,redirect,verifier string)(map[string]string,error){
  form:=url.Values{"client_id":{a.NetworkAppKey("THREADS_CLIENT_ID")},"client_secret":{a.NetworkAppKey("THREADS_CLIENT_SECRET")},"grant_type":{"authorization_code"},"redirect_uri":{redirect},"code":{code}}
  r,err:=networkHTTP(ctx,http.MethodPost,threadsPublishBase+"/oauth/access_token",map[string]string{"Content-Type":ctForm},formBody(form))
  short,err:=metaPublishExpect(r,err,"Threads: авторизация");if err!=nil{return nil,err}
  if jx.Str(short,"access_token")==""{return nil,Bad("Threads не вернул токен.")}
  q:=url.Values{"grant_type":{"th_exchange_token"},"client_secret":{a.NetworkAppKey("THREADS_CLIENT_SECRET")},"access_token":{jx.Str(short,"access_token")}}
  r,err=networkHTTP(ctx,http.MethodGet,threadsPublishBase+"/access_token?"+q.Encode(),nil,nil)
  data,err:=metaPublishExpect(r,err,"Threads: долгосрочный токен");if err!=nil{return nil,err};return threadsToken(data)
 },
 Identify:func(a *App,ctx context.Context,secrets map[string]string)(*NetResource,error){
  r,err:=networkHTTP(ctx,http.MethodGet,threadsPublishBase+"/v1.0/me?fields=id,username",bearer(secrets["access_token"]),nil)
  data,err:=metaPublishExpect(r,err,"Threads: профиль");if err!=nil{return nil,err}
  id,name:=jx.Str(data,"id"),jx.Str(data,"username")
  if !metaDigits.MatchString(id)||name==""{return nil,Bad("Threads не подтвердил профиль.")}
  return &NetResource{Ref:id,Title:"@"+name,Secrets:secrets,Meta:jx.M{}},nil
 },
 Refresh:func(a *App,ctx context.Context,secrets map[string]string)(map[string]string,error){
  q:=url.Values{"grant_type":{"th_refresh_token"},"access_token":{secrets["access_token"]}}
  r,err:=networkHTTP(ctx,http.MethodGet,threadsPublishBase+"/refresh_access_token?"+q.Encode(),nil,nil)
  data,err:=metaPublishExpect(r,err,"Threads: обновление токена");if err!=nil{return nil,err};return threadsToken(data)
 },
}

type mediaLeaseFunc func(Prepared)(string,func(),error)

func threadsPublish(a *App,ctx context.Context,c *Channel,p NetPost)(*NetSent,error){
 return threadsPublishWith(ctx,c,p,networkHTTP,a.leaseDeliveryMedia,func(ctx context.Context)error{
  t:=time.NewTimer(5*time.Second);defer t.Stop()
  select{case <-ctx.Done():return ctx.Err();case <-t.C:return nil}
 })
}

func threadsPublishWith(ctx context.Context,c *Channel,p NetPost,request networkRequestFunc,lease mediaLeaseFunc,wait func(context.Context)error)(*NetSent,error){
 if !metaDigits.MatchString(c.Ref)||c.Secrets["access_token"]==""{return nil,Bad("Threads: аккаунт не подключён.")}
 if runeLen(p.Text)>500||len(p.Media)>10{return nil,Bad("Threads: превышен лимит текста или вложений.")}
 if strings.TrimSpace(p.Text)==""&&len(p.Media)==0{return nil,Bad("Threads: пустой пост.")}
 auth:=bearer(c.Secrets["access_token"])
 base:=threadsPublishBase+"/v1.0"
 var urls []string
 for _,f:=range p.Media{
  if f.Kind!="photo"&&f.Kind!="video"{return nil,Bad("Threads: неподдерживаемый тип вложения.")}
  u,revoke,err:=lease(f);if err!=nil{return nil,err};defer revoke();urls=append(urls,u)
 }
 create:=func(form url.Values)(string,error){
  r,err:=request(ctx,http.MethodPost,base+"/"+c.Ref+"/threads",withCT(auth,ctForm),formBody(form))
  data,err:=metaPublishExpect(r,err,"Threads: создание контейнера")
  if err!=nil{return "",networkUploadError(err,"Threads не создал контейнер")}
  id:=jx.Str(data,"id");if !metaDigits.MatchString(id){return "",Bad("Threads не подтвердил создание контейнера.")};return id,nil
 }
 ready:=func(id string)error{
  for i:=0;i<24;i++{
   r,err:=request(ctx,http.MethodGet,base+"/"+id+"?fields=status,error_message",auth,nil)
   data,err:=metaPublishExpect(r,err,"Threads: обработка контейнера")
   if err!=nil{return networkUploadError(err,"Threads: не удалось проверить контейнер")}
   switch jx.Str(data,"status"){
   case "FINISHED":return nil
   case "ERROR","EXPIRED","PUBLISHED":return fmt.Errorf("Threads: контейнер не готов к публикации (%s): %w",jx.Str(data,"status"),ErrNetworkRejected)
   case "IN_PROGRESS":if err:=wait(ctx);err!=nil{return err}
   default:return Bad("Threads: неизвестный статус контейнера.")
   }
  }
  return fmt.Errorf("Threads не завершил обработку медиа за отведённое время; публикация не отправлена")
 }
 form:=url.Values{"text":{p.Text},"media_type":{"TEXT"}}
 if len(p.Media)>0{
  mediaForm:=func(i int)url.Values{
   if p.Media[i].Kind=="video"{return url.Values{"media_type":{"VIDEO"},"video_url":{urls[i]}}}
   return url.Values{"media_type":{"IMAGE"},"image_url":{urls[i]}}
  }
  if len(p.Media)==1{form=mediaForm(0);form.Set("text",p.Text)}else{
   var ids []string
   for i:=range p.Media{
    child:=mediaForm(i);child.Set("is_carousel_item","true")
    id,err:=create(child);if err!=nil{return nil,err}
    if err=ready(id);err!=nil{return nil,err};ids=append(ids,id)
   }
   form.Set("media_type","CAROUSEL");form.Set("children",strings.Join(ids,","))
  }
 }
 id,err:=create(form);if err!=nil{return nil,err}
 if len(p.Media)>0{if err=ready(id);err!=nil{return nil,err}}
 r,err:=request(ctx,http.MethodPost,base+"/"+c.Ref+"/threads_publish",withCT(auth,ctForm),formBody(url.Values{"creation_id":{id}}))
 data,err:=metaPublishExpect(r,err,"Threads: публикация");if err!=nil{return nil,err}
 postID:=jx.Str(data,"id");if !metaDigits.MatchString(postID){return nil,uncertainPublish("Threads не вернул ID опубликованного поста.")}
 return &NetSent{MessageIDs:[]string{postID}},nil
}
