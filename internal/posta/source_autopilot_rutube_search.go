package posta

import (
	"encoding/json"
	"errors"
	"net/url"
	"time"

	"posta/internal/jx"
)

func autopilotRutubeSearchCandidates(r FetchResultURL, at int64) ([]DiscoveryCandidate, error) {
	p, err := url.Parse(r.URL)
	if !r.OK || err != nil || p.Scheme != "https" || p.Host != "rutube.ru" || p.User != nil || p.Path != "/api/search/video/" {
		return nil, errors.New("Rutube search unavailable")
	}
	var envelope struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := json.Unmarshal(r.Bytes, &envelope); err != nil || envelope.Results == nil {
		return nil, errors.New("unrecognized Rutube search response")
	}
	posts, err := RutubeParse(r.Bytes)
	if err != nil {
		return nil, err
	}
	return autopilotCandidatesFromPosts(map[string][]jx.M{PlatformRutube: posts}, at, 8), nil
}

func (u *U) autopilotFindRutube(query string) []DiscoveryCandidate {
	r := u.RSSGet("https://rutube.ru/api/search/video/?query="+url.QueryEscape(query)+"&page=1", 3<<20, 15*time.Second, HTTPUA)
	out, err := autopilotRutubeSearchCandidates(r, time.Now().Unix())
	status := "unavailable"
	if err == nil {
		status = "empty"
		if len(out) > 0 {
			status = "ok"
		}
	}
	if u.discoverySearchObservation != nil {
		u.discoverySearchObservation("rutube", query, status)
	}
	return out
}
