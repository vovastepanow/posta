package posta

import (
 "bytes"
 "context"
 "image"
 "image/jpeg"
 "io"
 "mime"
 "mime/multipart"
 "net/http"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"

 "posta/internal/jx"
)

func TestXPhotoPublication(t *testing.T){
 var b bytes.Buffer
 if err:=jpeg.Encode(&b,image.NewRGBA(image.Rect(0,0,8,8)),nil);err!=nil{t.Fatal(err)}
 path:=filepath.Join(t.TempDir(),"photo.jpg");if err:=os.WriteFile(path,b.Bytes(),0600);err!=nil{t.Fatal(err)}
 c:=&Channel{Secrets:map[string]string{"access_token":"token"}}
 calls:=0
 req:=func(ctx context.Context,method,endpoint string,h map[string]string,body io.Reader)(*netResp,error){
  calls++
  if endpoint==xMediaBase{return &netResp{Status:200,Data:jx.M{"data":jx.M{"id":"12345"}}},nil}
  if endpoint=="https://api.x.com/2/tweets"{
   raw,_:=io.ReadAll(body);if !strings.Contains(string(raw),"\"media_ids\":[\"12345\"]"){t.Fatal(string(raw))}
   return &netResp{Status:201,Data:jx.M{"data":jx.M{"id":"23456"}}},nil
  }
  t.Fatal(endpoint);return nil,nil
 }
 sent,err:=xPublishWith(context.Background(),c,NetPost{Media:[]Prepared{{Kind:"photo",Path:path}}},req,func(context.Context,time.Duration)error{return nil})
 if err!=nil||sent.MessageIDs[0]!="23456"||calls!=2{t.Fatalf("%+v %v %d",sent,err,calls)}
}

func TestXChunkedVideoFailureStopsPost(t *testing.T){
 path:=filepath.Join(t.TempDir(),"video.mp4")
 // ISO base media header for MIME detection; test is a recorded transport flow,
 // not a real video codec validation.
 raw:=make([]byte,4*1024*1024+23);copy(raw,[]byte{0,0,0,24,'f','t','y','p','m','p','4','2',0,0,0,0,'m','p','4','2','i','s','o','m'})
 if err:=os.WriteFile(path,raw,0600);err!=nil{t.Fatal(err)}
 chunks:=0;total:=0
 req:=func(ctx context.Context,method,endpoint string,h map[string]string,body io.Reader)(*netResp,error){
  switch{
  case strings.HasSuffix(endpoint,"/initialize"):return &netResp{Status:200,Data:jx.M{"data":jx.M{"id":"12345"}}},nil
  case strings.HasSuffix(endpoint,"/append"):
   _,params,err:=mime.ParseMediaType(h["Content-Type"]);if err!=nil{t.Fatal(err)}
   mr:=multipart.NewReader(body,params["boundary"])
   first,err:=mr.NextPart();if err!=nil||first.FormName()!="segment_index"{t.Fatal("segment missing")}
   first.Close();part,err:=mr.NextPart();if err!=nil||part.FormName()!="media"{t.Fatal("media missing")}
   n,err:=io.Copy(io.Discard,part);if err!=nil{t.Fatal(err)};total+=int(n);chunks++;return &netResp{Status:204},nil
  case strings.HasSuffix(endpoint,"/finalize"):return &netResp{Status:200,Data:jx.M{"data":jx.M{"id":"12345","processing_info":jx.M{"state":"pending","check_after_secs":1}}}},nil
  case method==http.MethodGet:return &netResp{Status:200,Data:jx.M{"data":jx.M{"id":"12345","processing_info":jx.M{"state":"failed"}}}},nil
  default:t.Fatal("published failed video");return nil,nil
  }
 }
 _,err:=xPublishWith(context.Background(),&Channel{Secrets:map[string]string{"access_token":"token"}},NetPost{Media:[]Prepared{{Kind:"video",Path:path}}},req,func(context.Context,time.Duration)error{return nil})
 if err==nil||chunks!=2||total!=len(raw){t.Fatalf("err %v chunks %d total %d",err,chunks,total)}
}
