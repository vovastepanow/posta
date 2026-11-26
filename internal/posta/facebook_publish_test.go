package posta

import (
	"context"
	"io"
	"errors"
	"mime"
	"mime/multipart"
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

func TestFacebookVideoPublish(t *testing.T){
	path:=filepath.Join(t.TempDir(),"video.mp4")
	if err:=os.WriteFile(path,[]byte{0,0,0,24,'f','t','y','p','m','p','4','2',0,0,0,0,'m','p','4','2','i','s','o','m'},0600);err!=nil{t.Fatal(err)}
	c:=&Channel{Ref:"12345",Secrets:map[string]string{"access_token":"page-token"}}
	for _,id:=range []string{"67890",""}{
		calls:=0
		sent,err:=facebookPublishWith(context.Background(),"https://graph.facebook.com/v24.0",c,NetPost{Text:"caption",Media:[]Prepared{{Kind:"video",Path:path}}},func(_ context.Context,method,raw string,h map[string]string,body io.Reader)(*netResp,error){
			calls++
			if method!="POST"||raw!="https://graph-video.facebook.com/v24.0/12345/videos"||h["Authorization"]!="Bearer page-token"{t.Fatal("incorrect video route")}
			_,params,err:=mime.ParseMediaType(h["Content-Type"]);if err!=nil{t.Fatal(err)}
			reader:=multipart.NewReader(body,params["boundary"])
			fields:=map[string]string{}
			for{part,err:=reader.NextPart();if err==io.EOF{break};if err!=nil{t.Fatal(err)};b,err:=io.ReadAll(part);if err!=nil{t.Fatal(err)};fields[part.FormName()]=string(b)}
			if fields["description"]!="caption"||fields["published"]!="true"||len(fields["source"])!=24{t.Fatal("video upload is missing fields")}
			return &netResp{Status:200,Data:jx.M{"id":id}},nil
		})
		if calls!=1{t.Fatal("unexpected request count",calls)}
		if id==""{if !errors.Is(err,ErrPublishUncertain){t.Fatal(sent,err)}}else if err!=nil||sent==nil||sent.MessageIDs[0]!=id{t.Fatal(sent,err)}
	}
	_,err:=facebookPublishWith(context.Background(),"https://graph.facebook.com/v24.0",c,NetPost{Media:[]Prepared{{Kind:"photo",Path:"unread"},{Kind:"video",Path:path}}},func(context.Context,string,string,map[string]string,io.Reader)(*netResp,error){t.Fatal("mixed album reached API");return nil,nil})
	if err==nil{t.Fatal("mixed album accepted")}
}
