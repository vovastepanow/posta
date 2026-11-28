package posta

import (
	"context"
	"fmt"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"

	"posta/internal/jx"
)

// Автопостинг: реестр площадок, проверки и отправка очереди. Статусы: ready — публикует; beta — публикует,
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
	Warning string
	CommentID string
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

var (
	networkCatalogOnce sync.Once
	networkCatalog map[string]*Network
)

// networkTemplates builds immutable adapter definitions once. Configuration
// and mutable slices/maps are copied when a caller requests a network.
func networkTemplates() map[string]*Network {
	networkCatalogOnce.Do(func() { networkCatalog = newNetworkCatalog() })
	return networkCatalog
}

func newNetworkCatalog() map[string]*Network {
	na := func(title, note string) *Network { return &Network{Title: title, Status: "unavailable", Note: note} }
	r := map[string]*Network{
		"telegram": {Title: "Telegram", Prefix: "tg", Status: "ready", TextLimit: [2]int{1024, 4096}, MediaMax: 10, Kinds: []string{"photo", "video", "animation", "audio", "document", "voice","sticker","video_note"}, MaxBytes: map[string]int64{"audio": 50_000_000, "document": 50_000_000, "voice":50_000_000,"sticker":524288,"video_note":50_000_000}, Poll: true},
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
	}
	return r
}

func (a *App) network(platform string) *Network {
	template := networkTemplates()[platform]
	if template == nil {
		return nil
	}
	n := *template
	n.Kinds = slices.Clone(n.Kinds)
	n.MaxBytes = maps.Clone(n.MaxBytes)
	n.Fields = slices.Clone(n.Fields)
	n.AppKeys = slices.Clone(n.AppKeys)
	if n.OAuth != nil {
		flow := *n.OAuth
		n.OAuth = &flow
	}
	if len(n.AppKeys) > 0 {
		n.Status = "needs_keys"
		if a.networkKeysPresent(n.AppKeys) {
			n.Status = "beta"
		}
	}
	return &n
}

// Networks — независимые описания адаптеров с текущей конфигурацией.
func (a *App) Networks() map[string]*Network {
	out := make(map[string]*Network, len(networkTemplates()))
	for key := range networkTemplates() {
		out[key] = a.network(key)
	}
	return out
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
	n := a.network(platform)
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
	n := a.network(platform)
	if n == nil {
		return 0, nil, Bad("Неизвестная соцсеть: %s.", platform)
	}
	return networkPreflight(n, text, kinds)
}

func networkPreflight(n *Network, text string, kinds []string) (int, []string, error) {
	platform := n.Key
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
	n := a.network(platform)
	if n == nil {
		return Bad("Неизвестная соцсеть: %s.", platform)
	}
	return networkDeliveryPreflight(n, text, kinds)
}

func networkDeliveryPreflight(n *Network, text string, kinds []string) error {
	_, skipped, err := networkPreflight(n, text, kinds)
	if err != nil {
		return err
	}
	if len(skipped) > 0 {
		return Bad("Не все вложения поддерживаются: %s. Измените состав поста для этой площадки.", strings.Join(skipped, "; "))
	}
	return nil
}

func (a *App) NetworkPreparedPreflight(platform, text string, media []Prepared) error {
	n, err := a.NetworkFor(platform)
	if err != nil {
		return err
	}
	if err := networkDeliveryPreflight(n, text, PreparedKinds(media)); err != nil {
		return err
	}
	if platform=="tumblr" {
		for _,f:=range media{if f.Kind=="video" && (f.Duration<=0 || f.Duration>600){return Bad("Tumblr: длительность видео должна быть известна и не превышать 10 минут.")}}
	}
	_, skipped := networkFitMedia(n, media)
	if len(skipped) > 0 {
		return Bad("Не все вложения можно отправить: %s.", strings.Join(skipped, "; "))
	}
	return nil
}

// publishingInScope is the SmmBox product scope, independent of adapter readiness.
func publishingInScope(key string) bool {
	switch key {
	case "telegram", "vk", "ok", "max", "youtube", "x", "pinterest", "tumblr":
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
	return a.networksPublic(owner, true)
}

// PublishingInScope gates new publication requests, not historical records.
func PublishingInScope(platform string) bool { return publishingInScope(platform) }

// NetworksPublic — реестр для интерфейса.
func (a *App) NetworksPublic() jx.M {
	return a.networksPublic(false, false)
}

func (a *App) networksPublic(owner, scoped bool) jx.M {
	out := jx.M{}
	for k := range networkTemplates() {
		inScope := publishingInScope(k)
		if scoped && !inScope {
			continue
		}
		n := a.network(k)
		available := n.PublishingConnectionAvailable()
		if scoped {
			available = n.PublishingConnectionAllowed(owner)
		}
		out[k] = jx.M{"title": n.Title, "prefix": n.Prefix, "status": n.Status, "note": n.Note, "text_limit": n.TextLimit, "media_max": n.MediaMax,
			"kinds": orStrs(n.Kinds), "max_bytes": n.MaxBytes, "poll": n.Poll, "poll_limits": networkPollLimits(k), "extras":postExtraKinds(k), "fields": n.Fields, "docs": n.Docs, "oauth": n.OAuth != nil,
			"connect_available": available, "publishing_in_scope": inScope}
	}
	return out
}

// ---- Отправка поста очереди ----

func preparedFromPost(post jx.M) []Prepared {
	var out []Prepared
	for _, f := range jx.Maps(post, "media") {
		out = append(out, Prepared{Kind: jx.Str(f, "kind"), Path: jx.Str(f, "path"), Name: jx.Str(f, "name"), Src: jx.Str(f, "src"),
			Width: int(jx.Int(f, "width")), Height: int(jx.Int(f, "height")), Duration: int(jx.Int(f, "duration")),CoverPath:jx.Str(f,"cover_path")})
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
		poll, err = NetworkPollCheck(c.Platform, raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", err, ErrNetworkRejected)
		}
		text,err=PollContentText(c.Platform,text,len(media),poll)
		if err!=nil{return nil,fmt.Errorf("%s: %w",err,ErrNetworkRejected)}
	}
	extras, err := PublishExtrasForChannel(c,text,PreparedKinds(media),poll,jx.Map(post,"extras"))
	if err!=nil{return nil,fmt.Errorf("%s: %w",err,ErrNetworkRejected)}
 if err:=CheckVideoCovers(c.Platform,media,extras);err!=nil{return nil,fmt.Errorf("%s: %w",err,ErrNetworkRejected)}
	if c.Platform == "telegram" {
		if poll != nil {
			return a.TelegramPublishPoll(ctx, c, poll)
		}
		return a.TelegramPublishPrepared(ctx, c, text, media, nil, extras)
	}
	if !PostExtrasStandalone(extras){if err := a.NetworkPreparedPreflight(c.Platform, PollPreflightText(text,poll), media); err != nil {
		return nil, fmt.Errorf("%s: %w", err, ErrNetworkRejected)
	}}
	if err := a.networkRefreshChannel(ctx, n, c); err != nil {
		return nil, err
	}
	kept, skipped := networkFitMedia(n, media)
	if len(skipped) > 0 {
		return nil, fmt.Errorf("%s: публикация остановлена, чтобы не потерять вложения: %s: %w", n.Title, strings.Join(skipped, "; "), ErrNetworkRejected)
	}
	if len(kept) == 0 && strings.TrimSpace(text) == "" && poll==nil && !PostExtrasStandalone(extras) {
		return nil, fmt.Errorf("%s: в посте нет подходящих файлов и текста: %w", n.Title, ErrNetworkRejected)
	}
	sent, err := n.Publish(a, ctx, c, NetPost{Text: text, Media: kept, Poll: poll, Extras:extras})
	if err != nil {
		return nil, err
	}
	if err := validateNetworkReceipt(sent); err != nil {
		return nil, err
	}
	return &SentResult{URL: sent.URL, ExternalIDs: sent.MessageIDs, Skipped: skipped, MediaKept: orPrepared(kept),Warning:sent.Warning,CommentID:sent.CommentID}, nil
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

// ---- Ресурсы для интерфейса ----

func (u *U) NetworksChannelsPublic() []jx.M {
	return u.networkChannelsPublic(false)
}

// Inactive connections are available only for explicit removal, not as editor targets.
func (u *U) InactiveNetworksChannelsPublic() []jx.M {
	return u.networkChannelsPublic(true)
}

func (u *U) networkChannelsPublic(inactive bool) []jx.M {
	out := []jx.M{}
	for _, c := range u.Channels() {
		if c.Platform != "telegram" && publishingInScope(c.Platform) != inactive {
			out = append(out, jx.M{"id": c.ID, "platform": c.Platform, "title": c.ChatTitle, "ref": c.Ref, "screen_name": jx.Str(c.Meta, "screen_name"), "connected_at": c.ConnectedAt, "schedule": ChannelSchedule(c), "style": u.ChannelStylePublic(c), "hashtags": c.Hashtags, "poll_available":u.ChannelPollAvailable(c), "editor_extras":ChannelExtraKinds(c)})
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

func titleAndBody(text string) (string, string) {
	t, b, _ := strings.Cut(strings.TrimSpace(text), "\n")
	return strings.TrimSpace(t), strings.TrimSpace(b)
}
