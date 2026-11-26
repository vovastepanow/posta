package posta

import (
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/textproto"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Spool multipart bodies to a private temporary file rather than keeping
// hundreds of megabytes in RAM. Close and remove it on every return path.
func networkMultipartFile(fields map[string]string, jsonFields map[string][]byte, files map[string]string) (*os.File,string,func(),error) {
	f,err:=os.CreateTemp("", "posta-api-upload-*")
	if err!=nil{return nil,"",func(){},Bad("Не удалось подготовить загрузку.")}
	cleanup:=func(){f.Close();os.Remove(f.Name())}
	mw:=multipart.NewWriter(f)
	fail:=func()(*os.File,string,func(),error){cleanup();return nil,"",func(){},Bad("Не удалось подготовить файлы для API.")}
	for k,v:=range fields {if err:=mw.WriteField(k,v);err!=nil{return fail()}}
	for k,v:=range jsonFields{
		h:=make(textproto.MIMEHeader)
		h.Set("Content-Disposition",mime.FormatMediaType("form-data",map[string]string{"name":k}))
		h.Set("Content-Type","application/json")
		part,err:=mw.CreatePart(h);if err!=nil{return fail()}
		if _,err=part.Write(v);err!=nil{return fail()}
	}
	keys:=make([]string,0,len(files))
	for k:=range files{keys=append(keys,k)}
	sort.Strings(keys)
	for _,k:=range keys{
		path:=files[k]
		src,err:=os.Open(path);if err!=nil{return fail()}
		h:=make(textproto.MIMEHeader)
		h.Set("Content-Disposition",mime.FormatMediaType("form-data",map[string]string{"name":k,"filename":networkUploadFilename(path)}))
		h.Set("Content-Type",sniffMime(path))
		part,err:=mw.CreatePart(h)
		if err==nil{_,err=io.Copy(part,src)}
		closeErr:=src.Close()
		if err!=nil||closeErr!=nil{return fail()}
	}
	if err=mw.Close();err!=nil{return fail()}
	if _,err=f.Seek(0,io.SeekStart);err!=nil{return fail()}
	return f,mw.FormDataContentType(),cleanup,nil
}

func waitNetworkContext(ctx context.Context, d time.Duration) error {
	t:=time.NewTimer(d);defer t.Stop()
	select {case <-ctx.Done():return ctx.Err();case <-t.C:return nil}
}

// An upload is not a visible post. Do not tell the user to reconcile a
// publication if only an upload failed; retrying the upload may create an
// unused media object, but cannot duplicate a feed post.
func networkUploadError(err error, what string) error {
	if err==nil{return nil}
	return fmt.Errorf("%s (пост ещё не отправлен).",what)
}


// Prepared source and browser uploads may have no extension. Some remote
// upload endpoints validate the filename as well as the MIME header.
func networkUploadFilename(path string) string {
	name:=filepath.Base(path)
	ext:=map[string]string{"image/jpeg":".jpg","image/png":".png","image/gif":".gif","image/webp":".webp","video/mp4":".mp4","video/webm":".webm","image/bmp":".bmp","video/quicktime":".mov"}[sniffMime(path)]
	if ext!="" && !strings.EqualFold(filepath.Ext(name),ext){name+=ext}
	return name
}
