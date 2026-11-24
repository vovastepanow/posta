package posta

import (
	"encoding/json"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Use the public search results themselves, rather than requiring an
// external index to contain a channel page. Only main result renderers can
// nominate a publisher; suggestions, ads and arbitrary script IDs cannot.
func autopilotYouTubeSearchCandidates(r FetchResultURL) ([]DiscoveryCandidate, error) {
	p, err := url.Parse(r.URL)
	if !r.OK || err != nil || p.Scheme != "https" || p.Host != "www.youtube.com" || p.User != nil || p.Path != "/results" {
		return nil, errors.New("YouTube search unavailable")
	}
	const marker = "var ytInitialData ="
	page := string(r.Bytes)
	pos := strings.Index(page, marker)
	if pos < 0 {
		return nil, errors.New("YouTube search data unavailable")
	}
	var data struct {
		Contents struct {
			Results struct {
				Primary struct {
					Sections struct {
						Contents []json.RawMessage `json:"contents"`
					} `json:"sectionListRenderer"`
				} `json:"primaryContents"`
			} `json:"twoColumnSearchResultsRenderer"`
		} `json:"contents"`
	}
	if err := json.NewDecoder(strings.NewReader(page[pos+len(marker):])).Decode(&data); err != nil {
		return nil, errors.New("invalid YouTube search data")
	}
	sections := data.Contents.Results.Primary.Sections.Contents
	if sections == nil {
		return nil, errors.New("unrecognized YouTube search layout")
	}
	type result struct {
		Channel struct {
			ID string `json:"channelId"`
		} `json:"channelRenderer"`
		Video struct {
			ID string `json:"videoId"`
			Owner struct {
				Runs []struct {
					Endpoint struct {
						Browse struct {
							ID string `json:"browseId"`
						} `json:"browseEndpoint"`
					} `json:"navigationEndpoint"`
				} `json:"runs"`
			} `json:"ownerText"`
		} `json:"videoRenderer"`
	}
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	add := func(id string) {
		if len(out) >= 8 || !ytChannelIDRe.MatchString(id) || seen[id] {
			return
		}
		seen[id] = true
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: YouTubeChannelFeedURL(id), OriginKind: "youtube"})
	}
	for _, raw := range sections {
		var section struct {
			Items struct {
				Contents []result `json:"contents"`
			} `json:"itemSectionRenderer"`
		}
		if json.Unmarshal(raw, &section) != nil {
			continue
		}
		for _, item := range section.Items.Contents {
			add(item.Channel.ID)
			if videoIDRe.MatchString(item.Video.ID) && len(item.Video.Owner.Runs) == 1 {
				add(item.Video.Owner.Runs[0].Endpoint.Browse.ID)
			}
		}
	}
	return out, nil
}

var autopilotNativeYouTubePause struct {
	sync.Mutex
	until time.Time
}

func (u *U) autopilotNativeYouTubeSearch(query string) ([]DiscoveryCandidate, error) {
	return u.autopilotNativeYouTubeSearchWith(query, u.RSSGet)
}

func (u *U) autopilotNativeYouTubeSearchWith(query string, fetch func(string, int64, time.Duration, string) FetchResultURL) ([]DiscoveryCandidate, error) {
	autopilotNativeYouTubePause.Lock()
	until := autopilotNativeYouTubePause.until
	autopilotNativeYouTubePause.Unlock()
	if u.DB != nil {
		if saved := KV(u.App, u.Ctx, "autopilot_youtube_search_pause_until", int64(0)); saved > until.Unix() {
			until = time.Unix(saved, 0)
		}
	}
	if time.Now().Before(until) {
		return nil, errors.New("YouTube search cooldown")
	}
	r := fetch("https://www.youtube.com/results?search_query="+url.QueryEscape(query), 5<<20, 15*time.Second, BrowserUA)
	if r.Status == 429 {
		until = autopilotBraveRetryAt(time.Now(), r.RetryAfter)
		autopilotNativeYouTubePause.Lock()
		if until.After(autopilotNativeYouTubePause.until) {
			autopilotNativeYouTubePause.until = until
		}
		autopilotNativeYouTubePause.Unlock()
		if u.DB != nil {
			_ = u.KVSet(u.Ctx, "autopilot_youtube_search_pause_until", until.Unix(), time.Until(until))
		}
	}
	return autopilotYouTubeSearchCandidates(r)
}
