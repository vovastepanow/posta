package posta

import (
	"net/url"
	"strings"
	"math"
	"strconv"
	"regexp"

	"posta/internal/jx"
)

func postExtraKinds(platform string) []string {
	switch platform {
	case "", "telegram":
		return []string{"buttons","silent","protect","spoiler","preview","location","contact"}
	case "max":
		return []string{"buttons","preview","location","contact"}
	case "vk":
		return []string{"signed","close_comments","mark_as_ads","copyright","link_attachment","vk_geo"}
	case "threads":
		return []string{"alt_text","topic_tag","link_attachment","spoiler"}
	case "instagram":
		return []string{"alt_text"}
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
		case "location","contact":
			if value==nil{continue}
			if jx.ToMap(value)==nil{return nil,Bad("Некорректная карточка публикации.")}
		case "buttons":
			if len(jx.ToList(value))==0{continue}
		case "silent","protect","spoiler","signed","close_comments","mark_as_ads":
			v,ok:=value.(bool);if !ok{return nil,Bad("Некорректный параметр публикации: %s.",key)};if !v{continue}
		case "alt_text","topic_tag","link_attachment","copyright","vk_geo":
			v,ok:=value.(string);if !ok{return nil,Bad("Некорректное значение дополнения %s.",key)};if strings.TrimSpace(v)==""{continue}
		case "preview":
			v,ok:=value.(string);if !ok{return nil,Bad("Некорректная настройка предпросмотра.")};if v==""||v=="default"{continue}
			if v!="on"&&v!="off"{return nil,Bad("Некорректная настройка предпросмотра.")}
		default:return nil,Bad("Неизвестное дополнение публикации: %s.",key)
		}
		if !contains(postExtraKinds(platform),key){return nil,Bad("Дополнение %s не поддерживается площадкой %s.",key,platform)}
		if poll!=nil{return nil,Bad("Дополнительные настройки обычного поста нельзя совмещать с опросом.")}
		switch key {
		case "location":
			m:=jx.ToMap(value)
			lat,err1:=strconv.ParseFloat(jx.ToStr(m["latitude"]),64)
			lon,err2:=strconv.ParseFloat(jx.ToStr(m["longitude"]),64)
			if err1!=nil||err2!=nil||math.IsNaN(lat)||math.IsNaN(lon)||math.IsInf(lat,0)||math.IsInf(lon,0)||lat < -90||lat > 90||lon < -180||lon > 180{return nil,Bad("Укажите корректную широту и долготу.")}
			out[key]=jx.M{"latitude":lat,"longitude":lon}
		case "contact":
			m:=jx.ToMap(value);first,last,phone:=strings.TrimSpace(jx.Str(m,"first_name")),strings.TrimSpace(jx.Str(m,"last_name")),strings.TrimSpace(jx.Str(m,"phone"))
			if first==""||runeLen(first)>64||runeLen(last)>64||strings.ContainsAny(first+last,"\r\n\x00")||!publishPhonePattern.MatchString(phone){return nil,Bad("Укажите имя контакта и телефон в международном формате, например +3581234567.")}
			out[key]=jx.M{"first_name":first,"last_name":last,"phone":phone}
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
		case "alt_text":
			if len(kinds)!=1||!contains([]string{"photo","image"},kinds[0]){return nil,Bad("Альтернативный текст доступен для одной фотографии.")}
			v:=strings.TrimSpace(jx.ToStr(value));if runeLen(v)>1000{return nil,Bad("Альтернативный текст: до 1000 символов.")};out[key]=v
		case "topic_tag":
			v:=strings.TrimSpace(jx.ToStr(value));if runeLen(v)>50||strings.ContainsAny(v,"#\r\n"){return nil,Bad("Тема Threads: до 50 символов, без # и переносов.")};out[key]=v
		case "copyright","link_attachment":
			v:=strings.TrimSpace(jx.ToStr(value));u,err:=url.Parse(v)
			if len(v)>2048||err!=nil||u.Hostname()==""||u.User!=nil||(u.Scheme!="http"&&u.Scheme!="https")||strings.ContainsAny(v,", \r\n"){return nil,Bad("Укажите корректную HTTP(S)-ссылку.")}
			if key=="link_attachment"&&((platform=="threads"&&len(kinds)>0)||(platform=="vk"&&len(kinds)>=10)){return nil,Bad("Ссылка несовместима с текущими вложениями.")};out[key]=v
		case "vk_geo":
			v:=strings.Split(jx.ToStr(value),",");if len(v)!=2{return nil,Bad("Укажите широту и долготу через запятую.")}
			lat,e1:=strconv.ParseFloat(strings.TrimSpace(v[0]),64);lon,e2:=strconv.ParseFloat(strings.TrimSpace(v[1]),64)
			if e1!=nil||e2!=nil||math.IsNaN(lat)||math.IsNaN(lon)||math.Abs(lat)>90||math.Abs(lon)>180{return nil,Bad("Некорректные координаты.")}
			out[key]=strconv.FormatFloat(lat,'f',-1,64)+","+strconv.FormatFloat(lon,'f',-1,64)
		case "spoiler":
			if platform=="threads"&&(len(kinds)!=1||!contains([]string{"photo","image","video"},kinds[0])){return nil,Bad("Спойлер Threads доступен для одного фото или видео.")}
			if len(kinds)==0{return nil,Bad("Спойлер применяется к фото, GIF или видео.")}
			for _,k:=range kinds{if !contains([]string{"image","photo","animation","video"},k){return nil,Bad("Спойлер не поддерживается для этого типа вложения.")}}
			out[key]=true
		case "preview":
			if len(kinds)!=0||strings.TrimSpace(text)==""{return nil,Bad("Управление превью ссылки доступно для текстового поста.")}
			out[key]=value
		default:out[key]=true
		}
	}
	if PostExtrasStandalone(out){
		if jx.Map(out,"location")!=nil&&jx.Map(out,"contact")!=nil{return nil,Bad("Выберите одну карточку: геолокацию или контакт.")}
		if len(kinds)>0||strings.TrimSpace(text)!=""||len(jx.Maps(out,"buttons"))>0||jx.Bool(out,"spoiler")||jx.Str(out,"preview")!=""{return nil,Bad("Карточка геолокации или контакта публикуется отдельно от текста и медиа.")}
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

var publishPhonePattern=regexp.MustCompile(`^[+][1-9][0-9]{6,14}$`)

// PostExtrasStandalone is only used after validating extras.
func PostExtrasStandalone(extra jx.M) bool { return jx.Map(extra,"location")!=nil||jx.Map(extra,"contact")!=nil }

func PostExtrasSummary(text string,extra jx.M)string{
	if strings.TrimSpace(text)!=""{return text}
	if jx.Map(extra,"location")!=nil{return "Геолокация"}
	if jx.Map(extra,"contact")!=nil{return "Карточка контакта"}
	return text
}

func telegramStandaloneFields(extra jx.M)(string,map[string]string){
	if loc:=jx.Map(extra,"location");loc!=nil{
		return "sendLocation",map[string]string{"latitude":jx.ToStr(loc["latitude"]),"longitude":jx.ToStr(loc["longitude"])}
	}
	if c:=jx.Map(extra,"contact");c!=nil{
		return "sendContact",map[string]string{"phone_number":jx.Str(c,"phone"),"first_name":jx.Str(c,"first_name"),"last_name":jx.Str(c,"last_name")}
	}
	return "",nil
}

func vcardEscape(s string)string{
	return strings.NewReplacer("\\","\\\\",";","\\;",",","\\,").Replace(s)
}
func maxStandaloneAttachment(extra jx.M)jx.M{
	if loc:=jx.Map(extra,"location");loc!=nil{return jx.M{"type":"location","latitude":loc["latitude"],"longitude":loc["longitude"]}}
	if c:=jx.Map(extra,"contact");c!=nil{
		first,last:=jx.Str(c,"first_name"),jx.Str(c,"last_name")
		name:=strings.TrimSpace(first+" "+last)
		card:="BEGIN:VCARD\r\nVERSION:3.0\r\nN:"+vcardEscape(last)+";"+vcardEscape(first)+";;;\r\nFN:"+vcardEscape(name)+"\r\nTEL;TYPE=CELL:"+jx.Str(c,"phone")+"\r\nEND:VCARD\r\n"
		return jx.M{"type":"contact","payload":jx.M{"name":name,"vcf_info":card}}
	}
	return nil
}
