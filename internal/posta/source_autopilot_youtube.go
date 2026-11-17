package posta

import (
	"encoding/json"
	"errors"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var errAutopilotYouTubeStreamUnavailable = errors.New("youtube_stream_unavailable")

type autopilotYouTubeFormat struct {
	URL    string `json:"url"`
	Cipher string `json:"signatureCipher"`
	MIME   string `json:"mimeType"`
	Audio  string `json:"audioQuality"`
	Length string `json:"contentLength"`
}

type autopilotYouTubePlayer struct {
	Playability struct {
		Status string `json:"status"`
	} `json:"playabilityStatus"`
	Details struct {
		ID       string `json:"videoId"`
		Duration string `json:"lengthSeconds"`
		Live     bool   `json:"isLiveContent"`
		Private  bool   `json:"isPrivate"`
	} `json:"videoDetails"`
	Microformat struct {
		Player struct {
			FamilySafe *bool `json:"isFamilySafe"`
		} `json:"playerMicroformatRenderer"`
	} `json:"microformat"`
	Streaming struct {
		Formats []autopilotYouTubeFormat `json:"formats"`
	} `json:"streamingData"`
}

var youtubePlayerReference = regexp.MustCompile(`"(?:PLAYER_JS_URL|jsUrl)"\s*:\s*("(?:\\.|[^"\\])*")`)
var youtubePlayerPath = regexp.MustCompile(`^/s/player/[a-zA-Z0-9_-]+/[a-zA-Z0-9_./-]+/base\.js$`)
var youtubeSignatureParam = regexp.MustCompile(`^[a-zA-Z0-9_]{1,32}$`)

// Only a public YouTube watch or Shorts page with a validated video ID may
// produce a stream request. No cookies, browser session or external process.
func autopilotYouTubeVideoID(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" || (u.Host != "www.youtube.com" && u.Host != "youtube.com") {
		return ""
	}
	id := ""
	if u.Path == "/watch" {
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(q) != 1 || len(q["v"]) != 1 {
			return ""
		}
		id = q.Get("v")
	} else if strings.HasPrefix(u.Path, "/shorts/") && u.RawQuery == "" {
		id = strings.TrimPrefix(u.Path, "/shorts/")
	}
	if !videoIDRe.MatchString(id) {
		return ""
	}
	return id
}

func autopilotYouTubeStreamHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return strings.HasSuffix(host, ".googlevideo.com") && len(host) > len(".googlevideo.com")
}

// Select only a bounded, directly playable muxed MP4 with audio. Ciphered,
// adaptive, live, unavailable and oversized formats remain unassessed.
func parseAutopilotYouTubePlayer(page []byte, id string) (*autopilotYouTubePlayer, error) {
	if !videoIDRe.MatchString(id) {
		return nil, errors.New("invalid YouTube video")
	}
	const marker = "ytInitialPlayerResponse"
	pos := strings.Index(string(page), marker)
	if pos < 0 {
		return nil, errors.New("YouTube player unavailable")
	}
	rest := page[pos+len(marker):]
	eq := strings.IndexByte(string(rest), '=')
	if eq < 0 || eq > 64 {
		return nil, errors.New("YouTube player unavailable")
	}
	var player autopilotYouTubePlayer
	if err := json.NewDecoder(strings.NewReader(string(rest[eq+1:]))).Decode(&player); err != nil {
		return nil, errors.New("invalid YouTube player")
	}
	duration, err := strconv.Atoi(player.Details.Duration)
	if err != nil || duration <= 0 || duration > ytdlpWatchMaxSeconds || player.Details.Live || player.Details.Private || player.Details.ID != id || player.Playability.Status != "OK" || (player.Microformat.Player.FamilySafe != nil && !*player.Microformat.Player.FamilySafe) {
		return nil, errors.New("YouTube video unavailable")
	}
	return &player, nil
}

func autopilotYouTubeStream(page []byte, id string) (string, error) {
	player, err := parseAutopilotYouTubePlayer(page, id)
	if err != nil {
		return "", err
	}
	for _, f := range player.Streaming.Formats {
		size, err := strconv.ParseInt(f.Length, 10, 64)
		if err == nil && size > 0 && size <= publicClassificationVideoMaxBytes && strings.HasPrefix(strings.ToLower(f.MIME), "video/mp4") && f.Audio != "" && autopilotYouTubeStreamHost(f.URL) {
			return f.URL, nil
		}
	}
	return "", errors.New("no bounded YouTube stream")
}

// Resolve a muxed format without handing network access to the JS runtime.
// Signed URLs and challenges remain transient and are never written to state.
func resolveAutopilotYouTubeStream(page []byte, id string, fetch func(string, FetchOpts) (FetchResult, error), solve func([]byte, []youtubeChallenge) ([]map[string]string, error)) (string, error) {
	player, err := parseAutopilotYouTubePlayer(page, id)
	if err != nil {
		return "", err
	}
	for _, format := range player.Streaming.Formats {
		if !strings.HasPrefix(strings.ToLower(format.MIME), "video/mp4") || format.Audio == "" {
			continue
		}
		if format.Length != "" {
			size, err := strconv.ParseInt(format.Length, 10, 64)
			if err != nil || size <= 0 || size > publicClassificationVideoMaxBytes {
				continue
			}
		}
		stream, signature, sigParam := format.URL, "", ""
		if stream == "" && format.Cipher != "" {
			cipher, err := url.ParseQuery(format.Cipher)
			if err != nil || len(cipher["url"]) != 1 || len(cipher["s"]) != 1 || len(cipher["sp"]) > 1 {
				continue
			}
			stream, signature, sigParam = cipher.Get("url"), cipher.Get("s"), cipher.Get("sp")
			if sigParam == "" {
				sigParam = "signature"
			}
			if !youtubeSignatureParam.MatchString(sigParam) || (sigParam != "sig" && sigParam != "signature") || signature == "" {
				continue
			}
		}
		if !autopilotYouTubeStreamHost(stream) {
			continue
		}
		parsed, err := url.Parse(stream)
		if err != nil {
			continue
		}
		values, err := url.ParseQuery(parsed.RawQuery)
		if err != nil || len(values["n"]) > 1 {
			continue
		}
		n := values.Get("n")
		var requests []youtubeChallenge
		if signature != "" {
			requests = append(requests, youtubeChallenge{Type: "sig", Challenges: []string{signature}})
		}
		if n != "" {
			requests = append(requests, youtubeChallenge{Type: "n", Challenges: []string{n}})
		}
		if len(requests) > 0 {
			if solve == nil {
				continue
			}
			match := youtubePlayerReference.FindSubmatch(page)
			if len(match) != 2 {
				continue
			}
			var path string
			if json.Unmarshal(match[1], &path) != nil || len(path) > 512 || strings.Contains(path, "..") || !youtubePlayerPath.MatchString(path) {
				continue
			}
			js, err := fetch("https://www.youtube.com"+path, FetchOpts{MaxBytes: 5 << 20, Timeout: 15 * time.Second, UserAgent: BrowserUA})
			if err != nil {
				return "", err
			}
			if !js.OK {
				return "", errors.New("YouTube player script unavailable")
			}
			results, err := solve(js.Bytes, requests)
			if err != nil || len(results) != len(requests) {
				return "", errors.New("YouTube challenge unresolved")
			}
			for i, request := range requests {
				answer := results[i][request.Challenges[0]]
				if answer == "" || len(answer) > 4096 {
					return "", errors.New("YouTube challenge unresolved")
				}
				if request.Type == "sig" {
					values.Set(sigParam, answer)
				} else {
					values.Set("n", answer)
				}
			}
		}
		parsed.RawQuery = values.Encode()
		if autopilotYouTubeStreamHost(parsed.String()) {
			return parsed.String(), nil
		}
	}
	return "", errors.New("no bounded YouTube stream")
}

func (u *U) autopilotDownloadYouTube(pageURL, dest string) error {
	return autopilotDownloadYouTubeWithSolver(pageURL, dest, u.FetchPublic, func(player []byte, requests []youtubeChallenge) ([]map[string]string, error) {
		return u.App.solveAutopilotYouTube(u.Ctx, player, requests)
	})
}

func autopilotDownloadYouTubeWith(pageURL, dest string, fetch func(string, FetchOpts) (FetchResult, error)) error {
	return autopilotDownloadYouTubeWithSolver(pageURL, dest, fetch, nil)
}

func autopilotDownloadYouTubeWithSolver(pageURL, dest string, fetch func(string, FetchOpts) (FetchResult, error), solve func([]byte, []youtubeChallenge) ([]map[string]string, error)) error {
	id := autopilotYouTubeVideoID(pageURL)
	if id == "" {
		return errors.New("invalid YouTube URL")
	}
	watch := "https://www.youtube.com/watch?v=" + id
	page, err := fetch(watch, FetchOpts{MaxBytes: 5 << 20, Timeout: 15 * time.Second, UserAgent: BrowserUA})
	if err != nil {
		return err
	}
	if !page.OK {
		return errors.New("YouTube page unavailable")
	}
	stream, err := resolveAutopilotYouTubeStream(page.Bytes, id, fetch, solve)
	if err != nil {
		return err
	}
	for hop := 0; hop <= 3; hop++ {
		if !autopilotYouTubeStreamHost(stream) {
			return errors.New("invalid YouTube stream host")
		}
		res, err := fetch(stream, FetchOpts{MaxBytes: publicClassificationVideoMaxBytes, Timeout: 90 * time.Second, Dest: dest})
		if err != nil {
			return err
		}
		if res.Status >= 300 && res.Status < 400 && res.Location != "" {
			stream = RSSAbsoluteURL(stream, res.Location)
			continue
		}
		if !res.OK {
			return errors.New("YouTube stream unavailable")
		}
		return nil
	}
	return errors.New("too many YouTube stream redirects")
}

// Search pages supply candidate channel links only; each resulting feed is
// fetched and assessed independently by the ordinary source pipeline.
func (u *U) autopilotFindYouTube(query string) []DiscoveryCandidate {
	r := u.autopilotBraveSearch("site:youtube.com/channel " + query)
	final, err := url.Parse(r.URL)
	status := "unavailable"
	var out []DiscoveryCandidate
	if r.OK && err == nil && final.Scheme == "https" && final.Host == "search.brave.com" && !strings.Contains(strings.ToLower(string(r.Bytes)), "captcha") {
		status = "empty"
		seen := map[string]bool{}
		handles := 0
		for _, candidate := range SourceLinkedSources(string(r.Bytes)) {
			if candidate.Platform != PlatformRSS {
				continue
			}
			feed, ok := canonicalYouTubeFeed(candidate.ID)
			if !ok && strings.HasPrefix(candidate.ID, "https://www.youtube.com/@") && handles < 3 {
				handles++
				page := u.RSSGet(candidate.ID, 3<<20, 15*time.Second, BrowserUA)
				finalPage, parseErr := url.Parse(page.URL)
				if page.OK && parseErr == nil && finalPage.Scheme == "https" && finalPage.Host == "www.youtube.com" {
					m := ytPageIDRe.FindStringSubmatch(string(page.Bytes))
					if len(m) > 2 {
						id := m[1]
						if id == "" {
							id = m[2]
						}
						if ytChannelIDRe.MatchString(id) {
							feed, ok = YouTubeChannelFeedURL(id), true
						}
					}
				}
			}
			if !ok || seen[feed] {
				continue
			}
			seen[feed] = true
			out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: feed, Origin: "публичный поиск YouTube «" + query + "»", OriginKind: "youtube"})
			if len(out) >= 8 {
				break
			}
		}
		if len(out) > 0 {
			status = "ok"
		}
	}
	if u.discoverySearchObservation != nil {
		u.discoverySearchObservation("youtube", query, status)
	}
	return out
}
