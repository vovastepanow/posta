package posta

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"posta/internal/imgx"
	"posta/internal/jx"
)

// Автопостинг в соцсети кроме Telegram: реестр, общий HTTP, адаптеры, OAuth. Статусы: ready — публикует; beta — публикует,
// подключает пока только владелец; needs_keys — нужен ключ приложения на сервере; manual / unavailable — публикации нет.

type NetField struct {
	Name   string `json:"name"`
	Label  string `json:"label"`
	Secret bool   `json:"secret,omitempty"`
}

type OAuthFlow struct {
	Authorize func(a *App, state, redirect, verifier string) string
	Exchange  func(a *App, ctx context.Context, code, redirect, verifier string) (map[string]string, error)
	Identify  func(a *App, ctx context.Context, secrets map[string]string) (*NetResource, error)
	Refresh   func(a *App, ctx context.Context, secrets map[string]string) (map[string]string, error)
	PKCE      bool
	RefreshWithAccessToken bool
}

// NetResource — подключённый ресурс: ref, название, секреты, meta.
type NetResource struct {
	Ref     string
	Title   string
	Secrets map[string]string
	Meta    jx.M
}

// NetPost — пост очереди для адаптера: текст и файлы (path, kind).
type NetPost struct {
	Text  string
	Media []Prepared
	Poll  jx.M
	Extras jx.M
}

type NetSent struct {
	URL        string
	MessageIDs []string
}

type Network struct {
	Key       string
	Title     string
	Prefix    string
	Status    string
	Note      string
	TextLimit [2]int // с файлами, без файлов
	MediaMax  int
	Kinds     []string
	MaxBytes  map[string]int64
	Poll      bool
	Docs      string
	Fields    []NetField
	AppKeys   []string
	Connect   func(a *App, ctx context.Context, in map[string]string) (*NetResource, error)
	Publish   func(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error)
	OAuth     *OAuthFlow
}

var networkOrder = []string{"telegram", "vk", "max", "bluesky", "mastodon", "discord", "dzen", "ok", "youtube", "pinterest", "tumblr", "linkedin",
	"threads", "instagram", "facebook", "x", "reddit", "tiktok", "rutube", "viber", "tenchat", "medium", "bitrix", "wordpress", "google_business"}

// Networks — реестр соцсетей; ключи приложений берутся из posta.env.
func (a *App) Networks() map[string]*Network {
	na := func(title, note string) *Network { return &Network{Title: title, Status: "unavailable", Note: note} }
	r := map[string]*Network{
		"telegram": {Title: "Telegram", Prefix: "tg", Status: "ready", TextLimit: [2]int{1024, 4096}, MediaMax: 10, Kinds: []string{"photo", "video", "animation", "audio", "document", "voice"}, MaxBytes: map[string]int64{"audio": 50_000_000, "document": 50_000_000, "voice":50_000_000}, Poll: true},
		"vk": {Title: "ВКонтакте (группа)", Prefix: "vk", Status: "beta", Note: "Ключ сообщества позволяет проверить и подключить группу; фотографии и видео могут требовать пользовательский токен с доступом к методам API VK.", TextLimit: [2]int{16384, 16384},
			MediaMax: 10, Kinds: []string{"photo","video","animation","document"}, MaxBytes: map[string]int64{"photo": 50_000_000,"video":200_000_000,"animation":50_000_000,"document":50_000_000}, Docs: "partly", Poll: true,
			Fields: []NetField{{"token", "Ключ сообщества или пользовательский токен VK", true}, {"group", "Адрес или id сообщества", false}}, Connect: vkConnect, Publish: vkPublish},
		"max": {Title: "MAX", Prefix: "max", Status: "beta", Note: "Бот MAX: текст, фотографии, GIF и видео. Реальная отправка требует прав в целевом чате.", TextLimit: [2]int{4000, 4000}, MediaMax: 12, Kinds: []string{"photo", "animation", "video", "audio", "document"}, MaxBytes: map[string]int64{"photo":50_000_000,"animation":50_000_000,"video":250_000_000,"audio":50_000_000,"document":50_000_000}, Docs: "ok",
			Fields: []NetField{{"token", "Токен бота (MAX для бизнеса)", true}, {"chat_id", "id чата, где бот админ", false}}, Connect: maxConnect, Publish: maxPublish},
		"bluesky": {Title: "Bluesky", Prefix: "bsky", Status: "beta", Note: "Пароль приложения Bluesky.", TextLimit: [2]int{300, 300}, MediaMax: 4, Kinds: []string{"photo"},
			MaxBytes: map[string]int64{"photo": 20_000_000}, Docs: "unverified",
			Fields: []NetField{{"identifier", "Ник (handle), например name.bsky.social", false}, {"password", "Пароль приложения (не основной)", true}}, Connect: blueskyConnect, Publish: blueskyPublish},
		"mastodon": {Title: "Mastodon", Prefix: "mastodon", Status: "ready", Note: "Токен доступа (write:statuses, write:media) и адрес сервера.", TextLimit: [2]int{500, 500},
			MediaMax: 4, Kinds: []string{"photo", "animation", "video"}, MaxBytes: map[string]int64{"photo": 16_000_000, "video": 99_000_000}, Docs: "ok",
			Fields: []NetField{{"instance", "Сервер, например mastodon.social", false}, {"token", "Токен доступа (write:statuses, write:media)", true}}, Connect: mastodonConnect, Publish: mastodonPublish},
		"discord": {Title: "Discord", Prefix: "discord", Status: "ready", Note: "Вебхук канала.", TextLimit: [2]int{2000, 2000}, MediaMax: 10, Kinds: []string{"photo", "animation", "video"},
			MaxBytes: map[string]int64{"photo": 10_000_000, "animation": 10_000_000, "video": 10_000_000}, Docs: "ok",
			Fields: []NetField{{"webhook", "Адрес вебхука канала", true}}, Connect: discordConnect, Publish: discordPublish},
		"dzen": {Title: "Дзен", Status: "manual", Note: "Публичного API публикации нет. Посты Telegram-канала выходят в Дзене сами: Дзен-студия → настройки канала → код для Telegram → бот @zen_sync_bot админом в канал → /sync."},
		"ok": {Title: "Одноклассники (группа)", Prefix: "ok", Status: "needs_keys", Note: "Ключ приложения OK; GROUP_CONTENT, PHOTO_CONTENT для фото, VIDEO_CONTENT и VALUABLE_ACCESS для видео.", TextLimit: [2]int{4000, 4000}, MediaMax: 10, Kinds: []string{"photo","video"}, MaxBytes: map[string]int64{"photo":20_000_000,"video":200_000_000},
			Docs: "partly", Poll: true, AppKeys: []string{"OK_APP_KEY"}, Connect: okConnect, Publish: okPublish,
			Fields: []NetField{{"access_token", "Постоянный токен приложения", true}, {"session_secret_key", "Секретный ключ сессии", true}, {"gid", "id группы", false}}},
		"youtube": {Title: "YouTube Shorts", Prefix: "yt", Status: "needs_keys", Note: "Проект Google и экран согласия; без аудита ролики приватные. Только видео.",
			TextLimit: [2]int{5000, 5000}, MediaMax: 1, Kinds: []string{"video"}, MaxBytes: map[string]int64{"video": 200_000_000}, Docs: "ok",
			AppKeys: []string{"YOUTUBE_CLIENT_ID", "YOUTUBE_CLIENT_SECRET"}, OAuth: youtubeOAuth, Publish: youtubePublish},
		"pinterest": {Title: "Pinterest", Prefix: "pin", Status: "needs_keys", Note: "Приложение Pinterest; фото или видео с обложкой, одна доска на подключение.", TextLimit: [2]int{800, 800}, MediaMax: 1,
			Kinds: []string{"photo","video"}, MaxBytes: map[string]int64{"photo": 20_000_000,"video":200_000_000}, Docs: "ok", AppKeys: []string{"PINTEREST_CLIENT_ID", "PINTEREST_CLIENT_SECRET"},
			OAuth: pinterestOAuth, Publish: pinterestPublish},
		"tumblr": {Title: "Tumblr", Prefix: "tumblr", Status: "needs_keys", Note: "Приложение Tumblr, OAuth 2; текст, фото, GIF, один загружаемый ролик (обработка может занять время).", TextLimit: [2]int{4096, 4096}, MediaMax: 10, Kinds: []string{"photo","animation","video"}, MaxBytes: map[string]int64{"photo":20_000_000,"animation":5_000_000,"video":500_000_000}, Docs: "ok",
			AppKeys: []string{"TUMBLR_CLIENT_ID", "TUMBLR_CLIENT_SECRET"}, OAuth: tumblrOAuth, Publish: tumblrPublish},
		"linkedin": {Title: "LinkedIn (профиль)", Prefix: "li", Status: "needs_keys", Note: "Приложение LinkedIn с продуктом «Share on LinkedIn»; токен на 60 дней, без обновления.",
			TextLimit: [2]int{3000, 3000}, Docs: "partly", AppKeys: []string{"LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET"}, OAuth: linkedinOAuth, Publish: linkedinPublish},
		"threads": {Title:"Threads",Prefix:"threads",Status:"needs_keys",Note:"OAuth приложения Threads; текст, фото, видео и карусели. Для медиа необходим доступный HTTPS-адрес Posta.",TextLimit:[2]int{500,500},MediaMax:10,Kinds:[]string{"photo","video"},MaxBytes:map[string]int64{"photo":8_000_000,"video":200_000_000},Docs:"partly",Poll:true,AppKeys:[]string{"THREADS_CLIENT_ID","THREADS_CLIENT_SECRET"},OAuth:threadsOAuth,Publish:threadsPublish},
		"instagram": {Title:"Instagram",Prefix:"ig",Status:"needs_keys",Note:"Meta признана экстремистской и запрещена в РФ. OAuth профессионального аккаунта; фотографии, Reels и карусели. Требуется публичный HTTPS-адрес Posta.",TextLimit:[2]int{2200,2200},MediaMax:10,Kinds:[]string{"photo","video"},MaxBytes:map[string]int64{"photo":8_000_000,"video":200_000_000},Docs:"partly",AppKeys:[]string{"INSTAGRAM_CLIENT_ID","INSTAGRAM_CLIENT_SECRET"},OAuth:instagramOAuth,Publish:instagramPublish},
		"facebook": {Title:"Facebook (страница)",Prefix:"fb",Status:"beta",Note:"Meta признана экстремистской и запрещена в РФ. Нужен токен управляемой страницы с pages_manage_posts; текст, фотоальбом или одно видео.",TextLimit:[2]int{63206,63206},MediaMax:10,Kinds:[]string{"photo","video"},MaxBytes:map[string]int64{"photo":10_000_000,"video":200_000_000},Docs:"partly",Fields:[]NetField{{"page_id","ID страницы Facebook",false},{"access_token","Page access token",true}},Connect:facebookConnect,Publish:facebookPublish},
		"x": {Title: "X (Twitter)", Prefix: "x", Status: "needs_keys", Note: "Требуется доступ к API X и право media.write. Текст, до четырёх фото либо одно видео/GIF.", TextLimit: [2]int{280, 280},MediaMax:4,Kinds:[]string{"photo","video","animation"},MaxBytes:map[string]int64{"photo":5_000_000,"video":200_000_000,"animation":15_000_000},
			Docs: "partly", Poll: true, AppKeys: []string{"X_CLIENT_ID", "X_CLIENT_SECRET"}, OAuth: xOAuth, Publish: xPublish},
		"reddit": {Title: "Reddit", Prefix: "reddit", Status: "needs_keys", Note: "Приложение Reddit; постим только в свой профиль, текстом.", TextLimit: [2]int{40000, 40000},
			Docs: "partly", AppKeys: []string{"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET"}, OAuth: redditOAuth, Publish: redditPublish},
		"tiktok":   na("TikTok", "Публикация доступна только после аудита приложения TikTok: без проверки видно «только мне»."),
		"rutube":          na("Rutube", "Нужен подтверждённый способ загрузки через партнёрский API."),
		"viber":           na("Viber", "Требуется поддерживаемый API и подходящий тип аккаунта."),
		"tenchat":         na("TenChat", "Требуется доступ к партнёрской интеграции."),
		"medium":          na("Medium", "Требуется подтверждённый доступ к API публикации."),
		"bitrix":          na("Битрикс24", "Адаптер публикации в ленту ещё не реализован."),
		"google_business": na("Google Business Profile", "Требуется одобрение API и OAuth."),
		"wordpress": {Title: "WordPress", Prefix: "wp", Status: "beta", Note: "Самостоятельный сайт WordPress: REST API и пароль приложения; пока только текст.", TextLimit: [2]int{100000, 100000},
			Docs: "ok", Fields: []NetField{{"site", "HTTPS-адрес сайта WordPress", false}, {"username", "Имя пользователя", false}, {"password", "Пароль приложения WordPress", true}}, Connect: wordpressConnect, Publish: wordpressPublish},
	}
	r["telegram"].Publish = telegramNetworkPublish
	for k, n := range r {
		n.Key = k
		if len(n.AppKeys) > 0 {
			n.Status = "needs_keys"
			if a.networkKeysPresent(n.AppKeys) {
				n.Status = "beta"
			}
		}
	}
	return r
}

func (a *App) NetworkAppKey(name string) string { return strings.TrimSpace(a.Cfg.NetworkKeys[name]) }

func (a *App) networkKeysPresent(names []string) bool {
	for _, n := range names {
		if a.NetworkAppKey(n) == "" {
			return false
		}
	}
	return len(names) > 0
}

// NetworkFor — адаптер публикации для платформы; соцсеть без публикации — понятная ошибка.
func (a *App) NetworkFor(platform string) (*Network, error) {
	n := a.Networks()[platform]
	if n == nil {
		return nil, Bad("Неизвестная соцсеть: %s.", platform)
	}
	if n.Publish == nil {
		return nil, Bad("%s пока не публикует: %s", n.Title, n.Note)
	}
	return n, nil
}

func (n *Network) textLimit(withMedia bool) int {
	l := n.TextLimit[1]
	if withMedia {
		l = n.TextLimit[0]
	}
	if l == 0 {
		return 4096
	}
	return l
}

func kindName(kind string) string {
	switch kind {
	case "video":
		return "видео"
	case "animation":
		return "GIF"
	case "audio":
		return "аудио"
	case "document":
		return "документ"
	case "voice":
		return "голосовое сообщение"
	}
	return "картинка"
}

// NetworkPreflight — до скачивания: текст по лимиту ресурса, виды файлов по реестру.
func (a *App) NetworkPreflight(platform, text string, kinds []string) (int, []string, error) {
	n := a.Networks()[platform]
	if n == nil {
		return 0, nil, Bad("Неизвестная соцсеть: %s.", platform)
	}
	if limit := n.textLimit(len(kinds) > 0); runeLen(text) > limit {
		return 0, nil, Bad("%s: текст длиннее %d символов — сократите его.", n.Title, limit)
	}
	kept, videos := 0,0
	skipped := []string{}
	for _, k := range kinds {
		if k == "image" {
			k = "photo"
		}
		switch {
		case !contains(n.Kinds, k):
			skipped = append(skipped, kindName(k)+": "+n.Title+" такой вид не принимает")
		case platform == "tumblr" && k == "video" && videos >= 1:
			skipped = append(skipped,"Tumblr: в одном посте допускается только одно загружаемое видео")
		case kept >= max(n.MediaMax, 0):
			skipped = append(skipped, fmt.Sprintf("%s: больше %d файлов для %s", kindName(k), n.MediaMax, n.Title))
		default:
			kept++
			if k=="video"{videos++}
		}
	}
	if platform=="max" { if err:=maxAttachmentKindsCheck(kinds);err!=nil{return 0,nil,err} }
	if platform=="facebook" && len(kinds)>1 {for _,kind:=range kinds{if kind=="video"{return 0,nil,Bad("Facebook: допустим фотоальбом либо одно видео, без смешивания.")}}}
	if platform=="x" && len(kinds)>1 {for _,kind:=range kinds{if kind!="photo"&&kind!="image"{return 0,nil,Bad("X: допустимы до четырёх фотографий либо одно видео/GIF, без смешивания.")}}}
	if (platform == "youtube" || platform == "pinterest" || platform == "instagram") && kept == 0 {
		return 0, nil, Bad("%s: необходим поддерживаемый медиафайл.", n.Title)
	}
	if platform == "youtube" || platform == "pinterest" {
		title, _ := titleAndBody(text)
		if runeLen(title) > 100 {
			return 0, nil, Bad("%s: первая строка (заголовок) длиннее 100 символов.", n.Title)
		}
	}
	if kept == 0 && strings.TrimSpace(text) == "" {
		return 0, nil, Bad("%s: в посте нет ни подходящих файлов, ни текста.", n.Title)
	}
	return kept, skipped, nil
}

func networkFitMedia(n *Network, media []Prepared) ([]Prepared, []string) {
	var kept []Prepared
	skipped := []string{}
	for _, f := range media {
		st, err := os.Stat(f.Path)
		var size int64
		if err == nil {
			size = st.Size()
		}
		switch {
		case err != nil || !st.Mode().IsRegular() || size <= 0:
			skipped = append(skipped, kindName(f.Kind)+": файл отсутствует или пуст")
		case !contains(n.Kinds, f.Kind):
			skipped = append(skipped, kindName(f.Kind)+": "+n.Title+" такой вид не принимает")
		case n.MaxBytes[f.Kind] > 0 && size > n.MaxBytes[f.Kind]:
			skipped = append(skipped, kindName(f.Kind)+": больше лимита "+n.Title)
		case len(kept) >= n.MediaMax:
			skipped = append(skipped, fmt.Sprintf("%s: больше %d файлов для %s", kindName(f.Kind), n.MediaMax, n.Title))
		default:
			kept = append(kept, f)
		}
	}
	return kept, skipped
}

// NetworkDeliveryPreflight rejects an incompatible target variant instead of
// silently deleting attachments. The user may explicitly edit the variant.
func (a *App) NetworkDeliveryPreflight(platform, text string, kinds []string) error {
	_, skipped, err := a.NetworkPreflight(platform, text, kinds)
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		return Bad("Не все вложения поддерживаются: %s. Измените состав поста для этой площадки.", strings.Join(skipped, "; "))
	}
	return nil
}

func (a *App) NetworkPreparedPreflight(platform, text string, media []Prepared) error {
	if _, err := a.NetworkFor(platform); err != nil {
		return err
	}
	var kinds []string
	for _, file := range media {
		kinds = append(kinds, file.Kind)
	}
	if err := a.NetworkDeliveryPreflight(platform, text, kinds); err != nil {
		return err
	}
	if platform=="tumblr" {
		for _,f:=range media{if f.Kind=="video" && (f.Duration<=0 || f.Duration>600){return Bad("Tumblr: длительность видео должна быть известна и не превышать 10 минут.")}}
	}
	_, skipped := networkFitMedia(a.Networks()[platform], media)
	if len(skipped) > 0 {
		return Bad("Не все вложения можно отправить: %s.", strings.Join(skipped, "; "))
	}
	return nil
}

// publishingInScope is the SmmBox product scope, independent of adapter readiness.
func publishingInScope(key string) bool {
	switch key {
	case "telegram", "vk", "ok", "max", "instagram", "facebook", "youtube", "tiktok", "rutube", "threads", "x", "pinterest", "tumblr", "dzen":
		return true
	default:
		return false
	}
}

// PublishingConnectionAvailable controls new connections, not existing deliveries.
// Keep inactive adapters in Networks for existing resources and future integration work.
func (n *Network) PublishingConnectionAvailable() bool {
	if n == nil || n.Publish == nil || (n.Status != "ready" && n.Status != "beta") {
		return false
	}
	return publishingInScope(n.Key) && (n.Key == "telegram" || n.Connect != nil || n.OAuth != nil)
}

// PublishingConnectionAllowed is the connection capability for a particular user.
func (n *Network) PublishingConnectionAllowed(owner bool) bool {
	return n.PublishingConnectionAvailable() && (n.Status != "beta" || owner)
}

// NetworksPublicFor — реестр с учётом прав пользователя.
func (a *App) NetworksPublicFor(owner bool) jx.M {
	out := a.NetworksPublic()
	for k,n := range a.Networks() {
		if entry,ok:=out[k].(jx.M);ok {
			entry["connect_available"]=n.PublishingConnectionAllowed(owner)
		}
	}
	return out
}

// NetworksPublic — реестр для интерфейса.
func (a *App) NetworksPublic() jx.M {
	out := jx.M{}
	for k, n := range a.Networks() {
		out[k] = jx.M{"title": n.Title, "prefix": n.Prefix, "status": n.Status, "note": n.Note, "text_limit": n.TextLimit, "media_max": n.MediaMax,
			"kinds": orStrs(n.Kinds), "max_bytes": n.MaxBytes, "poll": n.Poll, "poll_limits": networkPollLimits(k), "extras":postExtraKinds(k), "fields": n.Fields, "docs": n.Docs, "oauth": n.OAuth != nil,
			"connect_available": n.PublishingConnectionAvailable(), "publishing_in_scope": publishingInScope(k)}
	}
	return out
}

// ---- Отправка поста очереди ----

func preparedFromPost(post jx.M) []Prepared {
	var out []Prepared
	for _, f := range jx.Maps(post, "media") {
		out = append(out, Prepared{Kind: jx.Str(f, "kind"), Path: jx.Str(f, "path"), Name: jx.Str(f, "name"), Src: jx.Str(f, "src"),
			Width: int(jx.Int(f, "width")), Height: int(jx.Int(f, "height")), Duration: int(jx.Int(f, "duration"))})
	}
	return out
}

// OutboxSendPost — отправка поста очереди в соцсеть канала.
func (a *App) OutboxSendPost(ctx context.Context, uid string, post jx.M, c *Channel) (*SentResult, error) {
	n, err := a.NetworkFor(c.Platform)
	if err != nil {
		return nil, err
	}
	media := preparedFromPost(post)
	text := jx.Str(post, "text")
	var poll jx.M
	if raw := jx.Map(post, "poll"); raw != nil {
		if len(media) != 0 {
			return nil, fmt.Errorf("Опрос не может содержать вложения: %w", ErrNetworkRejected)
		}
		poll, err = NetworkPollCheck(c.Platform, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", err, ErrNetworkRejected)
		}
		text = jx.Str(poll, "question")
	}
	extras, err := PublishExtrasCheck(c.Platform,text,PreparedKinds(media),poll,jx.Map(post,"extras"))
	if err!=nil{return nil,fmt.Errorf("%s: %w",err,ErrNetworkRejected)}
	if c.Platform == "telegram" {
		if poll != nil {
			return a.TelegramPublishPoll(ctx, c, poll)
		}
		return a.TelegramPublishPrepared(ctx, c, text, media, nil, extras)
	}
	if !PostExtrasStandalone(extras){if err := a.NetworkPreparedPreflight(c.Platform, text, media); err != nil {
		return nil, fmt.Errorf("%s: %w", err, ErrNetworkRejected)
	}}
	if err := a.networkRefreshChannel(ctx, n, c); err != nil {
		return nil, err
	}
	kept, skipped := networkFitMedia(n, media)
	if len(skipped) > 0 {
		return nil, fmt.Errorf("%s: публикация остановлена, чтобы не потерять вложения: %s: %w", n.Title, strings.Join(skipped, "; "), ErrNetworkRejected)
	}
	if len(kept) == 0 && strings.TrimSpace(text) == "" && !PostExtrasStandalone(extras) {
		return nil, fmt.Errorf("%s: в посте нет подходящих файлов и текста: %w", n.Title, ErrNetworkRejected)
	}
	sent, err := n.Publish(a, ctx, c, NetPost{Text: text, Media: kept, Poll: poll, Extras:extras})
	if err != nil {
		return nil, err
	}
	if err := validateNetworkReceipt(sent); err != nil {
		return nil, err
	}
	return &SentResult{URL: sent.URL, ExternalIDs: sent.MessageIDs, Skipped: skipped, MediaKept: orPrepared(kept)}, nil
}

// A successful HTTP status is not proof that a post was created.
func validateNetworkReceipt(sent *NetSent) error {
	if sent == nil || len(sent.MessageIDs) == 0 {
		return uncertainPublish("Соцсеть не вернула идентификатор публикации.")
	}
	for _, id := range sent.MessageIDs {
		id = strings.TrimSpace(id)
		if id == "" || id == "0" || id == "null" {
			return uncertainPublish("Соцсеть вернула недействительный идентификатор публикации.")
		}
	}
	return nil
}

func orPrepared(p []Prepared) []Prepared {
	if p == nil {
		return []Prepared{}
	}
	return p
}

func telegramNetworkPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	var s *SentResult
	var err error
	if p.Poll != nil {
		s, err = a.TelegramPublishPoll(ctx, c, p.Poll)
	} else {
		s, err = a.TelegramPublishPrepared(ctx, c, p.Text, p.Media, nil,p.Extras)
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, id := range s.MessageIDs {
		ids = append(ids, strconv.Itoa(id))
	}
	return &NetSent{URL: s.URL, MessageIDs: ids}, nil
}

// ---- Общий HTTP: только https и публичные адреса; секреты не попадают в ошибки ----

type netResp struct {
	Status  int
	Data    jx.M
	Raw     []byte
	Headers http.Header
}

var networkRejectStatuses = []int{400, 401, 403, 404, 410, 422}

// Resolve once and pin the connection to that validated answer. API requests
// carry credentials and must never follow redirects or environment proxies.
func networkResolve(raw string, lookup func(string) ([]net.IP, error)) (*url.URL, []string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, nil, Bad("Нужен публичный HTTPS-адрес API.")
	}
	return publicResolveAddresses(raw, lookup)
}

type networkRequestFunc func(context.Context, string, string, map[string]string, io.Reader) (*netResp, error)

func networkHTTP(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader) (*netResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	u, addresses, err := networkResolve(rawURL, func(host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip4", host)
	})
	if err != nil {
		return nil, fmt.Errorf("Адрес API отклонён или не разрешается: %w", ErrNetworkRejected)
	}
	dialer := &net.Dialer{Timeout: 8*time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialPublicAddresses(ctx, addresses, dialer.DialContext)
		},
		TLSHandshakeTimeout: 8*time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {return http.ErrUseLastResponse}}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, Bad("Неверный адрес запроса.")
	}
	if file, ok := body.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			req.ContentLength = info.Size()
		}
	}
	req.Header.Set("User-Agent", HTTPUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		if method != http.MethodGet && method != http.MethodHead {
			return nil, uncertainPublish("Связь с сервисом прервалась.")
		}
		return nil, Bad("Нет связи с сервисом.")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(raw)>8<<20 {
		if method != http.MethodGet && method != http.MethodHead {
			return nil, uncertainPublish("Ответ сервиса получен не полностью.")
		}
		return nil, Bad("Ответ сервиса получен не полностью.")
	}
	return &netResp{Status: resp.StatusCode, Data: jx.Unmarshal(raw), Raw: raw, Headers: resp.Header}, nil
}

func jsonBody(v any) io.Reader { return bytes.NewReader(jx.Marshal(v)) }

func formBody(v url.Values) io.Reader { return strings.NewReader(v.Encode()) }

const (
	ctJSON = "application/json"
	ctForm = "application/x-www-form-urlencoded"
)

// networkExpect — данные при ожидаемом коде, иначе ошибка с текстом сервиса; отказ, который повтор не исправит, — ErrNetworkRejected.
func networkExpect(r *netResp, err error, statuses []int, what string) (jx.M, error) {
	if err != nil {
		return nil, err
	}
	if r == nil { return nil, fmt.Errorf("%s: пустой ответ API",what) }
	if contains(statuses, r.Status) {
		return orEmpty(r.Data), nil
	}
	d := orEmpty(r.Data)
	msg := ""
	for _, k := range []string{"error_description", "error", "error_msg", "message", "detail"} {
		if v, ok := d[k]; ok {
			if m := jx.ToMap(v); m != nil {
				msg = jx.Str(m, "message")
				if msg == "" {
					msg = jx.Str(m, "error_msg")
				}
			} else {
				msg = jx.ToStr(v)
			}
			if msg != "" {
				break
			}
		}
	}
	text := fmt.Sprintf("%s: код %d.", what, r.Status)
	if msg != "" {
		text = what + ": " + trimRunes(msg, 200) + "."
	}
	if r.Status >= 500 || r.Status == 408 {
		return nil, uncertainPublish(fmt.Sprintf("%s: HTTP %d.", what, r.Status))
	}
	if contains(networkRejectStatuses, r.Status) {
		return nil, fmt.Errorf("%s%w", text, errRejectedSilent)
	}
	return nil, errors.New(text)
}

// errRejectedSilent — ErrNetworkRejected без добавки к тексту ошибки.
var errRejectedSilent = &silentErr{ErrNetworkRejected}

type silentErr struct{ e error }

func (s *silentErr) Error() string { return "" }
func (s *silentErr) Unwrap() error { return s.e }

func multipartBody(fields map[string]string, files map[string]string) (io.Reader, string, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		mw.WriteField(k, v)
	}
	names := make([]string, 0, len(files))
	for k := range files {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, name := range names {
		f, err := os.Open(files[name])
		if err != nil {
			return nil, "", Bad("Файл поста не найден.")
		}
		part, _ := mw.CreateFormFile(name, filepath.Base(files[name]))
		io.Copy(part, f)
		f.Close()
	}
	mw.Close()
	return &buf, mw.FormDataContentType(), nil
}

// ---- OAuth ----

const (
	networkStateSeconds  = 900
	networkRefreshMargin = 120
)

func (a *App) NetworkRedirectURI() string {
	base := a.Cfg.PublicURL
	if base == "" {
		base = "http://localhost"
	}
	return base + "/index.php?action=network_oauth_callback"
}

func (a *App) NetworkOAuthStateCreate(ctx context.Context, uid, platform, verifier string) (string,error) {
	state := randHex(16)
	if uid==""{return "",Bad("Требуется вход.")}
	if err:=a.KVSet(ctx, "netoauth:"+state, jx.M{"uid": uid, "platform": platform, "verifier": verifier, "at": now()}, networkStateSeconds*time.Second);err!=nil{return "",err}
	return state,nil
}

var stateRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

// A callback may consume its state only once, and only in the profile
// which initiated authorization. DELETE RETURNING is atomic across processes.
func (a *App) NetworkOAuthStateTake(ctx context.Context, state, uid string) jx.M {
	if !stateRe.MatchString(state)||uid==""{return nil}
	var raw []byte
	err:=a.DB.Pool.QueryRow(ctx,`DELETE FROM kv WHERE key=$1 AND expires_at>$2 AND value->>'uid'=$3 RETURNING value`,"netoauth:"+state,now(),uid).Scan(&raw)
	if err!=nil{return nil}
	d:=jx.Unmarshal(raw)
	if d==nil||now()-jx.Int(d,"at")>networkStateSeconds{return nil}
	return d
}

func PKCEVerifier() string {
	b := make([]byte, 48)
	rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

func expiresAt(t jx.M) string {
	in := jx.Int(t, "expires_in")
	if in == 0 {
		in = 3600
	}
	return strconv.FormatInt(now()+in, 10)
}

// networkRefreshChannel — токен, который скоро истечёт, обновляется до отправки и сохраняется в ресурс.
func (a *App) networkRefreshChannel(ctx context.Context, n *Network, c *Channel) error {
	if n.OAuth == nil || n.OAuth.Refresh == nil || (!n.OAuth.RefreshWithAccessToken && c.Secrets["refresh_token"] == "") {
		return nil
	}
	exp, _ := strconv.ParseInt(c.Secrets["expires_at"], 10, 64)
	margin:=int64(networkRefreshMargin)
	if n.OAuth.RefreshWithAccessToken { margin=7*24*60*60; if exp<=now(){return fmt.Errorf("Срок действия токена истёк; подключите аккаунт заново: %w",ErrNetworkRejected)} }
	if exp > now()+margin {
		return nil
	}
	fresh, err := n.OAuth.Refresh(a, ctx, c.Secrets)
	if err != nil {
		return err
	}
	c.Secrets = fresh
	_, err = a.User(ctx, c.UserID, nil).ChannelSave(c)
	return err
}

func (a *App) tokenRequest(ctx context.Context, tokenURL string, form url.Values, basicID, basicSecret, what string, extra map[string]string) (map[string]string, error) {
	h := map[string]string{"Content-Type": ctForm}
	if basicID != "" {
		h["Authorization"] = "Basic " + base64.StdEncoding.EncodeToString([]byte(a.NetworkAppKey(basicID)+":"+a.NetworkAppKey(basicSecret)))
	}
	for k, v := range extra {
		h[k] = v
	}
	r, err := networkHTTP(ctx, "POST", tokenURL, h, formBody(form))
	t, err := networkExpect(r, err, []int{200}, what)
	if err != nil {
		return nil, err
	}
	if jx.Str(t,"error")!=""||strings.TrimSpace(jx.Str(t,"access_token"))==""{
		return nil,Bad("%s: сервис не выдал токен доступа.",what)
	}
	return map[string]string{"access_token": jx.Str(t, "access_token"), "refresh_token": jx.Str(t, "refresh_token"), "expires_at": expiresAt(t)}, nil
}

func keepRefresh(old, fresh map[string]string) map[string]string {
	if fresh["refresh_token"] == "" {
		fresh["refresh_token"] = old["refresh_token"]
	}
	return fresh
}

func bearer(t string) map[string]string { return map[string]string{"Authorization": "Bearer " + t} }

func withCT(h map[string]string, ct string) map[string]string {
	out := map[string]string{"Content-Type": ct}
	for k, v := range h {
		out[k] = v
	}
	return out
}

func titleAndBody(text string) (string, string) {
	t, b, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(t), strings.TrimSpace(b)
}

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
	return &NetResource{Ref: chat, Title: title, Secrets: map[string]string{"token": token}, Meta: jx.M{}}, nil
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
		body, ct, err := multipartBody(nil, map[string]string{"file": f.Path})
		if err != nil {
			return nil, err
		}
		r, err := networkHTTP(ctx, "POST", inst+"/api/v2/media", withCT(auth, ct), body)
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
	body, ct, err := multipartBody(map[string]string{"payload_json": string(jx.Marshal(jx.M{"content": p.Text}))}, files)
	if err != nil {
		return nil, err
	}
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

// ---- YouTube Shorts ----

var youtubeOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://accounts.google.com/o/oauth2/v2/auth?" + url.Values{"client_id": {a.NetworkAppKey("YOUTUBE_CLIENT_ID")}, "redirect_uri": {redirect},
			"response_type": {"code"}, "scope": {"https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly"},
			"access_type": {"offline"}, "prompt": {"consent"}, "state": {state}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://oauth2.googleapis.com/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"client_id": {a.NetworkAppKey("YOUTUBE_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("YOUTUBE_CLIENT_SECRET")}}, "", "", "Google не выдал токен", nil)
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		f, err := a.tokenRequest(ctx, "https://oauth2.googleapis.com/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]},
			"client_id": {a.NetworkAppKey("YOUTUBE_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("YOUTUBE_CLIENT_SECRET")}}, "", "", "Google не выдал токен", nil)
		if err != nil {
			return nil, err
		}
		return keepRefresh(s, f), nil
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://www.googleapis.com/youtube/v3/channels?part=snippet&mine=true", bearer(s["access_token"]), nil)
		d, err := networkExpect(r, err, []int{200}, "Канал YouTube не получен")
		if err != nil {
			return nil, err
		}
		items := jx.Maps(d, "items")
		if len(items) == 0 {
			return nil, Bad("У аккаунта нет канала YouTube.")
		}
		title := jx.Str(jx.Map(items[0], "snippet"), "title")
		if title == "" {
			title = "YouTube"
		}
		return &NetResource{Ref: jx.Str(items[0], "id"), Title: title, Meta: jx.M{"privacy": "private"}}, nil
	},
}

func youtubePublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	return youtubePublishWith(ctx,c,p,networkHTTP,waitNetworkContext)
}

// ---- Pinterest ----

var pinterestOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.pinterest.com/oauth/?" + url.Values{"client_id": {a.NetworkAppKey("PINTEREST_CLIENT_ID")}, "redirect_uri": {redirect},
			"response_type": {"code"}, "scope": {"user_accounts:read,boards:read,pins:read,pins:write"}, "state": {state}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://api.pinterest.com/v5/oauth/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"continuous_refresh": {"true"}}, "PINTEREST_CLIENT_ID", "PINTEREST_CLIENT_SECRET", "Pinterest не выдал токен", nil)
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		f, err := a.tokenRequest(ctx, "https://api.pinterest.com/v5/oauth/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]},
			"continuous_refresh": {"true"}}, "PINTEREST_CLIENT_ID", "PINTEREST_CLIENT_SECRET", "Pinterest не выдал токен", nil)
		if err != nil {
			return nil, err
		}
		return keepRefresh(s, f), nil
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://api.pinterest.com/v5/user_account", bearer(s["access_token"]), nil)
		me, err := networkExpect(r, err, []int{200}, "Аккаунт Pinterest не получен")
		if err != nil {
			return nil, err
		}
		r, err = networkHTTP(ctx, "GET", "https://api.pinterest.com/v5/boards", bearer(s["access_token"]), nil)
		boards, err := networkExpect(r, err, []int{200}, "Доски Pinterest не получены")
		if err != nil {
			return nil, err
		}
		items := jx.Maps(boards, "items")
		if len(items) == 0 {
			return nil, Bad("У аккаунта Pinterest нет досок: создайте доску и подключите снова.")
		}
		return &NetResource{Ref: jx.Str(me, "username"), Title: "@" + jx.Str(me, "username"), Meta: jx.M{"board_id": jx.Str(items[0], "id")}}, nil
	},
}

func pinterestPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	cover:=func(ctx context.Context,path string)[]byte{
		board:=a.videoStoryboardContext(ctx,path,1)
		if board==nil||len(board.Frames)==0{return nil}
		return board.Frames[0]
	}
	return pinterestPublishWith(ctx,c,p,networkHTTP,cover,waitNetworkContext)
}

// ---- Tumblr ----

var tumblrOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.tumblr.com/oauth2/authorize?" + url.Values{"client_id": {a.NetworkAppKey("TUMBLR_CLIENT_ID")}, "response_type": {"code"},
			"scope": {"basic write offline_access"}, "state": {state}, "redirect_uri": {redirect}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://api.tumblr.com/v2/oauth2/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"client_id": {a.NetworkAppKey("TUMBLR_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("TUMBLR_CLIENT_SECRET")}}, "", "", "Tumblr не выдал токен", nil)
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		f, err := a.tokenRequest(ctx, "https://api.tumblr.com/v2/oauth2/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]},
			"client_id": {a.NetworkAppKey("TUMBLR_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("TUMBLR_CLIENT_SECRET")}}, "", "", "Tumblr не выдал токен", nil)
		if err != nil {
			return nil, err
		}
		return keepRefresh(s, f), nil
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://api.tumblr.com/v2/user/info", bearer(s["access_token"]), nil)
		d, err := networkExpect(r, err, []int{200}, "Блоги Tumblr не получены")
		if err != nil {
			return nil, err
		}
		blogs := jx.Maps(jx.Map(jx.Map(d, "response"), "user"), "blogs")
		var blog jx.M
		for _, b := range blogs {
			if jx.Bool(b, "primary") {
				blog = b
			}
		}
		if blog == nil && len(blogs) > 0 {
			blog = blogs[0]
		}
		if blog == nil {
			return nil, Bad("У аккаунта Tumblr нет блогов.")
		}
		title := jx.Str(blog, "title")
		if title == "" {
			title = jx.Str(blog, "name")
		}
		return &NetResource{Ref: jx.Str(blog, "name"), Title: title, Meta: jx.M{"blog": jx.Str(blog, "name") + ".tumblr.com"}}, nil
	},
}

func tumblrPublish(a *App,ctx context.Context,c *Channel,p NetPost)(*NetSent,error){
	return tumblrPublishWith(ctx,c,p,networkHTTP)
}

// ---- LinkedIn ----

var linkedinOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.linkedin.com/oauth/v2/authorization?" + url.Values{"response_type": {"code"}, "client_id": {a.NetworkAppKey("LINKEDIN_CLIENT_ID")},
			"redirect_uri": {redirect}, "state": {state}, "scope": {"openid profile w_member_social"}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		t, err := a.tokenRequest(ctx, "https://www.linkedin.com/oauth/v2/accessToken", url.Values{"grant_type": {"authorization_code"}, "code": {code},
			"client_id": {a.NetworkAppKey("LINKEDIN_CLIENT_ID")}, "client_secret": {a.NetworkAppKey("LINKEDIN_CLIENT_SECRET")}, "redirect_uri": {redirect}}, "", "", "LinkedIn не выдал токен", nil)
		if t != nil {
			delete(t, "refresh_token")
		}
		return t, err
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://api.linkedin.com/v2/userinfo", bearer(s["access_token"]), nil)
		me, err := networkExpect(r, err, []int{200}, "Аккаунт LinkedIn не получен")
		if err != nil {
			return nil, err
		}
		if jx.Str(me, "sub") == "" {
			return nil, Bad("Аккаунт LinkedIn не определён.")
		}
		title := jx.Str(me, "name")
		if title == "" {
			title = "LinkedIn"
		}
		return &NetResource{Ref: jx.Str(me, "sub"), Title: title, Meta: jx.M{}}, nil
	},
}

func linkedinPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	if strings.TrimSpace(p.Text) == "" {
		return nil, Bad("LinkedIn: пост без текста.")
	}
	body := jx.M{"author": "urn:li:person:" + c.Ref, "commentary": p.Text, "visibility": "PUBLIC",
		"distribution":   jx.M{"feedDistribution": "MAIN_FEED", "targetEntities": []any{}, "thirdPartyDistributionChannels": []any{}},
		"lifecycleState": "PUBLISHED", "isReshareDisabledByAuthor": false}
	h := withCT(bearer(c.Secrets["access_token"]), ctJSON)
	h["Linkedin-Version"], h["X-Restli-Protocol-Version"] = "202511", "2.0.0"
	r, err := networkHTTP(ctx, "POST", "https://api.linkedin.com/rest/posts", h, jsonBody(body))
	if _, err := networkExpect(r, err, []int{201}, "Пост не опубликован"); err != nil {
		return nil, err
	}
	urn := r.Headers.Get("X-Restli-Id")
	u := ""
	if urn != "" {
		u = "https://www.linkedin.com/feed/update/" + urn
	}
	return &NetSent{URL: u, MessageIDs: []string{urn}}, nil
}

// ---- X ----

var xOAuth = &OAuthFlow{
	PKCE: true,
	Authorize: func(a *App, state, redirect, verifier string) string {
		sum := sha256.Sum256([]byte(verifier))
		return "https://x.com/i/oauth2/authorize?" + url.Values{"response_type": {"code"}, "client_id": {a.NetworkAppKey("X_CLIENT_ID")}, "redirect_uri": {redirect},
			"scope": {"tweet.write users.read tweet.read media.write offline.access"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
			"code_challenge_method": {"S256"}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, verifier string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://api.x.com/2/oauth2/token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect},
			"code_verifier": {verifier}, "client_id": {a.NetworkAppKey("X_CLIENT_ID")}}, "X_CLIENT_ID", "X_CLIENT_SECRET", "X не выдал токен", nil)
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		f, err := a.tokenRequest(ctx, "https://api.x.com/2/oauth2/token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]},
			"client_id": {a.NetworkAppKey("X_CLIENT_ID")}}, "X_CLIENT_ID", "X_CLIENT_SECRET", "X не выдал токен", nil)
		if err != nil {
			return nil, err
		}
		return keepRefresh(s, f), nil
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://api.x.com/2/users/me", bearer(s["access_token"]), nil)
		me, err := networkExpect(r, err, []int{200}, "Аккаунт X не получен")
		if err != nil {
			return nil, err
		}
		user := jx.Map(me, "data")
		if jx.Str(user, "id") == "" {
			return nil, Bad("Аккаунт X не определён.")
		}
		return &NetResource{Ref: jx.Str(user, "id"), Title: "@" + jx.Str(user, "username"), Meta: jx.M{"username": jx.Str(user, "username")}}, nil
	},
}

func xPublish(a *App,ctx context.Context,c *Channel,p NetPost)(*NetSent,error){
 return xPublishWith(ctx,c,p,networkHTTP,func(ctx context.Context,d time.Duration)error{
  t:=time.NewTimer(d);defer t.Stop()
  select{case <-ctx.Done():return ctx.Err();case <-t.C:return nil}
 })
}

// ---- Reddit ----

const redditUA = "web:posta:1.0 (автопостинг)"

var redditOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.reddit.com/api/v1/authorize?" + url.Values{"client_id": {a.NetworkAppKey("REDDIT_CLIENT_ID")}, "response_type": {"code"}, "state": {state},
			"redirect_uri": {redirect}, "duration": {"permanent"}, "scope": {"identity submit"}}.Encode()
	},
	Exchange: func(a *App, ctx context.Context, code, redirect, _ string) (map[string]string, error) {
		return a.tokenRequest(ctx, "https://www.reddit.com/api/v1/access_token", url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}},
			"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "Reddit не выдал токен", map[string]string{"User-Agent": redditUA})
	},
	Refresh: func(a *App, ctx context.Context, s map[string]string) (map[string]string, error) {
		f, err := a.tokenRequest(ctx, "https://www.reddit.com/api/v1/access_token", url.Values{"grant_type": {"refresh_token"}, "refresh_token": {s["refresh_token"]}},
			"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "Reddit не выдал токен", map[string]string{"User-Agent": redditUA})
		if err != nil {
			return nil, err
		}
		return keepRefresh(s, f), nil
	},
	Identify: func(a *App, ctx context.Context, s map[string]string) (*NetResource, error) {
		r, err := networkHTTP(ctx, "GET", "https://oauth.reddit.com/api/v1/me", map[string]string{"Authorization": "bearer " + s["access_token"], "User-Agent": redditUA}, nil)
		me, err := networkExpect(r, err, []int{200}, "Аккаунт Reddit не получен")
		if err != nil {
			return nil, err
		}
		name := jx.Str(me, "name")
		if name == "" {
			return nil, Bad("Аккаунт Reddit не определён.")
		}
		return &NetResource{Ref: name, Title: "u/" + name, Meta: jx.M{"subreddit": "u_" + strings.ToLower(name)}}, nil
	},
}

func redditPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return nil, Bad("Reddit: пост без текста.")
	}
	title, body := titleAndBody(text)
	form := url.Values{"api_type": {"json"}, "kind": {"self"}, "sr": {jx.Str(c.Meta, "subreddit")}, "title": {trimRunes(title, 300)}, "text": {body}}
	r, err := networkHTTP(ctx, "POST", "https://oauth.reddit.com/api/submit", map[string]string{"Authorization": "bearer " + c.Secrets["access_token"], "User-Agent": redditUA,
		"Content-Type": ctForm}, formBody(form))
	d, err := networkExpect(r, err, []int{200}, "Пост не опубликован")
	if err != nil {
		return nil, err
	}
	j := jx.Map(d, "json")
	if errs := jx.List(j, "errors"); len(errs) > 0 {
		raw, _ := json.Marshal(errs)
		return nil, fmt.Errorf("Reddit: %s.", trimRunes(string(raw), 200))
	}
	data := jx.Map(j, "data")
	return &NetSent{URL: jx.Str(data, "url"), MessageIDs: []string{jx.Str(data, "name")}}, nil
}

// ---- Ресурсы для интерфейса ----

func (u *U) NetworksChannelsPublic() []jx.M {
	out := []jx.M{}
	for _, c := range u.Channels() {
		if c.Platform != "telegram" {
			out = append(out, jx.M{"id": c.ID, "platform": c.Platform, "title": c.ChatTitle, "ref": c.Ref, "screen_name": jx.Str(c.Meta, "screen_name"), "connected_at": c.ConnectedAt, "schedule": ChannelSchedule(c), "style": u.ChannelStylePublic(c), "hashtags": c.Hashtags, "poll_available":u.ChannelPollAvailable(c)})
		}
	}
	return out
}

// NetworkSaveResource — дубль (платформа, ref) и лимит тарифа проверяются здесь.
func (u *U) NetworkSaveResource(platform string, n *Network, res *NetResource) error {
	list := u.Channels()
	for _, c := range list {
		if c.Platform == platform && c.Ref == res.Ref {
			return Conflict("%s: этот ресурс уже подключён.", n.Title)
		}
	}
	if u.TBool("billing_enforce") {
		l := u.BillingLimits()
		if len(list) >= int(jx.Int(l, "channels")) {
			return Conflict("На тарифе «%s» можно подключить ресурсов: %d. Больше — в «Тариф и баланс».", jx.Str(l, "title"), jx.Int(l, "channels"))
		}
	}
	title := res.Title
	if title == "" {
		title = n.Title
	}
	_, err := u.ChannelSave(&Channel{Platform: platform, Ref: res.Ref, Secrets: res.Secrets, Meta: res.Meta, ChatTitle: title, Position: -1})
	return err
}
