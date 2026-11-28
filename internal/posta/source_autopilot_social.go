package posta

import (
	"errors"
	"net/url"
	"strings"
	"time"

	"posta/internal/jx"
)

// Search uses public endpoints only. The returned links are merely
// candidates and always go through public-page validation and assessment.
func (u *U) autopilotFindPublicSocial(query, platform string) []DiscoveryCandidate {
	// Native directory worked in the public smoke run. Avoid spending two
	// search-engine requests before using it, when it has usable candidates.
	if platform == PlatformTelegram && u.Ctx.Err() == nil && autopilotBudgetError(u.Ctx) == nil {
		if found, err := u.autopilotTelegramIndex(query); err == nil && len(found) > 0 {
			for i := range found {
				found[i].Origin = "публичный каталог Telegram «" + query + "»"
			}
			if u.discoverySearchObservation != nil {
				u.discoverySearchObservation(platform, query, "ok")
			}
			return found
		}
	}
	host := "t.me"
	if platform == PlatformVK {
		host = "vk.com"
	}
	if platform == PlatformTikTok {
		host = "tiktok.com"
	}
	search := "site:" + host + " " + query
	var r FetchResultURL
	if platform == PlatformTikTok {
		r = u.autopilotTikTokSearch(search)
	} else {
		r = u.autopilotPublisherSearch(search, platform)
	}
	out, status := autopilotPublicSocialSearchCandidates(r, query, platform)
	// The existing public VK catalog is independent of the web search
	// index. Use it only when the index supplied no valid publishers.
	if platform == PlatformVK && len(out) == 0 && u.Ctx.Err() == nil &&
		autopilotBudgetError(u.Ctx) == nil && !autopilotSearchFetchCapReached(u.Ctx) {
		if found, err := autopilotVKDirectoryCandidates(query, u.RSSGet); err == nil && len(found) > 0 {
			out, status = found, "ok"
		}
	}
	if u.discoverySearchObservation != nil {
		u.discoverySearchObservation(platform, query, status)
	}
	return out
}

// Treat unavailable or unrecognized search documents as failures, not topic evidence.
func autopilotPublicSocialSearchCandidates(r FetchResultURL, query, platform string) ([]DiscoveryCandidate, string) {
	doc := autopilotReadSearchDocument(r, platform)
	candidates, status := doc.assess(platform)
	if platform == PlatformTikTok {
		if !autopilotTikTokSearchResponseValid(r) {
			return nil, "unavailable"
		}
		if !doc.valid {
			candidates = autopilotCandidatesFromSearchLinks(doc.links, platform)
		}
	}
	var out []DiscoveryCandidate
	for _, candidate := range candidates {
		if _, ok := autopilotCandidateID(candidate); !ok {
			continue
		}
		candidate.Origin, candidate.OriginKind = "публичный веб-поиск «"+query+"»", "other"
		out = append(out, candidate)
	}
	if len(out) > 0 {
		status = "ok"
	}
	return out, status
}

const autopilotVKDirectoryBase = "https://ugcplatform.ru"

// Reuse the public catalog parser without account-derived evidence. A
// search only reads the catalog index and at most two matched categories;
// every returned publisher must still pass the normal review pipeline.
func autopilotVKDirectoryCandidates(query string, fetch func(string, int64, time.Duration, string) FetchResultURL) ([]DiscoveryCandidate, error) {
	if strings.TrimSpace(query) == "" {
		return nil, nil
	}
	read := func(path string) (string, bool) {
		r := fetch(autopilotVKDirectoryBase+path, 3<<20, 12*time.Second, BrowserUA)
		p, err := url.Parse(r.URL)
		if !r.OK || r.Status == 206 || err != nil || p.Scheme != "https" ||
			p.Host != "ugcplatform.ru" || p.User != nil || p.Path != path ||
			p.RawQuery != "" || p.Fragment != "" {
			return "", false
		}
		return string(r.Bytes), true
	}
	index, ok := read("/vk/")
	if !ok {
		return nil, errors.New("VK public catalog unavailable")
	}
	paths := vkDirectoryCategories(index, []string{query})
	seen := map[string]bool{}
	var out []DiscoveryCandidate
	failed := false
	for _, path := range paths[:min(2, len(paths))] {
		page, ok := read(path)
		if !ok {
			failed = true
			continue
		}
		for _, candidate := range vkDirectoryResults(page) {
			id, ok := autopilotCandidateID(candidate)
			if candidate.Platform != PlatformVK || !ok || seen[id] {
				continue
			}
			seen[id] = true
			candidate.Origin = "публичный каталог VK «" + query + "»"
			candidate.OriginKind = "other"
			out = append(out, candidate)
			if len(out) >= 8 {
				return out, nil
			}
		}
	}
	if len(out) == 0 && failed {
		return nil, errors.New("VK public catalog category unavailable")
	}
	return out, nil
}

func autopilotPublicSocialPosts(platform, id string, res FetchResultURL) ([]jx.M, error) {
	u, err := url.Parse(res.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return nil, errors.New("unexpected public social URL")
	}
	page := string(res.Bytes)
	lower := strings.ToLower(page)
	if strings.Contains(lower, "captcha_sid") || strings.Contains(lower, "challenge_required") {
		return nil, errors.New("social access challenge")
	}
	switch platform {
	case PlatformTelegram:
		if u.Hostname() != "t.me" || !strings.EqualFold(strings.TrimSuffix(u.Path, "/"), "/s/"+id) || !strings.Contains(page, "tgme_widget_message") {
			return nil, errors.New("invalid public Telegram channel page")
		}
		return TelegramPostsFromHTML(page, jx.M{"id": id, "name": id}, 10, true, 0), nil
	case PlatformVK:
		if (u.Hostname() != "vk.com" && u.Hostname() != "www.vk.com" && u.Hostname() != "m.vk.com") || !strings.EqualFold(strings.TrimSuffix(u.Path, "/"), "/"+id) || (!strings.Contains(page, "wall_post") && !strings.Contains(page, "PostText")) {
			return nil, errors.New("invalid public VK page")
		}
		return vkPublicParse(res.Bytes, id, id, 10, true, 0), nil
	case PlatformOK:
		q := u.Query()
		if u.Hostname() != "m.ok.ru" || u.Path != "/dk" || q.Get("st.cmd") != "altGroupMain" || q.Get("st.groupId") != id || !strings.Contains(page, `item it feed-card`) {
			return nil, errors.New("invalid public OK group page")
		}
		return OKParseFeed(page, id, 10, id), nil
	}
	return nil, errors.New("unsupported public social platform")
}
