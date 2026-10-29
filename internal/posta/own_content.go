package posta

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"

	"posta/internal/imgx"
	"posta/internal/jx"
)

// «Свой контент», «Лучшие посты источника», X по ссылкам, «Зеркало» и API: посты кладутся в подборку без оценки моделью.

const (
	OwnMaxFiles     = 10
	ownCSVMaxBytes  = 1 << 20
	ownCSVMaxRows   = 100
	ownCSVMaxErrors = 10
	ownTextMax      = 4096
	ownDayFiles     = 1000
	yadiskPerReq    = 100
	yadiskSeconds   = 50
	apiRatePerHour  = 60
)

func (u *U) ownSourceKey() string { return "own:" + u.ID }

// OwnPostItem — запись пула для своего поста: media — адреса картинок в кэше (несколько — галерея).
func (u *U) OwnPostItem(media []string, text, label string, postedAt int64) (jx.M, error) {
	if len(media) == 0 {
		return nil, Bad("В посте нет картинки.")
	}
	text = trimRunes(strings.TrimSpace(text), ownTextMax)
	if postedAt <= 0 {
		postedAt = now()
	}
	item := jx.M{"image": media[0], "images": []string{media[0]}, "image_local": media[0], "video": "", "text": text, "captions": []string{text}, "description": text,
		"source": "Свой контент · " + label, "source_key": u.ownSourceKey(), "source_url": "", "posted_at": postedAt, "pooled_at": now(),
		"topic": u.CurrentTopic(), "relevance": 100, "rating": 100, "own": true}
	if len(media) > 1 {
		var g []jx.M
		for _, m := range media {
			g = append(g, jx.M{"image": m, "video": "", "image_local": m})
		}
		item["gallery"] = g
	}
	return item, nil
}

// OwnStoreImage — картинка в кэш медиа (ключ — пользователь и хэш содержимого).
func (u *U) OwnStoreImage(b []byte) (string, error) {
	if err := imgx.Validate(b); err != nil {
		return "", Bad("%s", err.Error())
	}
	h := sha1.Sum(b)
	p := u.MediaStoreImage("own:"+u.ID+":"+hex.EncodeToString(h[:]), b)
	if p == "" {
		return "", Bad("Не удалось сохранить картинку на сервере.")
	}
	return p, nil
}

// OwnAddToPool — новые посты в начало подборки; уже известные пропускаются.
func (u *U) OwnAddToPool(items []jx.M) int {
	if len(items) == 0 {
		return 0
	}
	known := u.collectKnown()
	var fresh []jx.M
	for _, i := range items {
		if !candidateIsKnown(i, known) {
			fresh = append(fresh, i)
		}
	}
	if len(fresh) > 0 {
		u.PoolUpdate(func(pool []jx.M) []jx.M { return append(append([]jx.M{}, fresh...), pool...) })
		u.PoolItemsEnqueueVideos(u.Ctx, fresh, u.ID)
	}
	return len(fresh)
}

// ownQuotaTake — суточный лимит файлов: проверка до записи, чтобы отказ не тратил лимит.
func (u *U) ownQuotaTake(n int) error {
	return u.dailyQuota("own_files", n, ownDayFiles, "За сутки — не больше %d файлов: продолжите завтра.")
}

func (a *App) dailyQuotaFor(uid, name string, n, limit int, msg string) error {
	key := name + ":" + uid + ":" + time.Now().Format("2006-01-02")
	var err error
	KVUpdate(a, a.ctxOrBg(), key, 48*time.Hour, func(cur int, _ bool) *int {
		if cur+n > limit {
			err = Bad(msg, limit)
			return nil
		}
		cur += n
		return &cur
	})
	return err
}

func (u *U) dailyQuota(name string, n, limit int, msg string) error {
	return u.dailyQuotaFor(u.ID, name, n, limit, msg)
}

var videoNameRe = regexp.MustCompile(`(?i)\.(mp4|mov|webm|avi|mkv|m4v)$`)

// Upload — файл из запроса (временный путь на диске).
type Upload struct {
	Name, Path string
	Err        string
}

// OwnUpload — картинки: каждый файл — отдельный пост или один пост-альбом.
func (u *U) OwnUpload(files []Upload, text string, album bool) (jx.M, error) {
	if len(files) == 0 {
		return nil, Bad("Выберите файлы с картинками.")
	}
	if len(files) > OwnMaxFiles {
		return nil, Bad("За раз можно загрузить до %d файлов.", OwnMaxFiles)
	}
	if err := u.ownQuotaTake(len(files)); err != nil {
		return nil, err
	}
	type stored struct{ url, name string }
	var ok []stored
	errs := []string{}
	for _, f := range files {
		name := trimRunes(filepath.Base(f.Name), 60)
		if name == "" || name == "." {
			name = "файл"
		}
		err := func() error {
			if f.Err != "" {
				return Bad("%s", f.Err)
			}
			b, err := os.ReadFile(f.Path)
			if err != nil {
				return Bad("Не удалось загрузить файл.")
			}
			if videoNameRe.MatchString(name) || strings.HasPrefix(httpDetect(b), "video/") {
				return Bad("Видео пока не загружается: добавьте его через редактор поста.")
			}
			p, err := u.OwnStoreImage(b)
			if err != nil {
				return err
			}
			ok = append(ok, stored{p, name})
			return nil
		}()
		if err != nil {
			errs = append(errs, name+": "+err.Error())
		}
	}
	var items []jx.M
	if len(ok) > 0 && album {
		var urls []string
		for _, s := range ok {
			urls = append(urls, s.url)
		}
		label := ok[0].name
		if len(ok) > 1 {
			label = fmt.Sprintf("%d файлов", len(ok))
		}
		it, _ := u.OwnPostItem(urls, text, label, 0)
		items = append(items, it)
	} else {
		for _, s := range ok {
			it, _ := u.OwnPostItem([]string{s.url}, text, s.name, 0)
			items = append(items, it)
		}
	}
	return jx.M{"added": u.OwnAddToPool(items), "skipped": len(files) - len(ok), "errors": errs}, nil
}

type csvRow struct {
	Line              int
	Text, Image, Date string
}

// ownCSVRows — строки таблицы (text, image, date); разделитель ; или , по первой строке, UTF-8 или Windows-1251.
func ownCSVRows(data []byte) ([]csvRow, error) {
	data = bytes.TrimPrefix(data, []byte("\xEF\xBB\xBF"))
	if !utf8.Valid(data) {
		if d, err := charmap.Windows1251.NewDecoder().Bytes(data); err == nil {
			data = d
		}
	}
	first, _, _ := bytes.Cut(data, []byte("\n"))
	delim := ','
	if bytes.Count(first, []byte(";")) >= bytes.Count(first, []byte(",")) {
		delim = ';'
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma, r.FieldsPerRecord, r.LazyQuotes = delim, -1, true
	aliases := map[string]string{"text": "text", "текст": "text", "image": "image", "картинка": "image", "image_url": "image", "картинки": "image", "date": "date", "дата": "date"}
	var cols []string
	var rows []csvRow
	for line := 1; ; line++ {
		cells, err := r.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, Bad("Таблица не читается: строка %d.", line)
		}
		if len(cells) == 1 && strings.TrimSpace(cells[0]) == "" {
			continue
		}
		if cols == nil {
			for _, h := range cells {
				cols = append(cols, aliases[strings.ToLower(strings.TrimSpace(h))])
			}
			if !contains(cols, "image") {
				return nil, Bad("В таблице нет колонки с картинками: назовите её image, картинка или image_url.")
			}
			continue
		}
		row := map[string]string{}
		for i, name := range cols {
			if name != "" && i < len(cells) {
				if _, set := row[name]; !set {
					row[name] = strings.TrimSpace(cells[i])
				}
			}
		}
		rows = append(rows, csvRow{line, row["text"], row["image"], row["date"]})
	}
	return rows, nil
}

var csvURLSplitRe = regexp.MustCompile(`[\s|]+`)

func parseLooseDate(s string) int64 {
	for _, f := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02 15:04", "2006-01-02", "02.01.2006 15:04", "02.01.2006"} {
		if t, err := time.ParseInLocation(f, strings.TrimSpace(s), time.Local); err == nil {
			return t.Unix()
		}
	}
	return 0
}

// OwnImportCSV — строка — пост, в колонке картинок одна или несколько ссылок (через пробел или |).
func (u *U) OwnImportCSV(data []byte) (jx.M, error) {
	if len(data) > ownCSVMaxBytes {
		return nil, Bad("CSV больше 1 МБ.")
	}
	rows, err := ownCSVRows(data)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, Bad("В таблице нет строк с данными.")
	}
	if len(rows) > ownCSVMaxRows {
		return nil, Bad("В таблице больше %d строк: загрузите её частями.", ownCSVMaxRows)
	}
	if err := u.ownQuotaTake(len(rows)); err != nil {
		return nil, err
	}
	var items []jx.M
	errs := []string{}
	failed := 0
	for _, row := range rows {
		err := func() error {
			var urls []string
			for _, s := range csvURLSplitRe.Split(row.Image, -1) {
				if s != "" {
					urls = append(urls, s)
				}
			}
			if len(urls) == 0 {
				return Bad("нет ссылки на картинку")
			}
			var media []string
			for _, s := range urls[:min(len(urls), OwnMaxFiles)] {
				if !strings.HasPrefix(strings.ToLower(s), "http://") && !strings.HasPrefix(strings.ToLower(s), "https://") {
					return Bad("«%s» — не ссылка http(s)", s)
				}
				b, err := u.FetchPublicImage(s)
				if err != nil {
					return err
				}
				p, err := u.OwnStoreImage(b)
				if err != nil {
					return err
				}
				media = append(media, p)
			}
			it, err := u.OwnPostItem(media, row.Text, "CSV", parseLooseDate(row.Date))
			if err == nil {
				items = append(items, it)
			}
			return err
		}()
		if err != nil {
			failed++
			if len(errs) < ownCSVMaxErrors {
				errs = append(errs, fmt.Sprintf("Строка %d: %s", row.Line, err))
			}
		}
	}
	added := u.OwnAddToPool(items)
	return jx.M{"added": added, "skipped": failed + len(items) - added, "errors": errs}, nil
}

// ---- API: пост в подборку по токену (Make, n8n, свои скрипты) ----

func sha256Hex(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

// APITokenCreate — новый токен (хранится только sha256; сам токен показывается один раз).
func (u *U) APITokenCreate() string {
	token := randHex(24)
	u.KVSet(u.Ctx, "api_token:"+u.ID, sha256Hex(token), 0)
	return token
}

func (u *U) APITokenRevoke()      { u.KVDelete(u.Ctx, "api_token:"+u.ID) }
func (u *U) APITokenExists() bool { return u.KVRaw(u.Ctx, "api_token:"+u.ID) != nil }

var apiTokenRe = regexp.MustCompile(`^[a-f0-9]{48}$`)

// APITokenUser — пользователь по токену или "".
func (a *App) APITokenUser(ctx context.Context, token string) string {
	if !apiTokenRe.MatchString(token) {
		return ""
	}
	var key string
	a.DB.Pool.QueryRow(ctx, `SELECT key FROM kv WHERE key LIKE 'api_token:%' AND value = to_jsonb($1::text)`, sha256Hex(token)).Scan(&key)
	return strings.TrimPrefix(key, "api_token:")
}

// APIRateTake — не больше 60 запросов в час.
func (u *U) APIRateTake() bool {
	key := "api_rate:" + u.ID + ":" + time.Now().Format("2006010215")
	n, _ := KVUpdate(u.App, u.Ctx, key, 2*time.Hour, func(n int, _ bool) *int { n++; return &n })
	return n <= apiRatePerHour
}

// APIAddPost — text и image_url или image_urls (до 10).
func (u *U) APIAddPost(in jx.M) (jx.M, error) {
	var urls []string
	if l := jx.Strs(in, "image_urls"); len(l) > 0 {
		urls = l
	} else if s := jx.Str(in, "image_url"); s != "" {
		urls = []string{s}
	}
	var clean []string
	for _, s := range urls {
		if s = strings.TrimSpace(s); s != "" {
			clean = append(clean, s)
		}
	}
	if len(clean) == 0 {
		return nil, Bad("Нужна ссылка на картинку: image_url или image_urls.")
	}
	if len(clean) > OwnMaxFiles {
		return nil, Bad("Картинок не больше %d.", OwnMaxFiles)
	}
	var media []string
	for _, s := range clean {
		b, err := u.FetchPublicImage(s)
		if err != nil {
			return nil, err
		}
		p, err := u.OwnStoreImage(b)
		if err != nil {
			return nil, err
		}
		media = append(media, p)
	}
	text, _ := in["text"].(string)
	it, err := u.OwnPostItem(media, text, "API", 0)
	if err != nil {
		return nil, err
	}
	return jx.M{"added": u.OwnAddToPool([]jx.M{it})}, nil
}

// ---- Яндекс Диск ----

var yadiskRe = regexp.MustCompile(`^https://((?:disk\.yandex\.(?:ru|com)|yadi\.sk))/d/([A-Za-z0-9_-]{4,})(?:[/?#]|$)`)

func YadiskPublicURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	m := yadiskRe.FindStringSubmatch(raw)
	if len(raw) > 300 || m == nil {
		return "", Bad("Нужна публичная ссылка на папку Яндекс Диска: disk.yandex.ru/d/… или yadi.sk/d/…")
	}
	return "https://" + m[1] + "/d/" + m[2], nil
}

func (a *App) yadiskAPI(query string, params url.Values) (jx.M, error) {
	r, err := a.FetchPublic("https://cloud-api.yandex.net/v1/disk/public/resources"+query+"?"+params.Encode(), FetchOpts{MaxBytes: 2 << 20, Timeout: 20 * time.Second})
	if err != nil {
		return nil, Bad("Яндекс Диск не ответил: попробуйте позже.")
	}
	data := jx.Unmarshal(r.Bytes)
	if data != nil && jx.Has(data, "error") {
		if r.Status == 404 {
			return nil, Bad("Папка не найдена: проверьте, что к ней открыт доступ по ссылке.")
		}
		msg := jx.Str(data, "message")
		if msg == "" {
			msg = jx.Str(data, "error")
		}
		return nil, Bad("Яндекс Диск ответил ошибкой: %s", msg)
	}
	if !r.OK || data == nil {
		return nil, Bad("Яндекс Диск не ответил: попробуйте позже.")
	}
	return data, nil
}

func (a *App) yadiskCaption(public, path string) string {
	d, err := a.yadiskAPI("/download", url.Values{"public_key": {public}, "path": {path}})
	if err != nil {
		return ""
	}
	r, err := a.FetchPublic(jx.Str(d, "href"), FetchOpts{MaxBytes: 64 << 10, Timeout: 15 * time.Second})
	if err != nil || !r.OK {
		return ""
	}
	return trimRunes(strings.TrimSpace(string(r.Bytes)), ownTextMax)
}

// OwnImportYadisk — картинки из публичной папки: до 100 за нажатие, продолжение — по смещению.
func (u *U) OwnImportYadisk(public string) (jx.M, error) {
	link, err := YadiskPublicURL(public)
	if err != nil {
		return nil, err
	}
	h := sha1.Sum([]byte(link))
	key := "yadisk:" + u.ID + ":" + hex.EncodeToString(h[:])
	offset := KV(u.App, u.Ctx, key, 0)
	d, err := u.yadiskAPI("", url.Values{"public_key": {link}, "limit": {fmt.Sprint(yadiskPerReq)}, "offset": {fmt.Sprint(offset)}})
	if err != nil {
		return nil, err
	}
	folder := jx.Map(d, "_embedded")
	total := int(jx.Int(folder, "total"))
	items := jx.Maps(folder, "items")
	txt := map[string]string{}
	images := 0
	for _, i := range items {
		name := jx.Str(i, "name")
		if jx.Str(i, "type") == "file" && strings.ToLower(filepath.Ext(name)) == ".txt" {
			txt[strings.TrimSuffix(name, filepath.Ext(name))] = jx.Str(i, "path")
		}
		if jx.Str(i, "media_type") == "image" {
			images++
		}
	}
	if err := u.ownQuotaTake(images); err != nil {
		return nil, err
	}
	started := time.Now()
	done, skipped := 0, 0
	var posts []jx.M
	errs := []string{}
	for _, it := range items {
		if time.Since(started) > yadiskSeconds*time.Second {
			break
		}
		done++
		if jx.Str(it, "type") != "file" {
			continue
		}
		switch jx.Str(it, "media_type") {
		case "video":
			skipped++
			continue
		case "image":
		default:
			continue
		}
		name := jx.Str(it, "name")
		err := func() error {
			dl, err := u.yadiskAPI("/download", url.Values{"public_key": {link}, "path": {jx.Str(it, "path")}})
			if err != nil {
				return err
			}
			b, err := u.FetchPublicImage(jx.Str(dl, "href"))
			if err != nil {
				return err
			}
			p, err := u.OwnStoreImage(b)
			if err != nil {
				return err
			}
			caption := ""
			if t, ok := txt[strings.TrimSuffix(name, filepath.Ext(name))]; ok {
				caption = u.yadiskCaption(link, t)
			}
			post, _ := u.OwnPostItem([]string{p}, caption, "Яндекс Диск", 0)
			posts = append(posts, post)
			return nil
		}()
		if err != nil && len(errs) < ownCSVMaxErrors {
			errs = append(errs, name+": "+err.Error())
		}
	}
	added := u.OwnAddToPool(posts)
	next := offset + done
	finished := next >= total
	if finished {
		next = 0
	}
	u.KVSet(u.Ctx, key, next, 30*24*time.Hour)
	return jx.M{"added": added, "processed": done, "total": total, "done": finished, "skipped": skipped, "errors": errs}, nil
}

// ---- X (Twitter): отдельные твиты по ссылке ----

const (
	xLinksMax    = 20
	xLinksPerDay = 100
)

var xStatusRe = regexp.MustCompile(`(?i)^https?://(?:www\.|mobile\.)?(?:x|twitter)\.com/[A-Za-z0-9_]{1,15}/status(?:es)?/(\d{1,20})(?:[/?#]|$)`)

func XStatusID(link string) string {
	if m := xStatusRe.FindStringSubmatch(strings.TrimSpace(link)); m != nil {
		return m[1]
	}
	return ""
}

type xTweet struct {
	Text      string
	PostedAt  int64
	Images    []string
	VideoURL  string
	HasVideo  bool
}

func (a *App) xFetchPost(id string) (*xTweet, error) {
	r, err := a.FetchPublic("https://cdn.syndication.twimg.com/tweet-result?id="+id+"&token=x", FetchOpts{MaxBytes: 2 << 20})
	var data jx.M
	if err == nil && r.OK {
		data = jx.Unmarshal(r.Bytes)
	}
	if data == nil {
		return nil, Bad("Твит не найден, закрыт или X временно не отвечает.")
	}
	t := &xTweet{Text: strings.TrimSpace(jx.Str(data, "text"))}
	if ts, err := time.Parse(time.RFC3339, jx.Str(data, "created_at")); err == nil {
		t.PostedAt = ts.Unix()
	}
	var video jx.M
	for _, m := range jx.Maps(data, "mediaDetails") {
		switch jx.Str(m, "type") {
		case "photo":
			if s := jx.Str(m, "media_url_https"); strings.HasPrefix(s, "https://pbs.twimg.com/") {
				t.Images = append(t.Images, s)
			}
		case "video", "animated_gif":
			if video == nil {
				video = m
			}
		}
	}
	poster := jx.Str(video, "media_url_https")
	if !strings.HasPrefix(poster, "https://pbs.twimg.com/") {
		poster = ""
	}
	t.HasVideo = video != nil || jx.Has(data, "video")
	if len(t.Images) == 0 && poster != "" {
		t.Images = []string{poster}
	}
	if len(t.Images) == 0 && !t.HasVideo {
		return nil, Bad("В посте нет картинки или видео.")
	}
	if video != nil {
		rate := int64(-1)
		for _, v := range jx.Maps(jx.Map(video, "video_info"), "variants") {
			u := jx.Str(v, "url")
			if jx.Str(v, "content_type") == "video/mp4" && strings.HasPrefix(u, "https://video.twimg.com/") && jx.Int(v, "bitrate") > rate {
				t.VideoURL, rate = u, jx.Int(v, "bitrate")
			}
		}
	}
	return t, nil
}

func (u *U) xPostItem(link string, t *xTweet) (jx.M, error) {
	var media []string
	for _, s := range t.Images[:min(len(t.Images), OwnMaxFiles)] {
		b, err := u.FetchPublicImage(s)
		if err != nil {
			return nil, err
		}
		p, err := u.OwnStoreImage(b)
		if err != nil {
			return nil, err
		}
		media = append(media, p)
	}
	item, err := u.OwnPostItem(media, t.Text, "X", t.PostedAt)
	if err != nil {
		return nil, err
	}
	item["source_url"], item["url"] = link, link
	local := ""
	if t.VideoURL != "" {
		local = u.MediaStoreVideo(t.VideoURL, "", false, nil)
	}
	if local != "" {
		item["video"], item["video_local"] = local, local
	} else if t.HasVideo {
		item["video_via"], item["video_page"] = "ytdlp", link
	}
	return item, nil
}

// XImportLinks — импорт твитов: ошибка одной ссылки не останавливает остальные.
func (u *U) XImportLinks(links []string) (jx.M, error) {
	var clean []string
	for _, l := range links {
		if l = strings.TrimSpace(l); l != "" {
			clean = append(clean, l)
		}
	}
	if len(clean) == 0 {
		return nil, Bad("Вставьте ссылки на твиты, по одной в строке.")
	}
	if len(clean) > xLinksMax {
		return nil, Bad("За раз — не больше %d ссылок на твиты.", xLinksMax)
	}
	if err := u.dailyQuota("x_links", len(clean), xLinksPerDay, "За сутки — не больше %d ссылок на твиты: продолжите завтра."); err != nil {
		return nil, err
	}
	var items []jx.M
	errs := [][]string{}
	for _, link := range clean {
		err := func() error {
			id := XStatusID(link)
			if id == "" {
				return Bad("не похоже на ссылку на твит: x.com/имя/status/номер")
			}
			t, err := u.xFetchPost(id)
			if err != nil {
				return err
			}
			item, err := u.xPostItem(link, t)
			if err != nil {
				return err
			}
			if u.UAItemBlocked(item) != "" {
				return Bad("украинский твит — в Posta не принимаем")
			}
			items = append(items, item)
			return nil
		}()
		if err != nil {
			errs = append(errs, []string{link, err.Error()})
		}
	}
	added := u.OwnAddToPool(items)
	return jx.M{"added": added, "skipped": len(clean) - added, "errors": errs}, nil
}

// ---- «Лучшие посты источника» ----

var BestPostsDays = []int{1, 7, 30}

const (
	bestResults    = 30
	bestTakeMax    = 20
	BestTakeVideos = 3
	bestTGPages    = 5
	bestVKRequests = 3
	bestCacheSecs  = 900
	bestPerDay     = 30
	bestText       = 300
)

func BestPostsSortsFor(platform string) []string {
	switch platform {
	case PlatformTelegram:
		return []string{"date", "likes", "views", "viral"}
	case PlatformVK:
		return []string{"date", "likes", "reposts", "comments", "views", "viral"}
	case PlatformOK:
		return []string{"date", "likes", "reposts", "comments", "viral"}
	}
	return []string{"date"}
}

func medianOrOne(v []float64) float64 {
	if m := Median(v); m != nil && *m > 0 {
		return *m
	}
	return 1
}

func numOrNil(v any) any {
	if v == nil {
		return nil
	}
	return jx.ToInt(v)
}

func sumReactions(v any) int64 {
	var s int64
	switch r := v.(type) {
	case map[string]int64:
		for _, n := range r {
			s += n
		}
	default:
		for _, n := range jx.ToMap(v) {
			s += jx.ToInt(n)
		}
	}
	return s
}

// BestPostsVirality — во сколько раз пост лучше обычного поста этого источника.
func BestPostsVirality(posts []jx.M) {
	if len(posts) == 0 {
		return
	}
	round2 := func(f float64) float64 { return float64(int64(f*100+0.5)) / 100 }
	if jx.Str(posts[0], "kind") == "telegram" {
		views, rates := make([]float64, len(posts)), make([]float64, len(posts))
		for i, p := range posts {
			views[i] = float64(jx.Int(p, "views"))
			if views[i] > 0 {
				rates[i] = float64(jx.Int(p, "likes")) / views[i]
			}
		}
		mv, mr := medianOrOne(views), medianOrOne(rates)
		for i, p := range posts {
			p["viral"] = round2((views[i]/mv + rates[i]/mr) / 2)
		}
		return
	}
	score := func(p jx.M) float64 { return float64(jx.Int(p, "likes") + 2*jx.Int(p, "reposts") + jx.Int(p, "comments")) }
	var all []float64
	for _, p := range posts {
		all = append(all, score(p))
	}
	m := medianOrOne(all)
	for _, p := range posts {
		p["viral"] = round2(score(p) / m)
	}
}

func bestPost(kind string, p jx.M, views, likes, reposts, comments any) jx.M {
	return jx.M{"kind": kind, "post_id": jx.Str(p, "post_id"), "image": jx.Str(p, "image"), "images": orStrs(jx.Strs(p, "images")), "video": jx.Str(p, "video"),
		"text": jx.Str(p, "text"), "source": jx.Str(p, "source"), "url": jx.Str(p, "url"), "posted_at": jx.Int(p, "posted_at"),
		"views": numOrNil(views), "likes": numOrNil(likes), "reposts": numOrNil(reposts), "comments": numOrNil(comments)}
}

func (a *App) BestPostsFromTelegram(channel string, days int) []jx.M {
	cut := now() - int64(days)*86400
	var posts []jx.M
	var before int64
	for page := 0; page < bestTGPages; page++ {
		html := a.TelegramChannelPageCached(channel, before)
		blocks := TelegramPageBlocks(html)
		if len(blocks) == 0 {
			break
		}
		for _, p := range TelegramPostsFromHTML(html, jx.M{"id": channel, "name": SourceDisplayName(PlatformTelegram, channel)}, 1000, true, 0) {
			eng := jx.Map(p, "engagement")
			var views, likes any
			if eng != nil {
				views, likes = eng["views"], sumReactions(eng["reactions"])
			}
			posts = append(posts, bestPost("telegram", p, views, likes, nil, nil))
		}
		var minID, oldest int64 = 1 << 62, 1 << 62
		for _, b := range blocks {
			var id int64
			fmt.Sscan(b.ID, &id)
			minID = min(minID, id)
			oldest = min(oldest, TelegramPostTime(b.Content))
		}
		if oldest < cut || minID == before {
			break
		}
		before = minID
	}
	return posts
}

func (a *App) BestPostsFromVK(ctx context.Context, domain string, days int) []jx.M {
	cut := now() - int64(days)*86400
	var posts []jx.M
	for req := 0; req < bestVKRequests; req++ {
		items := a.VKWallItems(ctx, domain, 100, req*100)
		if len(items) == 0 {
			break
		}
		oldest := int64(1 << 62)
		for _, it := range items {
			oldest = min(oldest, jx.Int(it, "date"))
			if p := VKWallPost(it, SourceDisplayName(PlatformVK, domain), domain, true, 0); p != nil {
				posts = append(posts, bestPost("social", p, p["views"], p["likes"], p["reposts"], p["comments"]))
			}
		}
		if oldest < cut {
			break
		}
	}
	return posts
}

func (a *App) BestPostsFromOK(id string) []jx.M {
	list, _ := a.OKFetchPosts(id, OKPostsPerGroup, SourceDisplayName(PlatformOK, id))
	var out []jx.M
	for _, p := range list {
		out = append(out, bestPost("social", p, nil, p["likes"], p["reshares"], p["comments"]))
	}
	return out
}

func (u *U) BestPostsFromFeed(feed string) []jx.M {
	list, _ := u.RSSFetchPosts(feed, 100, "", false, true)
	var out []jx.M
	for _, p := range list {
		out = append(out, bestPost("feed", p, nil, nil, nil, nil))
	}
	return out
}

func (u *U) bestPostsCollect(platform, id string, days int) []jx.M {
	switch platform {
	case PlatformTelegram:
		return u.BestPostsFromTelegram(id, days)
	case PlatformVK:
		return u.BestPostsFromVK(u.Ctx, id, days)
	case PlatformOK:
		return u.BestPostsFromOK(id)
	}
	return u.BestPostsFromFeed(id)
}

func bestPostsMatching(posts []jx.M, typ, phrase string) []jx.M {
	var out []jx.M
	for _, p := range posts {
		imgs := len(jx.Strs(p, "images"))
		video := jx.Str(p, "video") != ""
		if (typ == "photo" && (imgs > 1 || video)) || (typ == "album" && imgs < 2) || (typ == "video" && !video) {
			continue
		}
		if phrase == "" || strings.Contains(strings.ToLower(jx.Str(p, "text")), strings.ToLower(phrase)) {
			out = append(out, p)
		}
	}
	return out
}

func bestPostsSort(posts []jx.M, by string) {
	key := func(p jx.M) float64 {
		switch by {
		case "likes", "reposts", "comments", "views":
			return float64(jx.Int(p, by))
		case "viral":
			return jx.Float(p, "viral")
		}
		return float64(jx.Int(p, "posted_at"))
	}
	sort.SliceStable(posts, func(i, j int) bool {
		if ki, kj := key(posts[i]), key(posts[j]); ki != kj {
			return ki > kj
		}
		return jx.Int(posts[i], "posted_at") > jx.Int(posts[j], "posted_at")
	})
}

func (a *App) bestPostToken(p jx.M) string {
	return a.Box.MAC(jx.Str(p, "source_key") + "|" + jx.Str(p, "post_id") + "|" + jx.Str(p, "image"))
}

// BestPosts — поиск: source, sort, sorts, posts. Модель не вызывается.
func (u *U) BestPosts(link string, days int, by, typ, phrase string) (jx.M, error) {
	if !contains(BestPostsDays, days) {
		return nil, Bad("Период: день, неделя или месяц.")
	}
	platform, id, err := u.SourceParseLink(link)
	if err != nil {
		return nil, err
	}
	if platform == PlatformVK && !u.VKCanRead(u.Ctx) {
		return nil, Bad("Сначала подключите VK на вкладке «Источники».")
	}
	sorts := BestPostsSortsFor(platform)
	if !contains(sorts, by) {
		by = "date"
	}
	cut := now() - int64(days)*86400
	var period []jx.M
	for _, p := range u.bestPostsCollect(platform, id, days) {
		if jx.Int(p, "posted_at") >= cut {
			period = append(period, p)
		}
	}
	// Виральность — относительно всех постов периода, а не отфильтрованных
	BestPostsVirality(period)
	found := bestPostsMatching(period, typ, phrase)
	bestPostsSort(found, by)
	key := SourceIDFor(platform, id)
	posts := []jx.M{}
	for _, p := range found[:min(len(found), bestResults)] {
		p["source_key"] = key
		p["token"] = u.bestPostToken(p)
		posts = append(posts, p)
	}
	return jx.M{"source": jx.M{"name": SourceDisplayName(platform, id), "platform": platform}, "sort": by, "sorts": sorts, "posts": posts}, nil
}

func (u *U) uaBestPostsResult(r jx.M) (jx.M, error) {
	posts := jx.Maps(r, "posts")
	kept := u.UAFilterPosts(posts)
	if len(posts) > 0 && len(kept) == 0 && u.UAFilterOn() {
		return nil, Bad("Источник украинский — в Posta недоступен.")
	}
	out := jx.Copy(r)
	out["posts"] = orMaps(kept)
	return out, nil
}

func orMaps(l []jx.M) []jx.M {
	if l == nil {
		return []jx.M{}
	}
	return l
}

// BestPostsSearch — поиск с кэшем на 15 минут.
func (u *U) BestPostsSearch(link string, days int, by, typ, phrase string) (jx.M, error) {
	h := sha1.Sum([]byte(strings.Join([]string{strings.TrimSpace(link), fmt.Sprint(days), by, typ, phrase}, "|")))
	key := "best:" + u.ID + ":" + hex.EncodeToString(h[:])
	var hit jx.M
	if u.CacheGet(u.Ctx, key, &hit) {
		r, err := u.uaBestPostsResult(hit)
		if err == nil {
			r["key"] = key
		}
		return r, err
	}
	if err := u.dailyQuota("best_posts", 1, bestPerDay, "Сегодня уже много поисков «Лучших постов»: попробуйте завтра (до %d в сутки)."); err != nil {
		return nil, err
	}
	res, err := u.BestPosts(strings.TrimSpace(link), days, by, typ, phrase)
	if err != nil {
		return nil, err
	}
	u.CacheSet(u.Ctx, key, res, bestCacheSecs*time.Second)
	r, err := u.uaBestPostsResult(jx.Unmarshal(jx.Marshal(res)))
	if err == nil {
		r["key"] = key
	}
	return r, err
}

// BestPostsPublic — выдача для страницы: без полного текста и адреса ролика.
func BestPostsPublic(r jx.M) jx.M {
	posts := []jx.M{}
	for _, p := range jx.Maps(r, "posts") {
		posts = append(posts, jx.M{"token": p["token"], "image": p["image"], "images": p["images"], "video": jx.Str(p, "video") != "",
			"text": trimRunes(jx.Str(p, "text"), bestText), "source_url": p["url"], "source_key": p["source_key"], "posted_at": p["posted_at"],
			"views": p["views"], "likes": p["likes"], "reposts": p["reposts"], "comments": p["comments"], "viral": jx.Float(p, "viral")})
	}
	return jx.M{"key": r["key"], "source": r["source"], "sort": r["sort"], "sorts": r["sorts"], "posts": posts}
}

// BestPostPoolItem — картинки и ролик в кэш, запись пула как у сборщика.
func (u *U) BestPostPoolItem(p jx.M, withVideo bool) (jx.M, error) {
	var remote []string
	for _, s := range append([]string{jx.Str(p, "image")}, jx.Strs(p, "images")...) {
		if strings.HasPrefix(s, "https://") && !contains(remote, s) {
			remote = append(remote, s)
		}
	}
	type cached struct{ image, local string }
	var got []cached
	for _, s := range remote[:min(len(remote), 4)] {
		b, err := u.FetchPublicImage(s)
		if err != nil {
			continue
		}
		if l := u.MediaStoreImage(s, b); l != "" {
			got = append(got, cached{s, l})
		}
	}
	if len(got) == 0 {
		return nil, Bad("Картинка не скачалась.")
	}
	video := jx.Str(p, "video")
	videoLocal := ""
	if withVideo && video != "" {
		videoLocal = u.MediaStoreVideo(video, "", false, nil)
	}
	text := jx.Str(p, "text")
	if text == EmptyPostText {
		text = ""
	}
	via, page := "", ""
	if video != "" && videoLocal == "" {
		if v := VideoPageVia(jx.Str(p, "url")); v != "" {
			via, page = v, jx.Str(p, "url")
		}
	}
	images := []string{}
	gallery := []jx.M{}
	for _, c := range got {
		images = append(images, c.image)
		gallery = append(gallery, jx.M{"image": c.image, "image_local": c.local, "video": ""})
	}
	if len(got) < 2 {
		gallery = []jx.M{}
	}
	videoURL := ""
	if videoLocal != "" {
		videoURL = video
	}
	src := jx.Str(p, "source_url")
	if src == "" {
		src = jx.Str(p, "url")
	}
	return jx.M{"image": got[0].image, "images": images, "image_local": got[0].local, "video": videoURL, "video_local": videoLocal, "video_via": via, "video_page": page,
		"gallery": gallery, "text": text, "captions": []string{text}, "description": text, "source": jx.Str(p, "source"), "source_key": jx.Str(p, "source_key"),
		"source_url": src, "posted_at": jx.Int(p, "posted_at"), "pooled_at": now(), "topic": u.CurrentTopic(), "relevance": 100, "rating": 100, "picked": true}, nil
}

// BestPostsTake — выбранные посты из кэшированной выдачи → в подборку.
func (u *U) BestPostsTake(key string, tokens []string) (int, error) {
	if !strings.HasPrefix(key, "best:"+u.ID+":") {
		return 0, Bad("Результаты поиска не найдены: выполните поиск снова.")
	}
	var hit jx.M
	if !u.CacheGet(u.Ctx, key, &hit) {
		return 0, Bad("Результаты поиска устарели: выполните поиск снова.")
	}
	tokens = uniqueStrings(tokens)
	if len(tokens) == 0 || len(tokens) > bestTakeMax {
		return 0, Bad("Выберите от 1 до %d постов.", bestTakeMax)
	}
	byToken := map[string]jx.M{}
	for _, p := range jx.Maps(hit, "posts") {
		byToken[u.bestPostToken(p)] = p
	}
	var items []jx.M
	videos := 0
	for _, t := range tokens {
		p, ok := byToken[t]
		if !ok {
			return 0, Bad("Пост не найден в результатах: обновите поиск.")
		}
		withVideo := jx.Str(p, "video") != "" && videos < BestTakeVideos
		it, err := u.BestPostPoolItem(p, withVideo)
		if err != nil {
			continue
		}
		if withVideo && jx.Str(it, "video_local") != "" {
			videos++
		}
		items = append(items, it)
	}
	if len(items) == 0 {
		return 0, Bad("Картинки выбранных постов не скачались: попробуйте другие посты.")
	}
	return u.OwnAddToPool(items), nil
}

func httpDetect(b []byte) string {
	if len(b) > 512 {
		b = b[:512]
	}
	return strings.ToLower(httpDetectContentType(b))
}

var httpDetectContentType = http.DetectContentType
