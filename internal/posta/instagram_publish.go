package posta

import(
 "context"
 "net/http"
 "net/url"
 "time"
 "posta/internal/jx"
)

const instagramPublishBase="https://graph.instagram.com"
func instagramGraphVersion(a *App)(string,error){
 v:=a.NetworkAppKey("META_GRAPH_API_VERSION")
 if v==""{v="v24.0"}
 if !metaVersion.MatchString(v){return "",Bad("Instagram: неверная версия Graph API.")}
 return instagramPublishBase+"/"+v,nil
}
func instagramToken(data jx.M)(map[string]string,error){
 if jx.Str(data,"access_token")==""||jx.Int(data,"expires_in")<=0{return nil,Bad("Instagram не вернул действующий долгосрочный токен.")}
 return map[string]string{"access_token":jx.Str(data,"access_token"),"expires_at":expiresAt(data)},nil
}
var instagramOAuth=&OAuthFlow{
 RefreshWithAccessToken:true,
 Authorize:func(a *App,state,redirect,verifier string)string{
  q:=url.Values{"client_id":{a.NetworkAppKey("INSTAGRAM_CLIENT_ID")},"redirect_uri":{redirect},"response_type":{"code"},"scope":{"instagram_business_basic,instagram_business_content_publish"},"state":{state},"enable_fb_login":{"0"}}
  return "https://www.instagram.com/oauth/authorize?"+q.Encode()
 },
 Exchange:func(a *App,ctx context.Context,code,redirect,verifier string)(map[string]string,error){
  form:=url.Values{"client_id":{a.NetworkAppKey("INSTAGRAM_CLIENT_ID")},"client_secret":{a.NetworkAppKey("INSTAGRAM_CLIENT_SECRET")},"grant_type":{"authorization_code"},"redirect_uri":{redirect},"code":{code}}
  r,err:=networkHTTP(ctx,http.MethodPost,"https://api.instagram.com/oauth/access_token",map[string]string{"Content-Type":ctForm},formBody(form))
  short,err:=metaPublishExpect(r,err,"Instagram: авторизация");if err!=nil{return nil,err}
  if jx.Str(short,"access_token")==""{return nil,Bad("Instagram не вернул токен.")}
  q:=url.Values{"grant_type":{"ig_exchange_token"},"client_secret":{a.NetworkAppKey("INSTAGRAM_CLIENT_SECRET")},"access_token":{jx.Str(short,"access_token")}}
  r,err=networkHTTP(ctx,http.MethodGet,instagramPublishBase+"/access_token?"+q.Encode(),nil,nil)
  data,err:=metaPublishExpect(r,err,"Instagram: долгосрочный токен");if err!=nil{return nil,err};return instagramToken(data)
 },
 Identify:func(a *App,ctx context.Context,secrets map[string]string)(*NetResource,error){
  base,err:=instagramGraphVersion(a);if err!=nil{return nil,err}
  r,err:=networkHTTP(ctx,http.MethodGet,base+"/me?fields=user_id,username,account_type",bearer(secrets["access_token"]),nil)
  data,err:=metaPublishExpect(r,err,"Instagram: профиль");if err!=nil{return nil,err}
  id,name:=jx.Str(data,"user_id"),jx.Str(data,"username")
  if !metaDigits.MatchString(id)||name==""||jx.Str(data,"account_type")=="PERSONAL"{return nil,Bad("Instagram: требуется профессиональный профиль.")}
  return &NetResource{Ref:id,Title:"@"+name,Secrets:secrets,Meta:jx.M{"account_type":jx.Str(data,"account_type")}},nil
 },
 Refresh:func(a *App,ctx context.Context,secrets map[string]string)(map[string]string,error){
  q:=url.Values{"grant_type":{"ig_refresh_token"},"access_token":{secrets["access_token"]}}
  r,err:=networkHTTP(ctx,http.MethodGet,instagramPublishBase+"/refresh_access_token?"+q.Encode(),nil,nil)
  data,err:=metaPublishExpect(r,err,"Instagram: обновление токена");if err!=nil{return nil,err};return instagramToken(data)
 },
}
func instagramPublish(a *App,ctx context.Context,c *Channel,p NetPost)(*NetSent,error){
 base,err:=instagramGraphVersion(a);if err!=nil{return nil,err}
 return metaContainerPublish(ctx,c,p,metaContainerSpec{name:"Instagram",base:base,create:"media",publish:"media_publish",status:"status_code",caption:"caption",textLimit:2200},networkHTTP,a.leaseDeliveryMedia,func(ctx context.Context)error{
  t:=time.NewTimer(5*time.Second);defer t.Stop()
  select{case <-ctx.Done():return ctx.Err();case <-t.C:return nil}
 })
}
