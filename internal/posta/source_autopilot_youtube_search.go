package posta

import (
	"encoding/json"
	"errors"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"posta/internal/jx"
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
	type resultText struct {
		Simple string `json:"simpleText"`
		Runs   []struct {
			Text string `json:"text"`
		} `json:"runs"`
	}
	readText := func(t resultText) string {
		if t.Simple != "" {
			return t.Simple
		}
		var text strings.Builder
		for _, run := range t.Runs {
			text.WriteString(run.Text)
		}
		return text.String()
	}
	type result struct {
		Channel struct {
			ID    string     `json:"channelId"`
			Title resultText `json:"title"`
		} `json:"channelRenderer"`
		Video struct {
			ID    string     `json:"videoId"`
			Title resultText `json:"title"`
			Published resultText `json:"publishedTimeText"`
			Owner struct {
				Runs []struct {
					Text     string `json:"text"`
					Endpoint struct {
						Browse struct {
							ID string `json:"browseId"`
						} `json:"browseEndpoint"`
					} `json:"navigationEndpoint"`
				} `json:"runs"`
			} `json:"ownerText"`
		} `json:"videoRenderer"`
	}
	type nomination struct {
		id, name, sample string
		matchingVideos   int
		freshMatches     int
		matchingName     bool
	}
	query := p.Query().Get("search_query")
	byID := map[string]*nomination{}
	seenVideo := map[string]bool{}
	var ranked []*nomination
	get := func(id, name string) *nomination {
		if !ytChannelIDRe.MatchString(id) {
			return nil
		}
		n := byID[id]
		if n == nil {
			if len(ranked) >= 100 {
				return nil
			}
			n = &nomination{id: id}
			byID[id] = n
			ranked = append(ranked, n)
		}
		if name != "" && n.name == "" {
			n.name = trimRunes(name, 120)
		}
		n.matchingName = n.matchingName || discoveryQueryEvidenceMatch(query, name)
		return n
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
			get(item.Channel.ID, readText(item.Channel.Title))
			if videoIDRe.MatchString(item.Video.ID) && !seenVideo[item.Video.ID] && len(item.Video.Owner.Runs) == 1 {
				owner := item.Video.Owner.Runs[0]
				n := get(owner.Endpoint.Browse.ID, owner.Text)
				if n == nil {
					continue
				}
				seenVideo[item.Video.ID] = true
				title := readText(item.Video.Title)
				if n.sample == "" {
					n.sample = trimRunes(title, 180)
				}
				if discoveryQueryEvidenceMatch(query, title) {
					n.matchingVideos++
					if autopilotYouTubeRecentPublication(readText(item.Video.Published)) {
						n.freshMatches++
					}
					n.sample = trimRunes(title, 180)
				}
			}
		}
	}
	// Multiple matching videos and a matching channel name are useful
	// nomination signals. They never approve a source; semantic synonyms
	// without literal overlap retain their native rank rather than being
	// discarded before the normal content review.
	score := func(n *nomination) int {
		rank := min(n.matchingVideos, 3)*2 + min(n.freshMatches, 3)*5
		if n.matchingName {
			rank++
		}
		return rank
	}
	sort.SliceStable(ranked, func(i, j int) bool { return score(ranked[i]) > score(ranked[j]) })
	var out []DiscoveryCandidate
	for _, n := range ranked[:min(len(ranked), 8)] {
		out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: YouTubeChannelFeedURL(n.id), OriginKind: "youtube",
			Info: jx.M{"title": n.name, "search_video_title": n.sample, "matching_videos": n.matchingVideos, "fresh_matches": n.freshMatches}})
	}
	return out, nil
}

// Relative publication time is only a positive ranking signal. Unknown or
// localized labels never exclude a channel, and live broadcasts are not
// assumed to have completed. Full review still uses actual publication dates.
func autopilotYouTubeRecentPublication(label string) bool {
	words := strings.Fields(strings.ToLower(label))
	if len(words) == 4 && (words[0] == "streamed" || words[0] == "premiered") {
		words = words[1:]
	}
	if len(words) != 3 || words[2] != "ago" {
		return false
	}
	n, err := strconv.Atoi(words[0])
	if err != nil || n < 0 {
		return false
	}
	switch strings.TrimSuffix(words[1], "s") {
	case "second", "minute", "hour":
		return n <= 60
	case "day":
		return n <= 30
	case "week":
		return n <= 4
	case "month":
		return n <= 1
	default:
		return false
	}
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
