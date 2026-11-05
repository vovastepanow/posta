package posta

import (
	"encoding/json"
	"fmt"
	"html"
	"math/rand"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/llm"
)

// 4chan: открытый JSON API (доски → треды → посты с файлами). Не чаще одного запроса в секунду, If-Modified-Since;
// файлы — с заголовками браузера, Referer и паузой 2 с (иначе 429).

const (
	FourchanAPI             = "https://a.4cdn.org"
	FourchanImg             = "https://i.4cdn.org"
	FourchanBoardsPerRun    = 4
	FourchanThreadsPerBoard = 6
	fourchanPostsPerThread  = 20
	fourchanAPIPerRun       = 30
	FourchanMediaGap        = 2 * time.Second
	fourchanAPIGap          = time.Second
	fourchanUA              = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
)

var fourchanAlwaysBoards = []string{"b"}

var fourchanBoardTags = map[string][]string{
	"wsg": {"гифки", "юмор", "мемы"}, "a": {"аниме", "японская культура", "манга"}, "v": {"видеоигры", "игры"}, "vg": {"видеоигры", "игры"},
	"vr": {"виртуальная реальность", "видеоигры"}, "ck": {"еда", "кулинария", "готовка", "рецепты"}, "an": {"животные", "питомцы", "кошки", "собаки"},
	"fit": {"фитнес", "спорт", "тренировки", "здоровье"}, "g": {"технологии", "программирование", "компьютеры", "гаджеты"}, "sci": {"наука", "космос", "научпоп"},
	"his": {"история"}, "tv": {"кино", "сериалы", "фильмы"}, "sp": {"спорт", "футбол"}, "o": {"автомобили", "авто", "транспорт"},
	"out": {"природа", "активный отдых", "туризм", "походы"}, "trv": {"путешествия", "туризм", "отдых"}, "diy": {"своими руками", "рукоделие", "хобби", "мастерская"},
	"wg": {"обои", "природа", "красивые картинки"}, "p": {"фотография", "фото"}, "x": {"паранормальное", "мистика", "крипота", "ужасы"},
	"biz": {"бизнес", "деньги", "финансы", "криптовалюта"}, "co": {"комиксы", "мультфильмы"}, "mu": {"музыка"}, "toy": {"игрушки", "хобби"},
	"n": {"транспорт", "поезда", "самолёты"}, "pol": {"политика", "новости"},
}

type fourchanCached struct {
	Body     []byte
	Modified string
	At       time.Time
}

func (u *U) fourchanStat(field string) {
	if u.run == nil {
		return
	}
	switch field {
	case "api":
		u.run.FourchanAPI++
	case "api_cached":
		u.run.FourchanAPICached++
	case "media":
		u.run.FourchanMedia++
	}
}

// FourchanGetJSON — JSON API 4chan с If-Modified-Since (304 — прошлый ответ), лимит запросов за проход.
func (u *U) FourchanGetJSON(path string, dest any) error {
	key := "4chan:" + path
	cached, ok := Mem[fourchanCached](u.Mem, key)
	if ok && time.Since(cached.At) < config.BoardPollMin*time.Second && json.Unmarshal(cached.Body, dest) == nil {
		u.fourchanStat("api_cached")
		return nil
	}
	if u.run != nil && u.run.FourchanAPI >= fourchanAPIPerRun {
		return Bad("лимит запросов к 4chan за проход исчерпан")
	}
	u.fourchanStat("api")
	headers := map[string]string{"Accept": "application/json"}
	if ok && cached.Modified != "" {
		headers["If-Modified-Since"] = cached.Modified
	}
	HostThrottle("a.4cdn.org", fourchanAPIGap)
	r, err := u.FetchPublic(FourchanAPI+"/"+strings.TrimLeft(path, "/"), FetchOpts{MaxBytes: 4 << 20, Timeout: 10 * time.Second, UserAgent: fourchanUA, Headers: headers})
	if err != nil {
		return err
	}
	if r.Status == 304 && ok {
		u.fourchanStat("api_cached")
		cached.At = time.Now()
		u.Mem.Set(key, cached, time.Hour)
		return json.Unmarshal(cached.Body, dest)
	}
	if !r.OK {
		if r.Status > 0 {
			return Bad("4chan не отдал %s (HTTP %d)", path, r.Status)
		}
		return Bad("4chan не отдал %s: %s", path, r.Error)
	}
	if err := json.Unmarshal(r.Bytes, dest); err != nil {
		return Bad("4chan отдал не JSON: %s", path)
	}
	u.Mem.Set(key, fourchanCached{r.Bytes, r.LastModified, time.Now()}, time.Hour)
	return nil
}

type fourchanBoard struct {
	Board string `json:"board"`
	Title string `json:"title"`
	WS    bool   `json:"ws"`
}

var boardNameRe = regexp.MustCompile(`^[a-z0-9]{1,6}$`)

// FourchanBoards — доски (кэш сутки).
func (u *U) FourchanBoards() []fourchanBoard {
	var cached struct {
		List []fourchanBoard `json:"list"`
	}
	if u.CacheGet(u.Ctx, "fourchan:boards", &cached) {
		return cached.List
	}
	var data struct {
		Boards []struct {
			Board   string `json:"board"`
			Title   string `json:"title"`
			WSBoard int    `json:"ws_board"`
		} `json:"boards"`
	}
	if u.FourchanGetJSON("boards.json", &data) != nil {
		return nil
	}
	var list []fourchanBoard
	for _, b := range data.Boards {
		if boardNameRe.MatchString(b.Board) {
			list = append(list, fourchanBoard{b.Board, b.Title, b.WSBoard == 1})
		}
	}
	u.CacheSet(u.Ctx, "fourchan:boards", jx.M{"list": list}, 24*time.Hour)
	return list
}

func (u *U) fourchanAllowedBoards() []fourchanBoard {
	adult := u.AdultAllowed()
	var out []fourchanBoard
	for _, b := range u.FourchanBoards() {
		if b.WS || adult {
			out = append(out, b)
		}
	}
	return out
}

// FourchanBoardsForTopic — доски по теме: теги досок совпадают с темой; нет — выбирает модель (один раз на тему).
func (u *U) FourchanBoardsForTopic(topic string) []string {
	words := u.DvachTagWords(topic)
	allowed := map[string]bool{}
	for _, b := range u.fourchanAllowedBoards() {
		allowed[b.Board] = true
	}
	var out []string
	boards := make([]string, 0, len(fourchanBoardTags))
	for b := range fourchanBoardTags {
		boards = append(boards, b)
	}
	sort.Strings(boards)
	for _, b := range boards {
		if !allowed[b] {
			continue
		}
		for _, t := range fourchanBoardTags[b] {
			if DvachTagMatches(t, words) {
				out = append(out, b)
				break
			}
		}
	}
	if len(out) == 0 {
		u.fourchanModelBoards(topic)
	}
	for _, b := range append(append([]string{}, fourchanAlwaysBoards...), u.FourchanApprovedBoards(topic)...) {
		if allowed[b] {
			out = append(out, b)
		}
	}
	return uniqueStrings(out)
}

func (u *U) fourchanModelBoards(topic string) {
	key := strings.ToLower(strings.TrimSpace(topic))
	asked := KV(u.App, u.Ctx, "fourchan_model_asked", map[string]int64{})
	if len(u.FourchanApprovedBoards(topic)) > 0 || asked[key] > 0 {
		return
	}
	allowed := u.fourchanAllowedBoards()
	var lines []string
	valid := map[string]bool{}
	for _, b := range allowed {
		lines = append(lines, b.Board+" — "+b.Title)
		valid[b.Board] = true
	}
	prompt := "Тема контента: " + u.TopicPromptContext(topic) + ". Ниже доски 4chan: адрес — название.\n" + strings.Join(lines, "\n") +
		"\n\nВыбери до 3 досок, где чаще всего публикуют картинки и видео именно по этой теме. Выбирай только доски, которые прямо о ней." +
		" Общие доски (юмор, аниме, обои, игры) для узкой темы не выбирай. Если подходящих нет, верни пустой список. Ответь только JSON: {\"boards\": [\"адрес\", ...]}"
	r, err := u.LLM.Generate(u.Ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, JSON: true, MaxTokens: 80, Temperature: 0, UserID: u.ID})
	if err != nil {
		u.Log("-> Нейросеть не выбрала доски 4chan: %s", err)
		return
	}
	KVUpdate(u.App, u.Ctx, "fourchan_model_asked", 0, func(m map[string]int64, _ bool) *map[string]int64 {
		if m == nil {
			m = map[string]int64{}
		}
		m[key] = now()
		return &m
	})
	for i, b := range jx.Strs(llm.DecodeJSON(r.Text), "boards") {
		if i >= 3 {
			break
		}
		b = strings.ToLower(strings.Trim(b, " /\t"))
		if valid[b] {
			u.FourchanApproveBoard(topic, b)
		}
	}
	got := u.FourchanApprovedBoards(topic)
	what := "подходящих нет"
	if len(got) > 0 {
		what = "/" + strings.Join(got, "/, /") + "/"
	}
	u.Log("-> Доски 4chan для темы «%s» выбрала модель: %s.", topic, what)
}

func FourchanBoardsThisRun(boards []string) []string {
	b := append([]string{}, boards...)
	rand.Shuffle(len(b), func(i, j int) { b[i], b[j] = b[j], b[i] })
	return b[:min(len(b), FourchanBoardsPerRun)]
}

// FourchanThreads — живые треды доски (без закреплённых и закрытых), свежие сначала.
func (u *U) FourchanThreads(board string) ([]jx.M, error) {
	var pages []jx.M
	if err := u.FourchanGetJSON(board+"/catalog.json", &pages); err != nil {
		return nil, err
	}
	var threads []jx.M
	for _, p := range pages {
		for _, t := range jx.Maps(p, "threads") {
			if !jx.Bool(t, "sticky") && !jx.Bool(t, "closed") && jx.Has(t, "no") {
				threads = append(threads, t)
			}
		}
	}
	sort.SliceStable(threads, func(i, j int) bool { return jx.Int(threads[i], "last_modified") > jx.Int(threads[j], "last_modified") })
	return threads, nil
}

func (u *U) fourchanThreadPosts(board string, thread int64) ([]jx.M, error) {
	var data jx.M
	if err := u.FourchanGetJSON(fmt.Sprintf("%s/thread/%d.json", board, thread), &data); err != nil {
		return nil, err
	}
	posts := jx.Maps(data, "posts")
	return posts[:min(len(posts), fourchanPostsPerThread)], nil
}

func fourchanPostMedia(board string, post jx.M) []jx.M {
	tim, ext := jx.Int(post, "tim"), strings.ToLower(jx.Str(post, "ext"))
	if tim == 0 || ext == "" {
		return nil
	}
	file := fmt.Sprintf("%s/%s/%d%s", FourchanImg, board, tim, ext)
	thumb := fmt.Sprintf("%s/%s/%ds.jpg", FourchanImg, board, tim)
	switch ext {
	case ".webm", ".mp4":
		return []jx.M{{"image": thumb, "video": file}}
	case ".jpg", ".jpeg", ".png", ".gif":
		return []jx.M{{"image": file, "video": ""}}
	}
	return nil
}

var brRe = regexp.MustCompile(`(?i)<br\s*/?>`)

// FourchanPostText — HTML → текст; «>>123» убираем, «>зелёный текст» без знака.
func FourchanPostText(post jx.M) string {
	text := html.UnescapeString(tagRe.ReplaceAllString(brRe.ReplaceAllString(jx.Str(post, "com"), "\n"), ""))
	var lines []string
	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(refNumRe.ReplaceAllString(l, ""))
		if l != "" {
			lines = append(lines, strings.TrimLeft(l, ">"))
		}
	}
	subject := strings.TrimSpace(html.UnescapeString(jx.Str(post, "sub")))
	var parts []string
	if subject != "" {
		parts = append(parts, subject)
	}
	if len(lines) > 0 {
		parts = append(parts, strings.Join(lines, "\n"))
	}
	return strings.TrimSpace(strings.Join(parts, "\n"))
}

func fourchanPostURL(board string, thread, post int64) string {
	return fmt.Sprintf("https://boards.4chan.org/%s/thread/%d#p%d", board, thread, post)
}

func FourchanThreadDeadReason(t jx.M) string {
	if jx.Bool(t, "closed") {
		return "тред закрыт"
	}
	if r := jx.Int(t, "replies"); r < config.DvachThreadMinPosts {
		return fmt.Sprintf("в треде всего %d ответов", r)
	}
	if days := (now() - jx.Int(t, "last_modified")) / config.DaySeconds; days > config.SourceStaleDays {
		return fmt.Sprintf("в тред не писали %d дн", days)
	}
	return ""
}

// FourchanMediaHeaders — заголовки браузера и Referer доски — иначе 429.
func FourchanMediaHeaders(raw string) map[string]string {
	if !strings.HasPrefix(raw, FourchanImg+"/") {
		return nil
	}
	board := strings.SplitN(strings.TrimPrefix(raw, FourchanImg+"/"), "/", 2)[0]
	return map[string]string{"Referer": "https://boards.4chan.org/" + board + "/", "Accept": "image/*,video/*;q=0.8,*/*;q=0.5"}
}

// FourchanFetchMedia — файл 4chan с паузой и заголовками; nil — не вышло.
func (u *U) FourchanFetchMedia(raw string, video bool) []byte {
	HostThrottle("i.4cdn.org", FourchanMediaGap)
	u.fourchanStat("media")
	maxB := int64(config.MaxImageBytes)
	if video {
		maxB = config.VideoMaxBytes
	}
	r, err := u.FetchPublic(raw, FetchOpts{MaxBytes: maxB, Timeout: 25 * time.Second, UserAgent: fourchanUA, Headers: FourchanMediaHeaders(raw)})
	if err != nil || !r.OK {
		return nil
	}
	return r.Bytes
}

// FourchanThreadMedia — медиа-посты одного треда как записи пула.
func (u *U) FourchanThreadMedia(board string, t jx.M, capN int, extra jx.M) []jx.M {
	thread := jx.Int(t, "no")
	posts, err := u.fourchanThreadPosts(board, thread)
	if err != nil {
		u.Log("-> 4chan /%s/%d: %s", board, thread, err)
		return nil
	}
	op := t
	if len(posts) > 0 {
		op = posts[0]
	}
	if jx.Bool(op, "closed") || jx.Bool(op, "archived") {
		return nil
	}
	title := CleanHTMLText(jx.Str(t, "sub"))
	if title == "" {
		title = CleanHTMLText(jx.Str(op, "sub"))
	}
	title = trimRunes(title, config.ThreadTitleMax)
	var out []jx.M
	for _, p := range posts {
		files := fourchanPostMedia(board, p)
		if len(files) == 0 {
			continue
		}
		id := jx.Int(p, "no")
		flag := ""
		if strings.ToUpper(jx.Str(p, "country")) == "UA" || strings.ToUpper(jx.Str(op, "country")) == "UA" {
			flag = "UA"
		}
		label := title
		if label == "" {
			label = "#" + strconv.FormatInt(thread, 10)
		}
		rec := jx.M{"source_key": fmt.Sprintf("4chan:%s:%d", board, thread), "posted_at": jx.Int(p, "time"), "board": board, "thread_id": strconv.FormatInt(thread, 10),
			"post_id": strconv.FormatInt(id, 10), "country": flag, "source": Platform4chan + " /" + board + "/ · " + trimRunes(label, 60),
			"source_url": fourchanPostURL(board, thread, id), "image": files[0]["image"], "images": []any{files[0]["image"]}, "video": files[0]["video"],
			"captions": []any{}, "engagement": jx.M{"source": "4chan", "replies": jx.Int(t, "replies"), "images": jx.Int(t, "images")},
			"url": fourchanPostURL(board, thread, id), "thread_title": title, "is_op": id == thread}
		if len(files) > 1 {
			rec["gallery"] = files
		}
		for k, v := range extra {
			rec[k] = v
		}
		out = append(out, rec)
		if len(out) >= capN {
			break
		}
	}
	return out
}

// ParseFourchan — посты доски для сбора: живые треды, с темой — отбор тредов по ней.
func (u *U) ParseFourchan(board string, threadLimit int, topic string) []jx.M {
	threads, err := u.FourchanThreads(board)
	if err != nil {
		u.Log("-> 4chan /%s/: %s", board, err)
		return nil
	}
	threads = threads[:min(len(threads), threadLimit)]
	var alive []jx.M
	for _, t := range threads {
		if FourchanThreadDeadReason(t) == "" {
			alive = append(alive, t)
		}
	}
	var out []jx.M
	for _, t := range alive {
		out = append(out, u.FourchanThreadMedia(board, t, 200-len(out), nil)...)
		if len(out) >= 200 {
			break
		}
	}
	return out
}

func (a *App) FourchanApprovedBoards(topic string) []string {
	m := KV(a, a.ctxOrBg(), "fourchan_topic_boards", map[string][]string{})
	return m[strings.ToLower(strings.TrimSpace(topic))]
}

func (a *App) FourchanApproveBoard(topic, board string) {
	if !boardNameRe.MatchString(board) {
		return
	}
	key := strings.ToLower(strings.TrimSpace(topic))
	KVUpdate(a, a.ctxOrBg(), "fourchan_topic_boards", 0, func(m map[string][]string, _ bool) *map[string][]string {
		if m == nil {
			m = map[string][]string{}
		}
		m[key] = uniqueStrings(append(m[key], board))
		return &m
	})
}

// FourchanSourceCandidates — треды досок темы — кандидаты на вкладке «Источники».
func (u *U) FourchanSourceCandidates(voted map[string]bool, topic string) []jx.M {
	boards := u.FourchanBoardsForTopic(topic)
	rand.Shuffle(len(boards), func(i, j int) { boards[i], boards[j] = boards[j], boards[i] })
	var out []jx.M
	for _, board := range boards[:min(3, len(boards))] {
		threads, err := u.FourchanThreads(board)
		if err != nil {
			continue
		}
		for _, t := range threads[:min(10, len(threads))] {
			sid := fmt.Sprintf("4chan:%s:%d", board, jx.Int(t, "no"))
			if voted[sid] || FourchanThreadDeadReason(t) != "" {
				continue
			}
			name := strings.TrimSpace(jx.Str(t, "sub"))
			if name == "" {
				name = "#" + jx.Str(t, "no")
			}
			out = append(out, SourceCandidate(sid, Platform4chan, Platform4chan+" /"+board+"/ · "+trimRunes(name, 80), "Тред 4chan с картинками по теме.",
				jx.M{"board": board, "thread_id": jx.Str(t, "no"), "thread_posts": jx.Int(t, "replies"), "last_hit": jx.Int(t, "last_modified")}))
		}
	}
	return out
}

// FourchanPreview — превью треда для карточки.
func (u *U) FourchanPreview(c jx.M, count int) []jx.M {
	thread := jx.M{"no": jx.Int(c, "thread_id"), "sub": ""}
	var out []jx.M
	for _, p := range u.FourchanThreadMedia(jx.Str(c, "board"), thread, count, nil) {
		text := jx.Str(p, "text")
		if text == "" {
			text = EmptyPostText
		}
		out = append(out, jx.M{"image": p["image"], "images": p["images"], "video": p["video"], "text": text, "source": p["source"],
			"post_id": p["post_id"], "posted_at": p["posted_at"], "url": p["source_url"]})
	}
	return out
}

// SourceCandidate — кандидат для вкладки «Источники».
func SourceCandidate(id, platform, name, reason string, extra jx.M) jx.M {
	c := jx.M{"source_id": id, "platform": platform, "name": name, "reason": reason}
	for k, v := range extra {
		c[k] = v
	}
	return c
}
