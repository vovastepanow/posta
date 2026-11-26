package posta

import (
 "context"
 "io"
 "net/url"
 "os"
 "path/filepath"
 "testing"
 "posta/internal/jx"
)

func TestVKUploadURL(t *testing.T){
 for _,u:=range []string{"https://pu.vk.com/upload","https://sun1.userapi.com/upload"}{if !vkUploadURL(u){t.Fatalf("rejected %s",u)}}
 for _,u:=range []string{"http://vk.com/upload","https://vk.com.evil.test/upload","https://vk.com@evil.test/","https://127.0.0.1/","https://vk.com:444/"}{if vkUploadURL(u){t.Fatalf("accepted %s",u)}}
}
func TestVKPhotoAttachmentRejectsWrongOwnerAndIncompleteUpload(t *testing.T){
 path:=filepath.Join(t.TempDir(),"image.jpg");if err:=os.WriteFile(path,[]byte{255,216,255,224},0600);err!=nil{t.Fatal(err)}
 for _,tc:=range []struct{status int;owner int;ok bool}{{500,-12,false},{200,-13,false},{200,-12,true}}{
  calls:=0
  call:=func(_ context.Context,method string,_ url.Values,_ string)(any,error){
   calls++
   if method=="photos.getWallUploadServer"{return jx.M{"upload_url":"https://pu.vk.com/upload"},nil}
   return []any{jx.M{"id":123,"owner_id":tc.owner}},nil
  }
  request:=func(_ context.Context,_,_ string,_ map[string]string,_ io.Reader)(*netResp,error){return &netResp{Status:tc.status,Data:jx.M{"photo":"[1]","hash":"abc","server":42}},nil}
  id,err:=vkPhotoAttachmentWith(context.Background(),"12",path,"token",call,request)
  if (err==nil)!=tc.ok{t.Fatalf("%+v: %s %v",tc,id,err)}
  if tc.status!=200&&calls!=1{t.Fatalf("save after failed upload")}
  if tc.ok&&id!="photo-12_123"{t.Fatal(id)}
 }
}
