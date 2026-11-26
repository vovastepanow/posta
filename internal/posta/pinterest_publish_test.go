package posta

import(
 "context"
 "io"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "time"
 "posta/internal/jx"
)
func TestPinterestVideoWaitsForProcessing(t *testing.T){
 p:=filepath.Join(t.TempDir(),"video.mp4")
 if err:=os.WriteFile(p,make([]byte,32),0600);err!=nil{t.Fatal(err)}
 for _,failed:=range []bool{false,true}{
  var polls,pins int
  request:=func(_ context.Context,method,loc string,h map[string]string,b io.Reader)(*netResp,error){
   switch{
   case loc=="https://api.pinterest.com/v5/media":
    return &netResp{Status:201,Data:jx.M{"media_id":"123","upload_url":"https://pinterest-media-upload.s3-accelerate.amazonaws.com/","upload_parameters":jx.M{"key":"example","policy":"signed"}}},nil
   case strings.Contains(loc,"amazonaws.com"):
    if h["Authorization"]!=""{t.Fatal("OAuth token leaked")}
    return &netResp{Status:204},nil
   case loc=="https://api.pinterest.com/v5/media/123":
    polls++
    status:="processing"
    if failed{status="failed"}else if polls>1{status="succeeded"}
    return &netResp{Status:200,Data:jx.M{"status":status}},nil
   case loc=="https://api.pinterest.com/v5/pins":
    pins++
    raw,_:=io.ReadAll(b)
    if !strings.Contains(string(raw),`"media_id":"123"`)||!strings.Contains(string(raw),"cover_image_data"){t.Fatal(string(raw))}
    return &netResp{Status:201,Data:jx.M{"id":"456"}},nil
   default:t.Fatal(loc);return nil,nil
   }
  }
  sent,err:=pinterestPublishWith(context.Background(),&Channel{Meta:jx.M{"board_id":"789"}},NetPost{Media:[]Prepared{{Kind:"video",Path:p}}},request,func(context.Context,string)[]byte{return []byte{255,216,255,224,0,0,0,0}},func(context.Context,time.Duration)error{return nil})
  if failed{if err==nil||pins!=0{t.Fatalf("failure %v %d",err,pins)}}else if err!=nil||sent==nil||pins!=1||polls!=2{t.Fatalf("%v %v %d %d",sent,err,pins,polls)}
 }
}
func TestPinterestUploadDestination(t *testing.T){
 for _,raw:=range []string{"https://unrelated.s3.amazonaws.com/","https://pinterest-media-upload.s3.amazonaws.com.evil.test/","https://pinterest-media-upload.s3.amazonaws.com@evil.test/"}{if pinterestUploadURL(raw){t.Fatal(raw)}}
}
