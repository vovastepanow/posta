package posta

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"posta/internal/jx"
)

type okMethodFunc func(context.Context,string,map[string]string)(*netResp,error)
var okGroupIDPattern=regexp.MustCompile("^\\d{1,20}$")

func okPhotoUploadURL(raw string)bool{
	u,err:=url.Parse(raw)
	if err!=nil||u.Scheme!="https"||u.User!=nil||u.Fragment!=""||(u.Port()!=""&&u.Port()!="443"){return false}
	h:=strings.ToLower(u.Hostname())
	for _,domain:=range []string{"ok.ru","okcdn.ru","mycdn.me","odnoklassniki.ru"}{
		if h==domain||strings.HasSuffix(h,"."+domain){return true}
	}
	return false
}

func okUploadPhoto(ctx context.Context,gid string,f Prepared,call okMethodFunc,request networkRequestFunc)(string,error){
	if f.Kind!="photo"{return "",Bad("Одноклассники: требуется фотография.")}
	info,err:=os.Stat(f.Path);if err!=nil||!info.Mode().IsRegular()||info.Size()<=0{return "",Bad("Файл фотографии не найден.")}
	r,err:=call(ctx,"photosV2.getUploadUrl",map[string]string{"gid":gid,"count":"1","sizes":strconv.FormatInt(info.Size(),10)})
	d,err:=networkExpect(r,err,[]int{200},"Одноклассники: адрес загрузки фото")
	if err!=nil{return "",networkUploadError(err,"Одноклассники не выдали адрес загрузки")}
	if jx.Int(d,"error_code")!=0{return "",fmt.Errorf("Одноклассники не разрешили загрузку фото (код %d, проверьте PHOTO_CONTENT): %w",jx.Int(d,"error_code"),ErrNetworkRejected)}
	ids:=jx.Strs(d,"photo_ids")
	raw:=jx.Str(d,"upload_url")
	if len(ids)!=1||ids[0]==""||!okPhotoUploadURL(raw){return "",Bad("Одноклассники вернули некорректные данные загрузки.")}
	body,ct,cleanup,err:=networkMultipartFile(nil,nil,map[string]string{"pic1":f.Path})
	if err!=nil{return "",err};defer cleanup()
	uploaded,err:=request(ctx,http.MethodPost,raw,map[string]string{"Content-Type":ct},body)
	if err!=nil||uploaded==nil||uploaded.Status!=200{return "",Bad("Одноклассники не приняли фото; пост ещё не отправлен.")}
	token:=jx.Str(jx.Map(jx.Map(uploaded.Data,"photos"),ids[0]),"token")
	if token==""{return "",Bad("Одноклассники не подтвердили загрузку фотографии.")}
	return token,nil
}

func okPublishWith(ctx context.Context,c *Channel,p NetPost,call okMethodFunc,request networkRequestFunc)(*NetSent,error){
	gid:=jx.Str(c.Meta,"gid");if gid==""{gid=c.Ref}
	if !okGroupIDPattern.MatchString(gid)||gid!=c.Ref{return nil,Bad("Одноклассники: не определена группа.")}
	if len(p.Media)>10||runeLen(p.Text)>4000{return nil,Bad("Одноклассники: превышен лимит текста или вложений.")}
	var media []jx.M
	if strings.TrimSpace(p.Text)!=""{media=append(media,jx.M{"type":"text","text":p.Text})}
	var photos []jx.M
	for _,f:=range p.Media{
		token,err:=okUploadPhoto(ctx,gid,f,call,request)
		if err!=nil{return nil,err}
		photos=append(photos,jx.M{"id":token})
	}
	if len(photos)>0{media=append(media,jx.M{"type":"photo","list":photos})}
	if len(media)==0{return nil,Bad("Одноклассники: пустой пост.")}
	att:=string(jx.Marshal(jx.M{"media":media}))
	r,err:=call(ctx,"mediatopic.post",map[string]string{"type":"GROUP_THEME","gid":gid,"attachment":att})
	if err!=nil{return nil,err}
	id,err:=okPublishedID(r)
	if err!=nil{return nil,err}
	return &NetSent{URL:"https://ok.ru/group/"+gid+"/topic/"+id,MessageIDs:[]string{id}},nil
}
