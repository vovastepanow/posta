package posta

import("context";"io";"net/url";"os";"path/filepath";"testing";"posta/internal/jx")

func TestVKDocumentPublication(t *testing.T){
	path:=filepath.Join(t.TempDir(),"document.pdf")
	if err:=os.WriteFile(path,[]byte("%PDF-1.4\nfixture"),0600);err!=nil{t.Fatal(err)}
	calls:=0
	call:=func(_ context.Context,method string,p url.Values,token string)(any,error){
		calls++
		switch method{
		case "docs.getWallUploadServer":if p.Get("group_id")!="42"{t.Fatal(p)};return jx.M{"upload_url":"https://pu.vk.com/upload"},nil
		case "docs.save":if p.Get("file")!="ticket"{t.Fatal(p)};return jx.M{"type":"doc","doc":jx.M{"id":123,"owner_id":-42}},nil
		case "wall.post":if p.Get("attachments")!="doc-42_123"{t.Fatal(p)};return jx.M{"post_id":456},nil
		default:t.Fatal(method);return nil,nil
		}
	}
	request:=func(_ context.Context,_ string,endpoint string,h map[string]string,_ io.Reader)(*netResp,error){
		if endpoint!="https://pu.vk.com/upload"||h["Authorization"]!=""{t.Fatal(endpoint,h)}
		return &netResp{Status:200,Data:jx.M{"file":"ticket"}},nil
	}
	sent,err:=vkPublishWith(context.Background(),&Channel{Ref:"42",Secrets:map[string]string{"token":"community"}},NetPost{Media:[]Prepared{{Kind:"document",Path:path}}},call,request,nil)
	if err!=nil||sent==nil||calls!=3||sent.MessageIDs[0]!="456"{t.Fatal(sent,err,calls)}
}
