package posta
import("bytes";"compress/gzip";"testing")
func tgsBytes(t *testing.T,s string)[]byte{t.Helper();var b bytes.Buffer;w:=gzip.NewWriter(&b);if _,err:=w.Write([]byte(s));err!=nil{t.Fatal(err)};if err:=w.Close();err!=nil{t.Fatal(err)};return b.Bytes()}
func TestAnimatedStickerValidation(t *testing.T){
 if err:=validateTGS(tgsBytes(t,`{"w":512,"h":512,"fr":60,"ip":0,"op":180,"layers":[{}]}`));err!=nil{t.Fatal(err)}
 for _,v:=range []string{`{"w":512,"h":512,"fr":60,"ip":0,"op":181,"layers":[{}]}`,`{"w":511,"h":512,"fr":60,"ip":0,"op":100,"layers":[{}]}`,`{"w":512,"h":512,"fr":60,"ip":0,"op":100,"layers":[]}`,"bad"}{if validateTGS(tgsBytes(t,v))==nil{t.Fatal("accepted",v)}}
 good:=VideoInfo{Container:"matroska,webm",Video:"vp9",Width:512,Height:350,FPS:30,DurationSec:3}
 if err:=validateWebMSticker(good,256*1024);err!=nil{t.Fatal(err)}
 good.Audio="opus";if validateWebMSticker(good,1024)==nil{t.Fatal("audio accepted")}
 good.Audio="";good.DurationSec=3.01;if validateWebMSticker(good,1024)==nil{t.Fatal("long sticker accepted")}
 if stickerFrameRate("30000/1001")<29.9||stickerFrameRate("0/0")!=0{t.Fatal("fps")}
}
