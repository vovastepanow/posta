package posta

import (
	"net/url"
	"strings"

	"posta/internal/jx"
)

func postExtraKinds(platform string) []string {
	switch platform {
	case "", "telegram":
		return []string{"buttons","silent","protect","spoiler","preview"}
	case "max":
		return []string{"buttons","preview"}
	}
	return []string{}
}

func PreparedKinds(media []Prepared) []string {
	out:=make([]string,0,len(media))
	for _,f:=range media{out=append(out,f.Kind)}
	return out
}

// PublishExtrasCheck is used before queuing, on editing and immediately before
// delivery. Unknown or incompatible fields are rejected instead of discarded.
func PublishExtrasCheck(platform,text string,kinds []string,poll,raw jx.M) (jx.M,error) {
	if len(raw)==0{return nil,nil}
	if platform==""{platform="telegram"}
	out:=jx.M{}
	for key,value:=range raw {
		switch key {
		case "buttons":
			if len(jx.ToList(value))==0{continue}
		case "silent","protect","spoiler":
			v,ok:=value.(bool);if !ok{return nil,Bad("Некорректный параметр публикации: %s.",key)};if !v{continue}
		case "preview":
			v,ok:=value.(string);if !ok{return nil,Bad("Некорректная настройка предпросмотра.")};if v==""||v=="default"{continue}
			if v!="on"&&v!="off"{return nil,Bad("Некорректная настройка предпросмотра.")}
		default:return nil,Bad("Неизвестное дополнение публикации: %s.",key)
		}
		if !contains(postExtraKinds(platform),key){return nil,Bad("Дополнение %s не поддерживается площадкой %s.",key,platform)}
		if poll!=nil{return nil,Bad("Дополнительные настройки обычного поста нельзя совмещать с опросом.")}
		switch key {
		case "buttons":
			rows:=jx.ToList(value);if len(rows)>8{return nil,Bad("Допустимо не более 8 кнопок-ссылок.")}
			if platform=="telegram"&&len(kinds)>1{return nil,Bad("Кнопки Telegram нельзя добавить непосредственно к альбому.")}
			buttons:=[]jx.M{}
			for _,v:=range rows {
				b:=jx.ToMap(v);label,link:=strings.TrimSpace(jx.Str(b,"text")),strings.TrimSpace(jx.Str(b,"url"))
				u,err:=url.Parse(link)
				if label==""||runeLen(label)>64||len(link)>2048||err!=nil||u.Hostname()==""||u.User!=nil||(u.Scheme!="http"&&u.Scheme!="https"){return nil,Bad("У каждой кнопки нужен текст до 64 символов и корректная HTTP(S)-ссылка.")}
				buttons=append(buttons,jx.M{"text":label,"url":link})
			}
			out[key]=buttons
		case "spoiler":
			if len(kinds)==0{return nil,Bad("Спойлер применяется к фото, GIF или видео.")}
			for _,k:=range kinds{if !contains([]string{"image","photo","animation","video"},k){return nil,Bad("Спойлер не поддерживается для этого типа вложения.")}}
			out[key]=true
		case "preview":
			if len(kinds)!=0||strings.TrimSpace(text)==""{return nil,Bad("Управление превью ссылки доступно для текстового поста.")}
			out[key]=value
		default:out[key]=true
		}
	}
	if len(out)==0{return nil,nil}
	return out,nil
}

func telegramExtraFields(fields map[string]string,extra jx.M) {
	if jx.Bool(extra,"silent"){fields["disable_notification"]="true"}
	if jx.Bool(extra,"protect"){fields["protect_content"]="true"}
	if buttons:=jx.Maps(extra,"buttons");len(buttons)>0{
		rows:=[][]jx.M{};for _,b:=range buttons{rows=append(rows,[]jx.M{b})}
		fields["reply_markup"]=string(jx.Marshal(jx.M{"inline_keyboard":rows}))
	}
}
