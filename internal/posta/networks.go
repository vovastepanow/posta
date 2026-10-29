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
	"threads", "instagram", "facebook", "x", "reddit", "tiktok", "rutube", "vk_clips", "whatsapp", "viber", "likee", "yappy"}

// Networks — реестр соцсетей; ключи приложений берутся из posta.env.
func (a *App) Networks() map[string]*Network {
	keys := func(title, prefix, note string) *Network {
		return &Network{Title: title, Prefix: prefix, Status: "needs_keys", Note: note}
	}
	na := func(title, note string) *Network { return &Network{Title: title, Status: "unavailable", Note: note} }
	r := map[string]*Network{
		"telegram": {Title: "Telegram", Prefix: "tg", Status: "ready", TextLimit: [2]int{1024, 4096}, MediaMax: 10, Kinds: []string{"photo", "video", "animation"}, Poll: true},
		"vk": {Title: "ВКонтакте (группа)", Prefix: "vk", Status: "beta", Note: "Токен администратора с правами wall, photos, groups.", TextLimit: [2]int{16384, 16384},
			MediaMax: 10, Kinds: []string{"photo"}, MaxBytes: map[string]int64{"photo": 50_000_000}, Docs: "unverified",
			Fields: []NetField{{"token", "Токен администратора (wall, photos, groups, offline)", true}, {"group", "Адрес или id сообщества", false}}, Connect: vkConnect, Publish: vkPublish},
		"max": {Title: "MAX", Prefix: "max", Status: "beta", Note: "Бот MAX в чате, токен из «MAX для бизнеса»; пока только текст.", TextLimit: [2]int{4000, 4000}, Docs: "partly",
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
		"ok": {Title: "Одноклассники (группа)", Prefix: "ok", Status: "needs_keys", Note: "Ключ приложения OK и постоянный токен группы; только текст.", TextLimit: [2]int{4000, 4000},
			Docs: "partly", AppKeys: []string{"OK_APP_KEY"}, Connect: okConnect, Publish: okPublish,
			Fields: []NetField{{"access_token", "Постоянный токен приложения", true}, {"session_secret_key", "Секретный ключ сессии", true}, {"gid", "id группы", false}}},
		"youtube": {Title: "YouTube Shorts", Prefix: "yt", Status: "needs_keys", Note: "Проект Google и экран согласия; без аудита ролики приватные. Только видео.",
			TextLimit: [2]int{5000, 5000}, MediaMax: 1, Kinds: []string{"video"}, MaxBytes: map[string]int64{"video": 200_000_000}, Docs: "ok",
			AppKeys: []string{"YOUTUBE_CLIENT_ID", "YOUTUBE_CLIENT_SECRET"}, OAuth: youtubeOAuth, Publish: youtubePublish},
		"pinterest": {Title: "Pinterest", Prefix: "pin", Status: "needs_keys", Note: "Приложение Pinterest; пин всегда с картинкой.", TextLimit: [2]int{800, 800}, MediaMax: 1,
			Kinds: []string{"photo"}, MaxBytes: map[string]int64{"photo": 20_000_000}, Docs: "ok", AppKeys: []string{"PINTEREST_CLIENT_ID", "PINTEREST_CLIENT_SECRET"},
			OAuth: pinterestOAuth, Publish: pinterestPublish},
		"tumblr": {Title: "Tumblr", Prefix: "tumblr", Status: "needs_keys", Note: "Приложение Tumblr, OAuth 2; текстовые посты.", TextLimit: [2]int{4096, 4096}, Docs: "ok",
			AppKeys: []string{"TUMBLR_CLIENT_ID", "TUMBLR_CLIENT_SECRET"}, OAuth: tumblrOAuth, Publish: tumblrPublish},
		"linkedin": {Title: "LinkedIn (профиль)", Prefix: "li", Status: "needs_keys", Note: "Приложение LinkedIn с продуктом «Share on LinkedIn»; токен на 60 дней, без обновления.",
			TextLimit: [2]int{3000, 3000}, Docs: "partly", AppKeys: []string{"LINKEDIN_CLIENT_ID", "LINKEDIN_CLIENT_SECRET"}, OAuth: linkedinOAuth, Publish: linkedinPublish},
		"threads":   keys("Threads", "threads", "Приложение Meta; картинка по публичной ссылке."),
		"instagram": keys("Instagram", "ig", "Meta (Instagram, Facebook) признана экстремистской и запрещена в РФ. Приложение Meta, профессиональный аккаунт."),
		"facebook":  keys("Facebook (страница)", "fb", "Meta (Instagram, Facebook) признана экстремистской и запрещена в РФ. Приложение Meta."),
		"x": {Title: "X (Twitter)", Prefix: "x", Status: "needs_keys", Note: "Платный доступ к API: бесплатного тарифа нет, посты с URL дороже.", TextLimit: [2]int{280, 280},
			Docs: "partly", AppKeys: []string{"X_CLIENT_ID", "X_CLIENT_SECRET"}, OAuth: xOAuth, Publish: xPublish},
		"reddit": {Title: "Reddit", Prefix: "reddit", Status: "needs_keys", Note: "Приложение Reddit; постим только в свой профиль, текстом.", TextLimit: [2]int{40000, 40000},
			Docs: "partly", AppKeys: []string{"REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET"}, OAuth: redditOAuth, Publish: redditPublish},
		"tiktok":   na("TikTok", "Публикация доступна только после аудита приложения TikTok: без проверки видно «только мне»."),
		"rutube":   na("Rutube", "Открытого API публикации нет."),
		"vk_clips": na("ВК Клипы", "Отдельной ленты с открытым API публикации нет."),
		"whatsapp": na("WhatsApp-каналы", "Открытого API публикации в каналы нет."),
		"viber":    na("Viber", "Открытого API публикации нет."),
		"likee":    na("Likee", "Открытого API публикации нет."),
		"yappy":    na("Яппи", "Открытого API публикации нет."),
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
	kept := 0
	skipped := []string{}
	for _, k := range kinds {
		switch {
		case !contains(n.Kinds, k):
			skipped = append(skipped, kindName(k)+": "+n.Title+" такой вид не принимает")
		case kept >= max(n.MediaMax, 0):
			skipped = append(skipped, fmt.Sprintf("%s: больше %d файлов для %s", kindName(k), n.MediaMax, n.Title))
		default:
			kept++
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
		var size int64
		if st, err := os.Stat(f.Path); err == nil {
			size = st.Size()
		}
		switch {
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

// NetworksPublic — реестр для интерфейса.
func (a *App) NetworksPublic() jx.M {
	out := jx.M{}
	for k, n := range a.Networks() {
		out[k] = jx.M{"title": n.Title, "prefix": n.Prefix, "status": n.Status, "note": n.Note, "text_limit": n.TextLimit, "media_max": n.MediaMax,
			"kinds": orStrs(n.Kinds), "poll": n.Poll, "fields": n.Fields, "docs": n.Docs, "oauth": n.OAuth != nil}
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
	if c.Platform == "telegram" {
		if poll := jx.Map(post, "poll"); poll != nil {
			return a.TelegramPublishPoll(ctx, c, poll)
		}
		return a.TelegramPublishPrepared(ctx, c, text, media, nil)
	}
	if err := a.networkRefreshChannel(ctx, n, c); err != nil {
		return nil, err
	}
	kept, skipped := networkFitMedia(n, media)
	if len(kept) == 0 && strings.TrimSpace(text) == "" {
		return nil, fmt.Errorf("%s: в посте нет подходящих файлов и текста: %w", n.Title, ErrNetworkRejected)
	}
	sent, err := n.Publish(a, ctx, c, NetPost{Text: text, Media: kept})
	if err != nil {
		return nil, err
	}
	return &SentResult{URL: sent.URL, Skipped: skipped, MediaKept: orPrepared(kept)}, nil
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
		s, err = a.TelegramPublishPrepared(ctx, c, p.Text, p.Media, nil)
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

func networkURLUnsafe(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" {
		return "Нужен адрес https."
	}
	ips, err := net.LookupIP(u.Hostname())
	if err != nil || len(ips) == 0 {
		return "Адрес не найден."
	}
	for _, ip := range ips {
		if !ipPublic(ip) {
			return "Адрес указывает во внутреннюю сеть сервера."
		}
	}
	return ""
}

func ipPublic(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() || ip.IsMulticast() ||
		ip.Equal(net.IPv4bcast) || (ip.To4() != nil && ip.To4()[0] == 100 && ip.To4()[1]&0xc0 == 64))
}

var netClient = &http.Client{Timeout: 120 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func networkHTTP(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader) (*netResp, error) {
	if reason := networkURLUnsafe(rawURL); reason != "" {
		return nil, fmt.Errorf("Запрос отклонён: %s: %w", reason, ErrNetworkRejected)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, body)
	if err != nil {
		return nil, Bad("Неверный адрес запроса.")
	}
	req.Header.Set("User-Agent", HTTPUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := netClient.Do(req)
	if err != nil {
		return nil, Bad("Нет связи с сервисом.")
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
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

func (a *App) NetworkOAuthStateCreate(ctx context.Context, uid, platform, verifier string) string {
	state := randHex(16)
	a.KVSet(ctx, "netoauth:"+state, jx.M{"uid": uid, "platform": platform, "verifier": verifier, "at": now()}, networkStateSeconds*time.Second)
	return state
}

var stateRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

func (a *App) NetworkOAuthStateTake(ctx context.Context, state string) jx.M {
	if !stateRe.MatchString(state) {
		return nil
	}
	d := KV(a, ctx, "netoauth:"+state, jx.M(nil))
	a.KVDelete(ctx, "netoauth:"+state)
	if d == nil || now()-jx.Int(d, "at") > networkStateSeconds {
		return nil
	}
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
	if n.OAuth == nil || n.OAuth.Refresh == nil || c.Secrets["refresh_token"] == "" {
		return nil
	}
	exp, _ := strconv.ParseInt(c.Secrets["expires_at"], 10, 64)
	if exp > now()+networkRefreshMargin {
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
	if e := jx.Str(t, "error"); e != "" {
		return nil, fmt.Errorf("%s: %s.", what, e)
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
	if e := jx.Map(r.Data, "error"); e != nil {
		return nil, fmt.Errorf("ВКонтакте: %s.", trimRunes(jx.Str(e, "error_msg"), 200))
	}
	return r.Data["response"], nil
}

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
	return &NetResource{Ref: strconv.FormatInt(jx.Int(info, "id"), 10), Title: jx.Str(info, "name"), Secrets: map[string]string{"token": token},
		Meta: jx.M{"screen_name": jx.Str(info, "screen_name")}}, nil
}

func vkPhotoAttachment(ctx context.Context, groupID, path, token string) (string, error) {
	res, err := vkCall(ctx, "photos.getWallUploadServer", url.Values{"group_id": {groupID}}, token)
	if err != nil {
		return "", err
	}
	upload := jx.Str(jx.ToMap(res), "upload_url")
	if upload == "" {
		return "", errors.New("ВКонтакте не дал адрес для загрузки фото.")
	}
	body, ct, err := multipartBody(nil, map[string]string{"photo": path})
	if err != nil {
		return "", err
	}
	sent, err := networkHTTP(ctx, "POST", upload, map[string]string{"Content-Type": ct}, body)
	if err != nil {
		return "", err
	}
	f := orEmpty(sent.Data)
	saved, err := vkCall(ctx, "photos.saveWallPhoto", url.Values{"group_id": {groupID}, "photo": {jx.Str(f, "photo")}, "server": {jx.Str(f, "server")}, "hash": {jx.Str(f, "hash")}}, token)
	if err != nil {
		return "", err
	}
	l := jx.ToMaps(saved)
	if len(l) == 0 {
		return "", errors.New("Фото не сохранилось на стене.")
	}
	return fmt.Sprintf("photo%d_%d", jx.Int(l[0], "owner_id"), jx.Int(l[0], "id")), nil
}

func vkPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	if runeLen(p.Text) > 16384 {
		return nil, Bad("Текст длиннее 16384 символов: ВКонтакте его не примет.")
	}
	token := c.Secrets["token"]
	var att []string
	for _, f := range p.Media {
		s, err := vkPhotoAttachment(ctx, c.Ref, f.Path, token)
		if err != nil {
			return nil, err
		}
		att = append(att, s)
	}
	params := url.Values{"owner_id": {"-" + c.Ref}, "from_group": {"1"}, "message": {p.Text}}
	if len(att) > 0 {
		params.Set("attachments", strings.Join(att[:min(10, len(att))], ","))
	}
	res, err := vkCall(ctx, "wall.post", params, token)
	if err != nil {
		return nil, err
	}
	id := jx.Int(jx.ToMap(res), "post_id")
	u := ""
	if id > 0 {
		u = fmt.Sprintf("https://vk.com/wall-%s_%d", c.Ref, id)
	}
	return &NetSent{URL: u, MessageIDs: []string{strconv.FormatInt(id, 10)}}, nil
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
	if strings.TrimSpace(p.Text) == "" {
		return nil, Bad("MAX пока публикует только текст, а в посте текста нет.")
	}
	if runeLen(p.Text) > 4000 {
		return nil, Bad("Текст длиннее 4000 символов.")
	}
	r, err := networkHTTP(ctx, "POST", maxAPI+"/messages?chat_id="+url.QueryEscape(c.Ref), map[string]string{"Authorization": c.Secrets["token"], "Content-Type": ctJSON},
		jsonBody(jx.M{"text": p.Text}))
	d, err := networkExpect(r, err, []int{200}, "Сообщение не отправлено")
	if err != nil {
		return nil, err
	}
	m := jx.Map(d, "message")
	id := jx.Str(jx.Map(m, "body"), "mid")
	if id == "" {
		id = jx.Str(m, "id")
	}
	return &NetSent{MessageIDs: []string{id}}, nil
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
	return networkHTTP(ctx, "GET", "https://api.ok.ru/fb.do?"+q.Encode(), nil, nil)
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
	title := strings.TrimSpace(jx.Str(me, "name"))
	if title == "" {
		title = "Одноклассники"
	}
	return &NetResource{Ref: gid, Title: title, Secrets: map[string]string{"access_token": token, "session_secret_key": secret}, Meta: jx.M{"gid": gid}}, nil
}

func okPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	text := strings.TrimSpace(p.Text)
	if text == "" {
		return nil, Bad("Одноклассники: пост без текста.")
	}
	gid := jx.Str(c.Meta, "gid")
	att := string(jx.Marshal(jx.M{"media": []jx.M{{"type": "text", "text": text}}}))
	r, err := a.okRequest(ctx, "mediatopic.post", map[string]string{"type": "GROUP_THEME", "gid": gid, "attachment": att}, c.Secrets["access_token"], c.Secrets["session_secret_key"])
	if err != nil {
		return nil, err
	}
	if r.Status != 200 {
		return nil, fmt.Errorf("Пост не опубликован: код %d.", r.Status)
	}
	id := strings.Trim(string(r.Raw), "\" \n")
	u := ""
	if id != "" {
		u = "https://ok.ru/group/" + gid + "/topic/" + id
	}
	return &NetSent{URL: u, MessageIDs: []string{id}}, nil
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
	var video *Prepared
	for i := range p.Media {
		if p.Media[i].Kind == "video" {
			video = &p.Media[i]
			break
		}
	}
	if video == nil {
		return nil, Bad("YouTube публикует только видео.")
	}
	b, err := os.ReadFile(video.Path)
	if err != nil {
		return nil, Bad("Файл видео не найден.")
	}
	title, desc := titleAndBody(p.Text)
	if title == "" {
		title = "Shorts"
	}
	privacy := jx.Str(c.Meta, "privacy")
	if privacy == "" {
		privacy = "private"
	}
	auth := bearer(c.Secrets["access_token"])
	meta := jx.M{"snippet": jx.M{"title": trimRunes(title, 100), "description": desc}, "status": jx.M{"privacyStatus": privacy, "selfDeclaredMadeForKids": false}}
	h := withCT(auth, "application/json; charset=UTF-8")
	h["X-Upload-Content-Length"], h["X-Upload-Content-Type"] = strconv.Itoa(len(b)), "video/mp4"
	start, err := networkHTTP(ctx, "POST", "https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status", h, jsonBody(meta))
	if _, err := networkExpect(start, err, []int{200}, "Загрузка видео не начата"); err != nil {
		return nil, err
	}
	loc := start.Headers.Get("Location")
	if loc == "" {
		return nil, errors.New("YouTube не выдал адрес загрузки.")
	}
	r, err := networkHTTP(ctx, "PUT", loc, map[string]string{"Content-Type": "video/mp4"}, bytes.NewReader(b))
	done, err := networkExpect(r, err, []int{200, 201}, "Видео не загружено")
	if err != nil {
		return nil, err
	}
	id := jx.Str(done, "id")
	u := ""
	if id != "" {
		u = "https://youtube.com/shorts/" + id
	}
	return &NetSent{URL: u, MessageIDs: []string{id}}, nil
}

// ---- Pinterest ----

var pinterestOAuth = &OAuthFlow{
	Authorize: func(a *App, state, redirect, _ string) string {
		return "https://www.pinterest.com/oauth/?" + url.Values{"client_id": {a.NetworkAppKey("PINTEREST_CLIENT_ID")}, "redirect_uri": {redirect},
			"response_type": {"code"}, "scope": {"boards:read,pins:write"}, "state": {state}}.Encode()
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
	var photo *Prepared
	for i := range p.Media {
		if p.Media[i].Kind == "photo" {
			photo = &p.Media[i]
			break
		}
	}
	if photo == nil {
		return nil, Bad("Pinterest публикует пины только с картинкой.")
	}
	b, err := os.ReadFile(photo.Path)
	if err != nil {
		return nil, Bad("Файл картинки не найден.")
	}
	title, desc := titleAndBody(p.Text)
	if title == "" {
		title = "Пин"
	}
	body := jx.M{"board_id": jx.Str(c.Meta, "board_id"), "title": trimRunes(title, 100), "description": trimRunes(desc, 800),
		"media_source": jx.M{"source_type": "image_base64", "content_type": http.DetectContentType(b), "data": base64.StdEncoding.EncodeToString(b)}}
	r, err := networkHTTP(ctx, "POST", "https://api.pinterest.com/v5/pins", withCT(bearer(c.Secrets["access_token"]), ctJSON), jsonBody(body))
	d, err := networkExpect(r, err, []int{201}, "Пин не опубликован")
	if err != nil {
		return nil, err
	}
	id := jx.Str(d, "id")
	u := ""
	if id != "" {
		u = "https://www.pinterest.com/pin/" + id + "/"
	}
	return &NetSent{URL: u, MessageIDs: []string{id}}, nil
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

func tumblrPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	if strings.TrimSpace(p.Text) == "" {
		return nil, Bad("Tumblr: пост без текста.")
	}
	blog := jx.Str(c.Meta, "blog")
	r, err := networkHTTP(ctx, "POST", "https://api.tumblr.com/v2/blog/"+blog+"/posts", withCT(bearer(c.Secrets["access_token"]), ctJSON),
		jsonBody(jx.M{"content": []jx.M{{"type": "text", "text": p.Text}}, "state": "published"}))
	d, err := networkExpect(r, err, []int{201}, "Пост не опубликован")
	if err != nil {
		return nil, err
	}
	id := jx.Str(jx.Map(d, "response"), "id")
	u := ""
	if id != "" {
		u = "https://" + blog + "/post/" + id
	}
	return &NetSent{URL: u, MessageIDs: []string{id}}, nil
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
		"distribution": jx.M{"feedDistribution": "MAIN_FEED", "targetEntities": []any{}, "thirdPartyDistributionChannels": []any{}},
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
			"scope": {"tweet.write users.read tweet.read offline.access"}, "state": {state}, "code_challenge": {base64.RawURLEncoding.EncodeToString(sum[:])},
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

func xPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	if strings.TrimSpace(p.Text) == "" {
		return nil, Bad("X: пост без текста.")
	}
	if runeLen(p.Text) > 280 {
		return nil, Bad("Текст длиннее 280 символов: X его не примет.")
	}
	r, err := networkHTTP(ctx, "POST", "https://api.x.com/2/tweets", withCT(bearer(c.Secrets["access_token"]), ctJSON), jsonBody(jx.M{"text": p.Text}))
	d, err := networkExpect(r, err, []int{201}, "Твит не опубликован")
	if err != nil {
		return nil, err
	}
	id := jx.Str(jx.Map(d, "data"), "id")
	user := jx.Str(c.Meta, "username")
	if user == "" {
		user = "i"
	}
	u := ""
	if id != "" {
		u = "https://x.com/" + user + "/status/" + id
	}
	return &NetSent{URL: u, MessageIDs: []string{id}}, nil
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
			out = append(out, jx.M{"id": c.ID, "platform": c.Platform, "title": c.ChatTitle, "ref": c.Ref, "connected_at": c.ConnectedAt})
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
