package posta

import (
	"errors"
	"net/url"
	"strings"

	"posta/internal/jx"
)

// Search uses public endpoints only. The returned links are merely
// candidates and always go through public-page validation and assessment.
func (u *U) autopilotFindPublicSocial(query, platform string) []DiscoveryCandidate {
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
	status := "unavailable"
	var out []DiscoveryCandidate
	valid := autopilotPublisherSearchResponseValid(r)
	if platform == PlatformTikTok {
		valid = autopilotTikTokSearchResponseValid(r)
	}
	if valid {
		status = "empty"
		for _, candidate := range autopilotSearchLinkedCandidates(r, platform) {
			if candidate.Platform != platform {
				continue
			}
			if _, ok := autopilotCandidateID(candidate); !ok {
				continue
			}
			candidate.Origin, candidate.OriginKind = "публичный веб-поиск «"+query+"»", "other"
			out = append(out, candidate)
		}
		if len(out) > 0 {
			status = "ok"
		}
	}
	if platform == PlatformTelegram && len(out) == 0 && u.Ctx.Err() == nil && autopilotBudgetError(u.Ctx) == nil {
		if found, err := u.autopilotTelegramIndex(query); err == nil {
			status = "empty"
			for _, candidate := range found {
				candidate.Origin = "публичный каталог Telegram «" + query + "»"
				out = append(out, candidate)
			}
			if len(out) > 0 {
				status = "ok"
			}
		}
	}
	if u.discoverySearchObservation != nil {
		u.discoverySearchObservation(platform, query, status)
	}
	return out
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
		if u.Hostname() != "t.me" || strings.TrimSuffix(u.Path, "/") != "/s/"+id || !strings.Contains(page, "tgme_widget_message") {
			return nil, errors.New("invalid public Telegram channel page")
		}
		return TelegramPostsFromHTML(page, jx.M{"id": id, "name": id}, 10, true, 0), nil
	case PlatformVK:
		if u.Hostname() != "vk.com" || strings.TrimSuffix(u.Path, "/") != "/"+id || (!strings.Contains(page, "wall_post") && !strings.Contains(page, "PostText")) {
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
