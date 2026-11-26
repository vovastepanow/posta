package posta

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
	"posta/internal/jx"
)

func TestMAXExtraUploads(t *testing.T) {
	for _,tc:=range []struct{kind,api,host string}{{"audio","audio","omu.okcdn.ru"},{"document","file","fu.oneme.ru"}} {
		path:=filepath.Join(t.TempDir(),"file");if err:=os.WriteFile(path,[]byte("test"),0600);err!=nil{t.Fatal(err)}
		n:=0
		a,err:=maxUploadAttachment(context.Background(),"secret",Prepared{Kind:tc.kind,Path:path},func(_ context.Context,_ string,endpoint string,h map[string]string,_ io.Reader)(*netResp,error){
			n++
			if n==1 {
				if endpoint!=maxAPI+"/uploads?type="+tc.api||h["Authorization"]!="secret"{t.Fatal(endpoint)}
				return &netResp{Status:200,Data:jx.M{"url":"https://"+tc.host+"/upload","token":"initial"}},nil
			}
			if endpoint!="https://"+tc.host+"/upload"||h["Authorization"]!=""{t.Fatal(endpoint,h)}
			if tc.api=="file"{return &netResp{Status:200,Data:jx.M{"fileId":42,"token":"uploaded"}},nil}
			return &netResp{Status:200,Raw:[]byte("<retval>1</retval>")},nil
		})
		want:="initial";if tc.api=="file"{want="uploaded"}
		if err!=nil||jx.Str(a,"type")!=tc.api||jx.Str(jx.Map(a,"payload"),"token")!=want||n!=2{t.Fatal(a,err,n)}
	}
	if maxAttachmentKindsCheck([]string{"audio","photo"})==nil{t.Fatal("mixed audio accepted")}
	if maxUploadURL("https://omu.okcdn.ru.evil.example/upload","audio"){t.Fatal("untrusted host accepted")}
}
