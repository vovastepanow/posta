package posta

import (
	"context"
	"net/url"
	"testing"

	"posta/internal/jx"
)

func TestVKPollPublication(t *testing.T) {
	ch:=&Channel{Ref:"42", Secrets:map[string]string{"token":"community"}}
	p:=NetPost{Poll:jx.M{"question":"Question?", "options":[]string{"A","B"}, "multiple":true}}
	calls:=0
	call:=func(_ context.Context, method string, args url.Values, token string)(any,error){
		calls++
		switch method {
		case "polls.create":
			if token!="user"||args.Get("owner_id")!="-42"||args.Get("is_multiple")!="1"||args.Get("add_answers")!="[\"A\",\"B\"]"{t.Fatal(method,args)}
			return jx.M{"id":123,"owner_id":-42},nil
		case "wall.post":
			if token!="community"||args.Get("attachments")!="poll-42_123"{t.Fatal(method,args)}
			return jx.M{"post_id":456},nil
		default:t.Fatal(method);return nil,nil
		}
	}
	sent,err:=vkPublishWith(context.Background(),ch,p,call,nil,func()string{return "user"})
	if err!=nil||sent==nil||calls!=2||sent.MessageIDs[0]!="456"{t.Fatal(sent,err,calls)}
	calls=0
	if _,err=vkPublishWith(context.Background(),ch,p,call,nil,func()string{return ""});err==nil||calls!=0{t.Fatal("missing user grant accepted",err,calls)}
}

func TestVKPollWrongOwnerStopsWallPost(t *testing.T) {
	ch:=&Channel{Ref:"42",Secrets:map[string]string{"token":"user"},Meta:jx.M{"poll_user_token":true}}
	p:=NetPost{Poll:jx.M{"question":"Question?", "options":[]string{"A","B"}}}
	_,err:=vkPublishWith(context.Background(),ch,p,func(_ context.Context, method string,_ url.Values,_ string)(any,error){
		if method!="polls.create"{t.Fatal("unexpected wall post")}
		return jx.M{"id":123,"owner_id":-43},nil
	},nil,nil)
	if err==nil{t.Fatal("wrong owner accepted")}
}
