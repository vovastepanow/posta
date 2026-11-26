package posta

import(
 "context"
 "errors"
 "io"
 "os"
 "path/filepath"
 "strings"
 "testing"
 "posta/internal/jx"
)

func TestOKVideoPublicationPreservesLongIDAndStopsAfterFinalize(t *testing.T){
 path:=filepath.Join(t.TempDir(),"v.mp4")
 if err:=os.WriteFile(path,make([]byte,16*1024),0600);err!=nil{t.Fatal(err)}
 for _,fail:=range []bool{false,true}{
  var methods []string
  call:=func(_ context.Context,m string,p map[string]string)(*netResp,error){
   methods=append(methods,m)
   switch m{
   case "video.getUploadUrl":
    if p["post_form"]!="true"||p["gid"]!="123"||p["attachment_type"]!="MOVIE"{t.Fatalf("invalid init: %v",p)}
    return &netResp{Status:200,Data:jx.M{"upload_url":"https://upload.okcdn.ru/video"},Raw:[]byte(`{"video_id":9007199254740993,"upload_url":"https://upload.okcdn.ru/video"}`)},nil
   case "video.update":
    if p["vid"]!="9007199254740993"{t.Fatalf("lost exact video ID: %v",p)}
    if fail{return &netResp{Status:500},nil}
    return &netResp{Status:200},nil
   case "mediatopic.post":
    if !strings.Contains(p["attachment"],`"id":"9007199254740993"`){t.Fatal(p["attachment"])}
    return &netResp{Status:200,Raw:[]byte(`"123456"`)},nil
   default:t.Fatal(m);return nil,nil
   }
  }
  request:=func(_ context.Context,_ ,_ string,_ map[string]string,_ io.Reader)(*netResp,error){return &netResp{Status:200},nil}
  sent,err:=okPublishWith(context.Background(),&Channel{Ref:"123"},NetPost{Text:"video",Media:[]Prepared{{Kind:"video",Path:path}}},call,request)
  if fail{if !errors.Is(err,ErrPublishUncertain)||len(methods)!=2{t.Fatalf("unexpected retry state %v %v",err,methods)}}else if err!=nil||sent==nil||len(methods)!=3{t.Fatalf("send %v %v %v",sent,err,methods)}
 }
}
