package posta

import (
	"net/url"
	"strings"
	"time"

	"posta/internal/jx"
)

func canonicalYouTubeSamplePage(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	id, short := "", false
	switch host {
	case "youtube.com", "www.youtube.com", "m.youtube.com":
		if strings.HasPrefix(u.Path, "/shorts/") {
			id, short = strings.TrimPrefix(u.Path, "/shorts/"), true
		} else if u.Path == "/watch" {
			id = u.Query().Get("v")
		}
	case "youtu.be":
		id = strings.TrimPrefix(u.Path, "/")
	}
	if !videoIDRe.MatchString(id) {
		return ""
	}
	if short {
		return "https://www.youtube.com/shorts/" + id
	}
	return "https://www.youtube.com/watch?v=" + id
}

// Prefer real saved names; the host/channel identifier remains useful when metadata is missing.
func pendingRSSName(display, registry, feed string) (string, bool) {
	host := strings.TrimPrefix(strings.ToLower(hostOf(feed)), "www.")
	for _, candidate := range []string{display, registry} {
		name := strings.TrimSpace(candidate)
		for strings.HasPrefix(name, "RSS |") {
			name = strings.TrimSpace(strings.TrimPrefix(name, "RSS |"))
		}
		normalized := strings.TrimPrefix(strings.ToLower(name), "www.")
		if name != "" && normalized != "rss" && normalized != "youtube" && normalized != "youtube shorts" && normalized != host && name != feed && !strings.HasPrefix(normalized, "youtube · uc") && !strings.HasPrefix(normalized, "youtube shorts · uc") {
			return name, false
		}
	}
	if cid, tab, ok := YouTubeFeedChannel(feed); ok {
		if tab == "shorts" {
			return "YouTube Shorts · " + cid, true
		}
		return "YouTube · " + cid, true
	}
	if host != "" {
		return host, true
	}
	return "RSS-источник", true
}

// RSS is a transport: present the actual platform, or the site's domain.
func RSSPlatformLabel(feed string) string {
	host := strings.TrimPrefix(strings.ToLower(hostOf(feed)), "www.")
	kind := RSSFeedKind(feed)
	if kind != "RSS" && (kind != "YouTube Shorts" || host == "youtube.com" || strings.HasSuffix(host, ".youtube.com")) {
		return kind
	}
	match := func(domain string) bool { return host == domain || strings.HasSuffix(host, "."+domain) }
	for _, p := range []struct{ domain, label string }{
		{"reddit.com", "Reddit"}, {"bsky.app", "Bluesky"}, {"bsky.social", "Bluesky"},
		{"dzen.ru", "Дзен"}, {"zen.yandex.ru", "Дзен"}, {"pikabu.ru", "Пикабу"},
		{"habr.com", "Хабр"}, {"joyreactor.cc", "JoyReactor"}, {"reactor.cc", "JoyReactor"},
		{"rutube.ru", "Rutube"}, {"medium.com", "Medium"}, {"substack.com", "Substack"},
		{"livejournal.com", "LiveJournal"}, {"vk.com", "ВКонтакте"}, {"t.me", "Telegram"},
		{"threads.net", "Threads"}, {"instagram.com", "Instagram"}, {"x.com", "X"}, {"twitter.com", "X"},
	} {
		if match(p.domain) {
			return p.label
		}
	}
	if host != "" {
		return "Сайт · " + host
	}
	return "Сайт / RSS"
}

// Resolve a missing title from the feed, then from YouTube's video metadata.
func (u *U) resolveRSSSourceTitle(sourceID, feed string) string {
	cacheKey := "rss_source_title:" + sha1Short(sourceID, 40)
	var cached string
	if u.CacheGet(u.Ctx, cacheKey, &cached) {
		if name, missing := pendingRSSName(cached, "", feed); !missing {
			return name
		}
	}
	fetchFeed := feed
	cid, _, youtube := YouTubeFeedChannel(feed)
	if youtube {
		fetchFeed = YouTubeChannelFeedURL(cid)
	}
	title := u.RSSFeedTitle(fetchFeed)
	name, missing := pendingRSSName(title, "", feed)
	if missing && youtube && u.Ctx.Err() == nil {
		if row := u.registryGet(sourceID); row != nil {
			for _, post := range row.Sample {
				page := canonicalYouTubeSamplePage(jx.Str(post, "url"))
				if page == "" {
					continue
				}
				endpoint := "https://www.youtube.com/oembed?format=json&url=" + url.QueryEscape(page)
				result := u.RSSGet(endpoint, 256<<10, 8*time.Second, HTTPUA)
				if result.OK {
					if author := jx.Str(jx.Unmarshal(result.Bytes), "author_name"); author != "" {
						name, missing = pendingRSSName(author, "", feed)
					}
				}
				break
			}
		}
	}
	if missing || u.Ctx.Err() != nil {
		return ""
	}
	u.CacheSet(u.Ctx, cacheKey, name, 7*24*time.Hour)
	if row := u.registryGet(sourceID); row != nil {
		if _, stillMissing := pendingRSSName("", row.Name, feed); stillMissing {
			// A manually updated name wins over the result of an in-flight lookup.
			_, _ = u.DB.Pool.Exec(u.Ctx, `UPDATE source_registry SET name=$2 WHERE source_id=$1 AND name=$3`, sourceID, name, row.Name)
		}
	}
	return name
}

// RSSSourceTitle resolves titles only for RSS sources available to the current topic.
func (u *U) RSSSourceTitle(sourceID string) (string, error) {
	d := u.Catalog()
	topic := u.CurrentTopic()
	for _, entry := range d.RSS {
		feed := strings.TrimSpace(jx.Str(entry, "id"))
		if SourceIDFor(PlatformRSS, feed) != sourceID || !u.CatalogEntryMatchesTopic(entry, topic, false) {
			continue
		}
		if name, missing := pendingRSSName(jx.Str(entry, "name"), "", feed); !missing {
			return name, nil
		}
		if row := u.registryGet(sourceID); row != nil {
			if name, missing := pendingRSSName("", row.Name, feed); !missing {
				return name, nil
			}
		}
		return u.resolveRSSSourceTitle(sourceID, feed), nil
	}
	return "", NotFound("Источник недоступен в этой теме.")
}
