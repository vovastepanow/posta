package posta

import (
	"context"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestFacebookPageConnectionRefusesWrongIdentity(t *testing.T){
	_,err:=facebookConnectWith(context.Background(),"https://graph.facebook.com/v24.0",map[string]string{"page_id":"12345","access_token":"token"},func(context.Context,string,string,map[string]string,io.Reader)(*netResp,error){return &netResp{Status:200,Data:jx.M{"id":"54321","name":"Page","category":"Blog"}},nil})
	if err==nil{t.Fatal("wrong page accepted")}
}
func TestFacebookPhotosRemainUnpublishedUntilAllUploaded(t *testing.T){
	path:=filepath.Join(t.TempDir(),"photo.jpg");if err:=os.WriteFile(path,[]byte("image"),0600);err!=nil{t.Fatal(err)}
	c:=&Channel{Ref:"12345",Secrets:map[string]string{"access_token":"page-token"}}
	n:=0
	sent,err:=facebookPublishWith(context.Background(),"https://graph.facebook.com/v24.0",c,NetPost{Text:"caption",Media:[]Prepared{{Kind:"photo",Path:path},{Kind:"photo",Path:path}}},func(_ context.Context,method,raw string,h map[string]string,body io.Reader)(*netResp,error){
		b,_:=io.ReadAll(body)
		if h["Authorization"]!="Bearer page-token"{t.Fatal("wrong token")}
		if strings.HasSuffix(raw,"/photos"){
			n++;if !strings.Contains(string(b),"false"){t.Fatal("photo published prematurely")}
			id:="11111";if n==2{id="22222"};return &netResp{Status:200,Data:jx.M{"id":id}},nil
		}
		q,err:=url.ParseQuery(string(b));if err!=nil||n!=2||q.Get("message")!="caption"||jx.Str(jx.Unmarshal([]byte(q.Get("attached_media[0]"))),"media_fbid")!="11111"||jx.Str(jx.Unmarshal([]byte(q.Get("attached_media[1]"))),"media_fbid")!="22222"{t.Fatal("order lost",q)}
		return &netResp{Status:200,Data:jx.M{"id":"12345_33333"}},nil
	})
	if err!=nil||sent.MessageIDs[0]!="12345_33333"{t.Fatal(sent,err)}
}
