package posta

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strings"
	"time"

	"posta/internal/jx"
)

var autopilotBlueskyRecordKey = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

// The DID is stable when an account changes its handle. Bind the record URI
// and the profile URL to the same DID before allowing source nomination.
func autopilotBlueskyProfileDID(raw string) string {
	p, err := url.Parse(raw)
	if err != nil || p.Scheme != "https" || p.Host != "bsky.app" || p.User != nil || p.RawQuery != "" || p.Fragment != "" {
		return ""
	}
	parts := strings.Split(strings.Trim(p.Path, "/"), "/")
	if len(parts) != 2 || parts[0] != "profile" || !blueskyDID.MatchString(parts[1]) {
		return ""
	}
	return parts[1]
}

func autopilotBlueskyTextPosts(data []byte, at int64) ([]jx.M, error) {
	var response struct {
		Posts []struct {
			URI    string            `json:"uri"`
			Labels []json.RawMessage `json:"labels"`
			Embed  json.RawMessage   `json:"embed"`
			Author struct {
				DID         string            `json:"did"`
				DisplayName string            `json:"displayName"`
				Labels      []json.RawMessage `json:"labels"`
			} `json:"author"`
			Record struct {
				Type      string          `json:"$type"`
				Text      string          `json:"text"`
				CreatedAt string          `json:"createdAt"`
				Embed     json.RawMessage `json:"embed"`
				Reply     json.RawMessage `json:"reply"`
				Labels    json.RawMessage `json:"labels"`
			} `json:"record"`
		} `json:"posts"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	if response.Posts == nil {
		return nil, errors.New("unrecognized Bluesky post search")
	}
	var out []jx.M
	seen := map[string]bool{}
	hasValue := func(raw json.RawMessage) bool { return len(raw) > 0 && string(raw) != "null" }
	for _, row := range response.Posts {
		did := row.Author.DID
		prefix := "at://" + did + "/app.bsky.feed.post/"
		if !blueskyDID.MatchString(did) || !strings.HasPrefix(row.URI, prefix) ||
			row.Record.Type != "app.bsky.feed.post" || len(row.Labels) > 0 || len(row.Author.Labels) > 0 ||
			hasValue(row.Record.Embed) || hasValue(row.Embed) || hasValue(row.Record.Reply) || hasValue(row.Record.Labels) {
			continue
		}
		key := strings.TrimPrefix(row.URI, prefix)
		stamp, err := time.Parse(time.RFC3339Nano, row.Record.CreatedAt)
		if !autopilotBlueskyRecordKey.MatchString(key) || err != nil || seen[row.URI] ||
			stamp.Unix() > at+300 || IsPostOutdated(stamp.Unix(), at, 2) {
			continue
		}
		profile := "https://bsky.app/profile/" + did
		source := "rss:" + profile + "/rss"
		post := jx.M{"url": profile + "/post/" + key, "source_url": profile,
			"source_key": source, "source": row.Author.DisplayName, "platform": PlatformRSS,
			"posted_at": stamp.Unix(), "text": strings.TrimSpace(row.Record.Text)}
		if len(autopilotSafePostHints(source, []jx.M{post}, at)) != 1 {
			continue
		}
		seen[row.URI] = true
		out = append(out, post)
		if len(out) >= 25 {
			break
		}
	}
	return out, nil
}

func (u *U) autopilotSearchBlueskyPosts(query string) ([]jx.M, error) {
	endpoint := "https://public.api.bsky.app/xrpc/app.bsky.feed.searchPosts?limit=25&sort=latest&q=" + url.QueryEscape(query)
	r := u.RSSGet(endpoint, 2<<20, 12*time.Second, HTTPUA)
	p, err := url.Parse(r.URL)
	if !r.OK || err != nil || p.Scheme != "https" || p.Host != "public.api.bsky.app" || p.User != nil || p.Path != "/xrpc/app.bsky.feed.searchPosts" {
		return nil, errors.New("public Bluesky post search unavailable")
	}
	return autopilotBlueskyTextPosts(r.Bytes, time.Now().Unix())
}
