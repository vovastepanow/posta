package posta

import (
	"strconv"
	"strings"
	"time"

	"posta/internal/jx"
)

// YouTube без лент RSS: ролики и шортсы канала через yt-dlp (лента videos.xml иногда отдаёт 404).

const (
	YouTubeListCount   = 15
	youtubeListCacheTTL = 30 * time.Minute
	youtubeShortsDates = 1
)

// YouTubeChannelPosts — посты вкладки videos или shorts в формате RSSParse (кэш 30 минут в памяти).
func (u *U) YouTubeChannelPosts(cid, tab string, count int) ([]jx.M, error) {
	if !ytChannelIDRe.MatchString(cid) || (tab != "videos" && tab != "shorts") {
		return nil, Bad("Нужен канал YouTube и вкладка videos или shorts.")
	}
	key := "youtube_posts:" + cid + ":" + tab
	posts, ok := Mem[[]jx.M](u.Mem, key)
	if !ok {
		var err error
		if posts, err = u.youtubeListPosts(cid, tab); err != nil {
			return nil, err
		}
		u.Mem.Set(key, posts, youtubeListCacheTTL)
	}
	out := make([]jx.M, 0, min(count, len(posts)))
	for _, p := range posts[:min(count, len(posts))] {
		out = append(out, jx.Copy(p))
	}
	return out, nil
}

// YouTubeFeedChannel — лента YouTube → канал и вкладка (videos.xml?channel_id=…, playlist_id=UUSH…).
func YouTubeFeedChannel(feed string) (cid, tab string, ok bool) {
	if m := ytFeedIDRe.FindStringSubmatch(feed); m != nil {
		return m[1], "videos", true
	}
	if i := strings.Index(feed, "playlist_id=UUSH"); i >= 0 && len(feed) >= i+16+22 {
		return "UC" + feed[i+16:i+16+22], "shorts", true
	}
	return "", "", false
}

func (u *U) youtubeListPosts(cid, tab string) ([]jx.M, error) {
	url, _ := YtdlpCheckURL("https://www.youtube.com/channel/" + cid + "/" + tab)
	count := strconv.Itoa(YouTubeListCount)
	rows, err := u.youtubeListRows([]string{"--flat-playlist", "--playlist-end", count, "--extractor-args", "youtube:lang=ru",
		"--print", "%(id)s|%(view_count)s|%(thumbnails.-1.url)s|%(title)s", "--", url}, 4)
	if err != nil {
		return nil, err
	}
	var dates map[string]int64
	if tab == "videos" {
		r, _ := u.youtubeListRows([]string{"--flat-playlist", "--playlist-end", count, "--extractor-args", "youtubetab:approximate_date",
			"--print", "%(id)s|%(timestamp)s", "--", url}, 2)
		dates = youtubeDatesMap(r)
	} else {
		var ids []string
		for _, r := range rows {
			if videoIDRe.MatchString(r[0]) && len(ids) < youtubeShortsDates {
				ids = append(ids, r[0])
			}
		}
		if len(ids) > 0 {
			args := []string{"--print", "%(id)s|%(timestamp)s", "--"}
			for _, id := range ids {
				args = append(args, "https://www.youtube.com/shorts/"+id)
			}
			r, _ := u.youtubeListRows(args, 2)
			dates = youtubeDatesMap(r)
		}
	}
	var posts []jx.M
	var prev int64
	for _, r := range rows {
		if !videoIDRe.MatchString(r[0]) {
			continue
		}
		at, ok := dates[r[0]]
		if !ok {
			at = prev // без даты — не новее предыдущего (вкладка идёт от новых к старым)
		}
		prev = at
		views, _ := strconv.ParseInt(r[1], 10, 64)
		posts = append(posts, YouTubePost(r[0], r[3], r[2], at, views, tab == "shorts"))
	}
	return posts, nil
}

func youtubeDatesMap(rows [][]string) map[string]int64 {
	out := map[string]int64{}
	for _, r := range rows {
		if ts, err := strconv.ParseInt(r[1], 10, 64); err == nil {
			out[r[0]] = ts
		}
	}
	return out
}

func (u *U) youtubeListRows(args []string, fields int) ([][]string, error) {
	r := u.YtdlpRun(u.Ctx, args, ytdlpListTimeout, true, nil)
	if !r.OK && strings.TrimSpace(r.Stdout) == "" {
		return nil, Bad("YouTube не отдал список: %s", YtdlpErrorText(r))
	}
	var rows [][]string
	for _, line := range strings.Split(r.Stdout, "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", fields)
		for len(parts) < fields {
			parts = append(parts, "")
		}
		rows = append(rows, parts)
	}
	return rows, nil
}

// YouTubePost — запись поста как в RSSParse; ролик скачивается через yt-dlp.
func YouTubePost(id, title, thumb string, at, views int64, short bool) jx.M {
	page := "https://www.youtube.com/watch?v=" + id
	if short {
		page = "https://www.youtube.com/shorts/" + id
	}
	img := thumb
	if !strings.HasPrefix(img, "https://") {
		img = "https://i.ytimg.com/vi/" + id + "/hqdefault.jpg"
	}
	text := title
	if !short {
		text += "\n" + page
	}
	text = trimRunes(strings.TrimSpace(text), 1000)
	if text == "" {
		text = EmptyPostText
	}
	return jx.M{"image": img, "images": []any{img}, "gallery": []any{}, "video": "", "text": text, "source": SourceDisplayName(PlatformRSS, "youtube.com"),
		"post_id": sha1Short(id, 12), "posted_at": at, "url": page, "video_unavailable": false, "engagement": nil, "views": views,
		"video_via": "ytdlp", "video_page": page}
}
