package posta

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestYouTubeCatalogActionsPreserveChannelIDCase(t *testing.T) {
	a := YouTubeChannelFeedURL("UCabcdefghijklmnopqrstuv")
	b := YouTubeChannelFeedURL("UCAbcdefghijklmnopqrstuv")
	if sameCatalogSource(PlatformRSS, a, b) {
		t.Fatal("distinct case-sensitive channels were merged")
	}
	if !sameCatalogSource(PlatformRSS, a, "https://WWW.YOUTUBE.COM/feeds/videos.xml?channel_id=UCabcdefghijklmnopqrstuv") {
		t.Fatal("host case should not distinguish the same channel")
	}
	if sameCatalogSource(PlatformRSS, RSSYouTubeShortsFeed("UCabcdefghijklmnopqrstuv"), RSSYouTubeShortsFeed("UCAbcdefghijklmnopqrstuv")) {
		t.Fatal("distinct case-sensitive Shorts feeds were merged")
	}
	if !sameCatalogSource(PlatformTelegram, "ExampleChannel", "examplechannel") {
		t.Fatal("Telegram comparison changed")
	}
	u := taxonomyTestUser()
	d := &SourceDB{RSS: []jx.M{{"id": a, "name": "A", "topics": []any{"Новости"}}}}
	u.addApprovedSource(d, jx.M{"platform": PlatformRSS, "id": b, "name": "B"}, "Новости")
	if len(d.RSS) != 2 {
		t.Fatalf("distinct channel was not added: %#v", d.RSS)
	}
	u.removeRejectedSource(d, jx.M{"platform": PlatformRSS, "id": b}, "Новости")
	if len(d.RSS) != 1 || jx.Str(d.RSS[0], "id") != a {
		t.Fatalf("removing channel also affected its case variant: %#v", d.RSS)
	}
}

func TestYouTubeLegacySettingsAndRegistryPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("isolated PostgreSQL required")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err = d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	const owner, topic = "youtube-legacy-key-test", "Тема старого ключа YouTube"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	feed := YouTubeChannelFeedURL("UCabcdefghijklmnopqrstuv")
	sid, old := SourceIDFor(PlatformRSS, feed), "rss:"+strings.ToLower(feed)
	defer d.Pool.Exec(context.Background(), `DELETE FROM source_registry WHERE source_id=$1`, old)
	if _, err = d.Pool.Exec(ctx, `INSERT INTO source_registry(source_id,platform,name,url,status,reason) VALUES($1,$2,'legacy','',$3,'test') ON CONFLICT(source_id) DO UPDATE SET status=$3`, old, PlatformRSS, RegStatusDead); err != nil {
		t.Fatal(err)
	}
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter("", "", "", ""))
	a := &App{DB: d, LLM: router, Cfg: config.Server{MediaDir: t.TempDir(), MediaURL: "/media"}}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	u := a.User(ctx, owner, nil)
	if err = u.SaveSettings(jx.M{"topic": topic, "active_sources_only": false, "topic_formats": jx.M{TopicNorm(topic): jx.M{"media": "optional"}}, "source_overrides_v2": jx.M{autopilotTopicKey(topic): jx.M{old: "off"}}}); err != nil {
		t.Fatal(err)
	}
	if !sourceExcludedByOverride(u.sourceOverridesForTopic(topic), sid) {
		t.Fatal("legacy exclusion not applied")
	}
	if verdict, _, _ := u.RegistryVerdict(sid, topic); verdict != regSkip {
		t.Fatal("legacy registry decision not preserved")
	}
	if err = u.SourceOverrideSet(sid, true); err != nil {
		t.Fatal(err)
	}
	if sourceExcludedByOverride(u.sourceOverridesForTopic(topic), sid) {
		t.Fatal("new explicit decision did not override legacy exclusion")
	}
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	key := autopilotTopicKey(topic)
	for _, table := range []string{"source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	otherFeed := YouTubeChannelFeedURL("UCAbcdefghijklmnopqrstuv")
	other := SourceIDFor(PlatformRSS, otherFeed)
	defer a.UpdateCatalog(context.Background(), func(c *SourceDB) bool {
		var keep []jx.M
		for _, entry := range c.RSS {
			if jx.Str(entry, "id") != feed && jx.Str(entry, "id") != otherFeed {
				keep = append(keep, entry)
			}
		}
		c.RSS = keep
		var votes []jx.M
		for _, vote := range c.Votes {
			if !(jx.Str(vote, "source_id") == old && jx.Str(vote, "topic") == topic) {
				votes = append(votes, vote)
			}
		}
		c.Votes = votes
		return true
	})
	if _, err = a.UpdateCatalog(ctx, func(c *SourceDB) bool {
		c.Votes = append(c.Votes, jx.M{"source_id": old, "vote": "reject", "topic": topic})
		return true
	}); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%v err=%v", job, err)
	}
	job.MaxProbation = 3
	for _, source := range []string{sid, other} {
		if err = registerAutopilotCandidate(ctx, d, job, source, time.Now()); err != nil {
			t.Fatal(err)
		}
		if err = transitionAutopilotSource(ctx, d, job, source, "candidate", "probation", "test", []byte(`{}`), time.Now(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if err = a.projectAutopilotSource(ctx, job, source, "probation"); !errors.Is(err, errAutopilotSourceOverride) {
			t.Fatalf("legacy reject bypassed: %s %v", source, err)
		}
	}
	if _, err = a.UpdateCatalog(ctx, func(c *SourceDB) bool {
		var keep []jx.M
		for _, vote := range c.Votes {
			if !(jx.Str(vote, "source_id") == old && jx.Str(vote, "topic") == topic) {
				keep = append(keep, vote)
			}
		}
		c.Votes = keep
		return true
	}); err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{sid, other} {
		if err = a.projectAutopilotSource(ctx, job, source, "probation"); err != nil {
			t.Fatalf("project %s: %v", source, err)
		}
	}
	active, probation, err := a.autopilotProjectedCounts(ctx, topic)
	if err != nil || active != 0 || probation != 2 {
		t.Fatalf("counts %d/%d %v", active, probation, err)
	}
	video, err := a.MediaWrite(MediaCacheName("yt-test-video", "mp4"), []byte("test media"))
	if err != nil {
		t.Fatal(err)
	}
	poster, err := a.MediaWrite(MediaCacheName("yt-test-poster", "jpg"), []byte("test image"))
	if err != nil {
		t.Fatal(err)
	}
	post := jx.M{"url": "https://www.youtube.com/watch?v=Abcdefgh123", "text": "Публичное видео для проверки персональной доставки по теме.", "posted_at": time.Now().Unix(), "video": video, "video_local": video, "image": poster, "image_local": poster}
	analysis := jx.M{"advertising": false, "relevance": 95, "topic_match": true, "model": "test", "description": "Видео", "why_relevant": "По теме", "tags": []any{}}
	for _, source := range []string{sid, other} {
		if err = a.saveAutopilotContentResult(ctx, job, source, post, analysis); err != nil {
			t.Fatal(err)
		}
	}
	if err = a.deliverAutopilotTopicContent(ctx, job); err != nil {
		t.Fatal(err)
	}
	var count int
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1 AND data->>'source_key'=$2`, owner, sid).Scan(&count); err != nil || count != 1 {
		t.Fatalf("explicitly enabled delivery %d %v", count, err)
	}
	if err = d.Pool.QueryRow(ctx, `SELECT count(*) FROM pool_items WHERE user_id=$1 AND data->>'source_key'=$2`, owner, other).Scan(&count); err != nil || count != 0 {
		t.Fatalf("legacy excluded delivery %d %v", count, err)
	}
}

func TestYouTubeSourceKeysAndLegacyExclusions(t *testing.T) {
	a := YouTubeChannelFeedURL("UCabcdefghijklmnopqrstuv")
	b := YouTubeChannelFeedURL("UCAbcdefghijklmnopqrstuv")
	first, second := SourceIDFor(PlatformRSS, a), SourceIDFor(PlatformRSS, b)
	if first == second || first != "rss:"+a || second != "rss:"+b {
		t.Fatalf("channel IDs were folded: %q %q", first, second)
	}
	if sharedSourceKey(PlatformRSS, a) == sharedSourceKey(PlatformRSS, b) {
		t.Fatal("YouTube cache keys collided")
	}
	old := "rss:" + strings.ToLower(a)
	for _, id := range []string{first, second} {
		if !sourceExcludedByOverride(jx.M{old: "off"}, id) || !sourceKnownWithLegacy(map[string]bool{old: true}, id) {
			t.Fatalf("old ambiguous exclusion was lost: %s", id)
		}
		oldShortHost := strings.Replace(old, "rss:https://www.youtube.com/", "rss:https://youtube.com/", 1)
		if !sourceExcludedByOverride(jx.M{oldShortHost: "off"}, id) || !sourceKnownWithLegacy(map[string]bool{oldShortHost: true}, id) {
			t.Fatalf("old short-host exclusion was lost: %s", id)
		}
	}
	if sourceExcludedByOverride(jx.M{old: "off", first: "on"}, first) {
		t.Fatal("explicit new decision did not override old exclusion")
	}
	if recommendationSourceKey(first) == recommendationSourceKey(second) {
		t.Fatal("distinct recommendations collapsed")
	}
	if !recommendationExcluded(map[string]bool{recommendationSourceKey(old): true}, first) {
		t.Fatal("old recommendation exclusion was lost")
	}
	if _, ok := canonicalYouTubeFeed(a + "&token=secret"); ok {
		t.Fatal("credential-bearing feed accepted")
	}
}
