package posta

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"posta/internal/jx"
)

// Only public channel identities are consumed from the directory; its
// descriptions and ranking are not evidence for source approval.
func autopilotTelegramIndexCandidates(data []byte) ([]DiscoveryCandidate, error) {
	return autopilotTelegramIndexRankedCandidates(data, "", time.Now())
}

func autopilotTelegramIndexRankedCandidates(data []byte, query string, at time.Time) ([]DiscoveryCandidate, error) {
	var response struct {
		JSONRPC string `json:"jsonrpc"`
		Error   json.RawMessage `json:"error"`
		Result struct {
			IsError    bool `json:"isError"`
			Structured struct {
				Results []struct {
					Type        string `json:"type"`
					Username    string `json:"username"`
					TelegramURL string `json:"telegram_url"`
					Title       string `json:"title"`
					About       string `json:"about"`
					LastPostAt  string `json:"last_post_at"`
					Posts30d    *int64 `json:"posts_30d"`
				} `json:"results"`
			} `json:"structuredContent"`
		} `json:"result"`
	}
	if err := json.Unmarshal(data, &response); err != nil {
		return nil, err
	}
	if response.JSONRPC != "2.0" || (len(response.Error) > 0 && string(response.Error) != "null") || response.Result.IsError || response.Result.Structured.Results == nil {
		return nil, errors.New("Telegram directory returned an error or unknown response")
	}
	seen := map[string]bool{}
	type rankedCandidate struct {
		candidate DiscoveryCandidate
		score int
	}
	var ranked []rankedCandidate
	for _, row := range response.Result.Structured.Results {
		if row.Type != "channel" || !IsValidTelegramChannel(row.Username) {
			continue
		}
		p, err := url.Parse(row.TelegramURL)
		if err != nil || p.Scheme != "https" || p.Host != "t.me" || p.User != nil || p.RawQuery != "" || p.Fragment != "" || !strings.EqualFold(p.Path, "/"+row.Username) {
			continue
		}
		c := DiscoveryCandidate{Platform: PlatformTelegram, ID: strings.ToLower(row.Username), OriginKind: "other", Info: jx.M{"title": trimRunes(row.Title, 120)}}
		id, ok := autopilotCandidateID(c)
		if !ok || seen[id] {
			continue
		}
		seen[id] = true
		// The catalog is not ground truth. Prefer recent activity, but
		// keep stale/unknown metadata as a weak ranking signal only.
		score := 0
		stamp, err := time.Parse(time.RFC3339, row.LastPostAt)
		if err != nil {
			stamp, err = time.Parse("2006-01-02T15:04:05", row.LastPostAt)
		}
		if err == nil && !stamp.After(at.Add(24*time.Hour)) {
			age := at.Sub(stamp)
			switch {
			case age <= 30*24*time.Hour:
				score += 5
			case age <= 90*24*time.Hour:
				score += 2
			case age > 180*24*time.Hour:
				score -= 4
			}
		}
		if row.Posts30d != nil {
			switch {
			case *row.Posts30d >= 3:
				score += 2
			case *row.Posts30d == 0:
				score -= 2
			}
		}
		if discoveryQueryEvidenceMatch(query, row.Title+" "+row.About) {
			score++
		}
		ranked = append(ranked, rankedCandidate{c, score})
		if len(ranked) >= 30 {
			break
		}
	}
	sort.SliceStable(ranked, func(i,j int) bool { return ranked[i].score > ranked[j].score })
	out := make([]DiscoveryCandidate,0,min(8,len(ranked)))
	for _, r := range ranked[:min(8,len(ranked))] {
		out = append(out,r.candidate)
	}
	return out, nil
}

func (u *U) autopilotTelegramIndexRequest(query string) ([]DiscoveryCandidate, error) {
	req := jx.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": jx.M{
		"name": "find_venues", "arguments": jx.M{"query": query, "type": "channel", "sort": "relevance", "limit": 30},
	}}
	r, err := u.FetchPublic("https://chatcrawler.app/mcp", FetchOpts{
		Method: "POST", Body: string(jx.Marshal(req)), MaxBytes: 256 << 10, Timeout: 10 * time.Second,
		Headers: map[string]string{"Content-Type": "application/json", "Accept": "application/json, text/event-stream", "MCP-Protocol-Version": "2025-03-26"},
	})
	if err != nil {
		return nil, err
	}
	if !r.OK {
		if r.Status == 429 && u.DB != nil {
			until := max(time.Now().UTC().Add(24*time.Hour).Unix(), autopilotBraveRetryAt(time.Now(), r.RetryAfter).Unix())
			_ = u.KVSet(u.Ctx, "autopilot_telegram_index_pause_until", until, time.Until(time.Unix(until, 0)))
		}
		return nil, fmt.Errorf("Telegram directory HTTP %d", r.Status)
	}
	return autopilotTelegramIndexRankedCandidates(r.Bytes,query,time.Now())
}

// Cache only validated public channel identities, not raw directory pages.
// Identical search intents can reuse one request across public topics.
func telegramIndexCacheKey(query string) string {
	return fmt.Sprintf("telegram_index:v1:%x", sha256.Sum256([]byte(strings.ToLower(strings.Join(strings.Fields(query), " ")))))
}

// A small optional fallback, not a bulk crawler. Four requests per UTC day
// across processes, at most one per normalized query. No user credentials
// or source contents are sent.
func (u *U) autopilotTelegramIndex(query string) ([]DiscoveryCandidate, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" || utf8.RuneCountInString(query) > 64 || u.DB == nil {
		return nil, errors.New("Telegram directory query unavailable")
	}
	job, leased := u.Ctx.Value(autopilotLeaseContextKey{}).(*autopilotJob)
	if !leased || !validAutopilotPublicJob(job) {
		return nil, errors.New("Telegram directory requires an active public worker")
	}
	key := telegramIndexCacheKey(query)
	var cached []DiscoveryCandidate
	if u.CacheGet(u.Ctx, key, &cached) {
		return cached, nil
	}
	now := time.Now().UTC()
	if KV(u.App, u.Ctx, "autopilot_telegram_index_pause_until", int64(0)) > now.Unix() {
		return nil, errors.New("Telegram directory cooldown")
	}
	day := now.Format("2006-01-02")
	id := fmt.Sprintf("telegram_index:%s:%x", day, sha256.Sum256([]byte(strings.ToLower(query))))
	if err := reserveAutopilotJobBudget(u.Ctx, u.DB, job, id, "server", "telegram_index", now, 1, 4); err != nil {
		return nil, err
	}
	out, err := u.autopilotTelegramIndexRequest(query)
	if err != nil {
		// A locally exhausted fetch quota or cancelled worker says nothing
		// about the health of the external directory.
		if u.Ctx.Err() == nil && autopilotBudgetError(u.Ctx) == nil && !autopilotSearchFetchCapReached(u.Ctx) {
			until := max(now.Add(24*time.Hour).Unix(), KV(u.App, u.Ctx, "autopilot_telegram_index_pause_until", int64(0)))
			_ = u.KVSet(u.Ctx, "autopilot_telegram_index_pause_until", until, time.Until(time.Unix(until, 0)))
		}
		return nil, err
	}
	u.CacheSet(u.Ctx, key, out, 24*time.Hour)
	return out, nil
}
