package posta

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"bytes"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestVKVideoUploadAndWallPublication(t *testing.T){
	file:=filepath.Join(t.TempDir(),"clip.mp4")
	raw:=[]byte{0,0,0,24,'f','t','y','p','m','p','4','2',0,0,0,0,'m','p','4','2','i','s','o','m'}
	if err:=os.WriteFile(file,raw,0600);err!=nil{t.Fatal(err)}
	for _,fail:=range []bool{false,true}{
		var methods []string
		call:=func(_ context.Context,method string,p url.Values,token string)(any,error){
			methods=append(methods,method+":"+token)
			switch method{
			case "video.save":
				if token=="group"{return nil,vkMethodError(method,27,"group auth")}
				if p.Get("group_id")!="42"||p.Get("wallpost")!="0"{t.Fatal(p)}
				return jx.M{"owner_id":-42,"video_id":123,"upload_url":"https://pu.vk.com/video-upload"},nil
			case "wall.post":
				if token!="group"||p.Get("owner_id")!="-42"||p.Get("attachments")!="video-42_123"{t.Fatal(p,token)}
				return jx.M{"post_id":456},nil
			default:t.Fatal(method);return nil,nil
			}
		}
		request:=func(_ context.Context,method,raw string,h map[string]string,b io.Reader)(*netResp,error){
			mediaType,params,err:=mime.ParseMediaType(h["Content-Type"])
			if err!=nil||mediaType!="multipart/form-data"||h["Authorization"]!=""||method!="POST"||raw!="https://pu.vk.com/video-upload"{t.Fatalf("bad upload headers: %v %v",mediaType,err)}
			reader:=multipart.NewReader(b,params["boundary"])
			part,err:=reader.NextPart()
			if err!=nil{t.Fatal(err)}
			data,err:=io.ReadAll(part)
			if err!=nil||part.FormName()!="video_file"||part.FileName()!="clip.mp4"||!bytes.Equal(data,[]byte{0,0,0,24,'f','t','y','p','m','p','4','2',0,0,0,0,'m','p','4','2','i','s','o','m'}){t.Fatalf("bad multipart video: name=%s filename=%s err=%v",part.FormName(),part.FileName(),err)}
			if _,err=reader.NextPart();err!=io.EOF{t.Fatalf("unexpected extra part: %v",err)}
			if fail{return &netResp{Status:200,Data:jx.M{"video_id":999,"size":24}},nil}
			return &netResp{Status:200,Data:jx.M{"video_id":123,"size":24}},nil
		}
		sent,err:=vkPublishWith(context.Background(),&Channel{Ref:"42",Secrets:map[string]string{"token":"group"}},NetPost{Media:[]Prepared{{Kind:"video",Path:file}}},call,request,func()string{return "user"})
		if fail{
			if !errors.Is(err,ErrPublishUncertain)||len(methods)!=2{t.Fatal(sent,err,methods)}
		}else if err!=nil||sent==nil||sent.MessageIDs[0]!="456"||strings.Join(methods,",")!="video.save:group,video.save:user,wall.post:group"{t.Fatal(sent,err,methods)}
	}
}
func TestVKVideoRequiresCorrectGroup(t *testing.T){
	file:=filepath.Join(t.TempDir(),"clip.mp4")
	if err:=os.WriteFile(file,[]byte{0,0,0,24,'f','t','y','p','m','p','4','2',0,0,0,0,'m','p','4','2','i','s','o','m'},0600);err!=nil{t.Fatal(err)}
	_,err:=vkVideoAttachmentWith(context.Background(),"42",file,"title","token",func(context.Context,string,url.Values,string)(any,error){return jx.M{"owner_id":-43,"video_id":1,"upload_url":"https://pu.vk.com/upload"},nil},func(context.Context,string,string,map[string]string,io.Reader)(*netResp,error){t.Fatal("uploaded to wrong group");return nil,nil})
	if err==nil{t.Fatal("wrong owner accepted")}
}
