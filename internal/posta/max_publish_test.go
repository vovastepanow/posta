package posta

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestMAXMixedMediaAndProcessingRetry(t *testing.T){
	dir:=t.TempDir()
	photo:=filepath.Join(dir,"photo.jpg");video:=filepath.Join(dir,"video.mp4")
	for _,p:=range []string{photo,video}{if err:=os.WriteFile(p,[]byte("test-file"),0600);err!=nil{t.Fatal(err)}}
	c:=&Channel{Ref:"-42",Secrets:map[string]string{"token":"bot-token"}}
	sends:=0;uploads:=0;waits:=0
	req:=func(_ context.Context,method,raw string,h map[string]string,body io.Reader)(*netResp,error){
		if method!=http.MethodPost{t.Fatal(method)}
		switch{
		case strings.Contains(raw,"/uploads?type=image"):return &netResp{Status:200,Data:jx.M{"url":"https://iu.oneme.ru/uploadImage?r=test"}},nil
		case strings.Contains(raw,"/uploads?type=video"):return &netResp{Status:200,Data:jx.M{"url":"https://omub.okcdn.ru/upload.do?sig=test","token":"v"}},nil
		case strings.HasPrefix(raw,"https://iu.oneme.ru/"):
			uploads++;b,_:=io.ReadAll(body);if !strings.Contains(string(b),"test-file")||h["Authorization"]!="bot-token"{t.Fatal("image upload invalid")}
			return &netResp{Status:200,Data:jx.M{"photos":jx.M{"1":jx.M{"token":"p"}}}},nil
		case strings.HasPrefix(raw,"https://omub.okcdn.ru/"):
			uploads++;if h["Authorization"]!=""{t.Fatal("token leaked to signed upload URL")};return &netResp{Status:200,Raw:[]byte("<retval>1</retval>")},nil
		case strings.Contains(raw,"/messages?"):
			sends++;b,_:=io.ReadAll(body);data:=jx.Unmarshal(b);a:=jx.Maps(data,"attachments")
			if len(a)!=2||jx.Str(a[0],"type")!="image"||jx.Str(jx.Map(a[1],"payload"),"token")!="v"||jx.Str(data,"text")!="caption"{t.Fatalf("media order lost: %s",b)}
			if sends==1{return &netResp{Status:400,Data:jx.M{"code":"attachment.not.ready"}},nil}
			return &netResp{Status:200,Data:jx.M{"message":jx.M{"body":jx.M{"mid":"mid.123"}}}},nil
		}
		t.Fatal(raw);return nil,nil
	}
	sent,err:=maxPublishWith(context.Background(),c,NetPost{Text:"caption",Media:[]Prepared{{Kind:"photo",Path:photo},{Kind:"video",Path:video}}},req,func(context.Context,time.Duration)error{waits++;return nil})
	if err!=nil||sent==nil||sent.MessageIDs[0]!="mid.123"||sends!=2||uploads!=2||waits!=1{t.Fatalf("sent=%v err=%v sends=%d uploads=%d waits=%d",sent,err,sends,uploads,waits)}
}

func TestMAXRejectsUnexpectedUploadHostAndAmbiguousSend(t *testing.T){
	for _,raw:=range []string{"https://evil.example/","https://iu.oneme.ru.evil.example/","https://user@iu.oneme.ru/","http://iu.oneme.ru/"}{if maxUploadURL(raw,"image"){t.Fatal(raw)}}
	c:=&Channel{Ref:"42",Secrets:map[string]string{"token":"token"}}
	calls:=0
	_,err:=maxPublishWith(context.Background(),c,NetPost{Text:"text"},func(context.Context,string,string,map[string]string,io.Reader)(*netResp,error){calls++;return nil,ErrPublishUncertain},func(context.Context,time.Duration)error{t.Fatal("ambiguous send retried");return nil})
	if calls!=1||!errors.Is(err,ErrPublishUncertain){t.Fatal(err,calls)}
}
