package posta

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
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
		post := searchPost(PlatformRSS, source, profile, "", strings.TrimSpace(row.Record.Text), "", "", profile+"/post/"+key, stamp.Unix())
		post["source"] = row.Author.DisplayName
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

const autopilotBlueskyAPI = "https://public.api.bsky.app/xrpc/"

var errAutopilotBlueskySearchRestricted = errors.New("Bluesky global post search requires access")
var autopilotBlueskyPostSearchPause struct {
	sync.Mutex
	until time.Time
}

// Only pause the restricted global post search, not public actor or feed reads.
func (u *U) autopilotBlueskySearchData(query string) ([]byte, error) {
	autopilotBlueskyPostSearchPause.Lock()
	paused := time.Now().Before(autopilotBlueskyPostSearchPause.until)
	autopilotBlueskyPostSearchPause.Unlock()
	if paused {
		return nil, errAutopilotBlueskySearchRestricted
	}
	endpoint := autopilotBlueskyAPI + "app.bsky.feed.searchPosts?limit=25&sort=latest&q=" + url.QueryEscape(query)
	r, err := u.FetchPublic(endpoint, FetchOpts{MaxBytes: 2 << 20, Timeout: 12*time.Second, UserAgent: HTTPUA, Headers: map[string]string{"Accept": "application/json"}})
	if err != nil {
		return nil, err
	}
	if r.Status == http.StatusUnauthorized || r.Status == http.StatusForbidden {
		autopilotBlueskyPostSearchPause.Lock()
		autopilotBlueskyPostSearchPause.until = time.Now().Add(6*time.Hour)
		autopilotBlueskyPostSearchPause.Unlock()
		return nil, errAutopilotBlueskySearchRestricted
	}
	if !r.OK {
		return nil, fmt.Errorf("Bluesky post search HTTP %d", r.Status)
	}
	return r.Bytes, nil
}

// An author feed may include reposts and replies. Only a record written by
// the requested stable DID is eligible, not a quoted or republished record.
func autopilotBlueskyAuthorTextPosts(data []byte, did string, at int64) ([]jx.M, error) {
	if !blueskyDID.MatchString(did) {
		return nil, errors.New("invalid Bluesky author")
	}
	var response struct {
		Feed []struct {
			Post json.RawMessage `json:"post"`
			Reason json.RawMessage `json:"reason"`
			Reply json.RawMessage `json:"reply"`
		} `json:"feed"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	if response.Feed == nil {
		return nil, errors.New("unrecognized Bluesky author feed")
	}
	records := make([]json.RawMessage, 0, len(response.Feed))
	for _, row := range response.Feed {
		if (len(row.Reason)>0 && string(row.Reason)!="null") || (len(row.Reply)>0 && string(row.Reply)!="null") {
			continue
		}
		if len(row.Post)>0 {
			records = append(records,row.Post)
		}
	}
	body, err := json.Marshal(map[string][]json.RawMessage{"posts":records})
	if err != nil {
		return nil, err
	}
	posts, err := autopilotBlueskyTextPosts(body, at)
	if err != nil {
		return nil, err
	}
	var out []jx.M
	for _, post := range posts {
		if jx.Str(post,"source_key") == "rss:https://bsky.app/profile/"+did+"/rss" {
			out = append(out,post)
		}
	}
	return out,nil
}

// A bounded fallback uses public actor search and author feeds. The
// individual posts still go through the existing independent AI gate.
func autopilotBlueskyAuthorFallback(query string, at int64, fetch func(string)([]byte,error)) ([]jx.M,error) {
	body,err := fetch(autopilotBlueskyAPI+"app.bsky.actor.searchActors?limit=12&q="+url.QueryEscape(query))
	if err!=nil {
		return nil,err
	}
	actors,err:=discoveryBlueskyActors(body)
	if err!=nil {
		return nil,err
	}
	seen:=map[string]bool{}
	var out []jx.M
	var lastErr error
	attempted, successful:=0,0
	for _, actor:=range actors {
		did:=autopilotBlueskyProfileDID(strings.TrimSuffix(actor.ID,"/rss"))
		if did=="" || seen[did] {
			continue
		}
		seen[did]=true
		if attempted>=3 {
			break
		}
		attempted++
		data,err:=fetch(autopilotBlueskyAPI+"app.bsky.feed.getAuthorFeed?limit=12&filter=posts_no_replies&actor="+url.QueryEscape(did))
		if err!=nil {
			lastErr=err
			continue
		}
		posts,err:=autopilotBlueskyAuthorTextPosts(data,did,at)
		if err!=nil {
			lastErr=err
			continue
		}
		successful++
		out=append(out,posts...)
	}
	if successful==0 && lastErr!=nil {
		return nil,lastErr
	}
	return out,nil
}

func (u *U) autopilotSearchBlueskyPosts(query string) ([]jx.M, error) {
	data, err := u.autopilotBlueskySearchData(query)
	if errors.Is(err, errAutopilotBlueskySearchRestricted) {
		return autopilotBlueskyAuthorFallback(query,time.Now().Unix(),u.searchGetJSON)
	}
	if err != nil {
		return nil,err
	}
	return autopilotBlueskyTextPosts(data,time.Now().Unix())
}
