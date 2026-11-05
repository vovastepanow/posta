package posta

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"posta/internal/jx"
)

// Новые площадки поиска по запросу: JSON- и HTML-разборы — чистые функции, проверены на сохранённых ответах (tools/fixtures).

const (
	PlatformRutube     = "Rutube"
	Platform9GAG       = "9GAG"
	PlatformLemmy      = "Lemmy"
	PlatformMastodon   = "Mastodon"
	PlatformJoyreactor = "Joyreactor"
)

var moscowZone = time.FixedZone("MSK", 3*3600)

// searchPost — общий каркас поста поисковой площадки: формат пула (как у постов каталога).
func searchPost(platform, sourceKey, sourceURL, title, text, image, video, page string, posted int64) jx.M {
	imgs := []any{}
	if image != "" {
		imgs = append(imgs, image)
	}
	return jx.M{"source": platform, "source_key": sourceKey, "source_url": page, "source_author_url": sourceURL, "found_by": "поиск " + platform,
		"title": title, "text": text, "image": image, "images": imgs, "gallery": []any{}, "video": video, "video_unavailable": false,
		"url": page, "posted_at": posted, "engagement": nil, "post_id": sha1Short(page, 12),
		"text_post": image == "" && video == "" && runeLen(strings.TrimSpace(text)) >= 20}
}

// setPostMedia keeps attachment order and represents an album as one post.
func setPostMedia(post jx.M, media []jx.M) {
	if len(media) == 0 {
		return
	}
	images := []string{}
	for _, m := range media {
		if image := jx.Str(m, "image"); image != "" {
			images = append(images, image)
		}
	}
	// Analysis needs an image; keep the original album order in gallery.
	main := media[0]
	if jx.Str(main, "image") == "" {
		for _, item := range media {
			if jx.Str(item, "image") != "" {
				main = item
				break
			}
		}
	}
	post["image"], post["video"], post["images"] = jx.Str(main, "image"), jx.Str(main, "video"), images
	if len(media) > 1 {
		post["gallery"] = media
	}
}

// RutubeParse — ответ поиска Rutube (results[]); время без пояса — по Москве.
func RutubeParse(data []byte) ([]jx.M, error) {
	var resp struct {
		Results []struct {
			ID           string `json:"id"`
			Title        string `json:"title"`
			VideoURL     string `json:"video_url"`
			ThumbnailURL string `json:"thumbnail_url"`
			CreatedTS    string `json:"created_ts"`
			Duration     int64  `json:"duration"`
			IsAdult      bool   `json:"is_adult"`
			IsHidden     bool   `json:"is_hidden"`
			IsDeleted    bool   `json:"is_deleted"`
			Author       struct {
				ID   int64  `json:"id"`
				Name string `json:"name"`
			} `json:"author"`
		} `json:"results"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	var out []jx.M
	for _, r := range resp.Results {
		if r.IsAdult || r.IsHidden || r.IsDeleted || r.ID == "" || r.Title == "" {
			continue
		}
		at, err := time.ParseInLocation("2006-01-02T15:04:05", r.CreatedTS[:min(len(r.CreatedTS), 19)], moscowZone)
		posted := int64(0)
		if err == nil {
			posted = at.Unix()
		}
		channel := ""
		if r.Author.ID > 0 {
			channel = fmt.Sprintf("https://rutube.ru/channel/%d/", r.Author.ID)
		}
		p := searchPost(PlatformRutube, fmt.Sprintf("rutube:%d", r.Author.ID), channel, r.Title, r.Title, r.ThumbnailURL, "", r.VideoURL, posted)
		p["author_name"] = r.Author.Name
		if VideoPageVia(r.VideoURL) == "ytdlp" {
			p["video_via"], p["video_page"], p["duration"] = "ytdlp", r.VideoURL, r.Duration
		}
		out = append(out, p)
	}
	return out, nil
}

// NineGAGParse — ответ 9GAG (data.posts[]): фото — image460, анимация и видео — mp4 из image460sv.
func NineGAGParse(data []byte) ([]jx.M, error) {
	var resp struct {
		Data struct {
			Posts []struct {
				ID      string `json:"id"`
				URL     string `json:"url"`
				Title   string `json:"title"`
				Type    string `json:"type"`
				NSFW    int    `json:"nsfw"`
				Promo   int    `json:"promoted"`
				Created int64  `json:"creationTs"`
				Images  struct {
					Image460 struct {
						URL string `json:"url"`
					} `json:"image460"`
					ImageFb struct {
						URL string `json:"url"`
					} `json:"imageFbThumbnail"`
					Image460sv struct {
						URL string `json:"url"`
					} `json:"image460sv"`
				} `json:"images"`
			} `json:"posts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	var out []jx.M
	for _, p := range resp.Data.Posts {
		if p.NSFW != 0 || p.Promo != 0 || p.Title == "" {
			continue
		}
		image, video := p.Images.Image460.URL, ""
		if p.Images.Image460sv.URL != "" && strings.HasSuffix(strings.ToLower(p.Images.Image460sv.URL), ".mp4") {
			video, image = p.Images.Image460sv.URL, p.Images.ImageFb.URL
		}
		if image == "" && video == "" {
			continue
		}
		out = append(out, searchPost(Platform9GAG, "9gag:"+p.ID, "", p.Title, p.Title, image, video, p.URL, p.Created))
	}
	return out, nil
}

// lemmyBodyVideo extracts an explicit video attachment embedded as a Markdown image.
// Ordinary links in a post are not promoted to media.
var lemmyBodyVideoRE = regexp.MustCompile(`!\[[^\]]*\]\((https://[^\s)]+)\)`)

func lemmyBodyVideo(body string) string {
	for _, match := range lemmyBodyVideoRE.FindAllStringSubmatch(body, -1) {
		parsed, err := url.Parse(match[1])
		if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
			continue
		}
		path := strings.ToLower(parsed.Path)
		if strings.HasSuffix(path, ".mp4") || strings.HasSuffix(path, ".webm") || strings.HasSuffix(path, ".m4v") {
			return match[1]
		}
	}
	return ""
}

func lemmyVideoURL(candidate string) string {
	parsed, err := url.Parse(candidate)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return ""
	}
	path := strings.ToLower(parsed.Path)
	if strings.HasSuffix(path, ".mp4") || strings.HasSuffix(path, ".webm") || strings.HasSuffix(path, ".m4v") {
		return candidate
	}
	return ""
}

// LemmyParse — ответ поиска Lemmy (posts[]): источник — сообщество; без картинки пост не берём.
func LemmyParse(data []byte) ([]jx.M, error) {
	var resp struct {
		Posts []struct {
			Post struct {
				Name         string `json:"name"`
				APID         string `json:"ap_id"`
				URL          string `json:"url"`
				ThumbnailURL string `json:"thumbnail_url"`
				Body         string `json:"body"`
				Published    string `json:"published"`
				NSFW         bool   `json:"nsfw"`
				Deleted      bool   `json:"deleted"`
				Removed      bool   `json:"removed"`
				CommunityID  int64  `json:"community_id"`
			} `json:"post"`
			Community struct {
				ActorID string `json:"actor_id"`
			} `json:"community"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	var out []jx.M
	for _, p := range resp.Posts {
		v := p.Post
		if v.NSFW || v.Deleted || v.Removed || v.Name == "" || v.Published == "" {
			continue
		}
		img := ""
		for _, candidate := range []string{v.URL, v.ThumbnailURL} {
			if lemmyVideoURL(candidate) != "" {
				continue
			}
			parsed, err := url.Parse(candidate)
			if err != nil || parsed.Scheme != "https" {
				continue
			}
			path := strings.ToLower(parsed.Path)
			if strings.Contains(path, "/pictrs/") || strings.HasSuffix(path, ".jpg") || strings.HasSuffix(path, ".jpeg") || strings.HasSuffix(path, ".png") || strings.HasSuffix(path, ".webp") || strings.HasSuffix(path, ".gif") || strings.HasSuffix(path, ".avif") {
				img = candidate
				break
			}
		}
		if img == "" && runeLen(strings.TrimSpace(v.Name+" "+CleanHTMLText(v.Body))) < 20 {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, v.Published)
		if err != nil {
			continue
		}
		page := v.APID
		if page == "" {
			page = v.URL
		}
		video := lemmyVideoURL(v.URL)
		if video == "" {
			video = lemmyBodyVideo(v.Body)
		}
		out = append(out, searchPost(PlatformLemmy, fmt.Sprintf("lemmy:%d", v.CommunityID), p.Community.ActorID, v.Name, CleanHTMLText(v.Name+"\n"+v.Body), img, video, page, at.Unix()))
	}
	return out, nil
}

// MastodonParse — ответ /api/v1/timelines/tag/<тег> (массив статусов); источник — автор статуса.
func MastodonParse(data []byte) ([]jx.M, error) {
	var statuses []struct {
		Content   string `json:"content"`
		CreatedAt string `json:"created_at"`
		URL       string `json:"url"`
		Sensitive bool   `json:"sensitive"`
		Reblog    any    `json:"reblog"`
		Account   struct {
			ID  string `json:"id"`
			URL string `json:"url"`
		} `json:"account"`
		Media []struct {
			Type       string `json:"type"`
			URL        string `json:"url"`
			PreviewURL string `json:"preview_url"`
		} `json:"media_attachments"`
	}
	if err := json.Unmarshal(data, &statuses); err != nil {
		return nil, err
	}
	var out []jx.M
	for _, s := range statuses {
		if s.Sensitive || s.Reblog != nil || s.Account.ID == "" {
			continue
		}
		var media []jx.M
		for _, m := range s.Media {
			switch m.Type {
			case "image":
				if strings.HasPrefix(m.URL, "https://") {
					media = append(media, jx.M{"image": m.URL, "video": ""})
				}
			case "video", "gifv":
				if strings.HasPrefix(m.URL, "https://") {
					media = append(media, jx.M{"image": m.PreviewURL, "video": m.URL})
				}
			}
		}
		text := CleanHTMLText(s.Content)
		if len(media) == 0 && runeLen(strings.TrimSpace(text)) < 20 {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, s.CreatedAt)
		if err != nil {
			continue
		}
		post := searchPost(PlatformMastodon, "mastodon:"+s.Account.ID, s.Account.URL, trimRunes(text, 80), text, "", "", s.URL, at.Unix())
		setPostMedia(post, media)
		if len(media) > 0 {
			post["text_post"] = false
		}
		out = append(out, post)
	}
	return out, nil
}

var (
	joyCardSplitRe = regexp.MustCompile(`content-card post-card`)
	joyUserRe      = regexp.MustCompile(`class="primary-link" href="/user/([^"]+)"`)
	joyPermaRe     = regexp.MustCompile(`href="(/post/\d+)"`)
	joyMediaRe     = regexp.MustCompile(`(?:data-src|src)="(https://[^"]+/pics/post/[^"]+)"`)
	joyTagRe       = regexp.MustCompile(`tag-reactor[^"]*"><a href="[^"]*">([^<]+)</a>`)
	joyAgoRe       = regexp.MustCompile(`(?:^|[\s>])(\d{1,2})\s*(мин|ч|дн|д|нед|мес|г)\.`)
	htmlCommentRe  = regexp.MustCompile(`<!--.*?-->`)
)

// JoyreactorParse — страница тега Joyreactor (карточки post-card): источник — автор поста, время — «N ч.» от момента разбора.
func JoyreactorParse(page string, at time.Time) []jx.M {
	var out []jx.M
	parts := joyCardSplitRe.Split(page, -1)
	for _, card := range parts[1:] {
		perma := joyPermaRe.FindStringSubmatch(card)
		if perma == nil {
			continue
		}
		var image, video string
		for _, m := range joyMediaRe.FindAllStringSubmatch(card, -1) {
			u := m[1]
			if strings.HasSuffix(u, ".webm") || strings.HasSuffix(u, ".mp4") {
				video = u
			} else if image == "" {
				image = u
			}
		}
		if image == "" && video == "" {
			continue
		}
		var tags []string
		for _, t := range joyTagRe.FindAllStringSubmatch(card, -1) {
			tags = append(tags, t[1])
		}
		user := ""
		if m := joyUserRe.FindStringSubmatch(card); m != nil {
			user = m[1]
		}
		text := strings.Join(tags, ", ")
		out = append(out, searchPost(PlatformJoyreactor, "joyreactor:"+user, "https://joyreactor.cc/user/"+user, trimRunes(text, 80), text,
			image, video, "https://joyreactor.cc"+perma[1], joyAgo(card, at)))
	}
	return out
}

// joyAgo — время поста из подписи «N ч.» (мин, ч, дн, нед, мес, г); не нашли — 0 (возраст не проверяем).
func joyAgo(card string, at time.Time) int64 {
	m := joyAgoRe.FindStringSubmatch(htmlCommentRe.ReplaceAllString(card, ""))
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	unit := map[string]time.Duration{"мин": time.Minute, "ч": time.Hour, "дн": 24 * time.Hour, "д": 24 * time.Hour,
		"нед": 7 * 24 * time.Hour, "мес": 30 * 24 * time.Hour, "г": 365 * 24 * time.Hour}[m[2]]
	return at.Add(-time.Duration(n) * unit).Unix()
}

// --- сеть: одна страница запроса ---

func (u *U) searchGetJSON(raw string) ([]byte, error) {
	body, status, err := u.App.HTTPGetStatus(raw, 12*time.Second, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("ответ %d", status)
	}
	return body, nil
}

// SearchRutube — поиск видео Rutube по запросу, страница page (с 1).
func (u *U) SearchRutube(q string, page int) ([]jx.M, bool, error) {
	body, err := u.searchGetJSON("https://rutube.ru/api/search/video/?query=" + url.QueryEscape(q) + "&page=" + strconv.Itoa(max(1, page)))
	if err != nil {
		return nil, false, err
	}
	posts, err := RutubeParse(body)
	return posts, len(posts) > 0, err
}

// SearchNineGAG — поиск постов 9GAG по запросу (в основном по-английски).
func (u *U) SearchNineGAG(q string, page int) ([]jx.M, bool, error) {
	body, err := u.searchGetJSON("https://9gag.com/v1/search-posts?query=" + url.QueryEscape(q))
	if err != nil {
		return nil, false, err
	}
	posts, err := NineGAGParse(body)
	return posts, false, err
}

// SearchLemmy — поиск постов Lemmy (lemmy.world) по запросу.
func (u *U) SearchLemmy(q string, page int) ([]jx.M, bool, error) {
	body, err := u.searchGetJSON("https://lemmy.world/api/v3/search?type_=Posts&limit=30&q=" + url.QueryEscape(q))
	if err != nil {
		return nil, false, err
	}
	posts, err := LemmyParse(body)
	return posts, false, err
}

// SearchMastodon — статусы по хэштегу (поиск по словам у Mastodon нет без входа): q — тег без пробелов.
func (u *U) SearchMastodon(q string, page int) ([]jx.M, bool, error) {
	tag := strings.TrimLeft(strings.ReplaceAll(q, " ", ""), "#")
	if tag == "" {
		return nil, false, nil
	}
	body, err := u.searchGetJSON("https://mastodon.social/api/v1/timelines/tag/" + url.PathEscape(strings.ToLower(tag)) + "?limit=40")
	if err != nil {
		return nil, false, err
	}
	posts, err := MastodonParse(body)
	return posts, false, err
}

// SearchJoyreactor — страница тега Joyreactor (тег = запрос без пробелов, как на сайте).
func (u *U) SearchJoyreactor(q string, page int) ([]jx.M, bool, error) {
	tag := strings.TrimLeft(strings.TrimSpace(q), "#")
	if tag == "" {
		return nil, false, nil
	}
	body, status, err := u.App.HTTPGetStatus("https://joyreactor.cc/tag/"+url.PathEscape(tag), 12*time.Second, nil)
	if err != nil {
		return nil, false, err
	}
	if status != 200 {
		return nil, false, fmt.Errorf("ответ %d", status)
	}
	return JoyreactorParse(string(body), time.Now()), false, nil
}
