package posta
import("context";"fmt";"net/url";"testing";"posta/internal/jx")
func TestFirstCommentDoesNotFailParent(t *testing.T){
 calls:=0
 for _,success:=range []bool{true,false}{
  calls=0
  sent,err:=vkPublishWith(context.Background(),&Channel{Ref:"42",Secrets:map[string]string{"token":"group"}},NetPost{Text:"post",Extras:jx.M{"first_comment":"comment"}},func(_ context.Context,method string,args url.Values,_ string)(any,error){
   calls++
   if method=="wall.post"{return jx.M{"post_id":123},nil}
   if method!="wall.createComment"||args.Get("post_id")!="123"||args.Get("message")!="comment"{t.Fatal(method,args)}
   if success{return jx.M{"comment_id":456},nil}
   return nil,fmt.Errorf("timeout")
  },nil,nil)
  if err!=nil||sent==nil||calls!=2||sent.MessageIDs[0]!="123"{t.Fatal(sent,err,calls)}
  if success&&(sent.CommentID!="456"||sent.Warning!=""){t.Fatal(sent)}
  if !success&&(sent.CommentID!=""||sent.Warning==""){t.Fatal(sent)}
 }
 if _,err:=PublishExtrasCheck("vk","post",nil,nil,jx.M{"first_comment":"comment","close_comments":true});err==nil{t.Fatal("conflicting comment accepted")}
 if contains(ChannelExtraKinds(&Channel{Platform:"max",Meta:jx.M{"chat_type":"chat"}}),"first_comment"){t.Fatal("chat exposed comments")}
}
