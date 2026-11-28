package posta

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"posta/internal/imgx"
	"posta/internal/jx"
)

// ---- ВКонтакте ----

const vkPostAPI = "https://api.vk.com/method/"

func vkCall(ctx context.Context, method string, params url.Values, token string) (any, error) {
	params.Set("access_token", token)
	params.Set("v", "5.199")
	r, err := networkHTTP(ctx, "POST", vkPostAPI+method, map[string]string{"Content-Type": ctForm}, formBody(params))
	if err != nil {
		return nil, err
	}
	if r.Status >= 500 || r.Status == 408 {
		return nil, uncertainPublish(fmt.Sprintf("ВКонтакте: HTTP %d.", r.Status))
	}
	if e := jx.Map(r.Data, "error"); e != nil {
		return nil, vkMethodError(method, jx.Int(e, "error_code"), jx.Str(e, "error_msg"))
	}
	if r.Status != 200 {
		_, err := networkExpect(r, nil, []int{200}, "ВКонтакте")
		return nil, err
	}
	if r.Data["response"] == nil {
		return nil, uncertainPublish("ВКонтакте: API не вернул результат операции.")
	}
	return r.Data["response"], nil
}

// VK restricts some methods by token type even if the community key has photos permission.
func vkMethodError(method string, code int64, message string) error {
	if code == 27 && (method == "photos.getWallUploadServer" || method == "photos.saveWallPhoto") {
		return &vkPhotoAuthError{Bad("ВКонтакте не разрешает загружать фотографии на стену с этим ключом сообщества (ошибка 27). Подключите в профиле VK с доступом к методам загрузки фото или переподключите группу с подходящим пользовательским токеном. Обычный вход через VK ID такого доступа не гарантирует.")}
	}
	if code==27&&method=="docs.getWallUploadServer"{return &vkDocumentAuthError{Bad("ВКонтакте: для загрузки документа нужен токен с доступом к docs.getWallUploadServer.")}}
	if code == 27 && method=="video.save" {
		return &vkVideoAuthError{Bad("ВКонтакте: для загрузки видео необходим пользовательский токен с правом video; обычный ключ сообщества не подходит.")}
	}
	if code == 27 {
		return Bad("ВКонтакте не разрешает метод %s с этим ключом сообщества (ошибка 27). Проверьте доступность метода и тип ключа в VK.", method)
	}
	return fmt.Errorf("ВКонтакте (%s): %s.", method, trimRunes(message, 200))
}

type vkPhotoAuthError struct{ error }

func (e *vkPhotoAuthError) Unwrap() error { return e.error }

func vkConnect(a *App, ctx context.Context, in map[string]string) (*NetResource, error) {
	token, group := in["token"], strings.TrimLeft(in["group"], "-")
	if token == "" || group == "" {
		return nil, Bad("Укажите токен и адрес сообщества.")
	}
	res, err := vkCall(ctx, "groups.getById", url.Values{"group_id": {group}}, token)
	if err != nil {
		return nil, err
	}
	var info jx.M
	if l := jx.ToMaps(res); len(l) > 0 {
		info = l[0]
	} else if l := jx.Maps(jx.ToMap(res), "groups"); len(l) > 0 {
		info = l[0]
	}
	if info == nil {
		return nil, Bad("Сообщество не найдено или токен не даёт к нему доступа.")
	}
	// Read-only identification distinguishes a user grant from a community grant.
	userInfo, userErr := vkCall(ctx,"users.get",url.Values{},token)
	users := jx.ToMaps(userInfo)
	userGrant := userErr == nil && len(users) == 1 && jx.Int(users[0],"id") > 0
	return &NetResource{Ref: strconv.FormatInt(jx.Int(info, "id"), 10), Title: jx.Str(info, "name"), Secrets: map[string]string{"token": token},
		Meta: jx.M{"screen_name": jx.Str(info, "screen_name"), "poll_user_token":userGrant}}, nil
}

func vkPhotoAttachment(ctx context.Context, groupID, path, token string) (string, error) {
	return vkPhotoAttachmentWith(ctx, groupID, path, token, vkCall, networkHTTP)
}

func vkPhotoAttachmentWith(ctx context.Context, groupID, path, token string, call func(context.Context,string,url.Values,string)(any,error), request networkRequestFunc) (string,error) {
	res,err:=call(ctx,"photos.getWallUploadServer",url.Values{"group_id":{groupID}},token)
	if err!=nil{return "",err}
	upload:=jx.Str(jx.ToMap(res),"upload_url")
	if !vkUploadURL(upload){return "",Bad("ВКонтакте вернул непроверенный адрес загрузки фото.")}
	body,ct,cleanup,err:=networkMultipartFile(nil,nil,map[string]string{"photo":path})
	if err!=nil{return "",err};defer cleanup()
	sent,err:=request(ctx,http.MethodPost,upload,map[string]string{"Content-Type":ct},body)
	if err!=nil{return "",networkUploadError(err,"ВКонтакте: не удалось загрузить фото")}
	if sent==nil||sent.Status!=200{return "",Bad("ВКонтакте не принял фотографию.")}
	f:=orEmpty(sent.Data)
	if jx.Str(f,"photo")==""||jx.Str(f,"photo")=="[]"||jx.Str(f,"hash")==""||jx.Int(f,"server")<=0{return "",Bad("ВКонтакте не подтвердил загрузку фотографии.")}
	saved,err:=call(ctx,"photos.saveWallPhoto",url.Values{"group_id":{groupID},"photo":{jx.Str(f,"photo")},"server":{jx.Str(f,"server")},"hash":{jx.Str(f,"hash")}},token)
	if err!=nil{return "",err}
	l:=jx.ToMaps(saved)
	if len(l)!=1||jx.Int(l[0],"id")<=0||strconv.FormatInt(jx.Int(l[0],"owner_id"),10)!="-"+groupID{return "",Bad("ВКонтакте не подтвердил сохранение фото в выбранном сообществе.")}
	return fmt.Sprintf("photo%d_%d",jx.Int(l[0],"owner_id"),jx.Int(l[0],"id")),nil
}

func vkUploadURL(raw string) bool {
	u,err:=url.Parse(raw)
	if err!=nil||u.Scheme!="https"||u.User!=nil||u.Fragment!=""||(u.Port()!=""&&u.Port()!="443"){return false}
	host:=strings.ToLower(u.Hostname())
	for _,domain:=range []string{"vk.com","vk.ru","userapi.com","vkcdn.com","vkuseraudio.net","vkvideo.ru","vk-cdn.net"}{
		if host==domain||strings.HasSuffix(host,"."+domain){return true}
	}
	return false
}

func vkPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	return vkPublishWith(ctx,c,p,vkCall,networkHTTP,func()string{
		return a.VKAccessToken(a.User(ctx,c.UserID,nil).Ctx)
	})
}

// ---- MAX ----

const maxAPI = "https://platform-api2.max.ru"

var chatIDRe = regexp.MustCompile(`^-?\d{1,20}$`)

func maxConnect(a *App, ctx context.Context, in map[string]string) (*NetResource, error) {
	token, chat := in["token"], in["chat_id"]
	if token == "" || !chatIDRe.MatchString(chat) {
		return nil, Bad("Укажите токен бота MAX и id чата.")
	}
	r, err := networkHTTP(ctx, "GET", maxAPI+"/chats/"+chat, map[string]string{"Authorization": token}, nil)
	d, err := networkExpect(r, err, []int{200}, "Чат не найден или бот в нём не админ")
	if err != nil {
		return nil, err
	}
	title := jx.Str(d, "title")
	if title == "" {
		title = "MAX"
	}
	return &NetResource{Ref: chat, Title: title, Secrets: map[string]string{"token": token}, Meta: jx.M{"chat_type":jx.Str(d,"type")}}, nil
}

func maxPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	return maxPublishWith(ctx,c,p,networkHTTP,waitNetworkContext)
}

// ---- Bluesky ----

const (
	blueskyPDS     = "https://bsky.social"
	blueskyBlobMax = 950000
	blueskyTextMax = 300
)

func blueskySession(ctx context.Context, pds, id, pass string) (jx.M, error) {
	r, err := networkHTTP(ctx, "POST", pds+"/xrpc/com.atproto.server.createSession", map[string]string{"Content-Type": ctJSON}, jsonBody(jx.M{"identifier": id, "password": pass}))
	return networkExpect(r, err, []int{200}, "Вход в Bluesky не удался (нужен пароль приложения)")
}

func blueskyConnect(a *App, ctx context.Context, in map[string]string) (*NetResource, error) {
	id, pass := in["identifier"], in["password"]
	if id == "" || pass == "" {
		return nil, Bad("Укажите ник Bluesky и пароль приложения.")
	}
	s, err := blueskySession(ctx, blueskyPDS, id, pass)
	if err != nil {
		return nil, err
	}
	return &NetResource{Ref: jx.Str(s, "did"), Title: "@" + jx.Str(s, "handle"), Secrets: map[string]string{"identifier": id, "password": pass},
		Meta: jx.M{"handle": jx.Str(s, "handle"), "pds": blueskyPDS}}, nil
}

var (
	bskyLinkRe = regexp.MustCompile(`https?://[^\s<>"']+`)
	bskyTagRe  = regexp.MustCompile(`(^|\s)(#([\p{L}\p{N}_]{1,60}))`)
)

// BlueskyFacets — ссылки и хэштеги; смещения — байты UTF-8.
func BlueskyFacets(text string) []jx.M {
	var out []jx.M
	for _, m := range bskyLinkRe.FindAllStringIndex(text, -1) {
		out = append(out, jx.M{"index": jx.M{"byteStart": m[0], "byteEnd": m[1]}, "features": []jx.M{{"$type": "app.bsky.richtext.facet#link", "uri": text[m[0]:m[1]]}}})
	}
	for _, m := range bskyTagRe.FindAllStringSubmatchIndex(text, -1) {
		out = append(out, jx.M{"index": jx.M{"byteStart": m[4], "byteEnd": m[5]}, "features": []jx.M{{"$type": "app.bsky.richtext.facet#tag", "tag": text[m[6]:m[7]]}}})
	}
	return out
}

func blueskyPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	pds := jx.Str(c.Meta, "pds")
	if pds == "" {
		pds = blueskyPDS
	}
	s, err := blueskySession(ctx, pds, c.Secrets["identifier"], c.Secrets["password"])
	if err != nil {
		return nil, err
	}
	auth := "Bearer " + jx.Str(s, "accessJwt")
	text := p.Text
	if runeLen(text) > blueskyTextMax {
		text = string([]rune(text)[:blueskyTextMax-1]) + "…"
	}
	var images []jx.M
	for _, f := range p.Media {
		b, err := os.ReadFile(f.Path)
		if err != nil {
			continue
		}
		for _, side := range []int{2048, 1600, 1200} {
			if len(b) <= blueskyBlobMax {
				break
			}
			b = imgx.Shrink(b, side, 80, true)
		}
		if len(b) > blueskyBlobMax {
			continue
		}
		r, err := networkHTTP(ctx, "POST", pds+"/xrpc/com.atproto.repo.uploadBlob", map[string]string{"Authorization": auth, "Content-Type": http.DetectContentType(b)}, bytes.NewReader(b))
		d, err := networkExpect(r, err, []int{200}, "Картинка не загружена")
		if err != nil {
			return nil, err
		}
		if blob := d["blob"]; blob != nil {
			images = append(images, jx.M{"alt": "", "image": blob})
		}
	}
	record := jx.M{"$type": "app.bsky.feed.post", "text": text, "createdAt": time.Now().UTC().Format("2006-01-02T15:04:05Z")}
	if f := BlueskyFacets(text); len(f) > 0 {
		record["facets"] = f
	}
	if len(images) > 0 {
		record["embed"] = jx.M{"$type": "app.bsky.embed.images", "images": images[:min(4, len(images))]}
	}
	r, err := networkHTTP(ctx, "POST", pds+"/xrpc/com.atproto.repo.createRecord", map[string]string{"Authorization": auth, "Content-Type": ctJSON},
		jsonBody(jx.M{"repo": jx.Str(s, "did"), "collection": "app.bsky.feed.post", "record": record}))
	made, err := networkExpect(r, err, []int{200}, "Пост не опубликован")
	if err != nil {
		return nil, err
	}
	uri := jx.Str(made, "uri")
	rkey := uri[strings.LastIndex(uri, "/")+1:]
	handle := jx.Str(c.Meta, "handle")
	if handle == "" {
		handle = jx.Str(s, "did")
	}
	u := ""
	if rkey != "" {
		u = "https://bsky.app/profile/" + handle + "/post/" + rkey
	}
	return &NetSent{URL: u, MessageIDs: []string{jx.Str(made, "cid")}}, nil
}

// ---- Mastodon ----

var mastoHostRe = regexp.MustCompile(`^[a-z0-9.-]{3,253}$`)

func MastodonInstanceURL(in string) (string, error) {
	h := strings.ToLower(strings.Trim(regexp.MustCompile(`(?i)^https?://`).ReplaceAllString(strings.TrimSpace(in), ""), "/"))
	if !mastoHostRe.MatchString(h) || !strings.Contains(h, ".") || net.ParseIP(h) != nil {
		return "", Bad("Укажите адрес сервера Mastodon, например mastodon.social.")
	}
	return "https://" + h, nil
}

func mastodonConnect(a *App, ctx context.Context, in map[string]string) (*NetResource, error) {
	inst, err := MastodonInstanceURL(in["instance"])
	if err != nil {
		return nil, err
	}
	token := in["token"]
	if token == "" {
		return nil, Bad("Вставьте токен доступа Mastodon.")
	}
	r, err := networkHTTP(ctx, "GET", inst+"/api/v1/accounts/verify_credentials", bearer(token), nil)
	me, err := networkExpect(r, err, []int{200}, "Токен не принят")
	if err != nil {
		return nil, err
	}
	r, err = networkHTTP(ctx, "GET", inst+"/api/v2/instance", nil, nil)
	conf, err := networkExpect(r, err, []int{200}, "Настройки сервера не получены")
	if err != nil {
		return nil, err
	}
	maxChars := jx.Int(jx.Map(jx.Map(conf, "configuration"), "statuses"), "max_characters")
	if maxChars == 0 {
		maxChars = 500
	}
	title := jx.Str(me, "display_name")
	if title == "" {
		title = jx.Str(me, "acct")
	}
	return &NetResource{Ref: strings.TrimPrefix(inst, "https://") + "/" + jx.Str(me, "id"), Title: title, Secrets: map[string]string{"token": token},
		Meta: jx.M{"instance": inst, "acct": "@" + jx.Str(me, "acct"), "max_chars": maxChars}}, nil
}

func mastodonPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	inst := jx.Str(c.Meta, "instance")
	auth := bearer(c.Secrets["token"])
	limit := int(jx.Int(c.Meta, "max_chars"))
	if limit == 0 {
		limit = 500
	}
	if runeLen(p.Text) > limit {
		return nil, Bad("Текст длиннее %d символов: Mastodon его не примет.", limit)
	}
	form := url.Values{"status": {p.Text}, "visibility": {"public"}}
	for _, f := range p.Media {
		body, ct, cleanup, err := networkMultipartFile(nil, nil, map[string]string{"file": f.Path})
		if err != nil {
			return nil, err
		}
		r, err := networkHTTP(ctx, "POST", inst+"/api/v2/media", withCT(auth, ct), body)
		cleanup()
		d, err := networkExpect(r, err, []int{200, 202}, "Файл не загружен")
		if err != nil {
			return nil, err
		}
		id := jx.Str(d, "id")
		if id == "" {
			return nil, errors.New("Файл не загружен: сервер не вернул номер.")
		}
		status := r.Status
		for try := 0; status == 202 && try < 30; try++ {
			time.Sleep(time.Second)
			if rr, err := networkHTTP(ctx, "GET", inst+"/api/v1/media/"+id, auth, nil); err == nil {
				status = rr.Status
			}
		}
		if status != 200 {
			return nil, errors.New("Файл не обработан Mastodon.")
		}
		form.Add("media_ids[]", id)
	}
	r, err := networkHTTP(ctx, "POST", inst+"/api/v1/statuses", withCT(auth, ctForm), formBody(form))
	sent, err := networkExpect(r, err, []int{200}, "Пост не опубликован")
	if err != nil {
		return nil, err
	}
	u := jx.Str(sent, "url")
	if u == "" {
		u = jx.Str(sent, "uri")
	}
	return &NetSent{URL: u, MessageIDs: []string{jx.Str(sent, "id")}}, nil
}

// ---- Discord ----

var discordHookRe = regexp.MustCompile(`^https://(?:discord|discordapp)\.com/api/webhooks/(\d{5,25})/[\w-]{20,200}$`)

func discordConnect(a *App, ctx context.Context, in map[string]string) (*NetResource, error) {
	u := in["webhook"]
	if !discordHookRe.MatchString(u) {
		return nil, Bad("Адрес вебхука Discord выглядит неверно.")
	}
	r, err := networkHTTP(ctx, "GET", u, nil, nil)
	h, err := networkExpect(r, err, []int{200}, "Вебхук не принят")
	if err != nil {
		return nil, err
	}
	title := jx.Str(h, "name")
	if title == "" {
		title = "Discord"
	}
	return &NetResource{Ref: jx.Str(h, "channel_id"), Title: title, Secrets: map[string]string{"webhook": u},
		Meta: jx.M{"guild_id": jx.Str(h, "guild_id"), "channel_id": jx.Str(h, "channel_id")}}, nil
}

func discordPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	if runeLen(p.Text) > 2000 {
		return nil, Bad("Текст длиннее 2000 символов: Discord его не примет.")
	}
	files := map[string]string{}
	for i, f := range p.Media {
		files[fmt.Sprintf("files[%d]", i)] = f.Path
	}
	body, ct, cleanup, err := networkMultipartFile(map[string]string{"payload_json": string(jx.Marshal(jx.M{"content": p.Text}))}, nil, files)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	hook := c.Secrets["webhook"]
	sep := "?"
	if strings.Contains(hook, "?") {
		sep = "&"
	}
	r, err := networkHTTP(ctx, "POST", hook+sep+"wait=true", map[string]string{"Content-Type": ct}, body)
	sent, err := networkExpect(r, err, []int{200}, "Пост не опубликован")
	if err != nil {
		return nil, err
	}
	guild, ch := jx.Str(c.Meta, "guild_id"), jx.Str(sent, "channel_id")
	if ch == "" {
		ch = jx.Str(c.Meta, "channel_id")
	}
	u := ""
	if guild != "" {
		u = "https://discord.com/channels/" + guild + "/" + ch + "/" + jx.Str(sent, "id")
	}
	return &NetSent{URL: u, MessageIDs: []string{jx.Str(sent, "id")}}, nil
}

// ---- Одноклассники ----

func OKSign(params map[string]string, secret string) string {
	keys := make([]string, 0, len(params))
	for k := range params {
		if k != "access_token" && k != "session_key" && k != "sig" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		b.WriteString(k + "=" + params[k])
	}
	h := md5.Sum([]byte(b.String() + secret))
	return hex.EncodeToString(h[:])
}

func (a *App) okRequest(ctx context.Context, method string, params map[string]string, token, secret string) (*netResp, error) {
	params["application_key"], params["method"], params["format"], params["access_token"] = a.NetworkAppKey("OK_APP_KEY"), method, "json", token
	params["sig"] = OKSign(params, secret)
	q := url.Values{}
	for k, v := range params {
		q.Set(k, v)
	}
	return networkHTTP(ctx, "POST", "https://api.ok.ru/fb.do", map[string]string{"Content-Type":ctForm},formBody(q))
}

func okConnect(a *App, ctx context.Context, in map[string]string) (*NetResource, error) {
	token, secret, gid := in["access_token"], in["session_secret_key"], in["gid"]
	if token == "" || secret == "" || !regexp.MustCompile(`^\d{1,20}$`).MatchString(gid) {
		return nil, Bad("Укажите токен, секретный ключ сессии и id группы.")
	}
	r, err := a.okRequest(ctx, "users.getCurrentUser", map[string]string{}, token, secret)
	me, err := networkExpect(r, err, []int{200}, "Одноклассники: users.getCurrentUser")
	if err != nil {
		return nil, err
	}
	if jx.Int(me,"error_code")!=0 || jx.Str(me,"id")=="" && jx.Str(me,"uid")=="" {
		return nil,Bad("Одноклассники: токен не подтверждён.")
	}
	title := strings.TrimSpace(jx.Str(me, "name"))
	if title == "" {
		title = "Одноклассники"
	}
	return &NetResource{Ref: gid, Title: title, Secrets: map[string]string{"access_token": token, "session_secret_key": secret}, Meta: jx.M{"gid": gid}}, nil
}

func okPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent,error) {
	call:=func(ctx context.Context,method string,params map[string]string)(*netResp,error){
		return a.okRequest(ctx,method,params,c.Secrets["access_token"],c.Secrets["session_secret_key"])
	}
	return okPublishWith(ctx,c,p,call,networkHTTP)
}

// OK returns an ID as a JSON scalar, but also uses HTTP 200 for API errors.
func okPublishedID(r *netResp) (string, error) {
	if r==nil{return "",uncertainPublish("Одноклассники не вернули ответ.")}
	if r.Status >= 500 || r.Status == 408 {
		return "", uncertainPublish(fmt.Sprintf("Одноклассники: HTTP %d.", r.Status))
	}
	if r.Status != 200 {
		_, err := networkExpect(r, nil, []int{200}, "Одноклассники")
		return "", err
	}
	if jx.Int(r.Data, "error_code") != 0 || jx.Str(r.Data, "error_msg") != "" {
		return "", fmt.Errorf("Одноклассники отклонили публикацию (код %d): %w", jx.Int(r.Data, "error_code"), ErrNetworkRejected)
	}
	id := strings.TrimSpace(string(r.Raw))
	var quoted string
	if json.Unmarshal(r.Raw, &quoted) == nil {
		id = quoted
	}
	if !regexp.MustCompile(`^[1-9][0-9]{0,24}$`).MatchString(id) {
		return "", uncertainPublish("Одноклассники: API не вернул ID темы.")
	}
	return id, nil
}
