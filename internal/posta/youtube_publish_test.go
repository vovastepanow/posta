package posta

import (
 "context"
 "fmt"
 "io"
 "net/http"
 "os"
 "path/filepath"
 "testing"
 "time"
 "posta/internal/jx"
)
func TestYouTubeResumeWithinSession(t *testing.T){
 p:=filepath.Join(t.TempDir(),"movie.mp4")
 if err:=os.WriteFile(p,make([]byte,youtubeChunkSize+23),0600);err!=nil{t.Fatal(err)}
 total:=youtubeChunkSize+23
 location:="https://www.googleapis.com/upload/youtube/v3/videos?upload_id=test"
 var starts,puts int
 req:=func(_ context.Context,method,raw string,h map[string]string,b io.Reader)(*netResp,error){
  if method=="POST"{starts++;return &netResp{Status:200,Headers:http.Header{"Location":{location}}},nil}
  puts++
  if raw!=location||h["Authorization"]!="Bearer test"{t.Fatalf("bad request %s %v",raw,h)}
  switch puts{
  case 1:
   if h["Content-Range"]!=fmt.Sprintf("bytes 0-%d/%d",youtubeChunkSize-1,total){t.Fatal(h)}
   return nil,fmt.Errorf("interrupted")
  case 2:
   if h["Content-Range"]!=fmt.Sprintf("bytes */%d",total){t.Fatal(h)}
   return &netResp{Status:308,Headers:http.Header{"Range":{fmt.Sprintf("bytes=0-%d",youtubeChunkSize-1)}}},nil
  case 3:
   if h["Content-Range"]!=fmt.Sprintf("bytes %d-%d/%d",youtubeChunkSize,total-1,total){t.Fatal(h)}
   n,_:=io.Copy(io.Discard,b);if n!=23{t.Fatalf("sent %d bytes",n)}
   return &netResp{Status:201,Data:jx.M{"id":"abcdefghijk"}},nil
  default:t.Fatal("unexpected upload");return nil,nil
  }
 }
 sent,err:=youtubePublishWith(context.Background(),&Channel{Secrets:map[string]string{"access_token":"test"}},NetPost{Media:[]Prepared{{Kind:"video",Path:p}}},req,func(context.Context,time.Duration)error{return nil})
 if err!=nil||sent==nil||starts!=1||puts!=3{t.Fatalf("%v %v %d %d",sent,err,starts,puts)}
}
func TestYouTubeUploadLocation(t *testing.T){
 for _,u:=range []string{"https://www.googleapis.com.evil.test/upload/youtube/v3/videos?upload_id=x","https://www.googleapis.com@evil.test/upload/youtube/v3/videos?upload_id=x","https://www.googleapis.com/upload/youtube/v3/videos","https://www.googleapis.com:444/upload/youtube/v3/videos?upload_id=x"}{if youtubeUploadLocation(u){t.Fatal(u)}}
}
