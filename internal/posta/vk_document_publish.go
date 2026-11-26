package posta

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"posta/internal/jx"
)

type vkDocumentAuthError struct{error}
func(e *vkDocumentAuthError)Unwrap()error{return e.error}

func vkDocumentAttachmentWith(ctx context.Context,group string,f Prepared,token string,call vkMethodFunc,request networkRequestFunc)(string,error){
	info,err:=os.Stat(f.Path)
	if err!=nil||!info.Mode().IsRegular()||info.Size()<=0||info.Size()>50_000_000{return "",Bad("ВКонтакте: документ отсутствует или превышает 50 МБ.")}
	mime:=sniffMime(f.Path)
	if f.Kind=="animation" {
		if mime!="image/gif"{return "",Bad("ВКонтакте: требуется GIF.")}
	}else if f.Kind!="document"||publishDocumentExtension(filepath.Base(f.Path),mime)==""{return "",Bad("ВКонтакте: неподдерживаемый документ.")}
	res,err:=call(ctx,"docs.getWallUploadServer",url.Values{"group_id":{group}},token)
	if err!=nil{return "",err}
	endpoint:=jx.Str(jx.ToMap(res),"upload_url")
	if !vkUploadURL(endpoint){return "",Bad("ВКонтакте: непроверенный адрес загрузки документа.")}
	body,ct,cleanup,err:=networkMultipartFile(nil,nil,map[string]string{"file":f.Path})
	if err!=nil{return "",err};defer cleanup()
	uploaded,err:=request(ctx,http.MethodPost,endpoint,map[string]string{"Content-Type":ct},body)
	if err!=nil||uploaded==nil||uploaded.Status!=200{return "",networkUploadError(err,"ВКонтакте: загрузка документа не подтверждена")}
	file:=jx.Str(uploaded.Data,"file")
	if file==""{return "",Bad("ВКонтакте не вернул токен загруженного документа.")}
	title:=strings.TrimSpace(f.Name);if title==""{title=filepath.Base(f.Path)}
	res,err=call(ctx,"docs.save",url.Values{"file":{file},"title":{trimRunes(title,128)}},token)
	if err!=nil{return "",uncertainPublish("ВКонтакте: сохранение документа не подтверждено. Проверьте документы сообщества перед повтором.")}
	m:=jx.ToMap(res);doc:=jx.Map(m,"doc")
	id,owner:=jx.Int(doc,"id"),jx.Int(doc,"owner_id")
	if jx.Str(m,"type")!="doc"||id<=0||strconv.FormatInt(owner,10)!="-"+group{return "",uncertainPublish("ВКонтакте не подтвердил документ выбранного сообщества.")}
	attachment:=fmt.Sprintf("doc%d_%d",owner,id)
	if key:=jx.Str(doc,"access_key");key!=""{
		if strings.ContainsAny(key,", \r\n"){return "",uncertainPublish("ВКонтакте вернул некорректный ключ документа.")}
		attachment+="_"+key
	}
	return attachment,nil
}
