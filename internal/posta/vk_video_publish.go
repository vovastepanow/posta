package posta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"posta/internal/jx"
)

type vkMethodFunc func(context.Context,string,url.Values,string)(any,error)
type vkVideoAuthError struct{error}
func(e *vkVideoAuthError)Unwrap()error{return e.error}

func vkVideoAttachmentWith(ctx context.Context,group,path,title,token string,call vkMethodFunc,request networkRequestFunc)(string,error){
	info,err:=os.Stat(path)
	if err!=nil||!info.Mode().IsRegular()||info.Size()<=0||info.Size()>200_000_000||sniffMime(path)!="video/mp4"{return "",Bad("ВКонтакте: требуется подготовленный MP4 до 200 МБ.")}
	if title==""{title="Видео"}
	res,err:=call(ctx,"video.save",url.Values{"group_id":{group},"name":{trimRunes(title,128)},"wallpost":{"0"}},token)
	if err!=nil{return "",err}
	m:=jx.ToMap(res)
	id,owner:=jx.Int(m,"video_id"),jx.Int(m,"owner_id")
	raw:=jx.Str(m,"upload_url")
	if id<=0||strconv.FormatInt(owner,10)!="-"+group||!vkUploadURL(raw){
		return "",Bad("ВКонтакте не подтвердил адрес загрузки видео в выбранное сообщество.")
	}
	body,ct,cleanup,err:=networkMultipartFile(nil,nil,map[string]string{"video_file":path})
	if err!=nil{return "",err};defer cleanup()
	r,err:=request(ctx,http.MethodPost,raw,map[string]string{"Content-Type":ct},body)
	// Uploaded video may become visible in the group's video list, even
	// though wall.post has not been invoked. Do not blindly replay.
	if err!=nil||r==nil||r.Status!=200||jx.Int(r.Data,"video_id")!=id||jx.Int(r.Data,"size")<=0||jx.Has(r.Data,"error"){
		return "",uncertainPublish("ВКонтакте: загрузка видео не подтверждена; проверьте список видео сообщества перед повтором.")
	}
	if got:=jx.Int(r.Data,"owner_id");got!=0&&got!=owner{return "",uncertainPublish("ВКонтакте: ответ загрузки другого владельца видео.")}
	return fmt.Sprintf("video%d_%d",owner,id),nil
}

func vkPublishWith(ctx context.Context,c *Channel,p NetPost,call vkMethodFunc,request networkRequestFunc,userToken func()string)(*NetSent,error){
	if len(p.Media)>10||runeLen(p.Text)>16384{return nil,Bad("ВКонтакте: превышен лимит текста или вложений.")}
	if len(p.Media)==0&&strings.TrimSpace(p.Text)==""&&p.Poll==nil{return nil,Bad("ВКонтакте: пустой пост.")}
	for _,f:=range p.Media{if f.Kind!="photo"&&f.Kind!="video"&&f.Kind!="document"&&f.Kind!="animation"{return nil,Bad("ВКонтакте: неподдерживаемое вложение.")}}
	group,token:=c.Ref,c.Secrets["token"]
	if id,err:=strconv.ParseInt(group,10,64);err!=nil||id<=0||token==""{return nil,Bad("ВКонтакте: сообщество не подключено.")}
	extra,err:=PublishExtrasCheck("vk",p.Text,PreparedKinds(p.Media),p.Poll,p.Extras);if err!=nil{return nil,err}
	mediaToken:=token
	var poll jx.M
	pt:=""
	if p.Poll!=nil {
		poll,err=NetworkPollCheck("vk",p.Poll);if err!=nil{return nil,err}
		p.Text,err=PollContentText("vk",p.Text,len(p.Media),poll);if err!=nil{return nil,err}
		if jx.Bool(c.Meta,"poll_user_token"){pt=token}else if userToken!=nil{pt=userToken()}
		if pt==""{return nil,Bad("ВКонтакте: для опроса нужен пользовательский токен с доступом к polls.create.")}
		if !jx.Bool(poll,"with_content"){p.Text=""}
	}
	var att []string
	videoUploaded:=false
	title,_:=titleAndBody(p.Text)
	// Once a video is uploaded, a subsequent failure may leave a visible
	// media object. Stop automatic replay, even if the wall was not posted.
	fail:=func(err error)(*NetSent,error){
		if videoUploaded{return nil,uncertainPublish("ВКонтакте: вложение уже загружено, публикация на стене не подтверждена; проверьте сообщество перед повтором.")}
		return nil,err
	}
	for _,f:=range p.Media{
		upload:=func(t string)(string,error){
			if f.Kind=="video"{return vkVideoAttachmentWith(ctx,group,f.Path,title,t,call,request)}
			if f.Kind=="document"||f.Kind=="animation"{return vkDocumentAttachmentWith(ctx,group,f,t,call,request)}
			return vkPhotoAttachmentWith(ctx,group,f.Path,t,call,request)
		}
		id,err:=upload(mediaToken)
		var photoErr *vkPhotoAuthError
		var videoErr *vkVideoAuthError
		var docErr *vkDocumentAuthError
		if err!=nil&&mediaToken==token&&(errors.As(err,&photoErr)||errors.As(err,&videoErr)||errors.As(err,&docErr)){
			// Explicit method authorization refusal before a video upload.
			// A profile-specific user token is never shared with other users.
			if userToken!=nil{if t:=userToken();t!=""&&t!=token{mediaToken=t;id,err=upload(t)}}
		}
		if err!=nil{return fail(err)}
		if f.Kind=="video"||f.Kind=="document"||f.Kind=="animation"{videoUploaded=true}
		att=append(att,id)
	}
	if poll!=nil{
		multiple:="0";if jx.Bool(poll,"multiple"){multiple="1"}
		params:=url.Values{"owner_id":{"-"+group},"question":{jx.Str(poll,"question")},"add_answers":{string(jx.Marshal(jx.Strs(poll,"options")))},"is_multiple":{multiple},"is_anonymous":{"1"}}
		if jx.Bool(poll,"public"){params.Set("is_anonymous","0")}
		if close:=jx.Int(poll,"close_minutes");close>0{params.Set("end_date",strconv.FormatInt(now()+60*close,10))}
		res,err:=call(ctx,"polls.create",params,pt)
		if err!=nil{return fail(err)}
		m:=jx.ToMap(res);id,owner:=jx.Int(m,"id"),jx.Int(m,"owner_id")
		if id<=0||strconv.FormatInt(owner,10)!="-"+group{return nil,uncertainPublish("ВКонтакте не подтвердил опрос выбранного сообщества.")}
		att=append(att,fmt.Sprintf("poll%d_%d",owner,id));videoUploaded=true
	}
	params:=url.Values{"owner_id":{"-"+group},"from_group":{"1"},"message":{p.Text}}
	if link:=jx.Str(extra,"link_attachment");link!=""{att=append(att,link)}
	if len(att)>0{params.Set("attachments",strings.Join(att,","))}
	for _,key:=range []string{"signed","close_comments","mark_as_ads"}{if jx.Bool(extra,key){params.Set(key,"1")}}
	if source:=jx.Str(extra,"copyright");source!=""{params.Set("copyright",source)}
	if geo:=jx.Str(extra,"vk_geo");geo!=""{parts:=strings.Split(geo,",");params.Set("lat",parts[0]);params.Set("long",parts[1])}
	res,err:=call(ctx,"wall.post",params,token)
	if err!=nil{return fail(err)}
	id:=jx.Int(jx.ToMap(res),"post_id")
	if id<=0{return nil,uncertainPublish("ВКонтакте не вернул ID публикации.")}
	sent:=&NetSent{URL:fmt.Sprintf("https://vk.com/wall-%s_%d",group,id),MessageIDs:[]string{strconv.FormatInt(id,10)}}
	if comment:=jx.Str(extra,"first_comment");comment!=""{
		res,err:=call(ctx,"wall.createComment",url.Values{"owner_id":{"-"+group},"post_id":{strconv.FormatInt(id,10)},"from_group":{group},"message":{comment},"guid":{randHex(16)}},token)
		if cid:=jx.Int(jx.ToMap(res),"comment_id");err==nil&&cid>0{sent.CommentID=strconv.FormatInt(cid,10)}else{sent.Warning=firstCommentWarning("ВКонтакте")}
	}
	return sent,nil
}
