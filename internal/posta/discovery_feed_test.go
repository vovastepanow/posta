package posta

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"posta/internal/jx"
)

func TestXPublicProfilePosts(t *testing.T) {
	tweet := jx.M{"__typename": "Tweet", "rest_id": "123", "details": jx.M{"full_text": "A photo", "created_at_ms": int64(1793400000000)}, "core": jx.M{"user_results": jx.M{"result": jx.M{"core": jx.M{"screen_name": "NASA"}}}}, "media_entities2": []any{jx.M{"type": "photo", "media_url_https": "https://pbs.twimg.com/media/test.jpg"}}}
	jsonBody, _ := json.Marshal(jx.M{"profile_user_originals_timeline": []any{tweet, tweet}})
	quoted, _ := json.Marshal(string(jsonBody))
	page := []byte(`<script>value:` + string(quoted) + `</script>`)
	posts := xProfilePosts(page, "nasa", 10, false)
	if len(posts) != 1 || jx.Str(posts[0], "url") != "https://x.com/nasa/status/123" || jx.Int(posts[0], "posted_at") != 1793400000 {
		t.Fatalf("posts: %#v", posts)
	}
	if len(xProfilePosts(page, "other", 10, false)) != 0 {
		t.Fatal("accepted another author's post")
	}
	if xProfileName("https://x.com.evil.example/nasa") != "" || xProfileName("https://x.com/search") != "" {
		t.Fatal("accepted invalid profile")
	}
}

func TestXSearchCandidates(t *testing.T) {
	page := `<a href="https://x.com/NASA">NASA</a><a href="https://twitter.com/NASA/status/123">post</a><a href="https://x.com/search?q=cat">search</a><a href="https://x.com.evil.example/NASA">bad</a>`
	got := xSearchCandidates(page)
	if len(got) != 1 || got[0].ID != "https://x.com/nasa" {
		t.Fatalf("candidates: %#v", got)
	}
}

func TestSearchPostDiscoveryFeeds(t *testing.T) {
	for _, tc := range []struct {
		name, key, profile, want string
	}{
		{"lemmy", "lemmy:42", "https://lemmy.world/c/cats", "https://lemmy.world/feeds/c/cats.xml"},
		{"mastodon", "mastodon:42", "https://mastodon.social/@cats", "https://mastodon.social/@cats.rss"},
		{"wrong path", "lemmy:42", "https://lemmy.world/post/1", ""},
		{"insecure", "mastodon:42", "http://mastodon.social/@cats", ""},
		{"credentials", "mastodon:42", "https://user@mastodon.social/@cats", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			post := searchPost("test", tc.key, tc.profile, "", "", "", "", "https://example.org/post/1", 1)
			if jx.Str(post, "source_url") != "https://example.org/post/1" {
				t.Fatal("post link was changed")
			}
			candidate := PostSearchAuthor(post)
			if tc.want == "" {
				if candidate != nil {
					t.Fatalf("unexpected candidate: %#v", candidate)
				}
				return
			}
			if candidate == nil || candidate.Platform != PlatformRSS || candidate.ID != tc.want {
				t.Fatalf("candidate = %#v, want RSS %q", candidate, tc.want)
			}
		})
	}
}

func TestDiscoveryLemmyCommunities(t *testing.T) {
	data := []byte(`{"communities":[{"community":{"actor_id":"https://lemmy.world/c/cats","title":"Cats"}},{"community":{"actor_id":"https://lemmy.world/c/deleted","deleted":true}},{"community":{"actor_id":"http://example.org/c/unsafe"}}]}`)
	candidates, err := discoveryLemmyCommunities(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ID != "https://lemmy.world/feeds/c/cats.xml" || jx.Str(candidates[0].Info, "title") != "Cats" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
	if _, err := discoveryLemmyCommunities([]byte(`{`)); err == nil {
		t.Fatal("expected malformed response error")
	}
}

func TestDiscoveryRedditCommunities(t *testing.T) {
	data := []byte(`{"data":{"children":[{"data":{"display_name":"Cats","title":"Cats","subreddit_type":"public"}},{"data":{"display_name":"private","subreddit_type":"private"}},{"data":{"display_name":"adult","subreddit_type":"public","over18":true}},{"data":{"display_name":"../bad","subreddit_type":"public"}}]}}`)
	candidates, err := discoveryRedditCommunities(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(candidates) != 1 || candidates[0].ID != "https://www.reddit.com/r/cats/.rss" || jx.Str(candidates[0].Info, "title") != "Cats" {
		t.Fatalf("unexpected candidates: %#v", candidates)
	}
	if _, err := discoveryRedditCommunities([]byte(`{`)); err == nil {
		t.Fatal("expected malformed response error")
	}
}

func TestDiscoveryHabrHubs(t *testing.T) {
	page := `<a href="/ru/hubs/python/"><span>Python</span></a><a href="/ru/hubs/python/articles/">Python</a><a href="https://evil.example/ru/hubs/python/">Python</a><a href="/ru/hubs/java/">Java</a>`
	got := discoveryHabrHubs(page, []string{"python"})
	if len(got) != 1 || got[0].ID != "https://habr.com/ru/rss/hubs/python/articles/all/?fl=ru" {
		t.Fatalf("unexpected hubs: %#v", got)
	}
}

func TestDiscoveryBluesky(t *testing.T) {
	did := "did:plc:oky5czdrnfjpqslsw2a5iclo"
	actors, err := discoveryBlueskyActors([]byte(`{"actors":[{"did":"` + did + `","handle":"jay.bsky.team","displayName":"Jay"},{"did":"../../bad"}]}`))
	if err != nil || len(actors) != 1 || actors[0].ID != "https://bsky.app/profile/"+did+"/rss" {
		t.Fatalf("actors: %#v, %v", actors, err)
	}
	posts, err := discoveryBlueskyPostAuthors([]byte(`{"posts":[{"author":{"did":"` + did + `","handle":"jay.bsky.team"}},{"author":{"did":"` + did + `"}}]}`))
	if err != nil || len(posts) != 1 || posts[0].ID != actors[0].ID {
		t.Fatalf("post authors: %#v, %v", posts, err)
	}
}

func TestDiscoveryNewsSites(t *testing.T) {
	data := []byte(`<rss><channel><item><link>https://example.org/article</link></item><item><link>https://example.org/other</link></item><item><link>https://www.bing.com/news/1</link></item><item><link>https://news.example/1</link><source url="https://publisher.example/path">Publisher</source></item><item><link>https://user@bad.example/a</link></item></channel></rss>`)
	got := discoveryNewsSites(data)
	if len(got) != 2 || got[0] != "https://example.org/" || got[1] != "https://publisher.example/" {
		t.Fatalf("sites: %#v", got)
	}
	if got := discoveryNewsSites([]byte(`not rss`)); len(got) != 0 {
		t.Fatalf("invalid feed: %#v", got)
	}
}

func TestLinkedAdditionalSources(t *testing.T) {
	text := `https://www.reddit.com/r/Cats/ https://bsky.app/profile/cats.bsky.social https://www.pinterest.com/artuser/cats/ https://rutube.ru/channel/123/`
	got := SourceLinkedSources(text)
	seen := map[string]bool{}
	for _, c := range got {
		seen[c.ID] = true
	}
	for _, want := range []string{"https://www.reddit.com/r/cats/.rss", "https://bsky.app/profile/cats.bsky.social/rss", "https://www.pinterest.com/artuser/cats.rss", "https://rutube.ru/channel/123/"} {
		if !seen[want] {
			t.Errorf("missing %s in %#v", want, got)
		}
	}
}

func TestRutubeSourceFromSearch(t *testing.T) {
	posts, err := RutubeParse(readFixture(t, "search_rutube.json"))
	if err != nil || len(posts) == 0 {
		t.Fatalf("posts: %d, %v", len(posts), err)
	}
	c := PostSearchAuthor(posts[0])
	if c == nil || c.Platform != PlatformRSS || RutubeChannelID(c.ID) == "" {
		t.Fatalf("candidate: %#v", c)
	}
	if RutubeChannelID("https://rutube.ru.example/channel/123/") != "" || RutubeChannelID("https://rutube.ru/video/123/") != "" {
		t.Fatal("accepted invalid channel")
	}
	feed, err := (&App{}).RSSKnownFeed("https://rutube.ru/video/person/123/")
	if err != nil || feed != "https://rutube.ru/channel/123/" {
		t.Fatalf("feed %q: %v", feed, err)
	}
}

func TestOsnovaCommunitySource(t *testing.T) {
	posts, err := osnovaTimelinePosts(readFixture(t, "vc_search.json"), "vc.ru", 199123)
	if err != nil || len(posts) == 0 {
		t.Fatalf("posts: %d, %v", len(posts), err)
	}
	c := PostSearchAuthor(posts[0])
	if c == nil {
		t.Fatal("missing community")
	}
	host, id := osnovaTimelineSource(c.ID)
	if host != "vc.ru" || id != 199123 {
		t.Fatalf("unexpected community: %#v", c)
	}
	if host, _ := osnovaTimelineSource("https://api.vc.ru.evil.example/v2.10/timeline?subsitesIds=1"); host != "" {
		t.Fatal("accepted foreign host")
	}
}

func TestDiscoveryPinterestBoards(t *testing.T) {
	page := `<a href="/catsuser/cats/">Cats</a><a href="https://www.pinterest.com/catsuser/cats/">Again</a><a href="https://evil.example/catsuser/cats/">Wrong host</a><a href="/pin/123/">Pin</a>`
	got := discoveryPinterestBoards(page)
	if len(got) != 1 || got[0].ID != "https://www.pinterest.com/catsuser/cats.rss" {
		t.Fatalf("boards: %#v", got)
	}
}

func TestPikabuDiscoveryAndPosts(t *testing.T) {
	candidates, err := pikabuSearchCommunities(readFixture(t, "search_pikabu.html"))
	if err != nil || len(candidates) == 0 {
		t.Fatalf("candidates: %d, %v", len(candidates), err)
	}
	for _, c := range candidates {
		if pikabuCommunityURL(c.ID) == "" {
			t.Fatalf("bad candidate: %#v", c)
		}
	}
	page := `<html><head><meta charset="utf-8"></head><body><article class="story" data-timestamp="1793400000"><header><img src="https://cdn.example/avatar.jpg"><h2><a class="story__title-link" href="/story/test_123">Кошка</a></h2></header><div class="story__content-inner"><div class="story-block_type_image"><img data-large-image="https://cs.pikabu.ru/cat.jpg"></div><div class="story-block_type_text">Текст</div></div></article></body></html>`
	posts, err := pikabuCommunityPosts([]byte(page), "https://pikabu.ru/community/Cat", 5, false)
	if err != nil || len(posts) != 1 || jx.Str(posts[0], "image") != "https://cs.pikabu.ru/cat.jpg" || jx.Int(posts[0], "posted_at") != 1793400000 {
		t.Fatalf("posts: %#v, %v", posts, err)
	}
}

func TestRedditAuthenticatedListing(t *testing.T) {
	data := []byte(`{"data":{"children":[{"data":{"title":"Cat","subreddit":"cats","permalink":"/r/cats/comments/abc/cat/","created_utc":1793400000,"preview":{"images":[{"source":{"url":"https://preview.redd.it/cat.jpg?a=1&amp;b=2"}}]}}},{"data":{"title":"Other","subreddit":"dogs","permalink":"/r/dogs/comments/abc/dog/","url":"https://i.redd.it/dog.jpg"}},{"data":{"title":"Adult","subreddit":"cats","over_18":true,"permalink":"/r/cats/comments/x/","url":"https://i.redd.it/cat.jpg"}}]}}`)
	posts, err := redditListingPosts(data, "cats", 10, false)
	if err != nil || len(posts) != 1 || jx.Str(posts[0], "image") != "https://preview.redd.it/cat.jpg?a=1&b=2" {
		t.Fatalf("posts: %#v, %v", posts, err)
	}
	if redditSourceName("https://www.reddit.com/r/Cats/.rss") != "cats" || redditSourceName("https://www.reddit.com.evil.example/r/cats/.rss") != "" {
		t.Fatal("invalid source handling")
	}
}

func TestDiscordSourceParsing(t *testing.T) {
	channels := []byte(`[{"id":"123456","guild_id":"654321","name":"cats","topic":"pictures of cats","type":0},{"id":"123457","guild_id":"654321","name":"cats-private","type":0,"nsfw":true}]`)
	candidates, err := discordMatchingChannels(channels, "654321", []string{"cats"})
	if err != nil || len(candidates) != 1 || candidates[0].ID != "https://discord.com/channels/654321/123456" {
		t.Fatalf("channels: %#v, %v", candidates, err)
	}
	data := []byte(`[{"id":"999999","channel_id":"123456","content":"cat","timestamp":"2026-10-31T12:00:00Z","attachments":[{"url":"https://cdn.discordapp.com/attachments/a/cat.jpg","content_type":"image/jpeg"}]},{"id":"999998","channel_id":"123457","content":"wrong channel","timestamp":"2026-10-31T12:00:00Z","attachments":[{"url":"https://cdn.discordapp.com/a.jpg","content_type":"image/jpeg"}]}]`)
	posts, err := discordMessages(data, "654321", "123456", 10, false)
	if err != nil || len(posts) != 1 || jx.Str(posts[0], "url") != "https://discord.com/channels/654321/123456/999999" {
		t.Fatalf("posts: %#v, %v", posts, err)
	}
	if g, _ := discordSource("https://discord.com.evil.example/channels/654321/123456"); g != "" {
		t.Fatal("accepted foreign host")
	}
}

func TestThreadsSources(t *testing.T) {
	data := []byte(`{"data":[{"id":"123","username":"Cats","text":"cat","media_type":"IMAGE","media_url":"https://cdn.example/cat.jpg","permalink":"https://www.threads.net/@cats/post/123","timestamp":"2026-10-31T12:00:00+0000"},{"id":"456","username":"Other","media_type":"IMAGE","media_url":"https://cdn.example/other.jpg","permalink":"https://www.threads.net/@other/post/456","timestamp":"2026-10-31T12:00:00+0000"}]}`)
	candidates, err := threadsSearchCandidates(data)
	if err != nil || len(candidates) != 2 {
		t.Fatalf("candidates: %#v %v", candidates, err)
	}
	posts, err := threadsProfilePosts(data, "cats", 10, false)
	if err != nil || len(posts) != 1 || jx.Str(posts[0], "image") != "https://cdn.example/cat.jpg" {
		t.Fatalf("posts: %#v %v", posts, err)
	}
	if threadsProfileName("https://threads.net.evil.example/@cats") != "" {
		t.Fatal("accepted foreign host")
	}
}

func TestMetaSources(t *testing.T) {
	ig := []byte(`{"business_discovery":{"username":"cats","media":{"data":[{"id":"1","caption":"cat","media_type":"IMAGE","media_url":"https://cdn.example/cat.jpg","permalink":"https://www.instagram.com/p/123/","timestamp":"2026-10-31T12:00:00+0000"}]}}}`)
	posts, err := instagramBusinessPosts(ig, "cats", 10, false)
	if err != nil || len(posts) != 1 || jx.Str(posts[0], "image") != "https://cdn.example/cat.jpg" {
		t.Fatalf("Instagram: %#v, %v", posts, err)
	}
	if _, err := instagramBusinessPosts(ig, "dogs", 10, false); err == nil {
		t.Fatal("accepted another Instagram profile")
	}
	fb := []byte(`{"data":[{"id":"123456_77","message":"hello","created_time":"2026-10-31T12:00:00+0000","permalink_url":"https://www.facebook.com/123456/posts/77","full_picture":"https://cdn.example/77.jpg"},{"id":"987654_78","created_time":"2026-10-31T12:00:00+0000","permalink_url":"https://www.facebook.com/987654/posts/78","full_picture":"https://cdn.example/78.jpg"}]}`)
	posts, err = facebookPagePosts(fb, "123456", 10, false)
	if err != nil || len(posts) != 1 {
		t.Fatalf("Facebook: %#v, %v", posts, err)
	}
	page := `<a href="https://www.instagram.com/cats/">cat</a><a href="https://www.instagram.com/p/abc/">post</a><a href="https://instagram.com.evil.example/cat/">bad</a>`
	if got := instagramSearchCandidates(page); len(got) != 1 || got[0].ID != "https://www.instagram.com/cats/" {
		t.Fatalf("profiles: %#v", got)
	}
}

func TestDzenSources(t *testing.T) {
	id := "691017fdbee3845d70b9a02b"
	page := `<a href="https://dzen.ru/id/` + id + `">канал</a><a href="https://dzen.ru.evil.example/id/` + id + `">wrong</a>`
	candidates := dzenSearchCandidates(page)
	if len(candidates) != 1 || dzenChannelID(candidates[0].ID) != id {
		t.Fatalf("candidates: %#v", candidates)
	}
	data := []byte(`{"items":[{"publisher_id":"` + id + `","title":"Заголовок","text":"` + strings.Repeat("текст ", 25) + `","link":"https://dzen.ru/a/abc","creation_time":"14 часов назад","domain_title":"Канал"},{"publisher_id":"other","link":"https://dzen.ru/a/other"}]}`)
	at := time.Unix(1793400000, 0)
	posts, err := dzenChannelPosts(data, id, 10, true, at)
	if err != nil || len(posts) != 1 || jx.Int(posts[0], "posted_at") != at.Add(-14*time.Hour).Unix() {
		t.Fatalf("posts: %#v, %v", posts, err)
	}
}

func TestSpecialSourcesCanBeAddedByLink(t *testing.T) {
	for _, raw := range []string{"https://x.com/NASA", "https://www.threads.net/@cats", "https://www.instagram.com/cats/", "https://www.facebook.com/123456", "https://dzen.ru/id/691017fdbee3845d70b9a02b", "https://discord.com/channels/654321/123456"} {
		if feed, err := (&App{}).RSSKnownFeed(raw); err != nil || feed == "" {
			t.Errorf("%s: feed %q, error %v", raw, feed, err)
		}
	}
}

func TestBraveAPISearchResults(t *testing.T) {
	data := []byte(`{"web":{"results":[{"url":"https://x.com/NASA?src=search&amp=1"},{"url":"javascript:alert(1)"},{"url":"https://www.instagram.com/cats/"}]}}`)
	page, err := braveAPISearchHTML(data)
	if err != nil {
		t.Fatal(err)
	}
	if got := xSearchCandidates(page); len(got) != 1 || got[0].ID != "https://x.com/nasa" {
		t.Fatalf("X: %#v", got)
	}
	if got := instagramSearchCandidates(page); len(got) != 1 || got[0].ID != "https://www.instagram.com/cats/" {
		t.Fatalf("Instagram: %#v", got)
	}
	if strings.Contains(page, "javascript:") {
		t.Fatal("accepted unsafe URL")
	}
}
