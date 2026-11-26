package posta

import("context";"io";"net/url";"strings";"testing";"posta/internal/jx")
func TestThreadsPollPublication(t *testing.T){
	calls:=0
	request:=func(_ context.Context,_ string,endpoint string,_ map[string]string,body io.Reader)(*netResp,error){
		calls++;raw,_:=io.ReadAll(body);q,_:=url.ParseQuery(string(raw))
		if strings.HasSuffix(endpoint,"/threads_publish"){if q.Get("creation_id")!="123"{t.Fatal(q)};return &netResp{Status:200,Data:jx.M{"id":"456"}},nil}
		p:=jx.Unmarshal([]byte(q.Get("poll_attachment")))
		if q.Get("media_type")!="TEXT"||q.Get("text")!="Question?"||jx.Str(p,"option_a")!="A"||jx.Str(p,"option_b")!="B"{t.Fatal(q)}
		return &netResp{Status:200,Data:jx.M{"id":"123"}},nil
	}
	p:=NetPost{Poll:jx.M{"question":"Question?","options":[]string{"A","B"}}}
	sent,err:=threadsPublishWith(context.Background(),&Channel{Ref:"42",Secrets:map[string]string{"access_token":"token"}},p,request,nil,func(context.Context)error{return nil})
	if err!=nil||sent==nil||calls!=2||sent.MessageIDs[0]!="456"{t.Fatal(sent,err,calls)}
}
