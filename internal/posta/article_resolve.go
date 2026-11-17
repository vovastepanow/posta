package posta

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	xhtml "golang.org/x/net/html"
)

var articleAccessRestricted = regexp.MustCompile(`(?i)"isAccessibleForFree"\s*:\s*(?:false|"false")`)

// Explicit access and preview signals are treated conservatively: an excerpt is
// useful as RSS text, but must not be labelled as the complete article.
func articleRestricted(page string) bool {
	if articleAccessRestricted.MatchString(page) {
		return true
	}
	p := strings.ToLower(page)
	for _, marker := range []string{"you have a preview view of this article", "thank you for your patience while we verify access", "subscribe to continue reading", "sign in to continue reading", "подпишитесь, чтобы читать полностью"} {
		if strings.Contains(p, marker) {
			return true
		}
	}
	return false
}

// Text explicitly ending in an access prompt or an ellipsis is an excerpt.
func articleIncomplete(text string) bool {
	t := strings.ToLower(strings.TrimSpace(text))
	if strings.HasSuffix(t, "...") || strings.HasSuffix(t, "…") {
		return true
	}
	for _, marker := range []string{"subscribe to continue reading", "sign in to continue reading", "подпишитесь, чтобы читать полностью"} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
}

func googleNewsURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && strings.EqualFold(u.Hostname(), "news.google.com") && (strings.HasPrefix(u.Path, "/rss/articles/") || strings.HasPrefix(u.Path, "/articles/") || strings.HasPrefix(u.Path, "/read/"))
}

func googleNewsAttributes(page string) (id, ts, sig string) {
	z := xhtml.NewTokenizer(strings.NewReader(page))
	for {
		tt := z.Next()
		if tt == xhtml.ErrorToken {
			return
		}
		if tt != xhtml.StartTagToken && tt != xhtml.SelfClosingTagToken {
			continue
		}
		token := z.Token()
		attrs := map[string]string{}
		for _, a := range token.Attr {
			attrs[a.Key] = a.Val
		}
		if attrs["data-n-a-id"] != "" && attrs["data-n-a-ts"] != "" && attrs["data-n-a-sg"] != "" {
			return attrs["data-n-a-id"], attrs["data-n-a-ts"], attrs["data-n-a-sg"]
		}
	}
}

func googleNewsResponse(body []byte) string {
	for _, line := range strings.Split(string(body), "\n") {
		var rows [][]json.RawMessage
		if json.Unmarshal([]byte(line), &rows) != nil {
			continue
		}
		for _, row := range rows {
			if len(row) < 3 {
				continue
			}
			var rpc, encoded string
			if json.Unmarshal(row[1], &rpc) != nil || rpc != "Fbv4je" || json.Unmarshal(row[2], &encoded) != nil {
				continue
			}
			var result []json.RawMessage
			if json.Unmarshal([]byte(encoded), &result) != nil || len(result) < 2 {
				continue
			}
			var kind, link string
			_ = json.Unmarshal(result[0], &kind)
			_ = json.Unmarshal(result[1], &link)
			if kind == "garturlres" {
				return rssHTTPS(link, "")
			}
		}
	}
	return ""
}

func (a *App) googleNewsOriginal(link, page string) string {
	if !googleNewsURL(link) {
		return ""
	}
	id, ts, sig := googleNewsAttributes(page)
	if id == "" {
		// Older Google News IDs directly contain the publisher URL.
		u, _ := url.Parse(link)
		token := strings.TrimSuffix(u.Path, "/")
		token = token[strings.LastIndex(token, "/")+1:]
		b, err := base64.RawURLEncoding.DecodeString(token)
		if err == nil {
			if m := regexp.MustCompile(`https?://[^\x00-\x20]+`).Find(b); len(m) > 0 {
				return rssHTTPS(string(m), "")
			}
		}
		return ""
	}
	timestamp, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || len(id) > 2048 || len(sig) > 512 {
		return ""
	}
	context := []any{[]any{"X", "X", []any{"X", "X"}, nil, nil, 1, 1, "US:en", nil, 1, nil, nil, nil, nil, nil, 0, nil, nil, []any{1608992183, 723341000}}, "X", "X", 1, []any{1, 1, 1}, 1, 1, nil, 0, 0, nil, 0}
	request, _ := json.Marshal([]any{"garturlreq", context, id, timestamp, sig})
	batch, _ := json.Marshal([]any{[]any{[]any{"Fbv4je", string(request), nil, "generic"}}})
	form := url.Values{"f.req": {string(batch)}}.Encode()
	r, err := a.FetchPublic("https://news.google.com/_/DotsSplashUi/data/batchexecute?rpcids=Fbv4je", FetchOpts{Method: "POST", Body: form, MaxBytes: 1 << 20, Timeout: 10 * time.Second, UserAgent: HTTPUA, Headers: map[string]string{"Content-Type": "application/x-www-form-urlencoded;charset=UTF-8"}})
	if err != nil || !r.OK {
		return ""
	}
	return googleNewsResponse(r.Bytes)
}

// Remove standalone publisher promotions, including those embedded in JSON-LD.
// Restrict matches to clearly worded whole paragraphs to preserve article text.
var articlePromotionParagraphs = []*regexp.Regexp{
	regexp.MustCompile(`(?i)^when you (?:purchase|buy) through links (?:in|on) (?:our articles|our site|this (?:article|page)),?\s*(?:we |(?:[a-z ]+ )?may )?(?:may )?(?:earn|receive) (?:a |an )?(?:small )?(?:affiliate )?commission\b[^\n]*$`),
	regexp.MustCompile(`(?i)^power up with unlimited access to wired\.\s*get best-in-class reporting and exclusive subscriber content that.s too important to ignore\.\s*subscribe today\.?$`),
}

func articleCleanPromotions(text string) string {
	parts := regexp.MustCompile(`\n\s*\n`).Split(strings.TrimSpace(text), -1)
	out := parts[:0]
	for _, part := range parts {
		p := strings.TrimSpace(part)
		if p == "" {
			continue
		}
		remove := false
		for _, pattern := range articlePromotionParagraphs {
			if pattern.MatchString(p) {
				remove = true
				break
			}
		}
		if !remove {
			out = append(out, p)
		}
	}
	return strings.Join(out, "\n\n")
}
