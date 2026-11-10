package posta

import (
	"encoding/json"
	"testing"

	"posta/internal/jx"
)

func TestAdultPreferenceAppliesAcrossTopics(t *testing.T) {
	a := taxonomyTestUser().App
	for _, topic := range []string{"Мемы", "Новости", "18+", "Жесть"} {
		for _, item := range []jx.M{
			{"tags": []string{"adult"}},
			{"tags": []string{"shock"}},
			{"tags": []string{ContentLewdHumorTag}},
			{"source_sensitive": true},
			{"board": "hc"},
		} {
			if a.contentAdultBlocked(item, topic, true) {
				t.Errorf("enabled preference blocked %v in %q", item, topic)
			}
			if !a.contentAdultBlocked(item, topic, false) {
				t.Errorf("disabled preference allowed %v in %q", item, topic)
			}
		}
	}
	if a.contentAdultBlocked(jx.M{"tags": []string{"animals.cats"}}, "Животные", false) {
		t.Fatal("ordinary post blocked by adult preference")
	}
}

func TestFullHumorVerdictSurvivesAdmission(t *testing.T) {
	u := taxonomyTestUser()
	for _, video := range []bool{false, true} {
		for _, kind := range []string{"finished_meme", "funny_situation"} {
			a := u.finishAnalysis(jx.M{"relevance": 90, "format": "photo", "meme_kind": kind, "joke": "Неожиданный поворот в конце", "evidence": "Видимая развязка", "advertising": false}, "Мемы", "", video, nil, false, "test")
			// No model is configured: a second single-frame judge would fail this test.
			if jx.Int(a, "relevance") != 90 {
				t.Fatalf("full verdict overridden: %v", a)
			}
			if reason := u.QualityReasons(a, jx.M{}, "Мемы", DefaultQualityOpts()); reason != "" {
				t.Fatalf("valid visual joke rejected: %s", reason)
			}
		}
	}
	if reason := u.QualityReasons(jx.M{"relevance": 90, "format": "photo", "meme_kind": "not_meme"}, nil, "Мемы", DefaultQualityOpts()); reason == "" {
		t.Fatal("ordinary photo without a joke admitted as a meme")
	}
}

func TestSensitiveSearchPostsReachPreferenceFilter(t *testing.T) {
	for _, tc := range []struct {
		name  string
		parse func([]byte) ([]jx.M, error)
		body  string
	}{
		{"rutube", RutubeParse, `{"results":[{"id":"1","title":"Sensitive video","is_adult":true,"thumbnail_url":"https://example.org/image.jpg"}]}`},
		{"9gag", NineGAGParse, `{"data":{"posts":[{"id":"1","title":"Sensitive image","nsfw":1,"images":{"image460":{"url":"https://example.org/image.jpg"}}}]}}`},
		{"lemmy", LemmyParse, `{"posts":[{"post":{"name":"Sensitive image","nsfw":true,"published":"2026-11-01T12:00:00Z","url":"https://example.org/image.jpg"}}]}`},
		{"mastodon", MastodonParse, `[{"sensitive":true,"created_at":"2026-11-01T12:00:00Z","account":{"id":"1"},"media_attachments":[{"type":"image","url":"https://example.org/image.jpg"}]}]`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			posts, err := tc.parse([]byte(tc.body))
			if err != nil || len(posts) != 1 {
				t.Fatalf("sensitive post lost before user preference: %v, %v", posts, err)
			}
			a := taxonomyTestUser().App
			if !a.ContentIsAdult(posts[0]) || !a.contentAdultBlocked(posts[0], "Мемы", false) || a.contentAdultBlocked(posts[0], "Мемы", true) {
				t.Fatalf("source sensitivity or preference lost: %v", posts[0])
			}
		})
	}
}

func TestSensitiveXPostReachesPreferenceFilter(t *testing.T) {
	tweet := jx.M{"__typename": "Tweet", "rest_id": "123", "legacy": jx.M{"possibly_sensitive": true},
		"details":         jx.M{"full_text": "Sensitive image"},
		"core":            jx.M{"user_results": jx.M{"result": jx.M{"core": jx.M{"screen_name": "example"}}}},
		"media_entities2": []any{jx.M{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/test.jpg"}}}
	body, _ := json.Marshal(jx.M{"profile_user_originals_timeline": []any{tweet}})
	quoted, _ := json.Marshal(string(body))
	posts := xProfilePosts([]byte(`<script>value:`+string(quoted)+`</script>`), "example", 10, false)
	if len(posts) != 1 || !jx.Bool(posts[0], "source_sensitive") {
		t.Fatalf("sensitive X post lost: %v", posts)
	}
	u := taxonomyTestUser()
	if !u.analysisPrivate("Мемы", posts[0]) {
		t.Fatal("source sensitivity must be respected by model routing")
	}
}
