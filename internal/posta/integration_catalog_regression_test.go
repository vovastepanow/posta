package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestPublishingCatalogCompetitorScope(t *testing.T) {
	a := &App{}
	catalog := a.Networks()
	for _, platform := range []string{"vk_clips", "whatsapp", "likee", "yappy"} {
		if catalog[platform] != nil {
			t.Errorf("unexpected publishing destination: %s", platform)
		}
	}
	expected := []string{"telegram", "vk", "ok", "max", "youtube", "rutube", "tiktok", "dzen", "instagram", "facebook", "threads", "x", "pinterest", "tumblr", "linkedin", "viber", "reddit", "discord", "bluesky", "mastodon", "tenchat", "medium", "bitrix", "wordpress", "google_business"}
	if len(catalog) != len(expected) {
		t.Errorf("catalog has %d entries, want %d", len(catalog), len(expected))
	}
	for _, platform := range expected {
		if catalog[platform] == nil {
			t.Errorf("missing publishing destination: %s", platform)
		}
	}
}

func TestTelegramPlayableVideoWithoutPoster(t *testing.T) {
	page := `<div data-post="testchannel/123"><a class="tgme_widget_message_video_player"><video src="https://cdn.example.com/video.mp4"></video></a></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 120)
	if len(posts) != 1 || jx.Str(posts[0], "video") != "https://cdn.example.com/video.mp4" || jx.Bool(posts[0], "video_unavailable") || jx.Bool(posts[0], "text_post") {
		t.Fatalf("playable coverless video lost: %v", posts)
	}
}

func TestTelegramPhotoFirstAlbumWithPlayableVideo(t *testing.T) {
	page := `<div data-post="testchannel/124"><a class="tgme_widget_message_photo_wrap" style="background-image:url(https://cdn.example.com/photo.jpg)"></a><a class="tgme_widget_message_video_player"><video src="https://cdn.example.com/video.mp4"></video></a></div>`
	posts := TelegramPostsFromHTML(page, jx.M{"id": "testchannel"}, 10, false, 120)
	if len(posts) != 1 || jx.Bool(posts[0], "video_unavailable") || !PostItemHasVideo(posts[0]) {
		t.Fatalf("mixed album incorrectly marked unavailable: %v", posts)
	}
	gallery := jx.Maps(posts[0], "gallery")
	if len(gallery) != 2 || jx.Str(gallery[0], "image") != "https://cdn.example.com/photo.jpg" || jx.Str(gallery[1], "video") != "https://cdn.example.com/video.mp4" {
		t.Fatalf("media lost or reordered: %v", gallery)
	}
}

func TestPublishingConnectionAvailability(t *testing.T) {
	a := &App{}
	catalog := a.Networks()
	for _, key := range []string{"telegram", "vk", "max"} {
		if !catalog[key].PublishingConnectionAvailable() {
			t.Errorf("expected connectable: %s", key)
		}
	}
	for _, key := range []string{"facebook", "discord", "mastodon", "bluesky", "wordpress", "reddit", "tumblr", "linkedin", "viber", "dzen", "instagram", "threads", "tiktok", "rutube", "youtube", "pinterest", "x", "ok", "tenchat", "medium", "bitrix", "google_business"} {
		if catalog[key].PublishingConnectionAvailable() {
			t.Errorf("unexpected new connection: %s", key)
		}
	}
	if catalog["discord"].Publish == nil || catalog["mastodon"].Publish == nil {
		t.Fatal("existing publishing adapters must remain intact")
	}
	if a.NetworksPublic()["discord"] == nil {
		t.Fatal("existing resource metadata must remain available")
	}
	vk := *catalog["vk"]
	vk.Status = "needs_keys"
	if vk.PublishingConnectionAvailable() {
		t.Fatal("unconfigured adapter exposed")
	}
}

func TestPublishingScopeContainsOnlySelectedImplementedAdapters(t *testing.T) {
	want := []string{"telegram", "vk", "ok", "max", "youtube", "x", "pinterest", "tumblr"}
	a := &App{}
	catalog := a.Networks()
	for _, key := range want {
		if catalog[key] == nil || !publishingInScope(key) {
			t.Errorf("missing SmmBox platform: %s", key)
		}
	}
	public := a.NetworksPublicFor(true)
	for key := range catalog {
		if (public[key] != nil) != contains(want, key) {
			t.Errorf("unexpected public catalog entry: %s", key)
		}
		if publishingInScope(key) != contains(want, key) {
			t.Errorf("unexpected scope: %s", key)
		}
	}
}

func TestNetworkCatalogCopiesAndCurrentKeys(t *testing.T) {
	a := &App{}
	a.Cfg.NetworkKeys = map[string]string{}
	first, err := a.NetworkFor("youtube")
	if err != nil || first.Status != "needs_keys" {
		t.Fatalf("unconfigured YouTube: %v, %v", first, err)
	}
	first.Kinds[0] = "changed"
	first.MaxBytes["video"] = 1
	first.AppKeys[0] = "changed"
	first.OAuth.PKCE = !first.OAuth.PKCE
	a.Cfg.NetworkKeys["YOUTUBE_CLIENT_ID"] = "test"
	a.Cfg.NetworkKeys["YOUTUBE_CLIENT_SECRET"] = "test"
	second, err := a.NetworkFor("youtube")
	if err != nil || second.Status != "beta" || second.Kinds[0] != "video" ||
		second.MaxBytes["video"] != 200_000_000 || second.AppKeys[0] != "YOUTUBE_CLIENT_ID" ||
		second.OAuth.PKCE == first.OAuth.PKCE {
		t.Fatalf("catalog leaked mutations or stale configuration: %v, %v", second, err)
	}
	if !jx.Bool(jx.Map(a.NetworksPublicFor(true), "youtube"), "connect_available") ||
		jx.Bool(jx.Map(a.NetworksPublicFor(false), "youtube"), "connect_available") {
		t.Fatal("beta adapter availability must depend on owner rights")
	}
}
