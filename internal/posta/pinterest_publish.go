package posta

import (
 "context"
 "encoding/base64"
 "fmt"
 "net/http"
 "net/url"
 "os"
 "regexp"
 "strings"
 "time"

 "posta/internal/jx"
)

var pinterestIDPattern=regexp.MustCompile("^[0-9]{1,30}$")
var pinterestS3RegionPattern=regexp.MustCompile("^pinterest-media-upload\\.s3[.-][a-z0-9-]+\\.amazonaws\\.com$")

func pinterestUploadURL(raw string)bool{
 u,err:=url.Parse(raw)
 if err!=nil||u.Scheme!="https"||u.User!=nil||u.Fragment!=""||(u.Port()!=""&&u.Port()!="443"){return false}
 host:=strings.ToLower(u.Hostname())
 return host=="pinterest-media-upload.s3.amazonaws.com"||host=="pinterest-media-upload.s3-accelerate.amazonaws.com"||pinterestS3RegionPattern.MatchString(host)
}

func pinterestPublishWith(ctx context.Context,c *Channel,p NetPost,request networkRequestFunc,cover func(context.Context,string)[]byte,wait func(context.Context,time.Duration)error)(*NetSent,error){
 if len(p.Media)!=1{return nil,Bad("Pinterest: нужен один файл изображения или видео.")}
 board:=jx.Str(c.Meta,"board_id")
 if !pinterestIDPattern.MatchString(board){return nil,Bad("Pinterest: не определена доска.")}
 f:=p.Media[0]
 info,err:=os.Stat(f.Path)
 if err!=nil||!info.Mode().IsRegular()||info.Size()<=0{return nil,Bad("Pinterest: файл не найден.")}
 auth:=bearer(c.Secrets["access_token"])
 var media jx.M
 switch f.Kind{
 case "photo":
  if info.Size()>20_000_000{return nil,Bad("Pinterest: изображение больше 20 МБ.")}
  b,err:=os.ReadFile(f.Path);if err!=nil{return nil,Bad("Pinterest: не удалось прочитать изображение.")}
  ct:=http.DetectContentType(b)
  if ct!="image/jpeg"&&ct!="image/png"{return nil,Bad("Pinterest: требуется JPEG или PNG.")}
  media=jx.M{"source_type":"image_base64","content_type":ct,"data":base64.StdEncoding.EncodeToString(b)}
 case "video":
  if info.Size()>200_000_000{return nil,Bad("Pinterest: видео больше 200 МБ.")}
  preview:=cover(ctx,f.Path)
  if len(preview)==0||len(preview)>8_000_000||http.DetectContentType(preview)!="image/jpeg"{return nil,Bad("Pinterest: не удалось подготовить обложку видео.")}
  r,err:=request(ctx,http.MethodPost,"https://api.pinterest.com/v5/media",withCT(auth,ctJSON),jsonBody(jx.M{"media_type":"video"}))
  d,err:=networkExpect(r,err,[]int{200,201},"Pinterest: регистрация видео")
  if err!=nil{return nil,networkUploadError(err,"Pinterest не зарегистрировал видео")}
  id,loc:=jx.Str(d,"media_id"),jx.Str(d,"upload_url")
  if !pinterestIDPattern.MatchString(id)||!pinterestUploadURL(loc){return nil,Bad("Pinterest вернул непроверенный адрес загрузки или ID видео.")}
  fields:=map[string]string{}
  for k,v:=range jx.Map(d,"upload_parameters"){
   val,ok:=v.(string);if !ok||k==""||strings.EqualFold(k,"file"){return nil,Bad("Pinterest вернул некорректные параметры загрузки.")}
   fields[k]=val
  }
  if len(fields)==0||fields["key"]==""||fields["policy"]==""{return nil,Bad("Pinterest не выдал подписанные параметры загрузки.")}
  body,ct,cleanup,err:=networkMultipartFile(fields,nil,map[string]string{"file":f.Path})
  if err!=nil{return nil,err}
  r,err=request(ctx,http.MethodPost,loc,map[string]string{"Content-Type":ct},body)
  cleanup()
  if err!=nil||r==nil||r.Status!=204{return nil,Bad("Pinterest: видео не загружено, пин не опубликован.")}
  ready:=false
  for attempt:=0;attempt<24;attempt++{
   r,err=request(ctx,http.MethodGet,"https://api.pinterest.com/v5/media/"+id,auth,nil)
   d,err=networkExpect(r,err,[]int{200},"Pinterest: состояние видео")
   if err!=nil{return nil,networkUploadError(err,"Pinterest не подтвердил обработку видео")}
   switch jx.Str(d,"status"){
   case "succeeded":ready=true
   case "failed":return nil,Bad("Pinterest отклонил видео при обработке.")
   case "registered","processing":
   default:return nil,Bad("Pinterest вернул неизвестный статус обработки видео.")
   }
   if ready{break}
   if err=wait(ctx,5*time.Second);err!=nil{return nil,err}
  }
  if !ready{return nil,Bad("Pinterest ещё обрабатывает видео; пин не опубликован.")}
  media=jx.M{"source_type":"video_id","media_id":id,"cover_image_content_type":"image/jpeg","cover_image_data":base64.StdEncoding.EncodeToString(preview)}
 default:return nil,Bad("Pinterest: неподдерживаемое вложение.")
 }
 title,desc:=titleAndBody(p.Text)
 if title==""{title="Пин"}
 if runeLen(title)>100||runeLen(desc)>800{return nil,Bad("Pinterest: превышен лимит заголовка или описания.")}
 r,err:=request(ctx,http.MethodPost,"https://api.pinterest.com/v5/pins",withCT(auth,ctJSON),jsonBody(jx.M{"board_id":board,"title":title,"description":desc,"media_source":media}))
 d,err:=networkExpect(r,err,[]int{200,201},"Пин не опубликован")
 if err!=nil{return nil,err}
 id:=jx.Str(d,"id")
 if !pinterestIDPattern.MatchString(id){return nil,uncertainPublish(fmt.Sprintf("Pinterest не вернул достоверный ID пина."))}
 return &NetSent{URL:"https://www.pinterest.com/pin/"+id+"/",MessageIDs:[]string{id}},nil
}
