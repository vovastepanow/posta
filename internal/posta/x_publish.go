package posta

import (
 "bytes"
 "context"
 "encoding/base64"
 "fmt"
 "io"
 "mime/multipart"
 "net/http"
 "net/url"
 "os"
 "strconv"
 "strings"
 "time"

 "posta/internal/jx"
)

const xMediaBase="https://api.x.com/2/media/upload"

func xMediaExpect(r *netResp,err error,what string)(jx.M,error){
 if err!=nil{return nil,networkUploadError(err,what)}
 if r==nil{return nil,fmt.Errorf("%s: пустой ответ",what)}
 if r.Status==401||r.Status==403{return nil,fmt.Errorf("%s: HTTP %d; проверьте разрешение media.write и переподключите X: %w",what,r.Status,ErrNetworkRejected)}
 if r.Status<200||r.Status>=300{return nil,networkUploadError(fmt.Errorf("HTTP %d",r.Status),what)}
 if len(jx.List(r.Data,"errors"))>0{return nil,fmt.Errorf("%s: API отклонил медиа: %w",what,ErrNetworkRejected)}
 return jx.Map(r.Data,"data"),nil
}

func xMediaReady(ctx context.Context,id string,data jx.M,auth map[string]string,request networkRequestFunc,wait func(context.Context,time.Duration)error)error{
 for i:=0;i<60;i++{
  processing:=jx.Map(data,"processing_info")
  if processing==nil{return nil}
  switch jx.Str(processing,"state"){
  case "succeeded":return nil
  case "failed":return fmt.Errorf("X не обработал вложение: %w",ErrNetworkRejected)
  case "pending","in_progress":
   seconds:=jx.Int(processing,"check_after_secs");if seconds<1{seconds=1}
   if seconds>240{return fmt.Errorf("X требует более длительной обработки; пост не отправлен")}
   if err:=wait(ctx,time.Duration(seconds)*time.Second);err!=nil{return err}
  default:return Bad("X: неизвестное состояние обработки медиа.")
  }
  q:=url.Values{"command":{"STATUS"},"media_id":{id}}
  r,err:=request(ctx,http.MethodGet,xMediaBase+"?"+q.Encode(),auth,nil)
  data,err=xMediaExpect(r,err,"X: статус медиа");if err!=nil{return err}
  if jx.Str(data,"id")!=id{return Bad("X: ответ статуса другого медиа.")}
 }
 return fmt.Errorf("X не завершил обработку медиа; пост не отправлен")
}

func xUploadMedia(ctx context.Context,f Prepared,auth map[string]string,request networkRequestFunc,wait func(context.Context,time.Duration)error)(string,error){
 mime:=sniffMime(f.Path)
 if f.Kind!="video"{
  category:="tweet_image";limit:=int64(5_000_000)
  if f.Kind=="animation"{category="tweet_gif";limit=15_000_000;if mime!="image/gif"{return "",Bad("X: требуется GIF.")}}
  if f.Kind=="photo"&&mime!="image/jpeg"&&mime!="image/png"&&mime!="image/webp"{return "",Bad("X: неподдерживаемый формат фото.")}
  st,err:=os.Stat(f.Path);if err!=nil||!st.Mode().IsRegular()||st.Size()<=0||st.Size()>limit{return "",Bad("X: размер изображения превышает лимит или файл недоступен.")}
  raw,err:=os.ReadFile(f.Path);if err!=nil{return "",err}
  r,err:=request(ctx,http.MethodPost,xMediaBase,withCT(auth,ctJSON),jsonBody(jx.M{"media":base64.StdEncoding.EncodeToString(raw),"media_category":category,"media_type":mime}))
  data,err:=xMediaExpect(r,err,"X: загрузка изображения");if err!=nil{return "",err}
  id:=jx.Str(data,"id");if !metaDigits.MatchString(id){return "",Bad("X не вернул ID медиа.")}
  if err:=xMediaReady(ctx,id,data,auth,request,wait);err!=nil{return "",err};return id,nil
 }
 file,err:=os.Open(f.Path);if err!=nil{return "",err};defer file.Close()
 st,err:=file.Stat()
 if err!=nil||!st.Mode().IsRegular()||st.Size()<=0||st.Size()>200_000_000||mime!="video/mp4"{return "",Bad("X: требуется MP4 до локального лимита 200 МБ.")}
 r,err:=request(ctx,http.MethodPost,xMediaBase+"/initialize",withCT(auth,ctJSON),jsonBody(jx.M{"media_type":"video/mp4","media_category":"tweet_video","total_bytes":st.Size()}))
 data,err:=xMediaExpect(r,err,"X: начало загрузки видео");if err!=nil{return "",err}
 id:=jx.Str(data,"id");if !metaDigits.MatchString(id){return "",Bad("X не вернул ID загрузки видео.")}
 buf:=make([]byte,4*1024*1024)
 for offset,segment:=int64(0),0;offset<st.Size();segment++{
  want:=min(int64(len(buf)),st.Size()-offset)
  n,err:=io.ReadFull(file,buf[:int(want)]);if err!=nil{return "",fmt.Errorf("X: видео изменилось при чтении")}
  var body bytes.Buffer
  mw:=multipart.NewWriter(&body)
  if err=mw.WriteField("segment_index",strconv.Itoa(segment));err!=nil{return "",err}
  part,err:=mw.CreateFormFile("media","chunk.mp4");if err!=nil{return "",err}
  if _,err=part.Write(buf[:n]);err!=nil{return "",err}
  if err=mw.Close();err!=nil{return "",err}
  r,err:=request(ctx,http.MethodPost,xMediaBase+"/"+id+"/append",withCT(auth,mw.FormDataContentType()),&body)
  if _,err=xMediaExpect(r,err,"X: загрузка сегмента");err!=nil{return "",err}
  offset+=int64(n)
 }
 r,err=request(ctx,http.MethodPost,xMediaBase+"/"+id+"/finalize",auth,nil)
 data,err=xMediaExpect(r,err,"X: завершение загрузки");if err!=nil{return "",err}
 if jx.Str(data,"id")!=id{return "",Bad("X не подтвердил ID завершённой загрузки.")}
 if err:=xMediaReady(ctx,id,data,auth,request,wait);err!=nil{return "",err}
 return id,nil
}

func xPublishWith(ctx context.Context,c *Channel,p NetPost,request networkRequestFunc,wait func(context.Context,time.Duration)error)(*NetSent,error){
 if c.Secrets["access_token"]==""{return nil,Bad("X: аккаунт не подключён.")}
 var poll jx.M
 if p.Poll!=nil{
  if len(p.Media)!=0{return nil,Bad("X: опрос нельзя совмещать с медиа.")}
  var err error;poll,err=NetworkPollCheck("x",p.Poll);if err!=nil{return nil,err}
  p.Text=jx.Str(poll,"question")
 }
 if runeLen(p.Text)>280||len(p.Media)>4||(len(p.Media)==0&&strings.TrimSpace(p.Text)==""){return nil,Bad("X: превышен лимит или пост пуст.")}
 for _,f:=range p.Media{if f.Kind!="photo"&&((f.Kind!="video"&&f.Kind!="animation")||len(p.Media)!=1){return nil,Bad("X: нельзя смешивать видео/GIF с другими файлами.")}}
 ctx,cancel:=context.WithTimeout(ctx,5*time.Minute);defer cancel()
 auth:=bearer(c.Secrets["access_token"])
 body:=jx.M{}
 if strings.TrimSpace(p.Text)!=""{body["text"]=p.Text}
 if poll!=nil{body["poll"]=jx.M{"options":jx.Strs(poll,"options"),"duration_minutes":jx.Int(poll,"duration_minutes")}}
 var ids []string
 for _,f:=range p.Media{id,err:=xUploadMedia(ctx,f,auth,request,wait);if err!=nil{return nil,err};ids=append(ids,id)}
 if len(ids)>0{body["media"]=jx.M{"media_ids":ids}}
 if ctx.Err()!=nil{return nil,ctx.Err()}
 r,err:=request(ctx,http.MethodPost,"https://api.x.com/2/tweets",withCT(auth,ctJSON),jsonBody(body))
 data,err:=networkExpect(r,err,[]int{201},"X: пост не опубликован");if err!=nil{return nil,err}
 id:=jx.Str(jx.Map(data,"data"),"id");if !metaDigits.MatchString(id){return nil,uncertainPublish("X не вернул ID поста.")}
 return &NetSent{URL:"https://x.com/i/status/"+id,MessageIDs:[]string{id}},nil
}
