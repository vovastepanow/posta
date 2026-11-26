package posta

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestOKPublishPhotoTokensInOrder(t *testing.T){
	dir:=t.TempDir();p1:=filepath.Join(dir,"1.jpg");p2:=filepath.Join(dir,"2.jpg")
	for _,p:=range []string{p1,p2}{if err:=os.WriteFile(p,[]byte("image"),0600);err!=nil{t.Fatal(err)}}
	c:=&Channel{Ref:"42",Meta:jx.M{"gid":"42"}}
	n:=0;published:=false
	call:=func(_ context.Context,method string,params map[string]string)(*netResp,error){
		switch method{
		case "photosV2.getUploadUrl":
			n++;id:=string(rune('a'+n-1));return &netResp{Status:200,Data:jx.M{"photo_ids":[]string{id},"upload_url":"https://upload.okcdn.ru/"+id}},nil
		case "mediatopic.post":
			published=true;data:=jx.Unmarshal([]byte(params["attachment"]));m:=jx.Maps(data,"media")
			if len(m)!=2||jx.Str(m[0],"text")!="caption"{t.Fatalf("bad attachment: %v",data)}
			list:=jx.Maps(m[1],"list");if len(list)!=2||jx.Str(list[0],"id")!="token-a"||jx.Str(list[1],"id")!="token-b"{t.Fatal(list)}
			return &netResp{Status:200,Raw:[]byte("\"12345\"")},nil
		}
		t.Fatal("unexpected method",method);return nil,nil
	}
	request:=func(_ context.Context,method,raw string,h map[string]string,body io.Reader)(*netResp,error){
		id:=raw[len(raw)-1:];b,_:=io.ReadAll(body)
		if method!="POST"||h["Authorization"]!=""||!strings.Contains(string(b),"image"){t.Fatal("bad upload")}
		return &netResp{Status:200,Data:jx.M{"photos":jx.M{id:jx.M{"token":"token-"+id}}}},nil
	}
	sent,err:=okPublishWith(context.Background(),c,NetPost{Text:"caption",Media:[]Prepared{{Kind:"photo",Path:p1},{Kind:"photo",Path:p2}}},call,request)
	if err!=nil||!published||sent.MessageIDs[0]!="12345"{t.Fatal(sent,err)}
}
func TestOKPhotoMissingTokenPreventsPublication(t *testing.T){
	path:=filepath.Join(t.TempDir(),"p.jpg");if err:=os.WriteFile(path,[]byte("image"),0600);err!=nil{t.Fatal(err)}
	_,err:=okPublishWith(context.Background(),&Channel{Ref:"42"},NetPost{Media:[]Prepared{{Kind:"photo",Path:path}}},func(_ context.Context,method string,_ map[string]string)(*netResp,error){
		if method!="photosV2.getUploadUrl"{t.Fatal("unexpected publication")}
		return &netResp{Status:200,Data:jx.M{"photo_ids":[]string{"a"},"upload_url":"https://upload.okcdn.ru/a"}},nil
	},func(context.Context,string,string,map[string]string,io.Reader)(*netResp,error){return &netResp{Status:200,Data:jx.M{"photos":jx.M{"a":jx.M{"error":"bad"}}}},nil})
	if err==nil{t.Fatal("missing token accepted")}
}
