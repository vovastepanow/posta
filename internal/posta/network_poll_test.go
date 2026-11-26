package posta

import (
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestNetworkPollChecks(t *testing.T) {
	p := jx.M{"question":"Choose", "options":[]string{"A","B"}, "multiple":true}
	if _, err := NetworkPollCheck("telegram",p); err != nil { t.Fatal(err) }
	if _, err := NetworkPollCheck("ok",p); err != nil { t.Fatal(err) }
	if _, err := NetworkPollCheck("x",p); err == nil { t.Fatal("X multiple accepted") }
	if _, err := NetworkPollCheck("instagram",p); err == nil { t.Fatal("unsupported poll accepted") }
	p["multiple"]=false
	p["options"]=[]string{"A",strings.Repeat("b",26)}
	if _, err := NetworkPollCheck("x",p); err == nil { t.Fatal("long option accepted") }
	p["options"]=[]string{"A","a"}
	if _, err := NetworkPollCheck("telegram",p); err == nil { t.Fatal("duplicate accepted") }
}

func TestOKPollPublish(t *testing.T) {
	p := NetPost{Poll:jx.M{"question":"Choose", "options":[]string{"A","B"}, "multiple":true}}
	sent,err:=okPublishWith(context.Background(),&Channel{Ref:"42"},p,func(_ context.Context,method string,params map[string]string)(*netResp,error){
		if method!="mediatopic.post"{t.Fatal(method)}
		m:=jx.Maps(jx.Unmarshal([]byte(params["attachment"])),"media")
		if len(m)!=1||jx.Str(m[0],"type")!="poll"||jx.Str(m[0],"question")!="Choose"||jx.Str(m[0],"options")!="AnonymousVoting"||len(jx.Maps(m[0],"answers"))!=2{t.Fatal(m)}
		return &netResp{Status:200,Raw:[]byte("\"12345\"")},nil
	},nil)
	if err!=nil||sent==nil{t.Fatal(sent,err)}
}

func TestXPollPublish(t *testing.T) {
	p:=NetPost{Poll:jx.M{"question":"Choose","options":[]string{"A","B"},"duration_minutes":60}}
	sent,err:=xPublishWith(context.Background(),&Channel{Secrets:map[string]string{"access_token":"token"}},p,func(_ context.Context,method,endpoint string,_ map[string]string,body io.Reader)(*netResp,error){
		if method!="POST"||endpoint!="https://api.x.com/2/tweets"{t.Fatal(method,endpoint)}
		raw,_:=io.ReadAll(body);data:=jx.Unmarshal(raw);poll:=jx.Map(data,"poll")
		if jx.Str(data,"text")!="Choose"||jx.Int(poll,"duration_minutes")!=60||len(jx.Strs(poll,"options"))!=2{t.Fatal(string(raw))}
		return &netResp{Status:201,Data:jx.M{"data":jx.M{"id":"12345"}}},nil
	},func(context.Context,time.Duration)error{return nil})
	if err!=nil||sent==nil{t.Fatal(sent,err)}
}
