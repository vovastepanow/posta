package posta

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"posta/internal/jx"
)

var publicVKPostID = regexp.MustCompile(`(?:wall|post)(-?\d+_\d+)`)
var publicVKRawID = regexp.MustCompile(`^-?\d+_\d+$`)
var publicInstagramCode = regexp.MustCompile(`^[A-Za-z0-9_-]{3,30}$`)

func publicAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func publicHTTPS(s string) string {
	u, e := url.Parse(s)
	if e == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil {
		return s
	}
	return ""
}

var socialPublicPause = struct {
	sync.Mutex
	until map[string]time.Time
}{until: make(map[string]time.Time)}

func socialPublicHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}
func socialPublicPaused(raw string) bool {
	socialPublicPause.Lock()
	defer socialPublicPause.Unlock()
	return time.Now().Before(socialPublicPause.until[socialPublicHost(raw)])
}
func socialPublicRateLimit(raw string) {
	socialPublicPause.Lock()
	defer socialPublicPause.Unlock()
	socialPublicPause.until[socialPublicHost(raw)] = time.Now().Add(30 * time.Minute)
}

// publicSocialHTML reads only the anonymous HTML response. It never retries an access challenge.
func (a *App) publicSocialHTML(raw string) ([]byte, error) {
	if socialPublicPaused(raw) {
		return nil, Bad("Площадка временно ограничила доступ; повторим позже")
	}
	r := a.RSSGet(raw, 5<<20, 15*time.Second, BrowserUA)
	if r.Status == 429 {
		socialPublicRateLimit(raw)
		return nil, Bad("Площадка ограничила запросы (HTTP 429); пауза 30 минут")
	}
	if !r.OK {
		return nil, Bad("Публичная страница недоступна (HTTP %d): %s", r.Status, r.Error)
	}
	final, _ := url.Parse(r.URL)
	if final != nil && (strings.Contains(final.Path, "/accounts/login") || strings.Contains(final.Path, "/login") || strings.Contains(final.Path, "/challenge")) {
		return nil, Bad("Площадка требует вход или проверку доступа")
	}
	lower := strings.ToLower(string(r.Bytes))
	if strings.Contains(lower, "challenge_required") || strings.Contains(lower, "captcha_sid") {
		return nil, Bad("Площадка запросила проверку доступа")
	}
	return r.Bytes, nil
}

var publicSocialBrowserSlot = make(chan struct{}, 1)

// publicSocialBrowser renders a single validated platform URL in an isolated,
// disposable anonymous profile. DNS is restricted to the platform and its CDN.
func (a *App) publicSocialBrowser(raw string) ([]byte, error) {
	if socialPublicPaused(raw) {
		return nil, Bad("Площадка временно ограничила доступ; повторим позже")
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return nil, Bad("Неверный адрес площадки")
	}
	var hosts string
	switch u.Hostname() {
	case "vk.com":
		hosts = "EXCLUDE vk.com, EXCLUDE *.vk.com, EXCLUDE vk.ru, EXCLUDE *.vk.ru, EXCLUDE *.vkuserphoto.ru, EXCLUDE *.vkuser.net, EXCLUDE *.vkuservideo.net, EXCLUDE *.vk-cdn.net"
	case "www.instagram.com":
		hosts = "EXCLUDE instagram.com, EXCLUDE *.instagram.com, EXCLUDE *.cdninstagram.com, EXCLUDE *.fbcdn.net, EXCLUDE *.facebook.com, EXCLUDE *.facebook.net"
	default:
		return nil, Bad("Неизвестная площадка")
	}
	select {
	case publicSocialBrowserSlot <- struct{}{}:
		defer func() { <-publicSocialBrowserSlot }()
	case <-a.ctxOrBg().Done():
		return nil, a.ctxOrBg().Err()
	}
	dir, err := os.MkdirTemp("", "posta-social-browser-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	args := []string{"--headless=new", "--disable-gpu", "--no-first-run", "--no-sandbox", "--no-zygote", "--disable-dev-shm-usage", "--disable-background-networking", "--disable-extensions", "--no-default-browser-check", "--host-resolver-rules=" + hosts + ", MAP * ~NOTFOUND", "--user-data-dir=" + filepath.Join(dir, "profile"), "--timeout=20000", "--virtual-time-budget=5000", "--user-agent=" + BrowserUA, "--dump-dom", raw}
	r := Exec(a.ctxOrBg(), a.Cfg.Chrome, args, 45*time.Second, append(os.Environ(), "HOME="+dir), nil)
	if !r.OK || len(r.Stdout) == 0 || len(r.Stdout) > 8<<20 {
		return nil, Bad("Браузер не смог загрузить публичную страницу")
	}
	lower := strings.ToLower(r.Stdout)
	if strings.Contains(lower, "challenge_required") || strings.Contains(lower, "captcha_sid") {
		return nil, Bad("Площадка запросила проверку доступа")
	}
	return []byte(r.Stdout), nil
}

func vkPublicParse(data []byte, domain, name string, count int, withText bool, minText int) []jx.M {
	root, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil
	}
	var out []jx.M
	seen := map[string]bool{}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if len(out) >= count {
			return
		}
		id := ""
		for _, key := range []string{"id", "data-post-id", "data-post"} {
			if m := publicVKPostID.FindStringSubmatch(publicAttr(n, key)); len(m) > 1 {
				id = m[1]
				break
			}
			if key != "id" && publicVKRawID.MatchString(publicAttr(n, key)) {
				id = publicAttr(n, key)
				break
			}
		}
		if n.Type == html.ElementNode && id != "" && !seen[id] {
			seen[id] = true
			var texts, images []string
			var posted int64
			video := ""
			var walk func(*html.Node)
			walk = func(x *html.Node) {
				if x.Type == html.ElementNode {
					cls := publicAttr(x, "class")
					if contains(strings.Fields(cls), "wall_post_text_wrapper") || (len(texts) == 0 && (contains(strings.Fields(cls), "wall_post_text") || contains(strings.Fields(cls), "PostText"))) {
						texts = append(texts, strings.TrimSpace(CleanHTMLText(nodeInnerHTML(x))))
					}
					if x.Data == "img" && !publicSocialAvatar(x) {
						for _, key := range []string{"data-src", "src"} {
							if v := publicHTTPS(publicAttr(x, key)); v != "" && !strings.Contains(strings.ToLower(cls), "avatar") {
								images = append(images, v)
								break
							}
						}
					}
					if posted == 0 && publicAttr(x, "data-testid") == "post_date_block_preview" {
						posted = OKParseDate(nodeInnerHTML(x), time.Now())
					}
					if x.Data == "time" {
						posted = ParseFeedTime(publicAttr(x, "datetime"))
						if posted == 0 {
							posted, _ = strconv.ParseInt(publicAttr(x, "data-time"), 10, 64)
						}
					}
					if x.Data == "a" {
						h := publicAttr(x, "href")
						if strings.HasPrefix(h, "/video") {
							video = "https://vk.com" + h
						}
					}
				}
				for c := x.FirstChild; c != nil; c = c.NextSibling {
					walk(c)
				}
			}
			walk(n)
			text := strings.Join(texts, "\n")
			images = uniqueStrings(images)
			if len(images) > 0 || video != "" || (withText && strings.TrimSpace(text) != "" && runeLen(text) >= minText) {
				if text == "" {
					text = EmptyPostText
				}
				image := ""
				if len(images) > 0 {
					image = images[0]
				}
				p := jx.M{"image": image, "images": images, "text": text, "text_post": len(images) == 0 && video == "", "source": name, "post_id": id, "posted_at": posted, "url": "https://vk.com/wall" + id}
				if video != "" {
					p["video_via"], p["video_page"] = "ytdlp", video
				}
				out = append(out, p)
			}
			return
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	return out
}

func publicSocialAvatar(n *html.Node) bool {
	for x := n; x != nil; x = x.Parent {
		c := strings.ToLower(publicAttr(x, "class"))
		if strings.Contains(c, "avatar") || strings.Contains(c, "comment") || strings.Contains(c, "reply") {
			return true
		}
		if x != n && (publicVKRawID.MatchString(publicAttr(x, "data-post-id")) || publicVKPostID.MatchString(publicAttr(x, "id"))) {
			break
		}
	}
	return false
}

func nodeInnerHTML(n *html.Node) string {
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		_ = html.Render(&b, c)
	}
	return b.String()
}

// instagramPublicParse recognizes post objects independent of their position in embedded JSON.
func instagramPublicParse(data []byte, username string, count int, withText bool) []jx.M {
	root, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil
	}
	var out []jx.M
	seen := map[string]bool{}
	var walkJSON func(any)
	walkJSON = func(v any) {
		if len(out) >= count {
			return
		}
		switch x := v.(type) {
		case []any:
			for _, q := range x {
				walkJSON(q)
			}
		case map[string]any:
			code, _ := x["shortcode"].(string)
			if code == "" {
				code, _ = x["code"].(string)
			}
			if owner, ok := x["owner"].(map[string]any); ok {
				if name, ok := owner["username"].(string); ok && !strings.EqualFold(name, username) {
					return
				}
			}
			if publicInstagramCode.MatchString(code) && !seen[code] {
				stamp, _ := x["taken_at_timestamp"].(float64)
				if stamp == 0 {
					stamp, _ = x["taken_at"].(float64)
				}
				image, _ := x["display_url"].(string)
				if image == "" {
					image, _ = x["thumbnail_src"].(string)
				}
				video, _ := x["video_url"].(string)
				image, video = publicHTTPS(image), publicHTTPS(video)
				caption := ""
				if s, ok := x["caption"].(string); ok {
					caption = s
				}
				if c, ok := x["edge_media_to_caption"].(map[string]any); ok {
					if edges, ok := c["edges"].([]any); ok && len(edges) > 0 {
						if e, ok := edges[0].(map[string]any); ok {
							if n, ok := e["node"].(map[string]any); ok {
								caption, _ = n["text"].(string)
							}
						}
					}
				}
				if stamp > 0 && (image != "" || video != "" || withText && runeLen(caption) >= 120) {
					seen[code] = true
					page := "https://www.instagram.com/p/" + code + "/"
					p := searchPost("Instagram", "instagram:"+username, "https://www.instagram.com/"+username+"/", trimRunes(caption, 80), caption, image, video, page, int64(stamp))
					var media []jx.M
					if side, ok := x["edge_sidecar_to_children"].(map[string]any); ok {
						if edges, ok := side["edges"].([]any); ok {
							for _, e := range edges {
								if em, ok := e.(map[string]any); ok {
									if n, ok := em["node"].(map[string]any); ok {
										im, _ := n["display_url"].(string)
										vi, _ := n["video_url"].(string)
										im, vi = publicHTTPS(im), publicHTTPS(vi)
										if im != "" || vi != "" {
											media = append(media, jx.M{"image": im, "video": vi})
										}
									}
								}
							}
						}
					}
					setPostMedia(p, media)
					out = append(out, p)
					return
				}
			}
			for _, q := range x {
				walkJSON(q)
			}
		}
	}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "script" && n.FirstChild != nil {
			s := strings.TrimSpace(n.FirstChild.Data)
			var v any
			if json.Unmarshal([]byte(s), &v) == nil {
				walkJSON(v)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	return out
}

func (a *App) vkPublicPosts(domain, name string, count int, withText bool, minText int) ([]jx.M, error) {
	if !IsValidVKDomain(domain) {
		return nil, Bad("Неверный адрес VK")
	}
	b, e := a.publicSocialHTML("https://vk.com/" + url.PathEscape(domain))
	if e != nil {
		return nil, e
	}
	p := vkPublicParse(b, domain, name, count, withText, minText)
	if len(p) == 0 {
		b, e = a.publicSocialBrowser("https://vk.com/" + url.PathEscape(domain))
		if e != nil {
			return nil, e
		}
		p = vkPublicParse(b, domain, name, count, withText, minText)
	}
	if len(p) == 0 {
		return nil, fmt.Errorf("VK: публичная страница не содержит доступных постов")
	}
	return p, nil
}

var instagramPublicLink = regexp.MustCompile(`^/(?:([A-Za-z0-9_.]+)/)?(p|reel)/([A-Za-z0-9_-]{3,30})/?$`)

// The rendered profile grid exposes thumbnails and links even when individual
// posts and their captions require sign-in; do not invent missing post metadata.
func instagramPublicDOMPosts(data []byte, username string, count int) []jx.M {
	root, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return nil
	}
	var out []jx.M
	seen := map[string]bool{}
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if len(out) >= count {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			href := publicAttr(n, "href")
			m := instagramPublicLink.FindStringSubmatch(href)
			if len(m) > 0 && (m[1] == "" || strings.EqualFold(m[1], username)) && !seen[m[3]] {
				var image string
				var find func(*html.Node)
				find = func(x *html.Node) {
					if x.Type == html.ElementNode && x.Data == "img" && image == "" {
						image = publicHTTPS(publicAttr(x, "src"))
						if image == "" {
							image = publicHTTPS(publicAttr(x, "data-src"))
						}
					}
					for c := x.FirstChild; c != nil; c = c.NextSibling {
						find(c)
					}
				}
				find(n)
				if image != "" {
					seen[m[3]] = true
					page := "https://www.instagram.com/" + m[2] + "/" + m[3] + "/"
					p := searchPost("Instagram", "instagram:"+username, "https://www.instagram.com/"+username+"/", "", EmptyPostText, image, "", page, 0)
					if m[2] == "reel" {
						p["video_via"], p["video_page"] = "ytdlp", page
					}
					out = append(out, p)
				}
				return
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	return out
}

// instagramPublicEnrichPost reads fields belonging to the requested post only.
func instagramPublicEnrichPost(data []byte, post jx.M) bool {
	root, err := html.Parse(strings.NewReader(string(data)))
	if err != nil {
		return false
	}
	meta := map[string]string{}
	var at int64
	var hasVideo bool
	var visit func(*html.Node)
	visit = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "meta" {
				key := publicAttr(n, "property")
				if key == "" {
					key = publicAttr(n, "name")
				}
				if key != "" && meta[key] == "" {
					meta[key] = publicAttr(n, "content")
				}
			}
			if n.Data == "video" {
				hasVideo = true
			}
			if n.Data == "time" && at == 0 {
				at = ParseFeedTime(publicAttr(n, "datetime"))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			visit(c)
		}
	}
	visit(root)
	original, err := url.Parse(jx.Str(post, "url"))
	if err != nil {
		return false
	}
	canonical, err := url.Parse(meta["og:url"])
	if err != nil || canonical.Scheme != "https" || (canonical.Hostname() != "instagram.com" && canonical.Hostname() != "www.instagram.com") {
		return false
	}
	a := instagramPublicLink.FindStringSubmatch(original.Path)
	b := instagramPublicLink.FindStringSubmatch(canonical.Path)
	if len(a) == 0 || len(b) == 0 || a[3] != b[3] {
		return false
	}
	description := meta["og:description"]
	if description == "" {
		description = meta["description"]
	}
	if i := strings.Index(description, `: "`); i >= 0 {
		description = strings.TrimSuffix(description[i+3:], `"`)
	} else {
		description = ""
	}
	if description != "" {
		post["text"] = description
		post["title"] = trimRunes(description, 80)
	}
	if at > 0 {
		post["posted_at"] = at
	}
	if hasVideo {
		post["video_via"], post["video_page"] = "ytdlp", jx.Str(post, "url")
	}
	if image := publicHTTPS(meta["og:image"]); image != "" {
		post["image"] = image
		post["images"] = []string{image}
	}
	return true
}
