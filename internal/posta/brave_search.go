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

type searchProviderPause struct {
	sync.Mutex
	until time.Time
	key   string
}

var bravePause = searchProviderPause{key: "brave_search_pause_until"}

func (p *searchProviderPause) untilLocked(u *U) time.Time {
	until := p.until
	if u.DB != nil {
		if saved := KV(u.App, u.Ctx, p.key, int64(0)); saved > until.Unix() {
			until = time.Unix(saved, 0)
		}
	}
	return until
}

func (p *searchProviderPause) deadline(u *U) time.Time {
	p.Lock()
	defer p.Unlock()
	return p.untilLocked(u)
}

// Never shorten an existing provider cooldown, including a longer Retry-After.
func (p *searchProviderPause) extend(u *U, until time.Time) time.Time {
	p.Lock()
	defer p.Unlock()
	if previous := p.untilLocked(u); previous.After(until) {
		until = previous
	}
	p.until = until
	if u.DB != nil {
		_ = u.KVSet(u.Ctx, p.key, until.Unix(), time.Until(until))
	}
	return until
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
	if until := bravePause.deadline(u); time.Now().Before(until) {
		return "", Bad("Brave ограничил веб-поиск; повтор не раньше %s.", until.Format("02.01 15:04"))
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
		until := bravePause.extend(u, time.Now().Add(30*time.Minute))
		return "", Bad("Brave ограничил запросы (HTTP 429); повтор не раньше %s.", until.Format("02.01 15:04"))
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
