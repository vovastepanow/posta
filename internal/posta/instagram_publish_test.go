package posta

import (
 "bytes"
 "context"
 "image"
 "image/png"
 "io"
 "net/http"
 "net/url"
 "os"
 "path/filepath"
 "strings"
 "testing"

 "posta/internal/jx"
)

func TestInstagramPreflightAndPrepare(t *testing.T){
 a:=&App{}
 if err:=a.NetworkDeliveryPreflight("instagram","text only",nil);err==nil{t.Fatal("text-only Instagram accepted")}
 dir:=t.TempDir();src:=filepath.Join(dir,"original.png")
 var b bytes.Buffer
 if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,480,600)));err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(src,b.Bytes(),0600);err!=nil{t.Fatal(err)}
 p,err:=metaPreparePhoto("instagram",src,dir,0)
 if err!=nil||p.Width!=480||p.Height!=600||sniffMime(p.Path)!="image/jpeg"||p.Path==src{t.Fatalf("invalid photo variant: %+v, %v",p,err)}
 old,_:=os.ReadFile(src);if !bytes.Equal(old,b.Bytes()){t.Fatal("source mutated")}
}

func TestInstagramReelsAndFailureStopsFinalPublish(t *testing.T){
 spec:=metaContainerSpec{name:"Instagram",base:"https://graph.instagram.com/v24.0",create:"media",publish:"media_publish",status:"status_code",caption:"caption",textLimit:2200}
 c:=&Channel{Ref:"12345",Secrets:map[string]string{"access_token":"token"}}
 p:=NetPost{Text:"caption",Media:[]Prepared{{Kind:"video",Width:1080,Height:1920}}}
 for _,status:=range []string{"FINISHED","ERROR"}{
  t.Run(status,func(t *testing.T){
   calls,revoked:=0,0
   req:=func(ctx context.Context,method,endpoint string,headers map[string]string,body io.Reader)(*netResp,error){
    calls++
    if method==http.MethodGet{
     if !strings.Contains(endpoint,"fields=status_code"){t.Fatal(endpoint)}
     return &netResp{Status:200,Data:jx.M{"status_code":status}},nil
    }
    raw,_:=io.ReadAll(body);q,_:=url.ParseQuery(string(raw))
    if strings.HasSuffix(endpoint,"/media_publish"){
     if status!="FINISHED"||q.Get("creation_id")!="10001"{t.Fatal("unexpected final publication")}
     return &netResp{Status:200,Data:jx.M{"id":"20001"}},nil
    }
    if q.Get("media_type")!="REELS"||q.Get("caption")!="caption"||q.Get("video_url")==""{t.Fatal(q)}
    return &netResp{Status:200,Data:jx.M{"id":"10001"}},nil
   }
   lease:=func(Prepared)(string,func(),error){return "https://example.org/clip.mp4",func(){revoked++},nil}
   sent,err:=metaContainerPublish(context.Background(),c,p,spec,req,lease,func(context.Context)error{return nil})
   if revoked!=1{t.Fatal("media lease not revoked")}
   if status=="FINISHED"{if err!=nil||sent.MessageIDs[0]!="20001"||calls!=3{t.Fatalf("sent %v err %v calls %d",sent,err,calls)}}else if err==nil||calls!=2{t.Fatalf("not stopped: %v %d",err,calls)}
  })
 }
}

func TestInstagramStoryAndReelCover(t *testing.T){
 spec:=metaContainerSpec{name:"Instagram",base:"https://graph.instagram.com/v24.0",create:"media",publish:"media_publish",status:"status_code",caption:"caption",textLimit:2200}
 for _,tc:=range []struct{name,account,caption string;extra jx.M;wantType,wantOffset string;wantErr bool}{
  {"business-story","BUSINESS","",jx.M{"ig_story":true},"STORIES","",false},
  {"creator-story","MEDIA_CREATOR","",jx.M{"ig_story":true},"","",true},
  {"story-with-caption","BUSINESS","must not lose",jx.M{"ig_story":true},"","",true},
  {"reel-cover","MEDIA_CREATOR","caption",jx.M{"ig_cover_seconds":"1.25"},"REELS","1250",false},
  {"cover-outside","BUSINESS","caption",jx.M{"ig_cover_seconds":"10"},"","",true},
 }{t.Run(tc.name,func(t *testing.T){
  c:=&Channel{Platform:"instagram",Ref:"12345",Secrets:map[string]string{"access_token":"token"},Meta:jx.M{"account_type":tc.account}}
  p:=NetPost{Text:tc.caption,Media:[]Prepared{{Kind:"video",Duration:10}},Extras:tc.extra}
  calls:=0;req:=func(ctx context.Context,method,endpoint string,headers map[string]string,body io.Reader)(*netResp,error){calls++;if method==http.MethodGet{return &netResp{Status:200,Data:jx.M{"status_code":"FINISHED"}},nil};if strings.HasSuffix(endpoint,"/media_publish"){return &netResp{Status:200,Data:jx.M{"id":"20001"}},nil};raw,_:=io.ReadAll(body);q,_:=url.ParseQuery(string(raw));if q.Get("media_type")!=tc.wantType||q.Get("thumb_offset")!=tc.wantOffset||q.Get("caption")!=tc.caption{t.Fatal(q)};if tc.wantType=="STORIES"&&q.Has("caption"){t.Fatal("story caption field")};return &netResp{Status:200,Data:jx.M{"id":"10001"}},nil}
  lease:=func(Prepared)(string,func(),error){return "https://example.org/clip.mp4",func(){},nil}
  _,err:=metaContainerPublish(context.Background(),c,p,spec,req,lease,func(context.Context)error{return nil})
  if tc.wantErr{if err==nil||calls!=0{t.Fatalf("expected preflight failure, calls=%d err=%v",calls,err)}}else if err!=nil||calls!=3{t.Fatalf("calls=%d err=%v",calls,err)}
 })}
}
