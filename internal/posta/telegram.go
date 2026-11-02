package posta

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"io"
	"math/rand"
	"mime/multipart"
	"net/url"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/imgx"
	"posta/internal/jx"
)

// Публикация в Telegram: бот (Bot API) — сразу, личный аккаунт (MTProto) — в «Отложенные» канала, своя очередь — по времени.

// ---- Bot API ----

func telegramErrorText(data jx.M, status int) string {
	d := strings.ToLower(jx.Str(data, "description"))
	has := func(words ...string) bool {
		for _, w := range words {
			if strings.Contains(d, w) {
				return true
			}
		}
		return false
	}
	retry := jx.Int(jx.Map(data, "parameters"), "retry_after")
	switch {
	case status == 401 || has("unauthorized"):
		return "Токен бота недействителен: подключите Telegram заново в «Личном кабинете»."
	case has("chat not found"):
		return "Telegram не нашёл канал: проверьте, что бот добавлен в канал."
	case has("not a member", "was kicked", "bot was blocked"):
		return "Бот не состоит в канале или удалён из него: добавьте его администратором."
	case has("not enough rights", "have no rights", "need administrator rights"):
		return "У бота нет права публиковать в канале: сделайте его администратором с правом «Публикация сообщений»."
	case status == 413 || has("too big"):
		return "Файл слишком большой для Telegram."
	case status == 429 || has("too many requests"):
		if retry > 0 {
			return fmt.Sprintf("Telegram просит подождать %d с перед следующей отправкой.", retry)
		}
		return "Telegram просит подождать перед следующей отправкой."
	case has("wrong file", "wrong type"):
		return "Telegram не принял файл: проверьте формат (фото JPEG/PNG, видео MP4)."
	}
	if jx.Str(data, "description") != "" {
		return "Telegram: " + jx.Str(data, "description")
	}
	return fmt.Sprintf("Telegram: ответ не распознан (HTTP %d)", status)
}

// ---- Прогресс отправки (в памяти; браузер опрашивает отдельным запросом) ----

var publishProgress = struct {
	sync.Mutex
	m map[string]jx.M
}{m: map[string]jx.M{}}

var progressIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

// PublishProgressSet — этап и процент отправки (id приходит от браузера).
func PublishProgressSet(id, stage string, percent *float64) {
	if !progressIDRe.MatchString(id) {
		return
	}
	var p any
	if percent != nil {
		p = int(min(100, max(0, *percent)) + 0.5)
	}
	publishProgress.Lock()
	publishProgress.m[id] = jx.M{"stage": stage, "percent": p, "at": now()}
	publishProgress.Unlock()
}

func PublishProgressGet(id string) jx.M {
	publishProgress.Lock()
	defer publishProgress.Unlock()
	p := publishProgress.m[id]
	if p != nil && now()-jx.Int(p, "at") > 600 {
		delete(publishProgress.m, id)
		return nil
	}
	return p
}

type progressFn func(stage string, percent *float64)

func noProgress(string, *float64) {}

type countingReader struct {
	r        io.Reader
	n, total int64
	on       func(float64)
	last     time.Time
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if c.on != nil && c.total > 0 && time.Since(c.last) > 300*time.Millisecond {
		c.last = time.Now()
		c.on(float64(c.n) / float64(c.total) * 100)
	}
	return n, err
}

func telegramAPIBase() string {
	if b := os.Getenv("POSTA_TELEGRAM_API"); b != "" {
		return b
	}
	return "https://api.telegram.org/bot"
}

// TelegramBotRequest — POST к Bot API; files — [поле → путь], уходят multipart.
func (a *App) TelegramBotRequest(ctx context.Context, token, method string, fields map[string]string, files map[string]string, timeout time.Duration, onUpload func(float64)) (json.RawMessage, error) {
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	contentType := "application/x-www-form-urlencoded"
	if len(files) > 0 {
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		contentType = mw.FormDataContentType()
		var total int64
		for _, path := range files {
			if st, err := os.Stat(path); err == nil {
				total += st.Size()
			}
		}
		go func() {
			for k, v := range fields {
				mw.WriteField(k, v)
			}
			for name, path := range files {
				f, err := os.Open(path)
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				part, _ := mw.CreateFormFile(name, filepath.Base(path))
				io.Copy(part, f)
				f.Close()
			}
			mw.Close()
			pw.Close()
		}()
		body = &countingReader{r: pr, total: total, on: onUpload}
	} else {
		form := make([]string, 0, len(fields))
		for k, v := range fields {
			form = append(form, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
		body = strings.NewReader(strings.Join(form, "&"))
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", telegramAPIBase()+token+"/"+method, body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", HTTPUA)
	resp, err := (&http.Client{}).Do(req)
	if err != nil {
		return nil, Bad("Telegram недоступен: %s", err)
	}
	defer resp.Body.Close()
	if onUpload != nil {
		onUpload(100)
	}
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var data struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if json.Unmarshal(raw, &data) != nil || !data.OK {
		return nil, Bad("%s", telegramErrorText(jx.Unmarshal(raw), resp.StatusCode))
	}
	return data.Result, nil
}

// TelegramConnection — канал Telegram пользователя (по id или по умолчанию) с токеном бота; nil — нет.
func (u *U) TelegramConnection(channelID string) *Channel {
	var c *Channel
	if channelID != "" {
		c = u.Channel(channelID)
	} else if l := u.TelegramChannels(); len(l) > 0 {
		c = l[0]
	}
	if c == nil || c.Token == "" {
		return nil
	}
	return c
}

// ChannelForPost — канал для отправки: любая соцсеть по id, без id — Telegram по умолчанию.
func (a *App) ChannelForPost(ctx context.Context, uid, channelID string) *Channel {
	u := a.User(ctx, uid, nil)
	var c *Channel
	if channelID != "" {
		c = u.Channel(channelID)
	} else {
		c = u.TelegramConnection("")
	}
	if c == nil || len(c.Secrets) == 0 {
		return nil
	}
	return c
}

func (u *U) ChannelOverLimit(chatID int64) bool {
	chs := u.Channels()
	for _, c := range chs {
		if c.ChatID == chatID {
			return false
		}
	}
	return len(chs) >= int(jx.Int(u.BillingLimits(), "channels"))
}

// ChannelConnectDenied — отказ подключить канал по тарифу (только при включённых лимитах).
func (u *U) ChannelConnectDenied(chatID int64) string {
	if !u.TBool("billing_enforce") || !u.ChannelOverLimit(chatID) {
		return ""
	}
	l := u.BillingLimits()
	return fmt.Sprintf("На тарифе «%s» можно подключить каналов: %d. Больше — в «Тариф и баланс».", jx.Str(l, "title"), jx.Int(l, "channels"))
}

func (u *U) ChannelDeleteDenied(channelID string) string {
	def := ""
	if l := u.TelegramChannels(); len(l) > 0 {
		def = l[0].ID
	}
	for _, e := range u.Outbox() {
		ch := jx.Str(e, "channel")
		if ch == "" {
			ch = def
		}
		if contains([]string{"telegram", "queued", "sending", "failed"}, jx.Str(e, "status")) && ch == channelID {
			return "У канала есть отложенные посты: сначала отмените их в очереди."
		}
	}
	return ""
}

// PublishChannelResolve — канал для публикации или ошибка со статусом.
func (u *U) PublishChannelResolve(channelID string) (*Channel, error) {
	if channelID == "" {
		if c := u.TelegramConnection(""); c != nil {
			return c, nil
		}
		return nil, &Error{409, "Telegram не подключён: подключите бота и канал в «Личном кабинете» → «Соцсети»."}
	}
	list := u.Channels()
	for i, c := range list {
		if c.ID != channelID {
			continue
		}
		if len(c.Secrets) == 0 {
			break
		}
		if u.TBool("billing_enforce") {
			l := u.BillingLimits()
			if i >= int(jx.Int(l, "channels")) {
				return nil, &Error{402, fmt.Sprintf("Канал за пределами тарифа: на тарифе «%s» публиковать можно в первые %d канала по порядку.", jx.Str(l, "title"), jx.Int(l, "channels"))}
			}
		}
		return c, nil
	}
	return nil, NotFound("Канал не найден: обновите страницу.")
}

func (u *U) TelegramChannelFields(channelID string) jx.M {
	var c *Channel
	if channelID != "" {
		c = u.Channel(channelID)
	} else if l := u.TelegramChannels(); len(l) > 0 {
		c = l[0]
	}
	if c == nil {
		return jx.M{"channel": "", "channel_title": ""}
	}
	return jx.M{"channel": c.ID, "channel_title": c.ChatTitle}
}

// ---- Лимит видео: документация (50 МБ бот, 2 ГБ аккаунт) уточняется ответами Telegram ----

type videoLimit struct{ Default, OKMax, FailMin int64 }

func (a *App) telegramVideoLimit(route string) videoLimit {
	if route == "" {
		route = "bot"
	}
	var state map[string]map[string]map[string]int64
	a.KVGet(a.ctxOrBg(), "telegram_limits", &state)
	k := state[route]["video"]
	def := config.TelegramLimitDefaults[route]
	if def == 0 {
		def = config.TelegramLimitDefaults["bot"]
	}
	return videoLimit{def, k["ok_max"], k["fail_min"]}
}

// TelegramVideoVerdict — ok | risky (больше известного — пробуем) | blocked.
func (a *App) TelegramVideoVerdict(size int64, route string) string {
	l := a.telegramVideoLimit(route)
	if l.FailMin > 0 && size >= l.FailMin {
		return "blocked"
	}
	if size <= max(l.Default, l.OKMax) {
		return "ok"
	}
	if size <= config.TelegramUploadMaxBytes {
		return "risky"
	}
	return "blocked"
}

func (a *App) TelegramVideoLimitText(size int64, route string) string {
	l := a.telegramVideoLimit(route)
	known := max(l.Default, l.OKMax)
	if l.FailMin > 0 {
		known = l.FailMin - 1
	}
	if size > config.TelegramUploadMaxBytes && size > known {
		return fmt.Sprintf("весит %.0f МБ, а приложение загружает не больше %d МБ", float64(size)/(1<<20), config.TelegramUploadMaxBytes>>20)
	}
	return fmt.Sprintf("весит %.0f МБ, Telegram принимает до %.0f МБ", float64(size)/(1<<20), float64(known)/(1<<20))
}

func (a *App) telegramVideoLimitLearn(size int64, accepted bool, route string) {
	if size <= 0 {
		return
	}
	if route == "" {
		route = "bot"
	}
	l := a.telegramVideoLimit(route)
	if accepted && size <= l.OKMax && (l.FailMin == 0 || size < l.FailMin) {
		return
	}
	KVUpdate(a, a.ctxOrBg(), "telegram_limits", 0, func(st map[string]map[string]map[string]int64, _ bool) *map[string]map[string]map[string]int64 {
		if st == nil {
			st = map[string]map[string]map[string]int64{}
		}
		if st[route] == nil {
			st[route] = map[string]map[string]int64{}
		}
		e := st[route]["video"]
		if e == nil {
			e = map[string]int64{}
		}
		if accepted {
			e["ok_max"] = max(e["ok_max"], size)
			if e["fail_min"] > 0 && size >= e["fail_min"] {
				e["fail_min"] = 0
			}
		} else {
			if e["fail_min"] > 0 {
				e["fail_min"] = min(e["fail_min"], size)
			} else {
				e["fail_min"] = size
			}
			if e["ok_max"] >= e["fail_min"] {
				e["ok_max"] = e["fail_min"] - 1
			}
		}
		e["updated"] = now()
		st[route]["video"] = e
		return &st
	})
}

// ---- Подготовка файлов ----

// Prepared — файл поста, готовый к отправке.
type Prepared struct {
	Kind     string `json:"kind"` // photo | video | animation
	Path     string `json:"path"`
	Name     string `json:"name"`
	Src      string `json:"src"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	Duration int    `json:"duration"`
}

func TelegramWorkDir() (string, error) {
	d, err := os.MkdirTemp("", "posta-publish-")
	if err != nil {
		return "", Bad("Не удалось создать временную папку.")
	}
	return d, nil
}

func (a *App) fetchPublicMedia(src, dest string, maxBytes int64) error {
	r, err := a.FetchPublic(src, FetchOpts{MaxBytes: maxBytes, Dest: dest, Timeout: 180 * time.Second})
	if err != nil {
		return err
	}
	if !r.OK || (!strings.HasPrefix(r.Type, "image/") && !strings.HasPrefix(r.Type, "video/")) {
		os.Remove(dest)
		if r.TooBig {
			return Bad("Файл по ссылке слишком большой.")
		}
		return Bad("Не удалось скачать файл по ссылке (%s).", src)
	}
	return nil
}

// telegramPreparePhoto — JPEG/PNG до 10 МБ, сумма сторон ≤ 10000, пропорции до 20:1; иначе пережимается.
func telegramPreparePhoto(path, dir string, index int) (Prepared, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Prepared{}, Bad("Файл не похож на картинку.")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(b))
	if err != nil {
		return Prepared{}, Bad("Файл не похож на картинку.")
	}
	w, h := cfg.Width, cfg.Height
	if (format == "jpeg" || format == "png") && len(b) <= config.TelegramMaxPhotoBytes && w+h <= 10000 && float64(max(w, h))/float64(max(1, min(w, h))) <= 20 {
		return Prepared{Kind: "photo", Path: path, Width: w, Height: h}, nil
	}
	var jpeg []byte
	for q := 90; q >= 60; q -= 10 {
		jpeg = imgx.Shrink(b, 2560, q, true)
		if bytes.Equal(jpeg, b) {
			return Prepared{}, Bad("Не удалось прочитать картинку.")
		}
		if len(jpeg) <= config.TelegramMaxPhotoBytes {
			break
		}
	}
	if len(jpeg) > config.TelegramMaxPhotoBytes {
		return Prepared{}, Bad("Картинка слишком большая даже после сжатия.")
	}
	out := filepath.Join(dir, fmt.Sprintf("photo-%d.jpg", index))
	os.WriteFile(out, jpeg, 0o644)
	w, h, _ = imgx.Size(jpeg)
	return Prepared{Kind: "photo", Path: out, Width: w, Height: h}, nil
}

type VideoInfo struct {
	Container, Video, Audio string
	Duration, Width, Height int
}

var videoInfoCache sync.Map

// TelegramVideoInfo — контейнер, кодеки, размеры (кэш по имени и размеру файла).
func (a *App) TelegramVideoInfo(path string) VideoInfo {
	st, err := os.Stat(path)
	if err != nil {
		return VideoInfo{}
	}
	key := filepath.Base(path) + ":" + strconv.FormatInt(st.Size(), 10)
	if v, ok := videoInfoCache.Load(key); ok {
		return v.(VideoInfo)
	}
	var d struct {
		Format struct {
			FormatName string `json:"format_name"`
			Duration   string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType string `json:"codec_type"`
			CodecName string `json:"codec_name"`
			Width     int    `json:"width"`
			Height    int    `json:"height"`
		} `json:"streams"`
	}
	json.Unmarshal([]byte(a.ffprobe(path, "-show_entries", "format=format_name,duration:stream=codec_type,codec_name,width,height", "-of", "json")), &d)
	dur, _ := strconv.ParseFloat(d.Format.Duration, 64)
	info := VideoInfo{Container: d.Format.FormatName, Duration: int(dur + 0.5)}
	for _, s := range d.Streams {
		if s.CodecType == "video" && info.Video == "" {
			info.Video, info.Width, info.Height = s.CodecName, s.Width, s.Height
		}
		if s.CodecType == "audio" && info.Audio == "" {
			info.Audio = s.CodecName
		}
	}
	if info.Video != "" {
		videoInfoCache.Store(key, info)
	}
	return info
}

func telegramVideoReady(i VideoInfo) bool {
	return strings.Contains(i.Container, "mp4") && i.Video == "h264" && (i.Audio == "" || i.Audio == "aac")
}

func (a *App) telegramPrepareVideo(path, dir string, index int, route string, progress progressFn) (Prepared, error) {
	info := a.TelegramVideoInfo(path)
	if info.Video == "" {
		return Prepared{}, Bad("Файл не похож на видео (или на сервере нет ffprobe).")
	}
	if !telegramVideoReady(info) {
		if !a.HasFFmpeg() {
			return Prepared{}, Bad("Видео нужно перекодировать в MP4, но на сервере нет ffmpeg.")
		}
		out := filepath.Join(dir, fmt.Sprintf("video-%d.mp4", index))
		ok := a.FFmpegRunProgress([]string{"-i", path, "-map", "0:v:0", "-map", "0:a:0?", "-c:v", "libx264", "-preset", "veryfast", "-crf", "26", "-pix_fmt", "yuv420p",
			"-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-c:a", "aac", "-b:a", "128k", "-movflags", "+faststart", "-f", "mp4"}, out, 10*time.Minute, float64(info.Duration),
			func(p *float64) { progress("Перекодирую видео", p) })
		if !ok {
			return Prepared{}, Bad("Не удалось перекодировать видео в MP4.")
		}
		path = out
		info = a.TelegramVideoInfo(out)
	}
	st, _ := os.Stat(path)
	if a.TelegramVideoVerdict(st.Size(), route) == "blocked" {
		return Prepared{}, Bad("Видео %s.", a.TelegramVideoLimitText(st.Size(), route))
	}
	return Prepared{Kind: "video", Path: path, Width: info.Width, Height: info.Height, Duration: info.Duration}, nil
}

// MediaItem — файл поста из редактора: загруженный файл, кэш сайта или ссылка.
type MediaItem struct {
	Kind, Src, Name string
	Upload          string
}

func sniffMime(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b := make([]byte, 512)
	n, _ := f.Read(b)
	return http.DetectContentType(b[:n])
}

// TelegramPrepareMedia — файлы поста → готовые к отправке. Гифка в альбоме становится видео.
func (a *App) TelegramPrepareMedia(items []MediaItem, uploads map[string]string, dir, route string, progress progressFn) ([]Prepared, error) {
	if len(items) > config.TelegramMaxMedia {
		return nil, Bad("В пост помещается не больше %d файлов.", config.TelegramMaxMedia)
	}
	if progress == nil {
		progress = noProgress
	}
	var out []Prepared
	for i, it := range items {
		name := trimRunes(it.Name, 120)
		if name == "" {
			name = fmt.Sprintf("файл %d", i+1)
		}
		p, err := func() (Prepared, error) {
			path := ""
			if it.Upload != "" && uploads[it.Upload] != "" {
				path = uploads[it.Upload]
			} else if c := a.MediaPath(it.Src); c != "" && fileExists(c) {
				path = c
			} else if strings.HasPrefix(strings.ToLower(it.Src), "http://") || strings.HasPrefix(strings.ToLower(it.Src), "https://") {
				if it.Kind == "video" {
					progress("Скачиваю видео", nil)
				}
				path = filepath.Join(dir, fmt.Sprintf("source-%d", i))
				if err := a.fetchPublicMedia(it.Src, path, config.TelegramUploadMaxBytes); err != nil {
					return Prepared{}, err
				}
			} else {
				return Prepared{}, Bad("файл недоступен (ни в кэше сайта, ни по ссылке).")
			}
			mime := sniffMime(path)
			switch {
			case mime == "image/gif":
				return Prepared{Kind: "animation", Path: path}, nil
			case strings.HasPrefix(mime, "image/"):
				return telegramPreparePhoto(path, dir, i)
			case strings.HasPrefix(mime, "video/") || mime == "application/octet-stream":
				return a.telegramPrepareVideo(path, dir, i, route, progress)
			}
			return Prepared{}, Bad("поддерживаются только картинки и видео")
		}()
		if err != nil {
			return nil, Bad("«%s»: %s", name, err.Error())
		}
		p.Name, p.Src = name, it.Src
		out = append(out, p)
	}
	if len(out) > 1 {
		for i, f := range out {
			if f.Kind != "animation" {
				continue
			}
			if !a.HasFFmpeg() {
				return nil, Bad("«%s»: гифку в альбоме нельзя отправить без ffmpeg. Отправьте её отдельным постом.", f.Name)
			}
			dst := filepath.Join(dir, fmt.Sprintf("gif-%d.mp4", i))
			if !a.FFmpegRun([]string{"-i", f.Path, "-movflags", "+faststart", "-pix_fmt", "yuv420p", "-vf", "scale=trunc(iw/2)*2:trunc(ih/2)*2", "-an", "-f", "mp4"}, dst, 5*time.Minute) {
				return nil, Bad("«%s»: не удалось превратить гифку в видео.", f.Name)
			}
			info := a.TelegramVideoInfo(dst)
			out[i] = Prepared{Kind: "video", Path: dst, Name: f.Name, Src: f.Src, Width: info.Width, Height: info.Height, Duration: info.Duration}
		}
	}
	return out, nil
}

// ---- Предварительная проверка записи («ракета» на карточке) ----

var remoteSizes sync.Map

func (a *App) telegramRemoteSize(u string) *int64 {
	type entry struct {
		size *int64
		at   time.Time
	}
	if v, ok := remoteSizes.Load(u); ok {
		e := v.(entry)
		ttl := 6 * time.Hour
		if e.size == nil {
			ttl = 10 * time.Minute
		}
		if time.Since(e.at) < ttl {
			return e.size
		}
	}
	var size *int64
	if pu, addr, err := publicResolve(u); err == nil {
		_ = addr
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		req, _ := http.NewRequestWithContext(ctx, "HEAD", pu.String(), nil)
		req.Header.Set("User-Agent", HTTPUA)
		if resp, err := defaultClient.Do(req); err == nil {
			if resp.StatusCode == 200 && resp.ContentLength > 0 {
				n := resp.ContentLength
				size = &n
			}
			resp.Body.Close()
		}
		cancel()
	}
	remoteSizes.Store(u, entry{size, time.Now()})
	return size
}

// TelegramPreflightItem — можно ли запостить видео записи: status ok | partial | blocked, problems, warnings, bad.
func (u *U) TelegramPreflightItem(item jx.M, remote bool) jx.M {
	via := jx.Str(item, "video_via")
	if (via == "ytdlp" || via == "tgu") && jx.Str(item, "video") == "" && !jx.Bool(item, "video_unavailable") {
		return jx.M{"status": "blocked", "problems": []string{"Видео ещё загружается, попробуйте через минуту"}, "warnings": []string{}, "bad": []int{0}}
	}
	gallery := jx.Maps(item, "gallery")
	main := ContentImageKey(item)
	type f struct{ video, local string }
	var files []f
	if len(gallery) > 1 {
		for _, g := range gallery {
			local := jx.Str(g, "video_local")
			if jx.Str(g, "image") == main {
				local = jx.Str(item, "video_local")
			}
			files = append(files, f{jx.Str(g, "video"), local})
		}
	} else {
		files = []f{{jx.Str(item, "video"), jx.Str(item, "video_local")}}
	}
	problems, warnings, bad := []string{}, []string{}, []int{}
	for i, x := range files {
		if x.video == "" {
			continue
		}
		label := "Видео: "
		if len(files) > 1 {
			label = fmt.Sprintf("Видео %d: ", i+1)
		}
		problem := ""
		var size *int64
		if p := u.MediaPath(x.local); p != "" && fileExists(p) {
			st, _ := os.Stat(p)
			s := st.Size()
			size = &s
			info := u.TelegramVideoInfo(p)
			if info.Video == "" {
				problem = "файл не распознан как видео (или на сервере нет ffprobe)"
			} else if !telegramVideoReady(info) && !u.HasFFmpeg() {
				problem = "нужно перекодировать в MP4, а ffmpeg на сервере не найден"
			}
		} else if remote {
			size = u.telegramRemoteSize(x.video)
		}
		if problem == "" && size != nil {
			switch u.TelegramVideoVerdict(*size, "bot") {
			case "blocked":
				problem = u.TelegramVideoLimitText(*size, "bot")
			case "risky":
				warnings = append(warnings, label+u.TelegramVideoLimitText(*size, "bot")+" по последним данным; Telegram мог изменить лимит, поэтому попробуем отправить.")
			}
		}
		if problem != "" {
			problems = append(problems, label+problem)
			bad = append(bad, i)
		}
	}
	status := "ok"
	if len(bad) > 0 {
		status = "partial"
		if len(bad) >= len(files) {
			status = "blocked"
		}
	}
	return jx.M{"status": status, "problems": problems, "warnings": warnings, "bad": bad}
}

// ---- Отправка ----

var supergroupRe = regexp.MustCompile(`^-100(\d+)$`)

func TelegramMessageURL(c *Channel, id int) string {
	if c.ChatUsername != "" {
		return fmt.Sprintf("https://t.me/%s/%d", c.ChatUsername, id)
	}
	if m := supergroupRe.FindStringSubmatch(strconv.FormatInt(c.ChatID, 10)); m != nil {
		return fmt.Sprintf("https://t.me/c/%s/%d", m[1], id)
	}
	return ""
}

func TelegramCheckText(text string, mediaCount int) error {
	limit := config.TelegramTextLimit
	if mediaCount > 0 {
		limit = config.TelegramCaptionLimit
	}
	if n := runeLen(text); n > limit {
		suffix := "."
		if mediaCount > 0 {
			suffix = " (подпись к медиа)."
		}
		return Bad("Текст длиннее лимита Telegram: %d из %d%s", n, limit, suffix)
	}
	if mediaCount == 0 && strings.TrimSpace(text) == "" {
		return Bad("Добавьте медиа или текст.")
	}
	return nil
}

type SentResult struct {
	MessageIDs []int
	URL        string
	Skipped    []string
	MediaKept  []Prepared
}

// TelegramPublishPrepared — отправка поста ботом.
func (a *App) TelegramPublishPrepared(ctx context.Context, c *Channel, text string, prepared []Prepared, progress progressFn) (*SentResult, error) {
	if c.Token == "" || c.ChatID == 0 {
		return nil, Bad("Telegram не подключён: подключите бота и канал в «Личном кабинете».")
	}
	if err := TelegramCheckText(text, len(prepared)); err != nil {
		return nil, err
	}
	if progress == nil {
		progress = noProgress
	}
	var biggest int64
	for _, f := range prepared {
		if f.Kind == "video" {
			if st, err := os.Stat(f.Path); err == nil {
				biggest = max(biggest, st.Size())
			}
		}
	}
	chat := strconv.FormatInt(c.ChatID, 10)
	onUpload := func(p float64) {
		if p < 100 {
			progress("Отправка в Telegram", &p)
		} else {
			progress("Telegram обрабатывает видео…", nil)
		}
	}
	var raw json.RawMessage
	var err error
	switch len(prepared) {
	case 0:
		raw, err = a.TelegramBotRequest(ctx, c.Token, "sendMessage", map[string]string{"chat_id": chat, "text": text, "disable_web_page_preview": "true"}, nil, 60*time.Second, nil)
		raw = json.RawMessage("[" + string(raw) + "]")
	case 1:
		f := prepared[0]
		method := map[string]string{"photo": "sendPhoto", "animation": "sendAnimation", "video": "sendVideo"}[f.Kind]
		fields := map[string]string{"chat_id": chat}
		if text != "" {
			fields["caption"] = text
		}
		if f.Kind == "video" {
			fields["supports_streaming"] = "true"
			fields["width"], fields["height"], fields["duration"] = strconv.Itoa(f.Width), strconv.Itoa(f.Height), strconv.Itoa(f.Duration)
		}
		raw, err = a.TelegramBotRequest(ctx, c.Token, method, fields, map[string]string{f.Kind: f.Path}, 10*time.Minute, onUpload)
		raw = json.RawMessage("[" + string(raw) + "]")
	default:
		var media []jx.M
		files := map[string]string{}
		for i, f := range prepared {
			m := jx.M{"type": "photo", "media": fmt.Sprintf("attach://file%d", i)}
			if f.Kind == "video" {
				m["type"], m["supports_streaming"], m["width"], m["height"], m["duration"] = "video", true, f.Width, f.Height, f.Duration
			}
			if i == 0 && text != "" {
				m["caption"] = text
			}
			media = append(media, m)
			files[fmt.Sprintf("file%d", i)] = f.Path
		}
		raw, err = a.TelegramBotRequest(ctx, c.Token, "sendMediaGroup", map[string]string{"chat_id": chat, "media": string(jx.Marshal(media))}, files, 10*time.Minute, onUpload)
	}
	if err != nil {
		if biggest > 0 && err.Error() == "Файл слишком большой для Telegram." {
			a.telegramVideoLimitLearn(biggest, false, "bot")
			return nil, Bad("Telegram не принял видео (%s). Лимит запомнен: такие видео приложение больше не предлагает.", a.TelegramVideoLimitText(biggest, "bot"))
		}
		return nil, err
	}
	a.telegramVideoLimitLearn(biggest, true, "bot")
	var msgs []struct {
		MessageID int `json:"message_id"`
	}
	json.Unmarshal(raw, &msgs)
	r := &SentResult{MediaKept: prepared}
	for _, m := range msgs {
		r.MessageIDs = append(r.MessageIDs, m.MessageID)
	}
	if len(r.MessageIDs) > 0 {
		r.URL = TelegramMessageURL(c, r.MessageIDs[0])
	}
	return r, nil
}

// TelegramPollCheck — вопрос до 300 символов, 2–10 вариантов по 100.
func TelegramPollCheck(poll jx.M) (jx.M, error) {
	q := strings.TrimSpace(jx.Str(poll, "question"))
	var opts []string
	for _, o := range jx.List(poll, "options") {
		if s := strings.TrimSpace(jx.ToStr(o)); s != "" {
			opts = append(opts, s)
		}
	}
	switch {
	case q == "":
		return nil, Bad("Напишите вопрос опроса.")
	case runeLen(q) > 300:
		return nil, Bad("Вопрос опроса длиннее 300 символов: %d.", runeLen(q))
	case len(opts) < 2:
		return nil, Bad("В опросе нужно минимум два варианта ответа.")
	case len(opts) > 10:
		return nil, Bad("В опросе не больше 10 вариантов.")
	}
	for _, o := range opts {
		if runeLen(o) > 100 {
			return nil, Bad("Вариант ответа длиннее 100 символов.")
		}
	}
	return jx.M{"question": q, "options": opts}, nil
}

func (a *App) TelegramPublishPoll(ctx context.Context, c *Channel, poll jx.M) (*SentResult, error) {
	if c.Token == "" || c.ChatID == 0 {
		return nil, Bad("Telegram не подключён: подключите бота и канал в «Личном кабинете».")
	}
	raw, err := a.TelegramBotRequest(ctx, c.Token, "sendPoll", map[string]string{"chat_id": strconv.FormatInt(c.ChatID, 10), "question": jx.Str(poll, "question"),
		"options": string(jx.Marshal(jx.Strs(poll, "options")))}, nil, 60*time.Second, nil)
	if err != nil {
		return nil, err
	}
	var m struct {
		MessageID int `json:"message_id"`
	}
	json.Unmarshal(raw, &m)
	return &SentResult{MessageIDs: []int{m.MessageID}, URL: TelegramMessageURL(c, m.MessageID)}, nil
}

func TelegramPostSummary(text string, media int) jx.M {
	return jx.M{"text": trimRunes(strings.TrimSpace(text), 120), "media": media}
}

func MediaSourcesOf(media []jx.M) []string {
	out := []string{}
	for _, f := range media {
		if s := jx.Str(f, "src"); s != "" {
			out = append(out, s)
		}
	}
	return out
}

func PreparedMaps(p []Prepared) []jx.M {
	out := []jx.M{}
	for _, f := range p {
		out = append(out, jx.M{"kind": f.Kind, "name": f.Name, "src": f.Src, "path": f.Path, "width": f.Width, "height": f.Height, "duration": f.Duration})
	}
	return out
}

func (u *U) TelegramLogPublished(entry jx.M) {
	u.PublishedAdd(entry)
	u.CH.Publish(ch.PublishEvent{TS: time.Now(), UserID: u.ID, ChannelID: jx.Str(entry, "channel"), Platform: "telegram", OK: jx.Str(entry, "status") == "ok",
		Error: jx.Str(entry, "error"), PostRef: jx.Str(entry, "url"), MediaCount: int(jx.Int(entry, "media")), Scheduled: jx.Int(entry, "scheduled_for") > 0})
}

// TelegramRecordNativePost — пост в «Отложенных» канала (через аккаунт): запись в очереди со статусом telegram.
func (u *U) TelegramRecordNativePost(text string, prepared []Prepared, publishAt int64, ids []int, channelID string) string {
	id := randHex(6)
	var media []jx.M
	for _, f := range prepared {
		media = append(media, jx.M{"kind": f.Kind, "name": f.Name})
	}
	thumb := u.PostThumbMake(PreparedMaps(prepared))
	u.OutboxUpdate(func(q []jx.M) []jx.M {
		return append(q, jx.M{"id": id, "created_at": now(), "publish_at": publishAt, "text": text, "media": media, "status": "telegram", "tg_ids": ids,
			"attempts": 0, "error": "", "channel": channelID, "thumb_id": thumb})
	})
	return id
}

// TelegramSettleNativePosts — отложенные посты, время которых прошло, — в журнал отправленных.
func (u *U) TelegramSettleNativePosts() {
	var settled []jx.M
	u.OutboxUpdate(func(q []jx.M) []jx.M {
		var kept []jx.M
		for _, e := range q {
			if jx.Str(e, "status") == "telegram" && jx.Int(e, "publish_at")+120 < now() {
				settled = append(settled, e)
			} else {
				kept = append(kept, e)
			}
		}
		if len(settled) == 0 {
			return nil
		}
		return orEmptyList(kept)
	})
	for _, e := range settled {
		media := jx.Maps(e, "media")
		kind := ""
		if len(media) > 0 {
			kind = jx.Str(media[0], "kind")
		}
		entry := jx.Merge(jx.Merge(TelegramPostSummary(jx.Str(e, "text"), len(media)), u.TelegramChannelFields(jx.Str(e, "channel"))),
			jx.M{"at": jx.Int(e, "publish_at"), "scheduled_for": jx.Int(e, "publish_at"), "url": "", "status": "ok", "error": "", "media_src": MediaSourcesOf(media),
				"thumb_id": jx.Str(e, "thumb_id"), "kind": kind})
		u.TelegramLogPublished(entry)
	}
}

func (u *U) TelegramUnqueuePost(postID string) bool {
	found := false
	u.OutboxUpdate(func(q []jx.M) []jx.M {
		var kept []jx.M
		for _, e := range q {
			if jx.Str(e, "id") != postID {
				kept = append(kept, e)
			}
		}
		found = len(kept) != len(q)
		if !found {
			return nil
		}
		return orEmptyList(kept)
	})
	return found
}

// ---- Расписание канала: слоты по дням недели в часовом поясе пользователя ----

const (
	schedMaxJitter      = 30
	schedMaxSlotsPerDay = 24
	schedSearchDays     = 60
	schedBusySeconds    = 60
)

var hhmmStrictRe = regexp.MustCompile(`^([01]\d|2[0-3]):[0-5]\d$`)

func loadTZ(name string) (*time.Location, string) {
	if name != "" {
		if l, err := time.LoadLocation(name); err == nil {
			return l, name
		}
	}
	return time.Local, systemTZName()
}

// systemTZName — имя часового пояса сервера (Asia/Yekaterinburg), а не «Local».
func systemTZName() string {
	if tz := os.Getenv("TZ"); tz != "" {
		return tz
	}
	if b, err := os.ReadFile("/etc/timezone"); err == nil && strings.TrimSpace(string(b)) != "" {
		return strings.TrimSpace(string(b))
	}
	if p, err := filepath.EvalSymlinks("/etc/localtime"); err == nil {
		if i := strings.Index(p, "zoneinfo/"); i >= 0 {
			return p[i+9:]
		}
	}
	return "UTC"
}

// ChannelSchedule — расписание канала с проверкой и значениями по умолчанию.
func ChannelSchedule(c *Channel) jx.M {
	s := c.Schedule
	_, tz := loadTZ(jx.Str(s, "tz"))
	slots := jx.M{}
	for day, times := range jx.Map(s, "slots") {
		d, err := strconv.Atoi(day)
		if err == nil && d >= 1 && d <= 7 && len(jx.ToList(times)) > 0 {
			slots[day] = jx.ToStrs(times)
		}
	}
	return jx.M{"tz": tz, "jitter": min(schedMaxJitter, max(0, int(jx.Int(s, "jitter")))), "slots": slots}
}

// ChannelScheduleSave — проверить и сохранить расписание.
func (u *U) ChannelScheduleSave(channelID string, sched jx.M) (jx.M, error) {
	c := u.Channel(channelID)
	if c == nil {
		return nil, Conflict("Такого канала нет.")
	}
	_, tz := loadTZ(jx.Str(sched, "tz"))
	jit := jx.Float(sched, "jitter")
	if jit != float64(int(jit)) || jit < 0 || jit > schedMaxJitter {
		return nil, Bad("Случайный сдвиг — целое число минут от 0 до %d.", schedMaxJitter)
	}
	slots := jx.M{}
	for day, times := range jx.Map(sched, "slots") {
		d, err := strconv.Atoi(day)
		if err != nil || d < 1 || d > 7 {
			return nil, Bad("День недели — число от 1 (пн) до 7 (вс).")
		}
		var list []string
		if s, ok := times.(string); ok {
			list = regexp.MustCompile(`[\s,;]+`).Split(strings.TrimSpace(s), -1)
		} else {
			list = jx.ToStrs(times)
		}
		var clean []string
		for _, t := range list {
			t = strings.TrimSpace(t)
			if t == "" {
				continue
			}
			if !hhmmStrictRe.MatchString(t) {
				return nil, Bad("Время «%s» нужно писать как ЧЧ:ММ, например 10:00.", t)
			}
			clean = append(clean, t)
		}
		clean = uniqueStrings(clean)
		sort.Strings(clean)
		if len(clean) > schedMaxSlotsPerDay {
			return nil, Bad("В день — не больше %d слотов.", schedMaxSlotsPerDay)
		}
		if len(clean) > 0 {
			slots[strconv.Itoa(d)] = clean
		}
	}
	saved := jx.M{"tz": tz, "jitter": int(jit), "slots": slots}
	if err := u.ChannelUpdateField(channelID, "schedule", saved); err != nil {
		return nil, err
	}
	c.Schedule = saved
	return ChannelSchedule(c), nil
}

// ChannelNextSlot — ближайший свободный слот не раньше after; (slot_at, publish_at со сдвигом) или nil.
func (a *App) ChannelNextSlot(c *Channel, after int64, queue []jx.M) jx.M {
	s := ChannelSchedule(c)
	slots := jx.Map(s, "slots")
	if len(slots) == 0 {
		return nil
	}
	loc, _ := loadTZ(jx.Str(s, "tz"))
	floor := max(after, now()+int64(a.TInt("min_delay")))
	var busy []int64
	for _, p := range queue {
		if jx.Str(p, "channel") == c.ID && contains([]string{"queued", "sending", "telegram"}, jx.Str(p, "status")) {
			at := jx.Int(p, "slot_at")
			if !jx.Has(p, "slot_at") || at == 0 {
				at = jx.Int(p, "publish_at")
			}
			busy = append(busy, at)
		}
	}
	jitter := int(jx.Int(s, "jitter"))
	start := time.Unix(floor, 0).In(loc)
	start = time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, loc)
	for d := 0; d <= schedSearchDays; d++ {
		day := start.AddDate(0, 0, d)
		wd := int(day.Weekday())
		if wd == 0 {
			wd = 7
		}
		for _, hm := range jx.ToStrs(slots[strconv.Itoa(wd)]) {
			var h, m int
			fmt.Sscanf(hm, "%d:%d", &h, &m)
			at := time.Date(day.Year(), day.Month(), day.Day(), h, m, 0, 0, loc).Unix()
			if at < floor {
				continue
			}
			taken := false
			for _, b := range busy {
				if b-at <= schedBusySeconds && at-b <= schedBusySeconds {
					taken = true
				}
			}
			if taken {
				continue
			}
			shift := int64(0)
			if jitter > 0 {
				shift = int64(rand.Intn(2*jitter+1)-jitter) * 60
			}
			return jx.M{"slot_at": at, "publish_at": max(floor, at+shift)}
		}
	}
	return nil
}

// ---- Своя очередь публикаций: queued / sending / failed ----

var outboxRetryDelays = []int64{60, 300, 900}

const (
	outboxMaxAttempts    = 3
	outboxSendingTimeout = 900
)

var outboxFatal = []string{"was kicked", "chat not found", "forbidden", "not enough rights", "have no rights", "not a member", "bot was blocked",
	"токен бота недействителен", "бот не состоит", "telegram не нашёл канал", "нет права публиковать", "файл слишком большой",
	"telegram не принял файл", "файлы поста не найдены"}

// ErrNetworkRejected — отказ соцсети: повтор не исправит.
var ErrNetworkRejected = errors.New("соцсеть отклонила пост")

var postIDRe = regexp.MustCompile(`^[a-f0-9]{12}$`)

func (a *App) OutboxMediaDir(postID string) (string, error) {
	if !postIDRe.MatchString(postID) {
		return "", Bad("Некорректный номер поста.")
	}
	return filepath.Join(a.Cfg.DataDir, "outbox", postID), nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// outboxMediaMove — файлы поста — в папку поста (и из временной папки, и из кэша сайта).
func (a *App) outboxMediaMove(from, postID string, media []jx.M) ([]jx.M, error) {
	to, err := a.OutboxMediaDir(postID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(to, 0o775); err != nil {
		return nil, Bad("Не удалось создать папку поста.")
	}
	var out []jx.M
	for i, f := range media {
		src := jx.Str(f, "path")
		if src == "" || !fileExists(src) {
			os.RemoveAll(to)
			return nil, Bad("Не удалось сохранить файлы поста: файл пропал до постановки.")
		}
		dest := filepath.Join(to, filepath.Base(src))
		if fileExists(dest) {
			dest = filepath.Join(to, fmt.Sprintf("%d-%s", i, filepath.Base(src)))
		}
		if err := copyFile(src, dest); err != nil {
			os.RemoveAll(to)
			return nil, Bad("Не удалось сохранить файлы поста.")
		}
		g := jx.Copy(f)
		g["path"] = dest
		out = append(out, g)
	}
	if from != "" && strings.HasPrefix(from, os.TempDir()) {
		os.RemoveAll(from)
	}
	return out, nil
}

// PublishArticleInput — признак статьи для записи очереди.
func PublishArticleInput(raw any) jx.M {
	var in jx.M
	if s, ok := raw.(string); ok {
		in = jx.Unmarshal([]byte(s))
	} else {
		in = jx.ToMap(raw)
	}
	if in == nil || strings.TrimSpace(jx.Str(in, "title")) == "" {
		return nil
	}
	field := func(k string, n int) string { return trimRunes(strings.TrimSpace(tagRe.ReplaceAllString(jx.Str(in, k), "")), n) }
	return jx.M{"title": field("title", 300), "lead": field("lead", 400), "site": field("site", 100), "rubric": field("rubric", 100), "author": field("author", 100),
		"minutes": max(0, min(999, int(jx.Int(in, "minutes"))))}
}

// OutboxQueuePost — пост в нашу очередь: файлы переносятся в папку поста.
func (u *U) OutboxQueuePost(c *Channel, text string, prepared []Prepared, publishAt int64, slotAt *int64, work string, poll, article jx.M, group, record string) (jx.M, error) {
	id := randHex(6)
	media, err := u.outboxMediaMove(work, id, PreparedMaps(prepared))
	if err != nil {
		return nil, err
	}
	var slot any
	if slotAt != nil {
		slot = *slotAt
	}
	post := jx.M{"id": id, "created_at": now(), "publish_at": publishAt, "slot_at": slot, "slot": slotAt != nil, "channel": c.ID, "channel_title": c.ChatTitle,
		"text": PostCaptionClean(text), "media": media, "status": "queued", "attempts": 0, "error": "", "next_try_at": 0, "source_key": ""}
	if poll != nil {
		post["poll"] = poll
	}
	if article != nil {
		post["article"] = article
	}
	if group != "" {
		post["group"] = group
	}
	if record != "" {
		post["record"] = record
	}
	dir, _ := u.OutboxMediaDir(id)
	if !outboxMediaReady(post) {
		os.RemoveAll(dir)
		return nil, Bad("Не удалось сохранить файлы поста.")
	}
	post["thumb_id"] = u.PostThumbMake(media)
	if _, err := u.OutboxUpdate(func(q []jx.M) []jx.M { return append(q, post) }); err != nil {
		os.RemoveAll(dir)
		return nil, err
	}
	return jx.M{"scheduled": true, "id": id, "publish_at": publishAt, "native": false, "channel_title": c.ChatTitle}, nil
}

func (u *U) OutboxRemovePost(postID string) bool {
	n, _ := execCount(u, `DELETE FROM outbox WHERE id = $1 AND user_id = $2 AND status IN ('queued', 'failed')`, postID, u.ID)
	if n == 0 {
		return false
	}
	if d, err := u.OutboxMediaDir(postID); err == nil {
		os.RemoveAll(d)
	}
	return true
}

func execCount(u *U, sql string, args ...any) (int64, error) {
	tag, err := u.DB.Pool.Exec(u.Ctx, sql, args...)
	return tag.RowsAffected(), err
}

// OutboxDuplicatePost — копия поста очереди на другое время.
func (u *U) OutboxDuplicatePost(postID string, publishAt int64) (jx.M, error) {
	var src jx.M
	for _, e := range u.Outbox() {
		if jx.Str(e, "id") == postID && contains([]string{"queued", "failed"}, jx.Str(e, "status")) {
			src = e
		}
	}
	if src == nil {
		return nil, nil
	}
	id := randHex(6)
	from, _ := u.OutboxMediaDir(postID)
	to, _ := u.OutboxMediaDir(id)
	if err := os.MkdirAll(to, 0o775); err != nil {
		return nil, Bad("Не удалось создать папку поста.")
	}
	var media []jx.M
	for _, f := range jx.Maps(src, "media") {
		name := filepath.Base(jx.Str(f, "path"))
		if err := copyFile(filepath.Join(from, name), filepath.Join(to, name)); err != nil {
			os.RemoveAll(to)
			return nil, Bad("Не удалось скопировать файлы поста.")
		}
		g := jx.Copy(f)
		g["path"] = filepath.Join(to, name)
		media = append(media, g)
	}
	cp := jx.Merge(src, jx.M{"id": id, "created_at": now(), "publish_at": publishAt, "slot_at": nil, "slot": false, "status": "queued", "attempts": 0, "error": "",
		"next_try_at": 0, "group": randHex(6), "media": media})
	if _, err := u.OutboxUpdate(func(q []jx.M) []jx.M { return append(q, cp) }); err != nil {
		os.RemoveAll(to)
		return nil, err
	}
	return cp, nil
}

func (a *App) outboxRowSave(ctx context.Context, postID string, post jx.M, status string) {
	post["status"] = status
	a.DB.Pool.Exec(ctx, `UPDATE outbox SET status = $1, publish_at = $2, data = $3 WHERE id = $4`, status, jx.Int(post, "publish_at"), jx.Marshal(post), postID)
}

func outboxErrorIsFatal(e string) bool {
	t := strings.ToLower(e)
	for _, p := range outboxFatal {
		if strings.Contains(t, p) {
			return true
		}
	}
	return false
}

func (a *App) outboxRetryOrFail(ctx context.Context, postID, uid string, post jx.M, errText string, force bool) bool {
	attempts := max(1, int(jx.Int(post, "attempts")))
	failed := force || outboxErrorIsFatal(errText) || attempts >= outboxMaxAttempts
	post["error"] = errText
	delete(post, "sending_at")
	status := "queued"
	if failed {
		status = "failed"
	} else {
		post["next_try_at"] = now() + outboxRetryDelays[attempts-1]
	}
	a.outboxRowSave(ctx, postID, post, status)
	if failed {
		a.outboxNotifyFailed(ctx, uid, post)
	}
	return failed
}

func (a *App) outboxFinishSent(ctx context.Context, postID, uid string, post jx.M, c *Channel, sent *SentResult) {
	shown := jx.Maps(post, "media")
	if sent != nil && sent.MediaKept != nil {
		shown = PreparedMaps(sent.MediaKept)
	}
	u := a.User(ctx, uid, nil)
	a.DB.Pool.Exec(ctx, `DELETE FROM outbox WHERE id = $1`, postID)
	url := ""
	var skipped []string
	if sent != nil {
		url, skipped = sent.URL, sent.Skipped
	}
	entry := jx.Merge(TelegramPostSummary(jx.Str(post, "text"), len(shown)), jx.M{"at": now(), "scheduled_for": jx.Int(post, "publish_at"), "url": url, "status": "ok",
		"error": "", "channel": c.ID, "channel_title": c.ChatTitle, "media_src": MediaSourcesOf(shown), "skipped": orStrs(skipped), "thumb_id": jx.Str(post, "thumb_id")})
	u.TelegramLogPublished(entry)
	if d, err := a.OutboxMediaDir(postID); err == nil {
		os.RemoveAll(d)
	}
}

func orStrs(l []string) []string {
	if l == nil {
		return []string{}
	}
	return l
}

func outboxMediaReady(post jx.M) bool {
	for _, f := range jx.Maps(post, "media") {
		if !fileExists(jx.Str(f, "path")) {
			return false
		}
	}
	return true
}

// ---- Миниатюры карточек календаря ----

const (
	thumbSide     = 160
	thumbKeepDays = 90
)

func (a *App) ThumbDir() string { return filepath.Join(a.Cfg.DataDir, "thumbs") }

func (a *App) ThumbFile(id string) string { return filepath.Join(a.ThumbDir(), id+".jpg") }

// PostThumbMake — миниатюра из первого файла поста (картинка — уменьшенная копия, видео — кадр); id или "".
func (a *App) PostThumbMake(media []jx.M) string {
	if len(media) == 0 {
		return ""
	}
	f := media[0]
	src := jx.Str(f, "path")
	if src == "" || !fileExists(src) {
		return ""
	}
	os.MkdirAll(a.ThumbDir(), 0o775)
	id := randHex(6)
	out := a.ThumbFile(id)
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease", thumbSide, thumbSide)
	if jx.Str(f, "kind") == "video" {
		if a.FFmpegRun([]string{"-ss", "1", "-i", src, "-frames:v", "1", "-vf", vf, "-q:v", "4", "-f", "image2"}, out, time.Minute) ||
			a.FFmpegRun([]string{"-i", src, "-frames:v", "1", "-vf", vf, "-q:v", "4", "-f", "image2"}, out, time.Minute) {
			return id
		}
		return ""
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return ""
	}
	j := imgx.Shrink(b, thumbSide, 80, true)
	if len(j) == 0 || os.WriteFile(out, j, 0o644) != nil {
		return ""
	}
	return id
}

// ThumbOwned — миниатюра в очереди или журнале этого пользователя.
func (u *U) ThumbOwned(id string) bool {
	if !postIDRe.MatchString(id) {
		return false
	}
	var ok bool
	u.DB.Pool.QueryRow(u.Ctx, `SELECT EXISTS (SELECT 1 FROM outbox WHERE user_id = $1 AND data->>'thumb_id' = $2) OR EXISTS (SELECT 1 FROM published WHERE user_id = $1 AND data->>'thumb_id' = $2)`,
		u.ID, id).Scan(&ok)
	return ok
}

// ThumbGC — миниатюры старше 90 дней, на которые никто не ссылается.
func (a *App) ThumbGC(ctx context.Context) int {
	entries, _ := os.ReadDir(a.ThumbDir())
	n := 0
	for _, e := range entries {
		info, err := e.Info()
		if err != nil || time.Since(info.ModTime()) < thumbKeepDays*24*time.Hour {
			continue
		}
		id := strings.TrimSuffix(e.Name(), ".jpg")
		var used bool
		a.DB.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM outbox WHERE data->>'thumb_id' = $1) OR EXISTS (SELECT 1 FROM published WHERE data->>'thumb_id' = $1)`, id).Scan(&used)
		if !used && os.Remove(filepath.Join(a.ThumbDir(), e.Name())) == nil {
			n++
		}
	}
	return n
}

// OutboxRepair — ремонт очереди: пропавший файл — из кэша сайта или по ссылке; без apply — только отчёт.
func (a *App) OutboxRepair(ctx context.Context, apply bool, onlyUID string) []string {
	var report []string
	var users []string
	if onlyUID != "" {
		users = []string{onlyUID}
	} else {
		rows, _ := a.DB.Pool.Query(ctx, `SELECT DISTINCT user_id FROM outbox`)
		for rows != nil && rows.Next() {
			var id string
			rows.Scan(&id)
			users = append(users, id)
		}
		if rows != nil {
			rows.Close()
		}
	}
	for _, uid := range users {
		u := a.User(ctx, uid, nil)
		changed := map[string]jx.M{}
		for _, post := range u.Outbox() {
			st := jx.Str(post, "status")
			if st != "queued" && st != "failed" {
				continue
			}
			id := jx.Str(post, "id")
			dir, err := a.OutboxMediaDir(id)
			if err != nil {
				continue
			}
			var lines []string
			ok := true
			for k, f := range jx.Maps(post, "media") {
				name := filepath.Base(jx.Str(f, "path"))
				if name == "" || fileExists(filepath.Join(dir, name)) {
					continue
				}
				src := jx.Str(f, "src")
				if c := a.MediaPath(src); c != "" && fileExists(c) {
					lines = append(lines, fmt.Sprintf("файл %d: найден в кэше", k+1))
					if apply {
						os.MkdirAll(dir, 0o775)
						if copyFile(c, filepath.Join(dir, name)) != nil {
							ok = false
						}
					}
				} else if strings.HasPrefix(src, "http") {
					lines = append(lines, fmt.Sprintf("файл %d: нет в кэше, скачиваю по ссылке", k+1))
					if apply {
						os.MkdirAll(dir, 0o775)
						if err := a.fetchPublicMedia(src, filepath.Join(dir, name), config.TelegramUploadMaxBytes); err != nil {
							ok = false
							lines = append(lines, fmt.Sprintf("файл %d: %s", k+1, err))
						}
					}
				} else {
					ok = false
					lines = append(lines, fmt.Sprintf("файл %d: не найден ни в кэше, ни по ссылке", k+1))
				}
			}
			nw := jx.Copy(post)
			if clean := PostCaptionClean(jx.Str(post, "text")); clean != jx.Str(post, "text") {
				nw["text"] = clean
				lines = append(lines, "заглушка в подписи очищена")
			}
			if st == "failed" && ok && strings.Contains(strings.ToLower(jx.Str(post, "error")), "файлы поста не найдены") {
				if jx.Int(post, "publish_at") > now() {
					for k, v := range (jx.M{"status": "queued", "error": "", "attempts": 0, "next_try_at": 0}) {
						nw[k] = v
					}
					lines = append(lines, "снова в очередь")
				} else {
					lines = append(lines, "время уже прошло — остаётся «не вышел»")
				}
			} else if !ok {
				lines = append(lines, "файл не восстановить — пост не выйдет")
			}
			if len(lines) == 0 {
				continue
			}
			tag := ""
			if !apply {
				tag = " · (проверка)"
			}
			report = append(report, fmt.Sprintf("%s · %s · %s · %s%s", uid[:8], id, time.Unix(jx.Int(post, "publish_at"), 0).Format("15:04 02.01"), st, tag))
			for _, l := range lines {
				report = append(report, "    "+l)
			}
			changed[id] = nw
		}
		if apply && len(changed) > 0 {
			u.OutboxUpdate(func(q []jx.M) []jx.M {
				for i, e := range q {
					if c, ok := changed[jx.Str(e, "id")]; ok {
						q[i] = c
					}
				}
				return q
			})
		}
	}
	if len(report) == 0 {
		return []string{"Проблем с очередью не найдено."}
	}
	return report
}

// ---- Публикатор: горутина сервера (раньше — cron tools/publisher.php) ----

var publisherWake = make(chan struct{}, 1)

func (u *U) KickPublisher() {
	select {
	case publisherWake <- struct{}{}:
	default:
	}
}

// RunPublisher — созревшие посты всех пользователей уходят по времени; раз в 20 с и по сигналу.
func (a *App) RunPublisher(ctx context.Context) {
	t := time.NewTicker(20 * time.Second)
	defer t.Stop()
	for {
		r := a.OutboxProcessDue(ctx)
		if r["sent"]+r["failed"] > 0 {
			fmt.Printf("публикатор: отправлено %d, не вышло %d\n", r["sent"], r["failed"])
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-publisherWake:
		}
	}
}

// OutboxProcessDue — отправить созревшие посты. Возвращает sent и failed.
func (a *App) OutboxProcessDue(ctx context.Context) map[string]int {
	res := map[string]int{"sent": 0, "failed": 0}
	t := now()
	rows, err := a.DB.Pool.Query(ctx, `SELECT id, user_id, data FROM outbox WHERE status = 'sending'`)
	if err == nil {
		type stuck struct {
			id, uid string
			post    jx.M
		}
		var list []stuck
		for rows.Next() {
			var id, uid string
			var raw []byte
			if rows.Scan(&id, &uid, &raw) == nil {
				list = append(list, stuck{id, uid, jx.Unmarshal(raw)})
			}
		}
		rows.Close()
		for _, s := range list {
			if jx.Int(s.post, "sending_at") > t-outboxSendingTimeout {
				continue
			}
			a.outboxRetryOrFail(ctx, s.id, s.uid, s.post, "Отправка прервалась: проверьте канал — пост мог выйти. Если его нет — нажмите «Повторить».", true)
			res["failed"]++
		}
	}
	rows, err = a.DB.Pool.Query(ctx, `SELECT id FROM outbox WHERE status = 'queued' AND publish_at <= $1 ORDER BY publish_at LIMIT 60`, t)
	if err != nil {
		return res
	}
	var ids []string
	for rows.Next() {
		var id string
		rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			break
		}
		var uid string
		var post jx.M
		err := a.DB.Tx(ctx, func(tx pgx.Tx) error {
			var status string
			var raw []byte
			if err := tx.QueryRow(ctx, `SELECT user_id, status, data FROM outbox WHERE id = $1 FOR UPDATE`, id).Scan(&uid, &status, &raw); err != nil || status != "queued" {
				return errSkip
			}
			post = jx.Unmarshal(raw)
			if jx.Int(post, "next_try_at") > t {
				return errSkip
			}
			post["status"], post["attempts"], post["sending_at"] = "sending", jx.Int(post, "attempts")+1, t
			_, err := tx.Exec(ctx, `UPDATE outbox SET status = 'sending', data = $1 WHERE id = $2`, jx.Marshal(post), id)
			return err
		})
		if err != nil {
			continue
		}
		c := a.ChannelForPost(ctx, uid, jx.Str(post, "channel"))
		if c == nil {
			if a.outboxRetryOrFail(ctx, id, uid, post, "Канал отключён", true) {
				res["failed"]++
			}
			continue
		}
		sent, err := func() (*SentResult, error) {
			if !outboxMediaReady(post) {
				return nil, Bad("Файлы поста не найдены.")
			}
			return a.OutboxSendPost(ctx, uid, post, c)
		}()
		if err != nil {
			if a.outboxRetryOrFail(ctx, id, uid, post, err.Error(), errors.Is(err, ErrNetworkRejected)) {
				res["failed"]++
			}
			continue
		}
		a.outboxFinishSent(ctx, id, uid, post, c, sent)
		if rec := jx.Str(post, "record"); rec != "" {
			a.User(ctx, uid, nil).ContentMarkPosted(rec)
		}
		res["sent"]++
	}
	return res
}

func (a *App) outboxNotifyFailed(ctx context.Context, uid string, post jx.M) {
	var tgID int64
	a.DB.Pool.QueryRow(ctx, `SELECT COALESCE(telegram_id, 0) FROM users WHERE id = $1`, uid).Scan(&tgID)
	bot := a.TelegramLoginBot(ctx)
	if tgID <= 0 || bot == nil {
		return
	}
	u := a.User(ctx, uid, nil)
	loc := time.Local
	if c := u.Channel(jx.Str(post, "channel")); c != nil {
		loc, _ = loadTZ(jx.Str(ChannelSchedule(c), "tz"))
	}
	when := time.Unix(jx.Int(post, "publish_at"), 0).In(loc).Format("02.01.2006 15:04")
	a.TelegramBotRequest(ctx, jx.Str(bot, "token"), "sendMessage", map[string]string{"chat_id": strconv.FormatInt(tgID, 10), "disable_web_page_preview": "true",
		"text": fmt.Sprintf("Пост в «%s» на %s не вышел: %s. Повторить или удалить — в «Личном кабинете» → «Соцсети» → «Очередь».", jx.Str(post, "channel_title"), when, jx.Str(post, "error"))},
		nil, 30*time.Second, nil)
}

var errSkip = errors.New("пропуск")
