package posta

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"posta/internal/jx"
)

// The legacy shared cache excludes Reddit because its ordinary reader can use
// credentials. Auto uses only this canonical public feed identity and makes its
// own anonymous, budgeted JSON requests; it never writes that shared cache.
func autopilotPublicRedditFeed(raw string) bool {
	community := redditSourceName(raw)
	return community != "" && raw == "https://www.reddit.com/r/"+community+"/.rss"
}

// Retain only rows that explicitly carry a false safety flag. Missing flags
// must not turn an unknown Reddit post or community into a public clean one.
func autopilotRedditSafeListing(data []byte, safetyKey string) ([]byte, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	var listing map[string]json.RawMessage
	if err := json.Unmarshal(root["data"], &listing); err != nil || listing == nil {
		return nil, errors.New("invalid Reddit listing")
	}
	var rows []json.RawMessage
	if raw, ok := listing["children"]; !ok || json.Unmarshal(raw, &rows) != nil || rows == nil {
		return nil, errors.New("invalid Reddit listing children")
	}
	safe := make([]json.RawMessage, 0, len(rows))
	for _, row := range rows {
		var envelope struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if json.Unmarshal(row, &envelope) != nil || envelope.Data == nil {
			continue
		}
		raw, ok := envelope.Data[safetyKey]
		if !ok || !bytes.Equal(bytes.TrimSpace(raw), []byte("false")) {
			continue
		}
		safe = append(safe, row)
	}
	children, err := json.Marshal(safe)
	if err != nil {
		return nil, err
	}
	listing["children"] = children
	root["data"], err = json.Marshal(listing)
	if err != nil {
		return nil, err
	}
	return json.Marshal(root)
}

func autopilotRedditListing(data []byte, feed, finalURL string) ([]jx.M, error) {
	if !autopilotPublicRedditFeed(feed) {
		return nil, errors.New("invalid public Reddit feed")
	}
	u, err := url.Parse(finalURL)
	if err != nil || u.Scheme != "https" || u.Host != "www.reddit.com" || u.User != nil || u.Path != "/r/"+redditSourceName(feed)+"/new.json" {
		return nil, errors.New("unexpected Reddit listing URL")
	}
	clean, err := autopilotRedditSafeListing(data, "over_18")
	if err != nil {
		return nil, err
	}
	return redditListingPosts(clean, redditSourceName(feed), 10, true)
}

func (u *U) autopilotFindReddit(query string) []DiscoveryCandidate {
	body, err := u.searchGetJSON("https://www.reddit.com/subreddits/search.json?limit=25&q=" + url.QueryEscape(query))
	if err == nil {
		body, err = autopilotRedditSafeListing(body, "over18")
	}
	var candidates []DiscoveryCandidate
	if err == nil {
		candidates, err = discoveryRedditCommunities(body)
	}
	status := "unavailable"
	if err == nil {
		status = "ok"
		for i := range candidates {
			candidates[i].Origin = "публичный поиск Reddit «" + strings.TrimSpace(query) + "»"
			candidates[i].OriginKind = "other"
		}
	}
	if u.discoverySearchObservation != nil {
		u.discoverySearchObservation("reddit", query, status)
	}
	return candidates
}
