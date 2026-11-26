package posta

import (
 "bytes"
 "image"
 "image/jpeg"
 "net/http"
 "net/http/httptest"
 "net/url"
 "os"
 "path/filepath"
 "testing"
 "posta/internal/config"
)

func TestDeliveryMediaLease(t *testing.T){
 a:=&App{Cfg:config.Server{PublicURL:"https://example.org"}}
 var buf bytes.Buffer
 if err:=jpeg.Encode(&buf,image.NewRGBA(image.Rect(0,0,2,2)),nil);err!=nil{t.Fatal(err)}
 file:=filepath.Join(t.TempDir(),"original")
 if err:=os.WriteFile(file,buf.Bytes(),0600);err!=nil{t.Fatal(err)}
 raw,revoke,err:=a.leaseDeliveryMedia(Prepared{Kind:"photo",Path:file})
 if err!=nil{t.Fatal(err)}
 defer revoke()
 u,_:=url.Parse(raw)
 request:=func(app *App, method,path,rng string)*httptest.ResponseRecorder{
  r:=httptest.NewRequest(method,path,nil)
  if rng!=""{r.Header.Set("Range",rng)}
  w:=httptest.NewRecorder();app.ServeDeliveryMedia(w,r);return w
 }
 if w:=request(a,"GET",u.Path,"");w.Code!=200||!bytes.Equal(w.Body.Bytes(),buf.Bytes()){t.Fatalf("GET %d",w.Code)}
 if w:=request(a,"GET",u.Path,"bytes=0-9");w.Code!=206||w.Body.Len()!=10{t.Fatalf("range %d",w.Code)}
 if w:=request(a,"HEAD",u.Path,"");w.Code!=200||w.Body.Len()!=0{t.Fatalf("HEAD %d",w.Code)}
 if w:=request(&App{},"GET",u.Path,"");w.Code!=404{t.Fatal("wrong app")}
 if w:=request(a,"POST",u.Path,"");w.Code!=405{t.Fatal("POST allowed")}
 revoke()
 if w:=request(a,"GET",u.Path,"");w.Code!=404{t.Fatal("revoked token still usable")}
}
