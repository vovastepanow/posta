package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"posta/internal/jx"
)

var threadsHandle = regexp.MustCompile(`^[A-Za-z0-9_.]{1,30}$`)

func threadsProfileName(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if host != "threads.net" && host != "www.threads.net" && host != "threads.com" && host != "www.threads.com" {
		return ""
	}
	path := strings.Trim(u.Path, "/")
	if !strings.HasPrefix(path, "@") || !threadsHandle.MatchString(path[1:]) {
		return ""
	}
	return strings.ToLower(path[1:])
}

type threadsPost struct {
	ID           string `json:"id"`
	Username     string `json:"username"`
	Text         string `json:"text"`
	MediaType    string `json:"media_type"`
	MediaURL     string `json:"media_url"`
	ThumbnailURL string `json:"thumbnail_url"`
	Permalink    string `json:"permalink"`
	Timestamp    string `json:"timestamp"`
}

func threadsParse(data []byte) ([]threadsPost, error) {
	var response struct {
		Data []threadsPost `json:"data"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	return response.Data, nil
}

func (u *U) threadsRead(path string, params url.Values) ([]byte, error) {
	token := u.NetworkAppKey("THREADS_ACCESS_TOKEN")
	if token == "" {
		return nil, Bad("Threads: нужен токен с доступом к поиску и публичным профилям.")
	}
	ctx, cancel := context.WithTimeout(u.Ctx, 20*time.Second)
	defer cancel()
	r, err := networkHTTP(ctx, "GET", "https://graph.threads.net/"+path+"?"+params.Encode(), map[string]string{"Authorization": "Bearer " + token}, nil)
	if err != nil {
		return nil, err
	}
	if r.Status != 200 {
		return nil, fmt.Errorf("Threads API: ответ %d; проверьте права токена", r.Status)
	}
	return r.Raw, nil
}

const threadsFields = "id,username,text,media_type,media_url,thumbnail_url,permalink,timestamp"

func threadsProfilePosts(data []byte, username string, count int, withText bool) ([]jx.M, error) {
	posts, err := threadsParse(data)
	if err != nil {
		return nil, err
	}
	var out []jx.M
	for _, p := range posts {
		if !strings.EqualFold(p.Username, username) || p.ID == "" {
			continue
		}
		link, err := url.Parse(p.Permalink)
		if err != nil || link.Scheme != "https" || (link.Hostname() != "www.threads.net" && link.Hostname() != "threads.net" && link.Hostname() != "www.threads.com" && link.Hostname() != "threads.com") {
			continue
		}
		at := ParseFeedTime(p.Timestamp)
		if at == 0 {
			continue
		}
		image, video := "", ""
		switch p.MediaType {
		case "IMAGE":
			image = p.MediaURL
		case "VIDEO":
			image, video = p.ThumbnailURL, p.MediaURL
		}
		if image == "" && video == "" && (!withText || runeLen(strings.TrimSpace(p.Text)) < 120) {
			continue
		}
		out = append(out, searchPost("Threads", "threads:"+strings.ToLower(username), "https://www.threads.net/@"+strings.ToLower(username), trimRunes(p.Text, 80), p.Text, image, video, p.Permalink, at))
		if len(out) >= count {
			break
		}
	}
	return out, nil
}

func threadsSearchCandidates(data []byte) ([]DiscoveryCandidate, error) {
	posts, err := threadsParse(data)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	for _, p := range posts {
		u := strings.ToLower(p.Username)
		if !threadsHandle.MatchString(u) || seen[u] {
			continue
		}
		seen[u] = true
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: "https://www.threads.net/@" + u, Info: jx.M{"title": "@" + u}})
	}
	return out, nil
}

func (u *U) discoveryFindThreads(queries []string) []DiscoveryCandidate {
	if u.NetworkAppKey("THREADS_ACCESS_TOKEN") == "" {
		u.Log("-> Threads: поиск пропущен, токен не настроен.")
		return nil
	}
	var out []DiscoveryCandidate
	for _, q := range queries[:min(2, len(queries))] {
		body, err := u.threadsRead("keyword_search", url.Values{"q": {q}, "search_type": {"TOP"}, "fields": {threadsFields}, "limit": {"25"}})
		if err != nil {
			u.Log("-> Threads «%s»: %s", q, err)
			continue
		}
		found, err := threadsSearchCandidates(body)
		if err != nil {
			u.Log("-> Threads «%s»: неверный ответ", q)
			continue
		}
		for _, c := range found {
			c.Origin, c.OriginKind = "поиск Threads «"+q+"»", "post_search"
			out = append(out, c)
		}
		u.Log("-> Поиск Threads «%s»: профилей %d.", q, len(found))
	}
	return out
}
