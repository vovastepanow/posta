package posta

import (
 "bytes"
 "context"
 "fmt"
 "net/http"
 "net/url"
 "os"
 "regexp"
 "strconv"
 "strings"
 "time"
 "posta/internal/jx"
)

const youtubeChunkSize=8*1024*1024
var youtubeIDPattern=regexp.MustCompile("^[A-Za-z0-9_-]{11}$")
var youtubeRangePattern=regexp.MustCompile("^bytes=0-([0-9]+)$")

func youtubeUploadLocation(raw string)bool{
 u,err:=url.Parse(raw)
 return err==nil&&u.Scheme=="https"&&u.User==nil&&u.Fragment==""&&strings.EqualFold(u.Hostname(),"www.googleapis.com")&&(u.Port()==""||u.Port()=="443")&&u.Path=="/upload/youtube/v3/videos"&&u.Query().Get("upload_id")!=""
}

func youtubeAcknowledged(r *netResp,total int64)(int64,bool){
 raw:=r.Headers.Get("Range")
 if raw==""{return 0,true}
 m:=youtubeRangePattern.FindStringSubmatch(raw)
 if m==nil{return 0,false}
 last,err:=strconv.ParseInt(m[1],10,64)
 if err!=nil||last<0||last>=total{return 0,false}
 return last+1,true
}

func youtubePublishWith(ctx context.Context,c *Channel,p NetPost,request networkRequestFunc,wait func(context.Context,time.Duration)error)(*NetSent,error){
 if len(p.Media)!=1||p.Media[0].Kind!="video"{return nil,Bad("YouTube: требуется ровно одно видео.")}
 f,err:=os.Open(p.Media[0].Path)
 if err!=nil{return nil,Bad("Файл видео не найден.")};defer f.Close()
 info,err:=f.Stat()
 if err!=nil||!info.Mode().IsRegular()||info.Size()<=0||info.Size()>200_000_000{return nil,Bad("YouTube: неподдерживаемый размер видео.")}
 total:=info.Size()
 title,desc:=titleAndBody(p.Text)
 if title==""{title="Shorts"}
 privacy:=jx.Str(c.Meta,"privacy")
 if privacy==""{privacy="private"}
 if privacy!="private"&&privacy!="public"&&privacy!="unlisted"{return nil,Bad("YouTube: неверная видимость.")}
 auth:=bearer(c.Secrets["access_token"])
 meta:=jx.M{"snippet":jx.M{"title":trimRunes(title,100),"description":desc},"status":jx.M{"privacyStatus":privacy,"selfDeclaredMadeForKids":false}}
 h:=withCT(auth,"application/json; charset=UTF-8")
 h["X-Upload-Content-Length"],h["X-Upload-Content-Type"]=strconv.FormatInt(total,10),"video/mp4"
 start,err:=request(ctx,"POST","https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status",h,jsonBody(meta))
 if _,err=networkExpect(start,err,[]int{200},"YouTube: загрузка не начата");err!=nil{return nil,networkUploadError(err,"YouTube: загрузка не начата")}
 loc:=start.Headers.Get("Location")
 if !youtubeUploadLocation(loc){return nil,Bad("YouTube вернул непроверенный адрес загрузки.")}
 var offset int64
 stalled:=0
 probe:=false
 // Keep all recovery within the original upload session. A completed
 // request without a trustworthy receipt must never create another video.
 for step:=0;step<128;step++{
  if ctx.Err()!=nil{return nil,uncertainPublish("YouTube: время загрузки истекло, проверьте канал перед повтором.")}
  h:=withCT(auth,"video/mp4")
  var body *bytes.Reader
  sentEnd:=offset
  if probe||offset==total{
   h["Content-Range"]=fmt.Sprintf("bytes */%d",total)
   body=bytes.NewReader(nil)
  }else{
   size:=min(int64(youtubeChunkSize),total-offset)
   buf:=make([]byte,int(size))
   if _,err=f.ReadAt(buf,offset);err!=nil{return nil,uncertainPublish("YouTube: локальный файл изменился во время загрузки.")}
   sentEnd=offset+size
   h["Content-Range"]=fmt.Sprintf("bytes %d-%d/%d",offset,sentEnd-1,total)
   body=bytes.NewReader(buf)
  }
  r,err:=request(ctx,http.MethodPut,loc,h,body)
  if err!=nil||r==nil||r.Status>=500||r.Status==408||r.Status==429{
   stalled++
   if stalled>=4{return nil,uncertainPublish("YouTube: не удалось подтвердить состояние текущей загрузки; проверьте канал перед повтором.")}
   if err=wait(ctx,time.Duration(stalled)*time.Second);err!=nil{return nil,uncertainPublish("YouTube: состояние загрузки не подтверждено.")}
   probe=true
   continue
  }
  if r.Status==200||r.Status==201{
   id:=jx.Str(r.Data,"id")
   if !youtubeIDPattern.MatchString(id){return nil,uncertainPublish("YouTube не вернул достоверный ID видео.")}
   return &NetSent{URL:"https://www.youtube.com/watch?v="+id,MessageIDs:[]string{id}},nil
  }
  if r.Status!=308{return nil,uncertainPublish(fmt.Sprintf("YouTube: загрузка прервана (HTTP %d); проверьте канал перед повтором.",r.Status))}
  next,ok:=youtubeAcknowledged(r,total)
  if !ok||next<offset||(!probe&&next>sentEnd){return nil,uncertainPublish("YouTube вернул противоречивый диапазон загруженных данных.")}
  if next==offset{stalled++;if stalled>=4{return nil,uncertainPublish("YouTube не подтвердил продвижение загрузки.")}}else{stalled=0}
  offset=next
  probe=false
 }
 return nil,uncertainPublish("YouTube: не удалось завершить загрузку в пределах лимита попыток.")
}
