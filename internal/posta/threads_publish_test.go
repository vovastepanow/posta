package posta

import (
 "context"
 "io"
 "net/url"
 "strings"
 "testing"
 "posta/internal/jx"
)

func TestThreadsCarousel(t *testing.T){
 var calls []string
 revoked:=0
 req:=func(ctx context.Context,method,endpoint string,h map[string]string,body io.Reader)(*netResp,error){
  if h["Authorization"]!="Bearer token"{t.Fatal("token absent")}
  if method=="GET"{calls=append(calls,"ready");return &netResp{Status:200,Data:jx.M{"status":"FINISHED"}},nil}
  data,_:=io.ReadAll(body);q,_:=url.ParseQuery(string(data))
  if strings.HasSuffix(endpoint,"/threads_publish"){
   if q.Get("creation_id")!="10003"{t.Fatalf("published wrong container: %s",q.Encode())};calls=append(calls,"publish");return &netResp{Status:200,Data:jx.M{"id":"20001"}},nil
  }
  kind:=q.Get("media_type");calls=append(calls,kind)
  id:="10001"
  switch kind{
  case "IMAGE":if q.Get("is_carousel_item")!="true"||q.Get("image_url")==""{t.Fatal(q)}
  case "VIDEO":id="10002";if q.Get("is_carousel_item")!="true"||q.Get("video_url")==""{t.Fatal(q)}
  case "CAROUSEL":id="10003";if q.Get("children")!="10001,10002"||q.Get("text")!="caption"{t.Fatal(q)}
  default:t.Fatal(kind)
  }
  return &netResp{Status:200,Data:jx.M{"id":id}},nil
 }
 lease:=func(p Prepared)(string,func(),error){return "https://example.org/media/"+p.Kind,func(){revoked++},nil}
 sent,err:=threadsPublishWith(context.Background(),&Channel{Ref:"12345",Secrets:map[string]string{"access_token":"token"}},NetPost{Text:"caption",Media:[]Prepared{{Kind:"photo"},{Kind:"video"}}},req,lease,func(context.Context)error{return nil})
 if err!=nil||sent.MessageIDs[0]!="20001"||revoked!=2||strings.Join(calls,",")!="IMAGE,ready,VIDEO,ready,CAROUSEL,ready,publish"{t.Fatalf("sent=%v err=%v calls=%v revoked=%d",sent,err,calls,revoked)}
}
