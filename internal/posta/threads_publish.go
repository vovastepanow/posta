package posta

import (
 "context"
 "net/http"
 "net/url"
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
 return metaContainerPublish(ctx,c,p,metaContainerSpec{name:"Threads",base:threadsPublishBase+"/v1.0",create:"threads",publish:"threads_publish",status:"status",caption:"text",textOnly:true,textLimit:500},request,lease,wait)
}
