package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"posta/internal/jx"
)

var redditSourcePath = regexp.MustCompile(`^/r/([A-Za-z0-9_]{3,21})/(?:\.rss)?$`)

func redditSourceName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || (u.Hostname() != "www.reddit.com" && u.Hostname() != "reddit.com") || u.User != nil || u.Port() != "" {
		return ""
	}
	if m := redditSourcePath.FindStringSubmatch(u.Path); m != nil {
		return strings.ToLower(m[1])
	}
	return ""
}

var redditTokens = struct {
	sync.Mutex
	tokens map[string]struct {
		token   string
		expires int64
	}
}{tokens: map[string]struct {
	token   string
	expires int64
}{}}

func (u *U) redditReadToken(ctx context.Context) (string, error) {
	key := u.NetworkAppKey("REDDIT_CLIENT_ID")
	if key == "" || u.NetworkAppKey("REDDIT_CLIENT_SECRET") == "" {
		return "", Bad("Для чтения Reddit нужны ключи приложения Reddit.")
	}
	redditTokens.Lock()
	defer redditTokens.Unlock()
	if entry := redditTokens.tokens[key]; entry.token != "" && entry.expires > time.Now().Unix()+60 {
		return entry.token, nil
	}
	t, err := u.tokenRequest(ctx, "https://www.reddit.com/api/v1/access_token", url.Values{"grant_type": {"client_credentials"}}, "REDDIT_CLIENT_ID", "REDDIT_CLIENT_SECRET", "Reddit не выдал токен для чтения", map[string]string{"User-Agent": redditUA})
	if err != nil {
		return "", err
	}
	expires, _ := strconv.ParseInt(t["expires_at"], 10, 64)
	if t["access_token"] == "" {
		return "", Bad("Reddit не выдал токен для чтения.")
	}
	redditTokens.tokens[key] = struct {
		token   string
		expires int64
	}{t["access_token"], expires}
	return t["access_token"], nil
}

func (u *U) redditRead(path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(u.Ctx, 20*time.Second)
	defer cancel()
	token, err := u.redditReadToken(ctx)
	if err != nil {
		return nil, err
	}
	r, err := networkHTTP(ctx, "GET", "https://oauth.reddit.com"+path, map[string]string{"Authorization": "Bearer " + token, "User-Agent": redditUA}, nil)
	if err != nil {
		return nil, err
	}
	if r.Status != 200 {
		return nil, fmt.Errorf("Reddit API: ответ %d", r.Status)
	}
	return r.Raw, nil
}

func redditListingPosts(data []byte, community string, count int, withText bool) ([]jx.M, error) {
	var response struct {
		Data struct {
			Children []struct {
				Data struct {
					Title       string  `json:"title"`
					Selftext    string  `json:"selftext"`
					Permalink   string  `json:"permalink"`
					Subreddit   string  `json:"subreddit"`
					Created     float64 `json:"created_utc"`
					Over18      bool    `json:"over_18"`
					Removed     string  `json:"removed_by_category"`
					URL         string  `json:"url"`
					GalleryData struct {
						Items []struct {
							MediaID string `json:"media_id"`
						} `json:"items"`
					} `json:"gallery_data"`
					MediaMetadata map[string]struct {
						Status string `json:"status"`
						Source struct {
							URL string `json:"u"`
						} `json:"s"`
					} `json:"media_metadata"`
					Preview struct {
						Images []struct {
							Source struct {
								URL string `json:"url"`
							} `json:"source"`
						} `json:"images"`
					} `json:"preview"`
				} `json:"data"`
			} `json:"children"`
		} `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	var out []jx.M
	for _, row := range response.Data.Children {
		p := row.Data
		if p.Over18 || p.Removed != "" || !strings.EqualFold(p.Subreddit, community) || !strings.HasPrefix(p.Permalink, "/r/") || p.Title == "" {
			continue
		}
		var images []string
		for _, item := range p.GalleryData.Items {
			m := p.MediaMetadata[item.MediaID]
			if image := html.UnescapeString(m.Source.URL); m.Status == "valid" && strings.HasPrefix(image, "https://") {
				images = append(images, image)
			}
		}
		if len(images) == 0 && len(p.Preview.Images) > 0 {
			images = append(images, html.UnescapeString(p.Preview.Images[0].Source.URL))
		}
		if len(images) == 0 && strings.HasPrefix(p.URL, "https://i.redd.it/") {
			images = append(images, p.URL)
		}
		text := strings.TrimSpace(p.Title + "\n" + p.Selftext)
		if len(images) == 0 && (!withText || runeLen(text) < 120) {
			continue
		}
		link := "https://www.reddit.com" + p.Permalink
		post := searchPost("Reddit", "reddit:"+strings.ToLower(community), "https://www.reddit.com/r/"+community+"/.rss", p.Title, text, "", "", link, int64(p.Created))
		if len(images) > 0 {
			post["image"], post["images"], post["text_post"] = images[0], images, false
		}
		out = append(out, post)
		if len(out) >= count {
			break
		}
	}
	return out, nil
}
