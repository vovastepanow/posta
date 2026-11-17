package posta

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"posta/internal/jx"
)

var autopilotMastodonName = regexp.MustCompile(`^[a-z0-9_]{1,30}$`)
var autopilotMastodonID = regexp.MustCompile(`^[0-9]{1,30}$`)

func autopilotMastodonFeed(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host != "mastodon.social" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	name := strings.TrimPrefix(strings.TrimSuffix(u.Path, ".rss"), "/@")
	if !strings.HasPrefix(u.Path, "/@") || !strings.HasSuffix(u.Path, ".rss") || !autopilotMastodonName.MatchString(name) {
		return "", false
	}
	return name, true
}

// Statuses without explicit public visibility and sensitive=false are unknown,
// and never enter shared evidence or a model prompt.
func autopilotMastodonSafeStatuses(data []byte, expectedFeed string) ([]jx.M, error) {
	var rows []json.RawMessage
	if err := json.Unmarshal(data, &rows); err != nil || rows == nil {
		return nil, errors.New("invalid Mastodon statuses")
	}
	safe := make([]json.RawMessage, 0, len(rows))
	for _, raw := range rows {
		var status struct {
			Sensitive  json.RawMessage `json:"sensitive"`
			Visibility string          `json:"visibility"`
			Account    struct {
				URL string `json:"url"`
			} `json:"account"`
		}
		if json.Unmarshal(raw, &status) != nil || !bytes.Equal(bytes.TrimSpace(status.Sensitive), []byte("false")) || status.Visibility != "public" {
			continue
		}
		if expectedFeed != "" && status.Account.URL+".rss" != expectedFeed {
			continue
		}
		safe = append(safe, raw)
	}
	clean, err := json.Marshal(safe)
	if err != nil {
		return nil, err
	}
	return MastodonParse(clean)
}

func (u *U) autopilotFindMastodon(query string) []DiscoveryCandidate {
	tag := strings.TrimLeft(strings.ReplaceAll(strings.ToLower(strings.TrimSpace(query)), " ", ""), "#")
	if tag == "" {
		return nil
	}
	r := u.RSSGet("https://mastodon.social/api/v1/timelines/tag/"+url.PathEscape(tag)+"?limit=40", 3<<20, 15*time.Second, HTTPUA)
	status := "unavailable"
	var out []DiscoveryCandidate
	final, parseErr := url.Parse(r.URL)
	if r.OK && parseErr == nil && final.Scheme == "https" && final.Host == "mastodon.social" && strings.HasPrefix(final.Path, "/api/v1/timelines/tag/") {
		posts, err := autopilotMastodonSafeStatuses(r.Bytes, "")
		if err == nil {
			status = "empty"
			seen := map[string]bool{}
			for _, post := range posts {
				candidate := PostSearchAuthor(post)
				if candidate == nil {
					continue
				}
				name, ok := autopilotMastodonFeed(candidate.ID)
				if !ok || seen[name] {
					continue
				}
				seen[name] = true
				candidate.Origin, candidate.OriginKind = "публичный поиск Mastodon «"+query+"»", "post_search"
				out = append(out, *candidate)
				if len(out) == 8 {
					break
				}
			}
			if len(out) > 0 {
				status = "ok"
			}
		}
	}
	if u.discoverySearchObservation != nil {
		u.discoverySearchObservation("mastodon", query, status)
	}
	return out
}

// Both calls use RSSGet/FetchPublic, so each consumes its own budget when
// invoked by a leased worker; no personal Mastodon token is read.
func (u *U) autopilotMastodonListing(feed string) FetchResultURL {
	name, ok := autopilotMastodonFeed(feed)
	if !ok {
		return FetchResultURL{FetchResult: FetchResult{Error: "invalid Mastodon feed"}}
	}
	lookup := u.RSSGet("https://mastodon.social/api/v1/accounts/lookup?acct="+url.QueryEscape(name), 1<<20, 15*time.Second, HTTPUA)
	if !lookup.OK {
		return lookup
	}
	lookupURL, err := url.Parse(lookup.URL)
	if err != nil || lookupURL.Scheme != "https" || lookupURL.Host != "mastodon.social" || lookupURL.Path != "/api/v1/accounts/lookup" {
		return FetchResultURL{FetchResult: FetchResult{Error: "unexpected Mastodon redirect"}}
	}
	var account struct {
		ID       string          `json:"id"`
		Username string          `json:"username"`
		URL      string          `json:"url"`
		Locked   json.RawMessage `json:"locked"`
	}
	if json.Unmarshal(lookup.Bytes, &account) != nil || !autopilotMastodonID.MatchString(account.ID) || strings.ToLower(account.Username) != name || account.URL+".rss" != feed || !bytes.Equal(bytes.TrimSpace(account.Locked), []byte("false")) {
		return FetchResultURL{FetchResult: FetchResult{Error: "unavailable public Mastodon account"}}
	}
	path := "/api/v1/accounts/" + account.ID + "/statuses"
	result := u.RSSGet("https://mastodon.social"+path+"?limit=20&exclude_reblogs=true", 3<<20, 15*time.Second, HTTPUA)
	final, err := url.Parse(result.URL)
	if err != nil || final.Scheme != "https" || final.Host != "mastodon.social" || final.Path != path {
		return FetchResultURL{FetchResult: FetchResult{Error: "unexpected Mastodon redirect"}}
	}
	return result
}
