package posta

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

func autopilotBraveRetryAt(at time.Time, header string) time.Time {
	delay := 30 * time.Minute
	if seconds, err := strconv.ParseInt(strings.TrimSpace(header), 10, 64); err == nil && seconds > 0 {
		delay = max(delay, time.Duration(min(seconds, int64((365*24*time.Hour).Seconds())))*time.Second)
	} else if until, err := http.ParseTime(header); err == nil && until.After(at) {
		delay = max(delay, min(until.Sub(at), 365*24*time.Hour))
	}
	return at.Add(delay + 4*time.Minute)
}

// The anonymous search shares the existing Brave cooldown across platforms
// and processes, but never selects the configured paid API key.
func (u *U) autopilotBraveSearch(query string) FetchResultURL {
	return u.autopilotBraveSearchWith(query, u.RSSGet)
}

func (u *U) autopilotBraveSearchWith(query string, fetch func(string, int64, time.Duration, string) FetchResultURL) FetchResultURL {
	bravePause.Lock()
	until := bravePause.until
	bravePause.Unlock()
	if u.DB != nil {
		if saved := KV(u.App, u.Ctx, "brave_search_pause_until", int64(0)); saved > until.Unix() {
			until = time.Unix(saved, 0)
		}
	}
	if time.Now().Before(until) {
		return FetchResultURL{FetchResult: FetchResult{Status: 429, Error: "search cooldown"}}
	}
	r := fetch("https://search.brave.com/search?q="+url.QueryEscape(query), 3<<20, 15*time.Second, BrowserUA)
	if r.Status == 429 {
		until = autopilotBraveRetryAt(time.Now(), r.RetryAfter)
		bravePause.Lock()
		if until.After(bravePause.until) {
			bravePause.until = until
		}
		bravePause.Unlock()
		if u.DB != nil {
			_ = u.KVSet(u.Ctx, "brave_search_pause_until", until.Unix(), time.Until(until))
		}
	}
	return r
}
