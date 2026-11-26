package posta
import("context";"net/url";"testing";"posta/internal/jx")
func TestCompoundPollContent(t *testing.T){
 p:=jx.M{"question":"Q","options":[]string{"A","B"},"with_content":true}
 checked,err:=NetworkPollCheck("vk",p);if err!=nil{t.Fatal(err)}
 if got,err:=PollContentText("vk","caption",9,checked);err!=nil||got!="caption"{t.Fatal(got,err)}
 if _,err=PollContentText("vk","caption",10,checked);err==nil{t.Fatal("poll overflow")}
 if _,err=NetworkPollCheck("telegram",p);err==nil{t.Fatal("compound Telegram accepted")}
 calls:=0
 sent,err:=vkPublishWith(context.Background(),&Channel{Ref:"42",Secrets:map[string]string{"token":"group"}},NetPost{Text:"caption",Poll:p},func(_ context.Context,method string,args url.Values,_ string)(any,error){
 calls++;if method=="polls.create"{return jx.M{"id":123,"owner_id":-42},nil}
 if method!="wall.post"||args.Get("message")!="caption"||args.Get("attachments")!="poll-42_123"{t.Fatal(method,args)}
 return jx.M{"post_id":456},nil
 },nil,func()string{return "user"})
 if err!=nil||sent==nil||calls!=2{t.Fatal(sent,err,calls)}
}
