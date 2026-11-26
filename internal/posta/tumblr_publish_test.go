package posta

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"io"
	"mime"
	"mime/multipart"
	"os"
	"path/filepath"
	"testing"

	"posta/internal/jx"
)

func TestTumblrNPFMultipartAndExactID(t *testing.T){
	var b bytes.Buffer
	if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,16,16)));err!=nil{t.Fatal(err)}
	p:=filepath.Join(t.TempDir(),"upload-0")
	if err:=os.WriteFile(p,b.Bytes(),0600);err!=nil{t.Fatal(err)}
	c:=&Channel{Meta:jx.M{"blog":"example.tumblr.com"},Secrets:map[string]string{"access_token":"token"}}
	sent,err:=tumblrPublishWith(context.Background(),c,NetPost{Text:"caption",Media:[]Prepared{{Kind:"photo",Path:p,Width:16,Height:16}}},func(_ context.Context,method,raw string,headers map[string]string,body io.Reader)(*netResp,error){
		_,params,err:=mime.ParseMediaType(headers["Content-Type"]);if err!=nil{t.Fatal(err)}
		mr:=multipart.NewReader(body,params["boundary"])
		part,err:=mr.NextPart();if err!=nil{t.Fatal(err)};data,_:=io.ReadAll(part)
		blocks:=jx.Maps(jx.Unmarshal(data),"content")
		if part.FormName()!="json"||len(blocks)!=2||jx.Str(blocks[0],"text")!="caption"||jx.Str(jx.Maps(blocks[1],"media")[0],"identifier")!="media-00"{t.Fatal(string(data))}
		part,err=mr.NextPart();if err!=nil{t.Fatal(err)};data,_=io.ReadAll(part)
		if part.FormName()!="media-00"||part.FileName()!="upload-0.png"||!bytes.Equal(data,b.Bytes()){t.Fatal("wrong media part")}
		return &netResp{Status:201,Raw:[]byte(`{"response":{"id":9007199254740993123}}`)},nil
	})
	if err!=nil||sent.MessageIDs[0]!="9007199254740993123"{t.Fatal(sent,err)}
}
func TestTumblrRejectsTwoVideos(t *testing.T){
	if err:=(&App{}).NetworkDeliveryPreflight("tumblr","caption",[]string{"video","video"});err==nil{t.Fatal("two native videos accepted")}
}
