package posta

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"posta/internal/jx"
)

func maxUploadKind(kind string) string {
	switch kind {
	case "photo","animation":return "image"
	case "video":return "video"
	case "audio":return "audio"
	case "document":return "file"
	default:return ""
	}
}

func maxUploadURL(raw, kind string) bool {
	u,err:=url.Parse(raw)
	if err!=nil||u.Scheme!="https"||u.User!=nil||u.Fragment!=""||(u.Port()!=""&&u.Port()!="443"){return false}
	switch kind {
	case "image":return strings.EqualFold(u.Hostname(),"iu.oneme.ru")
	case "video":return strings.EqualFold(u.Hostname(),"omub.okcdn.ru")
	case "audio":return strings.EqualFold(u.Hostname(),"omu.okcdn.ru")
	case "file":return strings.EqualFold(u.Hostname(),"fu.oneme.ru")
	}
	return false
}

func maxImageToken(data jx.M) string {
	photos:=jx.Map(data,"photos")
	if len(photos)!=1{return ""}
	for _,v:=range photos{return jx.Str(jx.ToMap(v),"token")}
	return ""
}

func maxUploadAttachment(ctx context.Context, token string, f Prepared, request networkRequestFunc)(jx.M,error){
	kind:=maxUploadKind(f.Kind)
	if kind==""{return nil,Bad("MAX: неподдерживаемый формат вложения.")}
	r,err:=request(ctx,http.MethodPost,maxAPI+"/uploads?type="+kind,map[string]string{"Authorization":token},nil)
	start,err:=networkExpect(r,err,[]int{200},"MAX не выдал адрес загрузки")
	if err!=nil{return nil,networkUploadError(err,"MAX не выдал адрес загрузки")}
	uploadURL:=jx.Str(start,"url")
	if !maxUploadURL(uploadURL,kind){return nil,Bad("MAX вернул неизвестный адрес загрузки.")}
	body,ct,cleanup,err:=networkMultipartFile(nil,nil,map[string]string{"data":f.Path})
	if err!=nil{return nil,err};defer cleanup()
	headers:=map[string]string{"Content-Type":ct}
	if kind=="image"{headers["Authorization"]=token}
	uploaded,err:=request(ctx,http.MethodPost,uploadURL,headers,body)
	if err!=nil||uploaded==nil||uploaded.Status!=200{return nil,Bad("MAX не принял файл; пост ещё не отправлен.")}
	mediaToken:=jx.Str(start,"token")
	if kind=="image"{mediaToken=maxImageToken(uploaded.Data)}else if kind=="file"{mediaToken=jx.Str(uploaded.Data,"token");if jx.Str(uploaded.Data,"fileId")==""&&jx.Int(uploaded.Data,"fileId")<=0{return nil,Bad("MAX не подтвердил ID файла.")}}else if strings.TrimSpace(string(uploaded.Raw))!="<retval>1</retval>" && jx.Int(uploaded.Data,"retval")!=1 {
		return nil,Bad("MAX не подтвердил загрузку медиа.")
	}
	if mediaToken==""{return nil,Bad("MAX не выдал токен вложения.")}
	return jx.M{"type":kind,"payload":jx.M{"token":mediaToken}},nil
}

// File and audio are conservatively published alone; a file may also have a keyboard.
func maxAttachmentKindsCheck(kinds []string) error {
	for _,kind:=range kinds {
		if (kind=="audio"||kind=="document")&&len(kinds)>1{return Bad("MAX: аудио или документ отправляется отдельным вложением.")}
	}
	return nil
}

func maxPublishWith(ctx context.Context,c *Channel,p NetPost,request networkRequestFunc,wait func(context.Context,time.Duration)error)(*NetSent,error){
	if !chatIDRe.MatchString(c.Ref)||c.Secrets["token"]==""{return nil,Bad("MAX: подключите чат и бота.")}

	if runeLen(p.Text)>4000||len(p.Media)>12{return nil,Bad("MAX: превышен лимит текста или вложений.")}
	kinds:=PreparedKinds(p.Media)
	if err:=maxAttachmentKindsCheck(kinds);err!=nil{return nil,err}
	extras,err:=PublishExtrasCheck("max",p.Text,kinds,p.Poll,p.Extras);if err!=nil{return nil,err}
	if jx.Str(extras,"first_comment")!=""&&!contains(ChannelExtraKinds(c),"first_comment"){return nil,Bad("Первый комментарий MAX доступен только для канала.")}
	if strings.TrimSpace(p.Text)==""&&len(p.Media)==0&&!PostExtrasStandalone(extras){return nil,Bad("MAX: нет текста или вложений.")}
	var attachments []jx.M
	if card:=maxStandaloneAttachment(extras);card!=nil{attachments=append(attachments,card)}
	for _,f:=range p.Media{
		attachment,err:=maxUploadAttachment(ctx,c.Secrets["token"],f,request)
		if err!=nil{return nil,err}
		attachments=append(attachments,attachment)
	}
	if buttons:=jx.Maps(extras,"buttons");len(buttons)>0{
		rows:=[][]jx.M{};for _,b:=range buttons{rows=append(rows,[]jx.M{{"type":"link","text":jx.Str(b,"text"),"url":jx.Str(b,"url")}})}
		attachments=append(attachments,jx.M{"type":"inline_keyboard","payload":jx.M{"buttons":rows}})
	}
	payload:=jx.M{}
	if strings.TrimSpace(p.Text)!=""{payload["text"]=p.Text;if jx.Bool(extras,"styled_text"){payload["text"]=publishStyledHTML(p.Text);payload["format"]="html"}}
	if len(attachments)>0{payload["attachments"]=attachments}
	headers:=map[string]string{"Authorization":c.Secrets["token"],"Content-Type":ctJSON}
	for attempt:=0;;attempt++{
		endpoint:=maxAPI+"/messages?chat_id="+url.QueryEscape(c.Ref)
		if preview:=jx.Str(extras,"preview");preview!=""{endpoint+="&disable_link_preview="+map[bool]string{true:"true",false:"false"}[preview=="off"]}
		r,err:=request(ctx,http.MethodPost,endpoint,headers,jsonBody(payload))
		// Only the explicit provider rejection is safe to retry.
		if err==nil&&r!=nil&&r.Status>=400&&r.Status<500&&jx.Str(r.Data,"code")=="attachment.not.ready"{
			if attempt>=3{return nil,Bad("MAX ещё обрабатывает вложения; пост не отправлен.")}
			if err:=wait(ctx,time.Duration(1<<attempt)*time.Second);err!=nil{return nil,err}
			continue
		}
		d,err:=networkExpect(r,err,[]int{200},"MAX не отправил сообщение")
		if err!=nil{return nil,err}
		m:=jx.Map(d,"message");if m==nil{m=d}
		id:=jx.Str(jx.Map(m,"body"),"mid")
		if id==""{id=jx.Str(m,"id")}
		sent:=&NetSent{URL:jx.Str(m,"url"),MessageIDs:[]string{id}}
		if err:=validateNetworkReceipt(sent);err!=nil{return nil,err}
		if comment:=jx.Str(extras,"first_comment");comment!=""{
			if typ:=jx.Str(jx.Map(m,"recipient"),"chat_type");typ!=""&&typ!="channel"{sent.Warning=firstCommentWarning("MAX");return sent,nil}
			if !maxCommentIDPattern.MatchString(id){sent.Warning=firstCommentWarning("MAX");return sent,nil}
			r,err:=request(ctx,http.MethodPost,maxAPI+"/messages/"+url.PathEscape(id)+"/comments",headers,jsonBody(jx.M{"text":comment}))
			if err==nil&&r!=nil&&r.Status==200{
				cm:=jx.Map(r.Data,"message")
				cid:=jx.Str(jx.Map(cm,"body"),"mid");if cid==""{cid=jx.Str(jx.Map(cm,"message"),"mid")}
				if maxCommentIDPattern.MatchString(cid){sent.CommentID=cid}
			}
			if sent.CommentID==""{sent.Warning=firstCommentWarning("MAX")}
		}
		return sent,nil
	}
}
