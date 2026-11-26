package posta

import (
 "bytes"
 "compress/gzip"
 "encoding/json"
 "fmt"
 "io"
 "math"
 "os"
 "path/filepath"
 "strconv"
 "strings"

 "posta/internal/imgx"
)

func stickerFrameRate(raw string) float64 {
 parts:=strings.Split(raw,"/")
 if len(parts)!=2{return 0}
 a,e1:=strconv.ParseFloat(parts[0],64);b,e2:=strconv.ParseFloat(parts[1],64)
 if e1!=nil||e2!=nil||a<=0||b<=0||math.IsNaN(a)||math.IsInf(a,0)||math.IsNaN(b)||math.IsInf(b,0){return 0}
 return a/b
}
func stickerDimensions(w,h int)bool{return w>0&&h>0&&w<=512&&h<=512&&(w==512||h==512)}
func validateTGS(data []byte)error{
 if len(data)>64*1024{return Bad("Анимированный стикер TGS: до 64 КБ.")}
 reader,err:=gzip.NewReader(bytes.NewReader(data));if err!=nil{return Bad("Некорректный TGS.")}
 defer reader.Close()
 const maxJSON=2*1024*1024
 raw,err:=io.ReadAll(io.LimitReader(reader,maxJSON+1));if err!=nil||len(raw)>maxJSON{return Bad("Некорректный или слишком большой JSON стикера.")}
 var d struct{W int `json:"w"`;H int `json:"h"`;FR float64 `json:"fr"`;IP float64 `json:"ip"`;OP float64 `json:"op"`;Layers []json.RawMessage `json:"layers"`}
 if json.Unmarshal(raw,&d)!=nil||d.W!=512||d.H!=512||(d.FR!=30&&d.FR!=60)||d.IP<0||d.OP<=d.IP||(d.OP-d.IP)/d.FR>3||len(d.Layers)==0{return Bad("TGS: 512×512 px, 30/60 FPS, длительность до 3 секунд и корректная анимация.")}
 return nil
}
func validateWebMSticker(info VideoInfo,size int64)error{
 if size>256*1024||!strings.Contains(info.Container,"webm")||info.Video!="vp9"||info.Audio!=""||!stickerDimensions(info.Width,info.Height)||!(info.DurationSec>0&&info.DurationSec<=3)||!(info.FPS>0&&info.FPS<=30){return Bad("WEBM-стикер: VP9 без звука, до 256 КБ, до 3 секунд, до 30 FPS; одна сторона 512 px.")}
 return nil
}
func(a *App)prepareTelegramSticker(path,dir string,index int,mime string,size int64)(Prepared,error){
 if size<=0||size>512*1024{return Prepared{},Bad("Стикер: допустимо WEBP до 512 КБ, TGS до 64 КБ, WEBM до 256 КБ.")}
 b,err:=os.ReadFile(path);if err!=nil{return Prepared{},err}
 f:=Prepared{Kind:"sticker"}
 ext:=""
 switch{
 case mime=="image/webp":
  w,h,ok:=imgx.Size(b);if !ok||!stickerDimensions(w,h){return Prepared{},Bad("Стикер WEBP: одна сторона 512 px, другая не более 512 px.")}
  // Animated WEBP is not a supported Telegram sticker format.
  if len(b)>=30&&string(b[12:16])=="VP8X"&&b[20]&2!=0{return Prepared{},Bad("Анимированный WEBP не поддерживается: используйте TGS или WEBM.")}
  ext=".webp";f.Width,f.Height=w,h
 case len(b)>=2&&b[0]==0x1f&&b[1]==0x8b:
  if err:=validateTGS(b);err!=nil{return Prepared{},err};ext=".tgs";f.Width,f.Height=512,512
 default:
  v:=a.TelegramVideoInfo(path);if err:=validateWebMSticker(v,size);err!=nil{return Prepared{},err}
  ext=".webm";f.Width,f.Height,f.Duration=v.Width,v.Height,v.Duration
 }
 f.Path=filepath.Join(dir,fmt.Sprintf("sticker-%d%s",index,ext));if err=copyFile(path,f.Path);err!=nil{return Prepared{},err};return f,nil
}
