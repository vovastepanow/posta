package posta

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"posta/internal/jx"
)

var autopilotTikTokPageRE = regexp.MustCompile(`^/@([A-Za-z0-9_.]{2,24})/video/([0-9]{5,25})$`)
var autopilotTikTokLinksRE = regexp.MustCompile(`https?://(?:www\.)?tiktok\.com/@[A-Za-z0-9_.]{2,24}/video/[0-9]{5,25}`)
var autopilotTikTokDataRE = regexp.MustCompile(`(?s)<script[^>]*id="__UNIVERSAL_DATA_FOR_REHYDRATION__"[^>]*>(.*?)</script>`)
var errAutopilotTikTokStreamUnavailable = errors.New("tiktok_stream_unavailable")

func autopilotTikTokVideoParts(raw string) (string, string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.RawQuery != "" || u.Fragment != "" || (u.Host != "www.tiktok.com" && u.Host != "tiktok.com") {
		return "", "", false
	}
	m := autopilotTikTokPageRE.FindStringSubmatch(u.Path)
	if len(m) != 3 || !IsValidTikTokUser(m[1]) || !tiktokIDRe.MatchString(m[2]) {
		return "", "", false
	}
	return strings.ToLower(m[1]), m[2], true
}

func autopilotTikTokStreamHost(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	host := strings.ToLower(u.Hostname())
	for _, suffix := range []string{".tiktok.com", ".tiktokcdn.com", ".tiktokcdn-eu.com", ".tiktokcdn-us.com", ".byteoversea.com", ".ibytedtos.com", ".muscdn.com"} {
		if strings.HasSuffix(host, suffix) && len(host) > len(suffix) {
			return true
		}
	}
	return false
}

type autopilotTikTokItem struct {
	ID         string            `json:"id"`
	Desc       string            `json:"desc"`
	Created    string            `json:"createTime"`
	Secret     *bool             `json:"secret"`
	Private    *bool             `json:"privateItem"`
	Friends    *bool             `json:"forFriend"`
	Prohibited *bool             `json:"isProhibited"`
	Review     *bool             `json:"isReviewing"`
	TakeDown   *int              `json:"takeDown"`
	Warn       []json.RawMessage `json:"warnInfo"`
	Author     struct {
		User    string `json:"uniqueId"`
		Private *bool  `json:"privateAccount"`
	} `json:"author"`
	Video struct {
		Duration int    `json:"duration"`
		Size     int64  `json:"size,string"`
		Format   string `json:"format"`
		Play     string `json:"playAddr"`
		Cover    string `json:"cover"`
	} `json:"video"`
}

// The page is authoritative for identity, public visibility and current stream.
// Stream URLs are returned only to the downloader. Cover URLs are transient
// preliminary evidence and are replaced by local storyboard frames on save.
func parseAutopilotTikTokPage(page []byte, user, id string) (jx.M, string, error) {
	m := autopilotTikTokDataRE.FindSubmatch(page)
	if len(m) != 2 {
		return nil, "", errors.New("TikTok page data unavailable")
	}
	var data struct {
		Scope struct {
			Detail struct {
				Status *int `json:"statusCode"`
				Item   struct {
					Video autopilotTikTokItem `json:"itemStruct"`
				} `json:"itemInfo"`
			} `json:"webapp.video-detail"`
		} `json:"__DEFAULT_SCOPE__"`
	}
	if json.Unmarshal(m[1], &data) != nil {
		return nil, "", errors.New("invalid TikTok page data")
	}
	item := data.Scope.Detail.Item.Video
	flags := []*bool{item.Secret, item.Private, item.Friends, item.Prohibited, item.Review, item.Author.Private}
	for _, flag := range flags {
		if flag == nil || *flag {
			return nil, "", errors.New("TikTok video is not confirmed public")
		}
	}
	if data.Scope.Detail.Status == nil || *data.Scope.Detail.Status != 0 || item.TakeDown == nil || *item.TakeDown != 0 || item.ID != id || !strings.EqualFold(item.Author.User, user) || len(item.Warn) != 0 || item.Video.Duration <= 0 || item.Video.Duration > tiktokMaxSeconds || item.Video.Size < 0 || item.Video.Size > publicClassificationVideoMaxBytes || !strings.EqualFold(item.Video.Format, "mp4") || !autopilotTikTokStreamHost(item.Video.Play) {
		return nil, "", errors.New("TikTok video unavailable")
	}
	pageURL := "https://www.tiktok.com/@" + user + "/video/" + id
	text := strings.TrimSpace(item.Desc)
	if text == "" {
		text = EmptyPostText
	}
	var created int64
	fmt.Sscan(item.Created, &created)
	post := jx.M{"text": trimRunes(text, 1000), "source": user, "post_id": id, "posted_at": created, "url": pageURL, "video_via": "tiktok_public", "video_page": pageURL, "duration": item.Video.Duration}
	if autopilotTikTokStreamHost(item.Video.Cover) {
		post["image"] = item.Video.Cover
	}
	return post, item.Video.Play, nil
}

func (u *U) autopilotDownloadTikTok(pageURL, dest string) error {
	return autopilotDownloadTikTokWith(pageURL, dest, u.RSSGet, u.FetchPublic)
}

// Some public CDNs use a generic binary content type for valid MP4/WebM.
// Sniff only the bounded temporary file; an HTML challenge remains rejected.
func autopilotTikTokVideoResponse(contentType, path string) bool {
	if strings.HasPrefix(strings.ToLower(contentType), "video/") {
		return true
	}
	if contentType != "" && !strings.HasPrefix(strings.ToLower(contentType), "application/octet-stream") {
		return false
	}
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	var prefix [512]byte
	n, _ := f.Read(prefix[:])
	mime := http.DetectContentType(prefix[:n])
	return mime == "video/mp4" || mime == "video/webm"
}

// Variants come exclusively from the validated page. Never persist signed URLs.
func autopilotTikTokStreams(page []byte, primary string) []string {
	streams := []string{primary}
	match := autopilotTikTokDataRE.FindSubmatch(page)
	if len(match) != 2 {
		return streams
	}
	var data struct {
		Scope struct {
			Detail struct {
				Item struct {
					Video struct {
						Video struct {
							Download string `json:"downloadAddr"`
							Play     struct {
								URLs []string `json:"UrlList"`
							} `json:"PlayAddrStruct"`
						} `json:"video"`
					} `json:"itemStruct"`
				} `json:"itemInfo"`
			} `json:"webapp.video-detail"`
		} `json:"__DEFAULT_SCOPE__"`
	}
	if json.Unmarshal(match[1], &data) != nil {
		return streams
	}
	seen := map[string]bool{primary: true}
	urls := append([]string{data.Scope.Detail.Item.Video.Video.Download}, data.Scope.Detail.Item.Video.Video.Play.URLs...)
	for _, raw := range urls {
		if !autopilotTikTokStreamHost(raw) || seen[raw] {
			continue
		}
		seen[raw] = true
		streams = append(streams, raw)
		if len(streams) == 3 {
			break
		}
	}
	return streams
}

func autopilotDownloadTikTokWith(pageURL, dest string, pageFetch func(string, int64, time.Duration, string) FetchResultURL, fetch func(string, FetchOpts) (FetchResult, error)) error {
	user, id, ok := autopilotTikTokVideoParts(pageURL)
	if !ok {
		return errors.New("invalid TikTok URL")
	}
	page := pageFetch("https://www.tiktok.com/@"+user+"/video/"+id, 3<<20, 20*time.Second, BrowserUA)
	if !page.OK {
		return errors.New("TikTok page unavailable")
	}
	finalUser, finalID, ok := autopilotTikTokVideoParts(page.URL)
	if !ok || finalUser != user || finalID != id {
		return errors.New("unexpected TikTok page URL")
	}
	_, stream, err := parseAutopilotTikTokPage(page.Bytes, user, id)
	if err != nil {
		return err
	}
	lastFailure := "TikTok stream unavailable"
	for _, initial := range autopilotTikTokStreams(page.Bytes, stream) {
		stream = initial
		for hop := 0; hop <= 3; hop++ {
			if !autopilotTikTokStreamHost(stream) {
				return errors.New("invalid TikTok stream host")
			}
			res, err := fetch(stream, FetchOpts{MaxBytes: publicClassificationVideoMaxBytes, Timeout: 90 * time.Second, Dest: dest, UserAgent: BrowserUA, Headers: map[string]string{"Referer": "https://www.tiktok.com/"}})
			if err != nil {
				return err
			}
			if res.Status >= 300 && res.Status < 400 && res.Location != "" {
				stream = RSSAbsoluteURL(stream, res.Location)
				continue
			}
			if !res.OK {
				lastFailure = fmt.Sprintf("TikTok stream unavailable (HTTP %d)", res.Status)
				if res.Status == 403 || res.Status == 404 || res.Status >= 500 {
					break
				}
				return fmt.Errorf("TikTok stream unavailable (HTTP %d)", res.Status)
			}
			if !autopilotTikTokVideoResponse(res.Type, dest) {
				lastFailure = "TikTok stream returned non-video content"
				break
			}
			return nil
		}
	}
	return errors.New(lastFailure)
}

func (u *U) autopilotTikTokListing(user string) FetchResultURL {
	search := u.autopilotTikTokSearch("site:tiktok.com/@" + user + "/video/")
	if !search.OK {
		return search
	}
	if !autopilotTikTokSearchResponseValid(search) {
		return FetchResultURL{}
	}
	var posts []jx.M
	seen := map[string]bool{}
	for _, link := range autopilotTikTokLinksRE.FindAllString(string(search.Bytes), -1) {
		author, id, ok := autopilotTikTokVideoParts(link)
		if !ok || author != user || seen[id] {
			continue
		}
		seen[id] = true
		raw := "https://www.tiktok.com/@" + user + "/video/" + id
		page := u.RSSGet(raw, 3<<20, 20*time.Second, BrowserUA)
		finalUser, finalID, valid := autopilotTikTokVideoParts(page.URL)
		if page.OK && valid && finalUser == user && finalID == id {
			if post, _, err := parseAutopilotTikTokPage(page.Bytes, user, id); err == nil {
				posts = append(posts, post)
			}
		}
		if len(seen) >= 5 {
			break
		}
	}
	if len(posts) == 0 {
		return FetchResultURL{}
	}
	b, err := json.Marshal(posts)
	if err != nil {
		return FetchResultURL{}
	}
	return FetchResultURL{URL: "https://www.tiktok.com/@" + user, FetchResult: FetchResult{OK: true, Bytes: b}}
}
