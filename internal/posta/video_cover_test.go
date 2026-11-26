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
 "time"

 "posta/internal/config"
 "posta/internal/jx"
)

func makeCoverFixture(t *testing.T) string {
 t.Helper()
 dir:=t.TempDir()
 p:=filepath.Join(dir,"original.png")
 var b bytes.Buffer
 if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,320,180)));err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(p,b.Bytes(),0600);err!=nil{t.Fatal(err)}
 dst,err:=prepareVideoCover(p,dir,0)
 if err!=nil{t.Fatal(err)}
 if sniffMime(dst)!="image/jpeg"{t.Fatal("cover not converted to JPEG")}
 old,_:=os.ReadFile(p);if !bytes.Equal(old,b.Bytes()){t.Fatal("original image changed")}
 return dst
}

func TestVideoCoverOutboxPersistence(t *testing.T){
 cover:=makeCoverFixture(t)
 dir:=t.TempDir()
 video:=filepath.Join(dir,"clip.mp4")
 if err:=os.WriteFile(video,[]byte("video fixture"),0600);err!=nil{t.Fatal(err)}
 media:=[]Prepared{{Kind:"video",Path:video,CoverPath:cover}}
 for _,platform:=range []string{"telegram","instagram","pinterest","youtube"}{
  if err:=CheckVideoCovers(platform,media,nil);err!=nil{t.Fatal(platform,err)}
 }
 if CheckVideoCovers("max",media,nil)==nil||CheckVideoCovers("instagram",media,jx.M{"ig_story":true})==nil||CheckVideoCovers("instagram",media,jx.M{"ig_cover_seconds":"2"})==nil{t.Fatal("incompatible cover accepted")}
 a:=&App{Cfg:config.Server{DataDir:t.TempDir()}}
 stored,err:=a.outboxMediaMove("",randHex(6),PreparedMaps(media))
 if err!=nil{t.Fatal(err)}
 post:=jx.M{"media":stored}
 restored:=preparedFromPost(post)
 if len(restored)!=1||restored[0].CoverPath==cover||restored[0].Path==video||!outboxMediaReady(post){t.Fatalf("not persisted: %+v",restored)}
 want,_:=os.ReadFile(cover);got,_:=os.ReadFile(restored[0].CoverPath)
 if !bytes.Equal(want,got){t.Fatal("cover changed")}
 if err:=os.Remove(restored[0].CoverPath);err!=nil{t.Fatal(err)}
 if outboxMediaReady(post)||CheckVideoCovers("telegram",restored,nil)==nil{t.Fatal("missing cover treated as ready")}
}

func TestYouTubeCoverFailureDoesNotRepublish(t *testing.T){
 cover:=makeCoverFixture(t)
 data,_:=os.ReadFile(cover)
 video:=filepath.Join(t.TempDir(),"clip.mp4")
 if err:=os.WriteFile(video,[]byte("video fixture"),0600);err!=nil{t.Fatal(err)}
 for _,failure:=range []bool{false,true}{
  starts,uploads,polls,covers:=0,0,0,0
  req:=func(_ context.Context,method,endpoint string,headers map[string]string,body io.Reader)(*netResp,error){
   u,err:=url.Parse(endpoint);if err!=nil{t.Fatal(err)}
   switch{
   case method=="POST"&&strings.HasSuffix(u.Path,"/videos"):
    starts++;return &netResp{Status:200,Headers:http.Header{"Location":{"https://www.googleapis.com/upload/youtube/v3/videos?upload_id=test"}}},nil
   case method=="PUT":
    uploads++;return &netResp{Status:201,Data:jx.M{"id":"abcdefghijk"}},nil
   case method=="GET":
    polls++;status:="processing";if polls>1{status="succeeded"}
    return &netResp{Status:200,Data:jx.M{"items":[]jx.M{{"id":"abcdefghijk","processingDetails":jx.M{"processingStatus":status}}}}},nil
   case method=="POST"&&strings.HasSuffix(u.Path,"/thumbnails/set"):
    covers++;b,_:=io.ReadAll(body);if !bytes.Equal(b,data)||u.Query().Get("videoId")!="abcdefghijk"||headers["Content-Type"]!="image/jpeg"{t.Fatal("incorrect cover upload")}
    if failure{return &netResp{Status:403},nil}
    return &netResp{Status:200,Data:jx.M{"items":[]jx.M{{"default":jx.M{"url":"https://example.org/cover.jpg"}}}}},nil
   default:t.Fatal("unexpected request",method,endpoint);return nil,nil
   }
  }
  result,err:=youtubePublishWith(context.Background(),&Channel{Secrets:map[string]string{"access_token":"test"}},NetPost{Media:[]Prepared{{Kind:"video",Path:video,CoverPath:cover}}},req,func(context.Context,time.Duration)error{return nil})
  if err!=nil||result==nil||starts!=1||uploads!=1||polls!=2||covers!=1{t.Fatalf("failure %v: sent=%+v err=%v calls=%d/%d/%d/%d",failure,result,err,starts,uploads,polls,covers)}
  if (result.Warning!="")!=failure{t.Fatal("wrong warning",result.Warning)}
 }
}

func TestInstagramStoryPhotoPreparation(t *testing.T){
 dir:=t.TempDir();p:=filepath.Join(dir,"story.png")
 var b bytes.Buffer
 if err:=png.Encode(&b,image.NewRGBA(image.Rect(0,0,720,1280)));err!=nil{t.Fatal(err)}
 if err:=os.WriteFile(p,b.Bytes(),0600);err!=nil{t.Fatal(err)}
 result,err:=metaPreparePhoto("instagram_story",p,dir,0)
 if err!=nil||result.Width!=720||result.Height!=1280||sniffMime(result.Path)!="image/jpeg"{t.Fatalf("story: %+v %v",result,err)}
 if _,err:=metaPreparePhoto("instagram",p,dir,1);err==nil{t.Fatal("story ratio accepted as ordinary photo")}
}
