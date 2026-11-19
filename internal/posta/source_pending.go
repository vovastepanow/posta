package posta

import (
	"encoding/json"
	"net/url"
	"sort"
	"strings"
	"time"

	"posta/internal/jx"
)

// PendingSources lists saved suggestions without network access or marking them seen.
func (u *U) PendingSources(filter string) ([]jx.M, error) {
	list, _, _, err := u.PendingSourcesPage(filter, nil, 0, "")
	return list, err
}

// PendingSourcesPage excludes previously delivered IDs before loading samples.
func (u *U) PendingSourcesPage(filter string, exclude []string, limit int, order string) ([]jx.M, int, bool, error) {
	return u.pendingSourcesPage(filter, exclude, limit, "", order)
}

func (u *U) pendingSourcesPage(filter string, exclude []string, limit int, target, order string) ([]jx.M, int, bool, error) {
	if filter == "" {
		filter = "any"
	}
	platform, ok := SourcePlatformFilters[strings.ToLower(filter)]
	if !ok {
		return nil, 0, false, Bad("Неизвестная платформа источника.")
	}
	topic := u.CurrentTopic()
	d := u.Catalog()
	voted := u.decidedSourceKeys(d.Votes, topic)
	cands := excludeDecidedSourceCandidates(u.suggestionSourceCandidates(d, platform, nil, topic), voted)
	// Transferred suggestions are now owned by source autopilot and should
	// not continue asking the user for the same manual decision.
	var err error
	cands, err = u.excludeAutopilotTransferredPending(cands, topic)
	if err != nil {
		return nil, 0, false, err
	}
	// Every remaining suggestion gets an explicit routing diagnosis. Do
	// not describe these as "waiting for the AI" when the platform or
	// selected topic cannot enter the source-autopilot queue at all.
	var mode string
	var paused bool
	var rate float64
	err = u.DB.Pool.QueryRow(u.Ctx, `SELECT
		COALESCE((SELECT mode FROM source_autopilot_demands WHERE user_id=$1 AND topic_key=$2),'off'),
		COALESCE((SELECT paused FROM source_autopilot_demands WHERE user_id=$1 AND topic_key=$2),false),
		COALESCE((SELECT posts_per_day FROM source_autopilot_demands WHERE user_id=$1 AND topic_key=$2),0)`,
		u.ID, autopilotTopicKey(topic)).Scan(&mode, &paused, &rate)
	if err != nil {
		return nil, 0, false, err
	}
	for _, c := range cands {
		status := u.pendingSourceAutopilotStatus(topic, c, mode, paused)
		if status == "awaiting_transfer" && mode == "auto" && rate <= 0 {
			status = "autopilot_no_rate"
		}
		c["autopilot_transfer_status"] = status
	}
	if err := sortPendingSources(cands, order); err != nil {
		return nil, 0, false, err
	}
	kind := sourceKindFilters[strings.ToLower(filter)]
	list := make([]jx.M, 0, len(cands))
	ids := make([]string, 0, len(cands))
	for _, c := range cands {
		if target != "" && jx.Str(c, "source_id") != target {
			continue
		}
		if !sourceMatchesKind(c, kind) {
			continue
		}
		list = append(list, c)
		ids = append(ids, jx.Str(c, "source_id"))
	}
	total := len(list)
	seen := make(map[string]bool, len(exclude))
	for _, id := range exclude {
		seen[id] = true
	}
	remaining := make([]jx.M, 0, len(list))
	for _, c := range list {
		if !seen[jx.Str(c, "source_id")] {
			remaining = append(remaining, c)
		}
	}
	hasMore := limit > 0 && len(remaining) > limit
	if hasMore {
		remaining = remaining[:limit]
	}
	list = remaining
	ids = ids[:0]
	for _, c := range list {
		ids = append(ids, jx.Str(c, "source_id"))
	}
	if len(ids) == 0 {
		return list, total, false, nil
	}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT source_id, name, sample, metrics FROM source_registry WHERE source_id=ANY($1)`, ids)
	if err != nil {
		return nil, 0, false, err
	}
	defer rows.Close()
	samples := map[string][]jx.M{}
	names := map[string]string{}
	metrics := map[string]jx.M{}
	for rows.Next() {
		var sid, name string
		var raw, metricRaw []byte
		if err := rows.Scan(&sid, &name, &raw, &metricRaw); err != nil {
			return nil, 0, false, err
		}
		names[sid] = name
		metrics[sid] = jx.Unmarshal(metricRaw)
		var posts []jx.M
		if len(raw) > 0 && json.Unmarshal(raw, &posts) == nil {
			samples[sid] = posts
		}
	}
	if err := rows.Err(); err != nil {
		return nil, 0, false, err
	}
	for _, c := range list {
		id := jx.Str(c, "id")
		c["metrics"] = metrics[jx.Str(c, "source_id")]
		c["url"] = registryURL(jx.Str(c, "platform"), id)
		if jx.Str(c, "platform") == PlatformRSS {
			c["platform_label"] = RSSPlatformLabel(id)
			if cid, tab, ok := YouTubeFeedChannel(id); ok {
				c["url"] = "https://www.youtube.com/channel/" + cid + "/" + tab
			}
			name, needsTitle := pendingRSSName(jx.Str(c, "name"), names[jx.Str(c, "source_id")], id)
			c["name"], c["needs_title"] = name, needsTitle
		}
		posts := samples[jx.Str(c, "source_id")]
		if jx.Str(c, "platform") == PlatformVK {
			for _, post := range posts {
				if jx.Str(post, "image") != "" && !jx.Has(post, "images") && !jx.Has(post, "gallery") {
					c["needs_media_refresh"] = true
					break
				}
			}
		}
		if jx.Str(c, "platform") == PlatformRSS && strings.HasPrefix(RSSFeedKind(id), "YouTube") {
			posts = restoreYouTubeSamples(posts)
		}
		c["posts"] = u.pendingPreviewPosts(posts)
	}
	return list, total, hasMore, nil
}

func (u *U) decidedSourceKeys(votes []jx.M, topic string) map[string]bool {
	decided := map[string]bool{}
	for _, v := range votes {
		if t := jx.Str(v, "topic"); t == "" || u.TopicsAreEquivalent(t, topic) {
			decided[recommendationSourceKey(jx.Str(v, "source_id"))] = true
		}
	}
	return decided
}

// A decision applies to all recommendation variants of the same source.
func excludeDecidedSourceCandidates(cands []jx.M, decided map[string]bool) []jx.M {
	out := make([]jx.M, 0, len(cands))
	for _, c := range cands {
		if !recommendationExcluded(decided, jx.Str(c, "source_id")) {
			out = append(out, c)
		}
	}
	return out
}

func sortPendingSources(list []jx.M, order string) error {
	switch order {
	case "", "rank":
		return nil
	case "newest", "oldest", "score_high", "score_low", "name":
	default:
		return Bad("Неизвестный порядок сортировки источников.")
	}
	sort.SliceStable(list, func(i, j int) bool {
		a, b := list[i], list[j]
		switch order {
		case "newest", "oldest":
			x, y := jx.Int(a, "timestamp"), jx.Int(b, "timestamp")
			if x == 0 || y == 0 {
				return x != 0 && y == 0
			}
			if x != y {
				if order == "newest" {
					return x > y
				}
				return x < y
			}
		case "score_high", "score_low":
			if a["score"] == nil || b["score"] == nil {
				return a["score"] != nil && b["score"] == nil
			}
			x, y := jx.Float(a, "score"), jx.Float(b, "score")
			if x != y {
				if order == "score_high" {
					return x > y
				}
				return x < y
			}
		case "name":
			x, y := strings.ToLower(jx.Str(a, "name")), strings.ToLower(jx.Str(b, "name"))
			if x != y {
				return x < y
			}
		}
		return jx.Str(a, "source_id") < jx.Str(b, "source_id")
	})
	return nil
}

// pendingPreviewPosts prefers measurable engagement; otherwise preserves the saved sample order.
func (u *U) pendingPreviewPosts(posts []jx.M) []jx.M {
	sorted := append([]jx.M{}, posts...)
	sort.SliceStable(sorted, func(i, j int) bool {
		score := func(p jx.M) int64 { return jx.Int(p, "views") + jx.Int(p, "likes") + jx.Int(p, "comments") }
		return score(sorted[i]) > score(sorted[j])
	})
	out := make([]jx.M, 0, min(6, len(sorted)))
	for _, p := range sorted {
		v := u.SourcePreviewPost(p)
		if jx.Str(v, "text") == "" && len(jx.List(v, "media")) == 0 {
			continue
		}
		out = append(out, v)
		if len(out) == 6 {
			break
		}
	}
	return out
}

// PendingSourcePreview fetches examples only for an existing suggestion of the current topic.
func (u *U) PendingSourcePreview(id string, titleOnly bool) (jx.M, error) {
	list, _, _, err := u.pendingSourcesPage("any", nil, 1, id, "")
	if err != nil {
		return nil, err
	}
	for _, c := range list {
		if jx.Str(c, "source_id") == id {
			p := jx.M{}
			if !titleOnly {
				p = u.BuildSourcePreview(c, sourcePreviewPosts)
			}
			if jx.Str(c, "platform") == PlatformRSS && jx.Bool(c, "needs_title") && u.Ctx.Err() == nil {
				if title := u.resolveRSSSourceTitle(id, jx.Str(c, "id")); title != "" {
					p["name"] = title
				}
			}
			if !titleOnly {
				p["quality"] = u.SourceQualityOf(u.Ctx, id, u.CurrentTopic())
			}
			posts := jx.Maps(p, "posts")
			sort.SliceStable(posts, func(i, j int) bool {
				return jx.Int(posts[i], "views")+jx.Int(posts[i], "reactions") > jx.Int(posts[j], "views")+jx.Int(posts[j], "reactions")
			})
			p["posts"] = posts[:min(6, len(posts))]
			return p, nil
		}
	}
	return nil, NotFound("Источник больше не ожидает решения.")
}

// Recover the video type for historical samples that only kept a YouTube URL and poster.
func restoreYouTubeSamples(posts []jx.M) []jx.M {
	out := make([]jx.M, 0, len(posts))
	for _, p := range posts {
		copy := jx.Copy(p)
		if jx.Str(copy, "video_page") == "" && jx.Str(copy, "video") == "" && !jx.Bool(copy, "video_unavailable") {
			if page := canonicalYouTubeSamplePage(jx.Str(copy, "url")); page != "" {
				copy["video_page"], copy["video_via"] = page, "ytdlp"
			}
		}
		out = append(out, copy)
	}
	return out
}

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
