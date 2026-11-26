package posta

import (
	"context"
	"encoding/json"
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

var okVideoIDPattern=regexp.MustCompile("^[1-9][0-9]{0,24}$")

func okUploadVideo(ctx context.Context,gid string,f Prepared,call okMethodFunc,request networkRequestFunc)(string,error){
	info,err:=os.Stat(f.Path)
	if err!=nil||!info.Mode().IsRegular()||info.Size()<16*1024||info.Size()>200_000_000{return "",Bad("Одноклассники: видео должно быть от 16 КБ до 200 МБ.")}
	r,err:=call(ctx,"video.getUploadUrl",map[string]string{"gid":gid,"file_name":networkUploadFilename(f.Path),"file_size":strconv.FormatInt(info.Size(),10),"attachment_type":"MOVIE","post_form":"true"})
	d,err:=networkExpect(r,err,[]int{200},"Одноклассники: адрес загрузки видео")
	if err!=nil{return "",networkUploadError(err,"Одноклассники не выдали адрес загрузки видео")}
	if jx.Int(d,"error_code")!=0{return "",fmt.Errorf("Одноклассники отклонили загрузку видео (код %d, проверьте VIDEO_CONTENT и VALUABLE_ACCESS): %w",jx.Int(d,"error_code"),ErrNetworkRejected)}
	var receipt struct{VideoID json.RawMessage `json:"video_id"`}
	if json.Unmarshal(r.Raw,&receipt)!=nil{return "",Bad("Одноклассники не вернули ID видео.")}
	id:=strings.TrimSpace(string(receipt.VideoID))
	var quoted string
	if json.Unmarshal(receipt.VideoID,&quoted)==nil{id=quoted}
	raw:=jx.Str(d,"upload_url")
	if !okVideoIDPattern.MatchString(id)||!okPhotoUploadURL(raw){return "",Bad("Одноклассники вернули некорректные данные загрузки видео.")}
	body,ct,cleanup,err:=networkMultipartFile(nil,nil,map[string]string{"data":f.Path})
	if err!=nil{return "",err};defer cleanup()
	uploaded,err:=request(ctx,http.MethodPost,raw,map[string]string{"Content-Type":ct},body)
	if err!=nil||uploaded==nil||uploaded.Status!=200{return "",Bad("Одноклассники не подтвердили загрузку видео; пост ещё не отправлен.")}
	return id,nil
}

func okPublishWith(ctx context.Context,c *Channel,p NetPost,call okMethodFunc,request networkRequestFunc)(*NetSent,error){
	gid:=jx.Str(c.Meta,"gid");if gid==""{gid=c.Ref}
	if !okGroupIDPattern.MatchString(gid)||gid!=c.Ref{return nil,Bad("Одноклассники: не определена группа.")}
	if len(p.Media)>10||runeLen(p.Text)>4000{return nil,Bad("Одноклассники: превышен лимит текста или вложений.")}
	var media []jx.M
	if p.Poll!=nil{
		poll,err:=NetworkPollCheck("ok",p.Poll);if err!=nil{return nil,err}
		p.Text,err=PollContentText("ok",p.Text,len(p.Media),poll);if err!=nil{return nil,err}
		if jx.Bool(poll,"with_content")&&strings.TrimSpace(p.Text)!=""{media=append(media,jx.M{"type":"text","text":p.Text})}
		answers:=[]jx.M{}
		for _,answer:=range jx.Strs(poll,"options"){answers=append(answers,jx.M{"text":answer})}
		flags:=[]string{}
		if !jx.Bool(poll,"multiple"){flags=append(flags,"SingleChoice")}
		if !jx.Bool(poll,"public"){flags=append(flags,"AnonymousVoting")}
		options:=strings.Join(flags,",")
		media=append(media,jx.M{"type":"poll","question":jx.Str(poll,"question"),"answers":answers,"options":options})
	}else if strings.TrimSpace(p.Text)!=""{media=append(media,jx.M{"type":"text","text":p.Text})}
	var videoIDs []string
	for _,f:=range p.Media{
		var id,kind string
		var err error
		switch f.Kind{
		case "photo":kind="photo";id,err=okUploadPhoto(ctx,gid,f,call,request)
		case "video":kind="movie";id,err=okUploadVideo(ctx,gid,f,call,request);if err==nil{videoIDs=append(videoIDs,id)}
		default:return nil,Bad("Одноклассники: неподдерживаемое вложение.")
		}
		if err!=nil{return nil,err}
		item:=jx.M{"id":id}
		if len(media)>0&&jx.Str(media[len(media)-1],"type")==kind{
			last:=media[len(media)-1]
			last["list"]=append(last["list"].([]jx.M),item)
		}else{media=append(media,jx.M{"type":kind,"list":[]jx.M{item}})}
	}
	if len(media)==0{return nil,Bad("Одноклассники: пустой пост.")}
	// video.update accepts the upload agreement and may make the video
	// visible. After that boundary any failure needs reconciliation.
	for _,id:=range videoIDs{
		r,err:=call(ctx,"video.update",map[string]string{"vid":id})
		if err!=nil||r==nil||r.Status!=200||jx.Int(r.Data,"error_code")!=0||jx.Str(r.Data,"error_msg")!=""{return nil,uncertainPublish("Одноклассники: завершение загрузки видео не подтверждено; проверьте видео группы перед повтором.")}
		raw:=strings.TrimSpace(string(r.Raw))
		if raw!=""&&raw!="null"&&raw!="{}"{return nil,uncertainPublish("Одноклассники: неожиданный ответ завершения загрузки видео; проверьте видео группы.")}
	}
	att:=string(jx.Marshal(jx.M{"media":media}))
	r,err:=call(ctx,"mediatopic.post",map[string]string{"type":"GROUP_THEME","gid":gid,"attachment":att})
	if err!=nil{
		if len(videoIDs)>0{return nil,uncertainPublish("Одноклассники: видео уже могло стать доступным, но тема не подтверждена; проверьте группу перед повтором.")}
		return nil,err
	}
	id,err:=okPublishedID(r)
	if err!=nil{
		if len(videoIDs)>0{return nil,uncertainPublish("Одноклассники: видео уже могло стать доступным, но тема не подтверждена; проверьте группу перед повтором.")}
		return nil,err
	}
	return &NetSent{URL:"https://ok.ru/group/"+gid+"/topic/"+id,MessageIDs:[]string{id}},nil
}
