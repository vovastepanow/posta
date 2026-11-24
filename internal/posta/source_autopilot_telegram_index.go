package posta

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	"posta/internal/jx"
)

// Only public channel identities are consumed from the directory; its
// descriptions and ranking are not evidence for source approval.
func autopilotTelegramIndexCandidates(data []byte) ([]DiscoveryCandidate, error) {
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
	var out []DiscoveryCandidate
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
		out = append(out, c)
		if len(out) >= 8 {
			break
		}
	}
	return out, nil
}

func (u *U) autopilotTelegramIndexRequest(query string) ([]DiscoveryCandidate, error) {
	req := jx.M{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": jx.M{
		"name": "find_venues", "arguments": jx.M{"query": query, "type": "channel", "sort": "relevance", "limit": 8},
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
	return autopilotTelegramIndexCandidates(r.Bytes)
}

// A small optional fallback, not a bulk crawler. Four requests per UTC day
// across processes, at most one per normalized query. Provider errors stop
// requests for the rest of the day. No user credentials or content are sent.
func (u *U) autopilotTelegramIndex(query string) ([]DiscoveryCandidate, error) {
	query = strings.Join(strings.Fields(query), " ")
	if query == "" || utf8.RuneCountInString(query) > 64 || u.DB == nil {
		return nil, errors.New("Telegram directory query unavailable")
	}
	now := time.Now().UTC()
	if KV(u.App, u.Ctx, "autopilot_telegram_index_pause_until", int64(0)) > now.Unix() {
		return nil, errors.New("Telegram directory cooldown")
	}
	day := now.Format("2006-01-02")
	id := fmt.Sprintf("telegram_index:%s:%x", day, sha256.Sum256([]byte(strings.ToLower(query))))
	if err := reserveAutopilotBudget(u.Ctx, u.DB, id, "server", "telegram_index", now, 1, 4); err != nil {
		return nil, err
	}
	out, err := u.autopilotTelegramIndexRequest(query)
	if err != nil {
		until := max(now.Add(24*time.Hour).Unix(), KV(u.App, u.Ctx, "autopilot_telegram_index_pause_until", int64(0)))
		_ = u.KVSet(u.Ctx, "autopilot_telegram_index_pause_until", until, time.Until(time.Unix(until, 0)))
		return nil, err
	}
	return out, nil
}
