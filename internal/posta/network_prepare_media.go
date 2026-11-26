package posta

import (
	"image"
	"os"
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
