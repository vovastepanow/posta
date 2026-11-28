package posta

import (
	"net/url"
	"sort"
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// Вкладка «Источники»: превью, каталог темы и сохранённые решения.

const SourceAddMaxLink = 500

func NewsSiteKind(feed string) string {
	u, _ := url.Parse(feed)
	host := ""
	if u != nil {
		host = strings.TrimPrefix(u.Hostname(), "www.")
	}
	return "Сайт · " + host
}

// SourcePreviewPost — пост превью в одном виде для всех площадок.
func (u *U) SourcePreviewPost(p jx.M) jx.M {
	eng := jx.Map(p, "engagement")
	text := CleanPostDisplayText(jx.Str(p, "text"), p)
	if text == EmptyPostText {
		text = ""
	}
	var views, reactions, comments any
	if jx.Has(p, "views") && p["views"] != nil {
		views = jx.Int(p, "views")
	} else if eng != nil && eng["views"] != nil {
		views = jx.Int(eng, "views")
	}
	if jx.Has(p, "likes") && p["likes"] != nil {
		reactions = jx.Int(p, "likes")
	} else if eng != nil && eng["reactions"] != nil {
		reactions = sumReactions(eng["reactions"])
	}
	if jx.Has(p, "comments") && p["comments"] != nil {
		comments = jx.Int(p, "comments")
	}
	return jx.M{"text": strings.TrimSpace(text), "url": jx.Str(p, "url"), "posted_at": jx.Int(p, "posted_at"),
		"text_post": jx.Bool(p, "text_post"), "media": u.sourcePreviewMedia(p), "views": views, "reactions": reactions, "comments": comments,
		"media_incomplete": jx.Bool(p, "media_incomplete"), "fetched_via": jx.Str(p, "fetched_via"), "text_incomplete": jx.Bool(p, "text_incomplete")}
}

func (u *U) sourcePreviewMedia(p jx.M) []jx.M {
	https := func(s string) string {
		if strings.HasPrefix(s, "https://") {
			return s
		}
		return u.MediaExisting(s)
	}
	item := func(kind, src, poster, page string, pending, unavailable bool) jx.M {
		return jx.M{"kind": kind, "src": src, "poster": poster, "page": page, "pending": pending, "unavailable": unavailable}
	}
	page := jx.Str(p, "url")
	out := []jx.M{}
	if attachments := jx.Maps(p, "source_attachments"); len(attachments) > 0 {
		for _, a := range attachments {
			poster := https(jx.Str(a, "image"))
			if poster == "" {
				poster = https(jx.Str(a, "image_local"))
			}
			switch jx.Str(a, "kind") {
			case "photo":
				if poster != "" {
					out = append(out, item("image", poster, "", "", false, false))
				}
			case "video":
				video := https(jx.Str(a, "video"))
				if video == "" {
					video = https(jx.Str(a, "video_local"))
				}
				target := jx.Str(a, "video_page")
				if target == "" && (len(attachments) == 1 || video != "") {
					target = page
				}
				out = append(out, item("video", video, poster, target, jx.Bool(a, "video_pending"), video == "" && VideoPageVia(target) == ""))
			}
		}
		return out
	}
	if g := jx.Maps(p, "gallery"); len(g) > 0 {
		videoPage := jx.Str(p, "video_page")
		for i, x := range g {
			poster, video := https(jx.Str(x, "image")), https(jx.Str(x, "video"))
			if i == 0 && video == "" && videoPage != "" {
				video = https(jx.Str(p, "video"))
				if video == "" {
					video = u.MediaExisting(jx.Str(p, "video_local"))
				}
			}
			if i == 0 && video == "" && videoPage != "" {
				out = append(out, item("video", "", poster, videoPage, jx.Bool(p, "video_pending"), jx.Bool(p, "video_unavailable")))
			} else if video != "" {
				out = append(out, item("video", video, poster, page, false, false))
			} else if poster != "" {
				out = append(out, item("image", poster, "", "", false, false))
			}
		}
		return out
	}
	var images []string
	for _, s := range append([]string{jx.Str(p, "image")}, jx.Strs(p, "images")...) {
		if https(s) != "" && !contains(images, s) {
			images = append(images, s)
		}
	}
	poster := ""
	if len(images) > 0 {
		poster = images[0]
	}
	video := https(jx.Str(p, "video"))
	if video == "" {
		video = u.MediaExisting(jx.Str(p, "video_local"))
	}
	videoPage := jx.Str(p, "video_page")
	unavailable := jx.Bool(p, "video_unavailable")
	switch {
	case video != "":
		out = append(out, item("video", video, poster, page, false, false))
	case unavailable:
		out = append(out, item("video", "", poster, page, false, true))
	case videoPage != "":
		out = append(out, item("video", "", poster, videoPage, jx.Bool(p, "video_pending"), false))
	}
	start := 0
	if video != "" || unavailable || videoPage != "" {
		start = 1
	}
	for _, img := range images[min(start, len(images)):] {
		media := item("image", img, "", "", false, false)
		if img == poster {
			media["fallback"] = https(jx.Str(p, "image_fallback"))
		}
		out = append(out, media)
	}
	return out
}

// Recommendation identity joins a YouTube channel's videos and Shorts feeds
// when migrating or filtering legacy saved candidates.
func recommendationSourceKey(id string) string {
	id = strings.TrimSpace(id)
	if strings.HasPrefix(id, "rss:") {
		if feed, ok := canonicalYouTubeFeed(strings.TrimPrefix(id, "rss:")); ok {
			channel, _, _ := YouTubeFeedChannel(feed)
			return "youtube-channel:" + channel
		}
	}
	id = strings.ToLower(id)
	if strings.HasPrefix(id, "rss:") {
		if u, err := url.Parse(strings.TrimPrefix(id, "rss:")); err == nil && (u.Hostname() == "youtube.com" || u.Hostname() == "www.youtube.com") && u.Path == "/feeds/videos.xml" {
			q := u.Query()
			channel := q.Get("channel_id")
			if p := q.Get("playlist_id"); strings.HasPrefix(p, "uush") && len(p) == 26 {
				channel = "uc" + p[4:]
			}
			if len(channel) == 24 && strings.HasPrefix(channel, "uc") {
				return "youtube-channel:" + channel
			}
		}
	}
	return id
}

func recommendationExcluded(excluded map[string]bool, id string) bool {
	key := recommendationSourceKey(id)
	if excluded[key] {
		return true
	}
	if strings.HasPrefix(key, "youtube-channel:UC") {
		return excluded["youtube-channel:"+strings.ToLower(strings.TrimPrefix(key, "youtube-channel:"))]
	}
	return false
}

var votePlatforms = []string{PlatformDvach, Platform4chan, PlatformTelegram, PlatformVK, PlatformRSS, PlatformOK, PlatformTikTok}

func sourceRejectionRecord(s jx.M) (jx.M, error) {
	id := strings.TrimSpace(jx.Str(s, "source_id"))
	if id == "" || len(id) > 600 {
		return nil, Bad("Некорректный источник.")
	}
	platform := jx.Str(s, "platform")
	if !contains(votePlatforms, platform) {
		platform = "Неизвестно"
	}
	name := trimRunes(strings.TrimSpace(jx.Str(s, "name")), 200)
	if name == "" {
		name = "Источник"
	}
	r := jx.M{"source_id": id, "platform": platform, "name": name, "vote": "reject", "timestamp": now()}
	if b := jx.Str(s, "board"); IsValidDvachBoard(b) {
		r["board"] = b
	}
	if t := jx.Str(s, "thread_id"); IsValidDvachID(t) {
		r["thread_id"] = t
	}
	if platform == PlatformRSS && IsValidRSSURL(jx.Str(s, "id")) {
		r["kind"] = RSSFeedKind(jx.Str(s, "id"))
	}
	return r, nil
}

// Only decisions explicitly made as thread decisions enable collection.
func (a *App) approvedImageboardThreads(d *SourceDB, topic string) []jx.M {
	var out []jx.M
	for _, v := range d.Votes {
		if jx.Str(v, "scope") != "thread" || jx.Str(v, "vote") != "approve" || !a.TopicSame(jx.Str(v, "topic"), topic) {
			continue
		}
		platform, board, thread := jx.Str(v, "platform"), jx.Str(v, "board"), jx.Str(v, "thread_id")
		if (platform == PlatformDvach || platform == Platform4chan) && IsValidDvachBoard(board) && IsValidDvachID(thread) {
			out = append(out, v)
		}
	}
	return out
}

// YouTube channel IDs are case-sensitive even though handles on other
// platforms and the historical source-key format are case-insensitive.
func sameCatalogSource(platform, a, b string) bool {
	if platform == PlatformRSS {
		aCanonical, aYouTube := canonicalYouTubeFeed(a)
		bCanonical, bYouTube := canonicalYouTubeFeed(b)
		if aYouTube || bYouTube {
			return aYouTube && bYouTube && aCanonical == bCanonical
		}
	}
	return strings.EqualFold(a, b)
}

// Adding an explicit source also reverses an earlier topic-local removal.
func (u *U) addManualCatalogSource(d *SourceDB, platform, id, name, topic string, preview []string) bool {
	list := d.List(CatalogKind(platform))
	if list == nil {
		return false
	}
	found, added := false, false
	for _, e := range *list {
		if !sameCatalogSource(platform, strings.TrimLeft(jx.Str(e, "id"), "@"), id) {
			continue
		}
		found = true
		var excluded []any
		for _, t := range jx.List(e, "excluded_topics") {
			if u.TopicSame(jx.ToStr(t), topic) {
				added = true
			} else {
				excluded = append(excluded, t)
			}
		}
		if len(excluded) == 0 {
			delete(e, "excluded_topics")
		} else {
			e["excluded_topics"] = excluded
		}
		topics := jx.List(e, "topics")
		has := false
		for _, t := range topics {
			if u.TopicSame(jx.ToStr(t), topic) {
				has = true
				break
			}
		}
		if len(topics) > 0 && !has {
			e["topics"] = append(topics, topic)
			added = true
		}
	}
	if !found {
		*list = append(*list, jx.M{"id": id, "name": name, "topics": []any{topic}, "manual": true, "preview": preview})
		added = true
	}
	return added
}

func (u *U) removeRejectedSource(d *SourceDB, s jx.M, topic string) {
	id := strings.TrimLeft(strings.TrimSpace(jx.Str(s, "id")), "@")
	list := d.List(CatalogKind(jx.Str(s, "platform")))
	if list == nil || id == "" {
		return
	}
	var kept []jx.M
	for _, e := range *list {
		if sameCatalogSource(jx.Str(s, "platform"), jx.Str(e, "id"), id) {
			if jx.Bool(e, "autopilot_managed") && !jx.Bool(e, "autopilot_manual") {
				profiles := jx.Map(e, "autopilot_profiles")
				delete(profiles, autopilotTopicKey(topic))
				if len(profiles) != 0 {
					e["autopilot_profiles"] = profiles
					e["topics"] = autopilotProjectionTopics(profiles)
					kept = append(kept, e)
				}
				continue
			}
			if len(jx.List(e, "topics")) == 0 {
				excluded := jx.List(e, "excluded_topics")
				found := false
				for _, t := range excluded {
					if u.TopicSame(jx.ToStr(t), topic) {
						found = true
						break
					}
				}
				if !found {
					e["excluded_topics"] = append(excluded, topic)
				}
				kept = append(kept, e)
				continue
			}
			var topics []any
			for _, t := range jx.List(e, "topics") {
				if !u.TopicSame(jx.ToStr(t), topic) {
					topics = append(topics, t)
				}
			}
			if len(topics) == 0 {
				continue
			}
			e["topics"] = topics
		}
		kept = append(kept, e)
	}
	*list = orMaps(kept)
}

// Persist a topic-local removal without reviving the retired approval queue.
// Historical suggestions remain available to the importer/audit; a rejection
// prevents them from being nominated again.
func (u *U) applyTopicSourceRejection(d *SourceDB, record, source jx.M, topic string) {
	sid := jx.Str(record, "source_id")
	if jx.Str(record, "scope") == "thread" {
		var found jx.M
		for _, s := range d.Suggestions {
			st := jx.Str(s, "topic")
			if jx.Str(s, "source_id") == sid && (st == "" || u.TopicSame(st, topic)) {
				found = s
				break
			}
		}
		record = jx.Merge(record, SourceVoteOrigin(found))
	} else {
		u.removeRejectedSource(d, source, topic)
	}
	var votes []jx.M
	for _, old := range d.Votes {
		if jx.Str(old, "source_id") != sid || !u.TopicSame(jx.Str(old, "topic"), topic) {
			votes = append(votes, old)
		}
	}
	d.Votes = append(votes, record)
}

func (u *U) rejectTopicSource(source jx.M, topic string, thread bool) error {
	record, err := sourceRejectionRecord(source)
	if err != nil {
		return err
	}
	record["topic"], record["reason"] = topic, "other"
	if thread {
		record["scope"] = "thread"
	}
	_, err = u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
		u.applyTopicSourceRejection(d, record, source, topic)
		return true
	})
	if err == nil {
		u.registrySetOwner(jx.Str(record, "source_id"), jx.Str(record, "platform"), jx.Str(record, "name"), RegStatusOwnerNo, topic)
	}
	return err
}

// RemoveTopicSource — убрать источник из темы; решение запоминается, автоподбор его не вернёт.
func (u *U) RemoveTopicSource(platform, id, name string) error {
	topic := u.CurrentTopic()
	id = strings.TrimLeft(strings.TrimSpace(id), "@")
	if (platform == PlatformDvach || platform == Platform4chan) && strings.Count(id, ":") == 2 {
		parts := strings.Split(id, ":")
		prefix := "dvach"
		if platform == Platform4chan {
			prefix = "4chan"
		}
		if parts[0] != prefix || !IsValidDvachBoard(parts[1]) || !IsValidDvachID(parts[2]) {
			return Bad("Некорректный тред.")
		}
		return u.rejectTopicSource(jx.M{"platform": platform, "source_id": id, "board": parts[1], "thread_id": parts[2], "name": name}, topic, true)
	}
	if platform == PlatformDvach {
		if !IsValidDvachBoard(id) {
			return Bad("Некорректная доска.")
		}
		_, err := u.UpdateCatalog(u.Ctx, func(d *SourceDB) bool {
			drop := func(l []string) []string {
				var out []string
				for _, b := range l {
					if b != id {
						out = append(out, b)
					}
				}
				return orStrs(out)
			}
			if u.TopicSame(config.DefaultTopic, topic) {
				d.Boards = drop(d.Boards)
			} else {
				k := strings.ToLower(topic)
				d.DvachTopicBoards[k] = drop(d.DvachTopicBoards[k])
			}
			return true
		})
		return err
	}
	if CatalogKind(platform) == "" || id == "" || len(id) > 500 {
		return Bad("Некорректный источник.")
	}
	if kind := CatalogKind(platform); kind != "" {
		for _, entry := range *u.catalogWithPersonalPins(topic).List(kind) {
			if SourceIDFor(platform, jx.Str(entry, "id")) == SourceIDFor(platform, id) && jx.Bool(entry, "autopilot_managed") {
				return u.SourceOverrideSet(SourceIDFor(platform, id), false)
			}
		}
	}
	if name == "" {
		name = id
	}
	source := jx.M{"platform": platform, "id": id, "name": name, "source_id": SourceIDFor(platform, id)}
	return u.rejectTopicSource(source, topic, false)
}

// TopicSources — источники текущей темы для вкладки.
func (u *U) TopicSources() []jx.M {
	return u.TopicSourcesFor(u.CurrentTopic())
}

// TopicSourcesFor uses the same catalog and inheritance rules as the current-topic view.
func (u *U) TopicSourcesFor(topic string) []jx.M {
	pins := u.personalSourcePins(topic)
	d := u.catalogWithPins(topic, pins)
	overrides := u.sourceOverridesForTopic(topic)
	autopilot := map[string]jx.M{}
	rows, err := u.DB.Pool.Query(u.Ctx, `SELECT source_id,state,reason,evidence FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1`, autopilotTopicKey(topic))
	if err == nil {
		for rows.Next() {
			var source, state, reason string
			var raw []byte
			if rows.Scan(&source, &state, &reason, &raw) == nil {
				autopilot[source] = jx.M{"state": state, "reason": reason, "evidence": jx.Unmarshal(raw)}
			}
		}
		rows.Close()
	}
	cards := []jx.M{}
	for _, ck := range CatalogKinds {
		platform := ck.Platform
		for _, e := range *d.List(ck.Kind) {
			id := strings.TrimLeft(jx.Str(e, "id"), "@")
			sid := SourceIDFor(platform, id)
			if !u.CatalogEntryMatchesTopic(e, topic, false) || u.UASourceBlocked(sid, jx.M{"title": jx.Str(e, "name")}) != "" {
				continue
			}
			if (platform == PlatformRSS && !IsValidRSSURL(id)) || (platform == PlatformOK && !IsValidOKGroup(id)) || (platform == PlatformTikTok && !IsValidTikTokUser(id)) {
				continue
			}
			aud := jx.Map(e, "audience")
			subs := jx.Int(aud, "subscribers")
			var link, handle, kind string
			switch platform {
			case PlatformRSS:
				link = SourceWebURL(platform, id)
				if pu, err := url.Parse(link); err == nil {
					handle = pu.Hostname() + pu.EscapedPath()
				}
				kind = RSSFeedKind(id)
				if jx.Bool(e, "news") {
					kind = NewsSiteKind(id)
				}
			case PlatformOK:
				link, handle, kind = "https://ok.ru/group/"+id, "ok.ru/group/"+id, platform
			case PlatformTikTok:
				link, handle, kind = "https://www.tiktok.com/@"+url.PathEscape(id), "tiktok.com/@"+id, platform
			case PlatformTelegram:
				link, handle, kind = "https://t.me/"+url.PathEscape(id), "@"+id, platform
			default:
				link, handle, kind = "https://vk.com/"+url.PathEscape(id), "vk.com/"+id, platform
			}
			name := jx.Str(e, "name")
			needsTitle := false
			if platform == PlatformRSS {
				name, needsTitle = pendingRSSName(name, "", id)
			} else if name == "" {
				name = id
			}
			var score, rate any
			if jx.Has(e, "score") {
				score = jx.Int(e, "score")
			}
			if aud != nil && aud["view_rate"] != nil {
				rate = jx.Float(aud, "view_rate")
			}
			label, activity := "", ""
			if subs > 0 {
				label = FormatSubscribers(subs)
			}
			if a := jx.Map(e, "activity"); a != nil {
				activity = DescribeSourceActivity(a)
			}
			preview := jx.List(e, "preview")
			cards = append(cards, jx.M{"source_id": sid, "platform": platform, "id": id, "name": name, "url": link, "handle": handle, "autopilot": autopilot[sid],
				"topics": orAny(jx.List(e, "topics")), "kind": kind, "needs_title": needsTitle, "platform_label": func() string {
					if platform == PlatformRSS {
						return RSSPlatformLabel(id)
					}
					return platform
				}(), "own": len(jx.List(e, "topics")) > 0 && u.CatalogEntryMatchesTopic(e, topic, true),
				"auto": jx.Bool(e, "auto"), "autopilot_managed": jx.Bool(e, "autopilot_managed"), "score": score, "muted": sourceExcludedByOverride(overrides, sid), "pinned": jx.Has(pins, sid), "subscribers": subs, "subscribers_label": label, "view_rate": rate, "activity": activity,
				"frozen": jx.Has(e, "frozen_until"), "frozen_reason": jx.Str(e, "frozen_reason"), "avatar": jx.Str(e, "avatar"), "preview": orAny(preview[:min(4, len(preview))])})
		}
	}
	for _, t := range u.approvedImageboardThreads(d, topic) {
		platform, board, thread, sid := jx.Str(t, "platform"), jx.Str(t, "board"), jx.Str(t, "thread_id"), jx.Str(t, "source_id")
		link := DvachPostURL(board, thread, thread)
		if platform == Platform4chan {
			link = fourchanPostURL(board, jx.ToInt(thread), jx.ToInt(thread))
		}
		cards = append(cards, jx.M{"source_id": sid, "platform": platform, "id": sid, "name": jx.Str(t, "name"), "url": link, "handle": "/" + board + "/ · #" + thread, "muted": sourceExcludedByOverride(overrides, sid),
			"topics": []any{topic}, "kind": "Тред", "own": true, "auto": false, "score": nil, "subscribers": 0, "view_rate": nil, "frozen": false, "preview": []any{}})
	}

	sort.SliceStable(cards, func(i, j int) bool {
		a, b := cards[i], cards[j]
		if jx.Bool(a, "own") != jx.Bool(b, "own") {
			return jx.Bool(a, "own")
		}
		if jx.Bool(a, "frozen") != jx.Bool(b, "frozen") {
			return !jx.Bool(a, "frozen")
		}
		return jx.Int(a, "subscribers") > jx.Int(b, "subscribers")
	})
	return cards
}
