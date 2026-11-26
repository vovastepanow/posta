package posta

import (
	"image"
	"os"
	"fmt"
	"path/filepath"
	"net/http"
	"posta/internal/imgx"
)

// Only formats explicitly enabled for a provider can bypass preparation.
// The original remains immutable; Telegram's image resize is a fallback
// for unsupported formats or oversized images, not the default.
func networkOriginalPhoto(n *Network,path,mime string,size int64)(Prepared,bool){
	if n==nil||size<=0||!contains(n.Kinds,"photo")||n.MaxBytes["photo"]>0&&size>n.MaxBytes["photo"]{return Prepared{},false}
	maxSide,maxPixels:=0,int64(0)
	switch n.Key{
	case "max":
		if mime!="image/jpeg"&&mime!="image/png"{return Prepared{},false}
		maxSide=7680
	case "tumblr":
		if mime!="image/jpeg"&&mime!="image/png"&&mime!="image/webp"{return Prepared{},false}
		maxSide,maxPixels=9999,100_000_000
	case "ok":
		if mime!="image/jpeg"&&mime!="image/png"{return Prepared{},false}
		maxSide,maxPixels=7680,40_000_000 // conservative local guard
	default:return Prepared{},false
	}
	f,err:=os.Open(path);if err!=nil{return Prepared{},false};defer f.Close()
	cfg,_,err:=image.DecodeConfig(f)
	if err!=nil||cfg.Width<=0||cfg.Height<=0||cfg.Width>maxSide||cfg.Height>maxSide||maxPixels>0&&int64(cfg.Width)*int64(cfg.Height)>maxPixels{return Prepared{},false}
	return Prepared{Kind:"photo",Path:path,Width:cfg.Width,Height:cfg.Height},true
}

// Prepare an immutable JPEG variant, never crop the source. Reject aspect
// ratios that the service would otherwise crop automatically.
func metaPreparePhoto(platform,path,dir string,index int)(Prepared,error){
 b,err:=os.ReadFile(path);if err!=nil{return Prepared{},Bad("Не удалось прочитать изображение.")}
 w,h,ok:=imgx.Size(b)
 if !ok||w<=0||h<=0||int64(w)*int64(h)>40_000_000{return Prepared{},Bad("Некорректный размер изображения.")}
 maxSide,maxBytes:=4096,8_000_000
 if platform=="x"{maxSide,maxBytes=2048,5_000_000}
 if platform=="instagram_story"{if w<320||float64(w)/float64(h)<0.55||float64(w)/float64(h)>0.58{return Prepared{},Bad("Instagram Stories: используйте портретное фото 9:16 шириной от 320 px.")};maxSide=1920}
 if platform=="instagram"{
  if w<320||float64(w)/float64(h)<0.8||float64(w)/float64(h)>1.91{return Prepared{},Bad("Instagram: фото требует ширину от 320 px и пропорции 4:5–1,91:1; автоматическая обрезка отключена.")}
  maxSide=1440
 }
 if http.DetectContentType(b)=="image/jpeg"&&len(b)<=maxBytes&&max(w,h)<=maxSide{return Prepared{Kind:"photo",Path:path,Width:w,Height:h},nil}
 for q:=90;q>=60;q-=10{
  out:=imgx.Shrink(b,maxSide,q,true)
  if len(out)==0||len(out)>maxBytes||http.DetectContentType(out)!="image/jpeg"{continue}
  nw,nh,ok:=imgx.Size(out);if !ok||nw<=0||nh<=0||max(nw,nh)>maxSide||((platform=="instagram"||platform=="instagram_story")&&nw<320){continue}
  dst:=filepath.Join(dir,fmt.Sprintf("meta-photo-%d.jpg",index))
  if err:=os.WriteFile(dst,out,0600);err!=nil{return Prepared{},err}
  return Prepared{Kind:"photo",Path:dst,Width:nw,Height:nh},nil
 }
 return Prepared{},Bad("Не удалось подготовить JPEG без потери вложения.")
}
