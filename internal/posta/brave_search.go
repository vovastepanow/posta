package posta

import (
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"strings"
	"sync"
	"time"
)

var bravePause struct {
	sync.Mutex
	until time.Time
}

func braveAPISearchHTML(data []byte) (string, error) {
	var result struct {
		Web struct {
			Results []struct {
				URL string `json:"url"`
			} `json:"results"`
		} `json:"web"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	var b strings.Builder
	for _, r := range result.Web.Results {
		u, err := url.Parse(r.URL)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil {
			continue
		}
		b.WriteString(`<a href="`)
		b.WriteString(html.EscapeString(r.URL))
		b.WriteString(`"></a>`)
	}
	return b.String(), nil
}

// braveSearchPage shares cached results and honours 429 across all source finders.
func (u *U) braveSearchPage(query string) (string, error) {
	key := "brave_source_search:" + sha1Short(strings.ToLower(strings.TrimSpace(query)), 20)
	var cached string
	if u.DB != nil && u.CacheGet(u.Ctx, key, &cached) {
		return cached, nil
	}
	bravePause.Lock()
	paused := time.Now().Before(bravePause.until)
	bravePause.Unlock()
	if paused {
		return "", Bad("Веб-поиск временно ограничен; повторим позже.")
	}
	var data []byte
	var status int
	if token := u.NetworkAppKey("BRAVE_SEARCH_API_KEY"); token != "" {
		raw := "https://api.search.brave.com/res/v1/web/search?q=" + url.QueryEscape(query) + "&count=20"
		r, err := u.FetchPublic(raw, FetchOpts{MaxBytes: 3 << 20, Timeout: 15 * time.Second, UserAgent: HTTPUA, Headers: map[string]string{"Accept": "application/json", "X-Subscription-Token": token}})
		if err != nil {
			return "", err
		}
		status = r.Status
		if r.OK {
			page, err := braveAPISearchHTML(r.Bytes)
			if err != nil {
				return "", err
			}
			data = []byte(page)
		}
	} else {
		r := u.RSSGet("https://search.brave.com/search?q="+url.QueryEscape(query), 3<<20, 15*time.Second, BrowserUA)
		status = r.Status
		if r.OK {
			data = r.Bytes
		}
	}
	if status == 429 {
		bravePause.Lock()
		bravePause.until = time.Now().Add(30 * time.Minute)
		bravePause.Unlock()
		return "", Bad("Веб-поиск ограничил запросы; пауза на 30 минут.")
	}
	if data == nil {
		return "", fmt.Errorf("веб-поиск недоступен (HTTP %d)", status)
	}
	page := string(data)
	if u.DB != nil {
		u.CacheSet(u.Ctx, key, page, time.Hour)
	}
	return page, nil
}
