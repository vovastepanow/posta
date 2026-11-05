package posta

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"log"
	"net/url"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

const (
	sharedCollectionInterval = 20 * time.Minute
	// RSSGet can follow several redirects, each with its own timeout.
	sharedCollectionLease   = 12 * time.Minute
	sharedCollectionSources = 8
)

type sharedPosts struct {
	At    int64  `json:"at"`
	Posts []jx.M `json:"posts"`
}

func sharedSourceKey(platform, id string) string {
	return "public_collection:v1:" + sha1Short(platform+"|"+strings.ToLower(id), 40)
}

// Only publicly accessible feeds and public Telegram pages are eligible. API
// integrations, signed URLs and query-specific sources stay in personal passes.
func sharedSourceEligible(platform string, entry jx.M) bool {
	id := strings.TrimSpace(jx.Str(entry, "id"))
	if id == "" || jx.Str(entry, "query") != "" || IsSourceFrozen(entry, 0) {
		return false
	}
	if platform == PlatformTelegram {
		return IsValidTelegramChannel(strings.TrimPrefix(id, "@"))
	}
	if platform == PlatformVK {
		return IsValidVKDomain(id)
	}
	if platform == PlatformOK {
		return IsValidOKGroup(id)
	}
	if platform == PlatformTikTok {
		return IsValidTikTokUser(strings.TrimPrefix(id, "@"))
	}
	if platform == PlatformDvach {
		return IsValidDvachBoard(id)
	}
	if platform == Platform4chan {
		return boardNameRe.MatchString(id)
	}
	if platform != PlatformRSS {
		return false
	}
	u, err := url.Parse(id)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return false
	}
	return instagramProfileName(id) == "" && facebookPageID(id) == "" && threadsProfileName(id) == "" && redditSourceName(id) == "" && !strings.Contains(strings.ToLower(u.Hostname()), "discord.com")
}

// RequestSharedCollection is idempotent: repeated requests raise priority but
// do not interrupt a running job or schedule a recently completed job again.
func (a *App) RequestSharedCollection(ctx context.Context, topic string, priority int) {
	topic = strings.TrimSpace(topic)
	key := TopicNorm(topic)
	if key == "" || a.TopicLookup(topic) == nil {
		return
	}
	_, err := a.DB.Pool.Exec(ctx, `INSERT INTO topic_collection_queue(topic_key,topic,priority,requested_at)
		VALUES($1,$2,$3,$4) ON CONFLICT(topic_key) DO UPDATE SET topic=EXCLUDED.topic,
		priority=GREATEST(topic_collection_queue.priority,EXCLUDED.priority),
		requested_at=CASE WHEN topic_collection_queue.last_done <= $4-$5 AND topic_collection_queue.requested_at=0 AND topic_collection_queue.lease_until <= $4 THEN $4 ELSE topic_collection_queue.requested_at END`,
		key, topic, priority, now(), int64(sharedCollectionInterval.Seconds()))
	if err != nil && ctx.Err() == nil {
		log.Printf("очередь тем: %v", err)
	}
}

func (a *App) runSharedCollection(ctx context.Context) {
	tick := time.NewTicker(2 * time.Second)
	hot := time.NewTicker(10 * time.Minute)
	defer tick.Stop()
	defer hot.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-hot.C:
			for _, t := range a.HotTopics(ctx, 20, false) {
				a.RequestSharedCollection(ctx, jx.Str(t, "title"), 1)
			}
		case <-tick.C:
			a.parsers.mu.Lock()
			if a.parsers.sharedPaused {
				a.parsers.mu.Unlock()
				continue
			}
			jobCtx, cancel := context.WithCancel(ctx)
			done := make(chan struct{})
			a.parsers.sharedCancel, a.parsers.sharedDone = cancel, done
			a.parsers.mu.Unlock()
			a.collectNextSharedTopic(jobCtx)
			cancel()
			a.parsers.mu.Lock()
			a.parsers.sharedCancel, a.parsers.sharedDone = nil, nil
			close(done)
			a.parsers.mu.Unlock()
		}
	}
}

func (a *App) collectNextSharedTopic(ctx context.Context) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return
	}
	lease := hex.EncodeToString(b[:])
	var key, topic string
	var lastDone int64
	err := a.DB.Pool.QueryRow(ctx, `UPDATE topic_collection_queue SET lease_until=$1,lease_id=$2,attempts=attempts+1
		WHERE topic_key=(SELECT topic_key FROM topic_collection_queue WHERE requested_at>0 AND requested_at <= $3 AND lease_until <= $3
		ORDER BY priority DESC,requested_at ASC FOR UPDATE SKIP LOCKED LIMIT 1)
		RETURNING topic_key,topic,last_done`, now()+int64(sharedCollectionLease.Seconds()), lease, now()).Scan(&key, &topic, &lastDone)
	if err != nil {
		return
	}
	jobCtx, cancel := context.WithTimeout(ctx, sharedCollectionLease-10*time.Second)
	defer cancel()
	count, eligible := a.collectPublicTopic(jobCtx, topic, lastDone)
	if ctx.Err() != nil {
		// Release the lease so the interrupted topic can be collected after resume.
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		_, _ = a.DB.Pool.Exec(cleanup, `UPDATE topic_collection_queue SET lease_until=0,lease_id='' WHERE topic_key=$1 AND lease_id=$2`, key, lease)
		return
	}
	if eligible > 0 && count == 0 {
		_, err = a.DB.Pool.Exec(ctx, `UPDATE topic_collection_queue SET requested_at=$3,lease_until=0,lease_id='' WHERE topic_key=$1 AND lease_id=$2`, key, lease, now()+300)
		if err != nil {
			log.Printf("повтор общего сбора темы %s: %v", topic, err)
		}
		return
	}
	_, err = a.DB.Pool.Exec(ctx, `UPDATE topic_collection_queue SET requested_at=0,priority=0,lease_until=0,lease_id='',last_done=$3 WHERE topic_key=$1 AND lease_id=$2`, key, lease, now())
	if err != nil {
		log.Printf("очередь темы %s: %v", topic, err)
	} else {
		log.Printf("общий сбор темы %q: источников %d", topic, count)
	}
}

func (a *App) collectPublicTopic(ctx context.Context, topic string, lastDone int64) (int, int) {
	type source struct {
		platform string
		entry    jx.M
	}
	var sources []source
	d := a.Catalog()
	for _, c := range CatalogKinds {
		for _, e := range *d.List(c.Kind) {
			if sharedSourceEligible(c.Platform, e) && a.CatalogEntryMatchesTopic(e, topic, false) {
				sources = append(sources, source{c.Platform, e})
			}
		}
	}

	if len(sources) == 0 {
		return 0, 0
	}
	start := int((lastDone / int64(sharedCollectionInterval.Seconds()) * sharedCollectionSources) % int64(len(sources)))
	completed := 0
	for i := 0; i < min(sharedCollectionSources, len(sources)) && ctx.Err() == nil; i++ {
		s := sources[(start+i)%len(sources)]
		id := strings.TrimLeft(strings.TrimSpace(jx.Str(s.entry, "id")), "@")
		key := sharedSourceKey(s.platform, id)
		var cached sharedPosts
		if a.CacheGet(ctx, key, &cached) && now()-cached.At < int64(sharedCollectionInterval.Seconds()) {
			completed++
			continue
		}
		name := jx.Str(s.entry, "name")
		if name == "" {
			name = id
		}
		name = SourceDisplayName(s.platform, name)
		var posts []jx.M
		switch s.platform {
		case PlatformTelegram:
			page := a.FetchTelegramChannelPage(id, 0)
			if page == "" {
				continue
			}
			posts = TelegramPostsFromHTML(page, jx.M{"id": id, "name": name}, config.TelegramPostsPerChannel, true, 120)
		case PlatformRSS:
			var err error
			posts, err = a.User(ctx, "", nil).RSSFetchPosts(id, config.RSSPostsPerFeed, name, true, true)
			if err != nil {
				continue
			}
		case PlatformOK:
			var err error
			posts, err = a.OKFetchPosts(id, OKPostsPerGroup, name)
			if err != nil {
				continue
			}
		case PlatformTikTok:
			var err error
			posts, err = a.TikTokFetchPosts(ctx, id, TikTokPostsPerAccount, name)
			if err != nil {
				continue
			}
		case PlatformVK:
			var err error
			posts, err = a.sharedVKPosts(id, name)
			if err != nil {
				continue
			}
		case PlatformDvach, Platform4chan:
			u := a.User(ctx, "", nil)
			u.run = newRun(a)
			if s.platform == PlatformDvach {
				posts = u.ParseDvach(id, "")
			} else {
				posts = u.ParseFourchan(id, FourchanThreadsPerBoard, "")
			}
			if len(posts) == 0 {
				continue
			}
		}
		for _, p := range posts {
			if s.platform == PlatformDvach || s.platform == Platform4chan {
				continue
			}
			p["source_key"] = SourceIDFor(s.platform, id)
			p["source_url"] = jx.Str(p, "url")
			if !jx.Has(p, "video") {
				p["video"] = ""
			}
		}
		a.CacheSet(ctx, key, sharedPosts{At: now(), Posts: posts}, sharedCollectionInterval)
		completed++
	}
	return completed, len(sources)
}

// sharedVKPosts uses only the server's service token, never an OAuth account.
func (a *App) sharedVKPosts(id, name string) ([]jx.M, error) {
	if a.Cfg.VKServiceToken == "" {
		return a.vkPublicPosts(id, name, 30, true, 120)
	}
	params := url.Values{"domain": {id}, "count": {"30"}, "v": {"5.199"}, "access_token": {a.Cfg.VKServiceToken}}
	body, status, err := a.HTTPPost("https://api.vk.com/method/wall.get", []byte(params.Encode()), "", nil, 15*time.Second)
	if err != nil || status != 200 {
		return a.vkPublicPosts(id, name, 30, true, 120)
	}
	var reply struct {
		Response struct {
			Items []jx.M `json:"items"`
		} `json:"response"`
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(body, &reply) != nil || len(reply.Error) > 0 && string(reply.Error) != "null" {
		return a.vkPublicPosts(id, name, 30, true, 120)
	}
	var posts []jx.M
	for _, row := range reply.Response.Items {
		if post := VKWallPost(row, name, id, true, 120); post != nil {
			posts = append(posts, post)
		}
	}
	if len(posts) == 0 {
		return a.vkPublicPosts(id, name, 30, true, 120)
	}
	return posts, nil
}

func (a *App) waitSharedCollection(ctx context.Context, topic string) {
	deadline := time.NewTimer(30 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		var pending bool
		err := a.DB.Pool.QueryRow(ctx, `SELECT requested_at>0 AND requested_at <= $2 FROM topic_collection_queue WHERE topic_key=$1`, TopicNorm(topic), now()).Scan(&pending)
		if err != nil || !pending {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			return
		case <-tick.C:
		}
	}
}
