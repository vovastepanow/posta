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

// A channel-specific filter must neither remove unrelated options nor
// affect the capability list of another account.
func TestChannelExtraKindsIsolation(t *testing.T) {
	for _, tc := range []struct {
		platform string
		meta jx.M
		excluded string
	}{
		{"instagram", jx.M{"account_type": "CREATOR"}, "ig_story"},
		{"instagram", nil, "ig_story"},
		{"max", jx.M{"chat_type": "chat"}, "first_comment"},
		{"max", jx.M{"chat_type": "group"}, "first_comment"},
	} {
		got := ChannelExtraKinds(&Channel{Platform: tc.platform, Meta: tc.meta})
		all := postExtraKinds(tc.platform)
		if contains(got, tc.excluded) || !contains(all, tc.excluded) || len(got) != len(all)-1 {
			t.Fatalf("%s: wrong filtered capabilities: %v / %v", tc.platform, got, all)
		}
		for _, kind := range all {
			if kind != tc.excluded && !contains(got, kind) {
				t.Fatalf("%s: unrelated capability removed: %s", tc.platform, kind)
			}
		}
	}
	for _, c := range []*Channel{
		{Platform: "instagram", Meta: jx.M{"account_type": "BUSINESS"}},
		{Platform: "max", Meta: jx.M{"chat_type": "channel"}},
		{Platform: "max"},
		{Platform: "vk"},
	} {
		got, all := ChannelExtraKinds(c), postExtraKinds(c.Platform)
		if len(got) != len(all) {
			t.Fatalf("%s: unexpected filter: %v / %v", c.Platform, got, all)
		}
		for i, kind := range all {
			if got[i] != kind {
				t.Fatalf("%s: capability order changed: %v / %v", c.Platform, got, all)
			}
		}
	}
	if got := ChannelExtraKinds(nil); got == nil || len(got) != 0 {
		t.Fatalf("nil channel: expected a non-nil empty list, got %v", got)
	}
}
