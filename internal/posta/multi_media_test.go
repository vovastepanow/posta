package posta

import (
	"os/exec"
	"path/filepath"
	"testing"

	"posta/internal/config"
	"posta/internal/imgx"
	"posta/internal/jx"
)

func requireAlbum(t *testing.T, posts []jx.M, want ...string) {
	t.Helper()
	if len(posts) != 1 {
		t.Fatalf("got %d posts", len(posts))
	}
	candidates := ExpandPostImages(posts)
	if len(candidates) != 1 {
		t.Fatalf("got %d candidates", len(candidates))
	}
	g := jx.Maps(candidates[0], "gallery")
	if len(g) != len(want) {
		t.Fatalf("gallery: got %#v, want %d items", g, len(want))
	}
	for i, image := range want {
		if jx.Str(g[i], "image") != image {
			t.Errorf("item %d: got %q, want %q", i, jx.Str(g[i], "image"), image)
		}
	}
}

func TestMastodonAlbumSurvivesExpansion(t *testing.T) {
	data := []byte(`[{"content":"post","created_at":"2026-11-01T12:00:00Z","url":"https://spacey.space/@a/1","account":{"id":"1","url":"https://spacey.space/@a"},"media_attachments":[{"type":"image","url":"https://example.com/1.jpg"},{"type":"image","url":"https://example.com/2.jpg"}]}]`)
	posts, err := MastodonParse(data)
	if err != nil {
		t.Fatal(err)
	}
	requireAlbum(t, posts, "https://example.com/1.jpg", "https://example.com/2.jpg")
}

func TestRSSAlbumSurvivesExpansion(t *testing.T) {
	data := []byte(`<rss><channel><item><title>Post</title><link>https://spacey.space/@a/1</link><enclosure url="https://example.com/1.jpg" type="image/jpeg"/><enclosure url="https://example.com/2.jpg" type="image/jpeg"/></item></channel></rss>`)
	posts, err := RSSParse(data, 10, "", "https://spacey.space/@a.rss", false, false, 120)
	if err != nil {
		t.Fatal(err)
	}
	requireAlbum(t, posts, "https://example.com/1.jpg", "https://example.com/2.jpg")
}

func TestDiscordAlbumSurvivesExpansion(t *testing.T) {
	data := []byte(`[{"id":"999999","channel_id":"123456","timestamp":"2026-11-01T12:00:00Z","attachments":[{"url":"https://example.com/1.jpg","content_type":"image/jpeg"},{"url":"https://example.com/2.jpg","content_type":"image/jpeg"}]}]`)
	posts, err := discordMessages(data, "12345", "123456", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	requireAlbum(t, posts, "https://example.com/1.jpg", "https://example.com/2.jpg")
}

func TestRedditGalleryOrder(t *testing.T) {
	data := []byte(`{"data":{"children":[{"data":{"title":"post","subreddit":"cats","permalink":"/r/cats/comments/1/post/","gallery_data":{"items":[{"media_id":"b"},{"media_id":"a"}]},"media_metadata":{"a":{"status":"valid","s":{"u":"https://example.com/1.jpg"}},"b":{"status":"valid","s":{"u":"https://example.com/2.jpg"}}}}}]}}`)
	posts, err := redditListingPosts(data, "cats", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	requireAlbum(t, posts, "https://example.com/2.jpg", "https://example.com/1.jpg")
}

func TestInstagramCarouselSurvivesExpansion(t *testing.T) {
	data := []byte(`{"business_discovery":{"username":"test","media":{"data":[{"id":"1","caption":"post","media_type":"CAROUSEL_ALBUM","permalink":"https://www.instagram.com/p/1/","timestamp":"2026-11-01T12:00:00Z","children":{"data":[{"media_type":"IMAGE","media_url":"https://example.com/1.jpg"},{"media_type":"IMAGE","media_url":"https://example.com/2.jpg"}]}}]}}}`)
	posts, err := instagramBusinessPosts(data, "test", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	requireAlbum(t, posts, "https://example.com/1.jpg", "https://example.com/2.jpg")
}

func TestRSSHTMLAlbumAndDuplicates(t *testing.T) {
	data := []byte(`<rss><channel><item><title>Post</title><link>https://example.com/post</link><description><![CDATA[<img src="/1.jpg"><img src="/2.jpg"><img src="/1.jpg">]]></description></item></channel></rss>`)
	posts, err := RSSParse(data, 10, "", "https://example.com/feed", false, false, 120)
	if err != nil {
		t.Fatal(err)
	}
	requireAlbum(t, posts, "https://example.com/1.jpg", "https://example.com/2.jpg")
}

func TestDiscordMixedMediaKeepsVideoWithoutPoster(t *testing.T) {
	data := []byte(`[{"id":"999999","channel_id":"123456","timestamp":"2026-11-01T12:00:00Z","attachments":[{"url":"https://example.com/clip.mp4","content_type":"video/mp4"},{"url":"https://example.com/1.jpg","content_type":"image/jpeg"}]}]`)
	posts, err := discordMessages(data, "12345", "123456", 10, false)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts=%v err=%v", posts, err)
	}
	candidates := ExpandPostImages(posts)
	if len(candidates) != 1 {
		t.Fatalf("candidate lost: %v", candidates)
	}
	gallery := ContentGallery(analysisRecord(jx.M{}, candidates[0], "test", ""))
	if len(gallery) != 2 || jx.Str(gallery[0], "video") != "https://example.com/clip.mp4" || jx.Str(gallery[1], "image") != "https://example.com/1.jpg" {
		t.Fatalf("mixed gallery lost or reordered: %v", gallery)
	}
}

func TestBestPostRetainsUncachedMedia(t *testing.T) {
	p := jx.M{"image": "https://example.com/1.jpg", "gallery": []jx.M{
		{"image": "https://example.com/1.jpg"}, {"video": "https://example.com/clip.mp4"}, {"image": "https://example.com/2.jpg"},
	}}
	filtered := bestPost("feed", p, nil, nil, nil, nil)
	g := bestPostGallery(filtered, []string{"https://example.com/1.jpg", "https://example.com/2.jpg"}, map[string]string{"https://example.com/1.jpg": "cache/1.jpg"})
	if len(g) != 3 || jx.Str(g[1], "video") == "" || jx.Str(g[2], "image") != "https://example.com/2.jpg" {
		t.Fatalf("best-post gallery lost attachments: %v", g)
	}
}

func TestVideoWithoutPosterReachesCandidateQueue(t *testing.T) {
	posts := []jx.M{{"video": "https://example.com/video.mp4", "image": "", "text_post": false}}
	candidates := ExpandPostImages(posts)
	if len(candidates) != 1 || !jx.Bool(candidates[0], "_video_without_poster") || jx.Str(candidates[0], "image") != jx.Str(posts[0], "video") {
		t.Fatalf("video without a poster was lost: %v", candidates)
	}
}

func TestVideoFrameCanServeAsPoster(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	path := filepath.Join(t.TempDir(), "video.mp4")
	cmd := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=64x64:r=10:d=2", "-c:v", "mpeg4", "-y", path)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create fixture: %v %s", err, output)
	}
	a := &App{Cfg: config.Server{FFmpeg: ffmpeg, FFprobe: ffprobe}}
	board := a.VideoStoryboard(path)
	if board == nil || len(board.Frames) == 0 || imgx.Validate(board.Frames[0]) != nil {
		t.Fatal("failed to obtain a valid image from video")
	}
}

func TestLemmyRSSOriginalAndPreviewAreOneImage(t *testing.T) {
	data := []byte(`<rss xmlns:media="http://search.yahoo.com/mrss/"><channel><item><title>Post</title><link>https://sh.itjust.works/post/1</link><description><![CDATA[<img src="https://other.example/pictrs/image/original.png">]]></description><enclosure url="https://other.example/pictrs/image/original.png" type="image/png"/><media:content medium="image" url="https://sh.itjust.works/pictrs/image/preview.png"/></item></channel></rss>`)
	posts, err := RSSParse(data, 10, "", "https://sh.itjust.works/feeds/c/test.xml", false, false, 120)
	if err != nil || len(posts) != 1 {
		t.Fatalf("posts=%v err=%v", posts, err)
	}
	images := jx.Strs(posts[0], "images")
	if len(images) != 1 || images[0] != "https://other.example/pictrs/image/original.png" {
		t.Fatalf("images=%v", images)
	}
}

func TestLemmyRSSDistinctImagesAreNotCollapsed(t *testing.T) {
	data := []byte(`<rss xmlns:media="http://search.yahoo.com/mrss/"><channel><item><title>Post</title><link>https://example.com/post/1</link><description><![CDATA[<img src="https://example.com/pictrs/image/other.png">]]></description><enclosure url="https://example.com/pictrs/image/original.png" type="image/png"/><media:content medium="image" url="https://example.com/pictrs/image/second.png"/></item></channel></rss>`)
	posts, err := RSSParse(data, 10, "", "https://example.com/feeds/c/test.xml", false, false, 120)
	if err != nil {
		t.Fatal(err)
	}
	requireAlbum(t, posts, "https://example.com/pictrs/image/original.png", "https://example.com/pictrs/image/second.png")
}
