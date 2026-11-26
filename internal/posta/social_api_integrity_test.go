package posta

import (
	"testing"
	"time"

	"posta/internal/jx"
)

func TestSocialAPIErrorIsNotEmptyFeed(t *testing.T) {
	body := []byte(`{"error":{"message":"Unavailable"}}`)
	if _, err := threadsParse(body); err == nil {
		t.Fatal("Threads accepted error response")
	}
	if _, err := redditListingPosts(body, "cats", 10, true); err == nil {
		t.Fatal("Reddit accepted error response")
	}
	if _, err := facebookPagePosts(body, "12345", 10, true); err == nil {
		t.Fatal("Facebook accepted error response")
	}
	if _, err := dzenChannelPosts(body, "abc", 10, true, time.Now()); err == nil {
		t.Fatal("Dzen accepted error response")
	}
	if _, err := threadsParse([]byte(`{"data":[]}`)); err != nil {
		t.Fatal("valid empty Threads feed rejected", err)
	}
}

func TestSocialMissingVideoIsNotPhoto(t *testing.T) {
	threads := []byte(`{"data":[{"id":"1","username":"cats","media_type":"VIDEO","thumbnail_url":"https://cdn.example/cover.jpg","permalink":"https://www.threads.net/@cats/post/1","timestamp":"2026-11-01T10:00:00Z"}]}`)
	p, err := threadsProfilePosts(threads, "cats", 10, false)
	if err != nil || len(p) != 1 || !jx.Bool(p[0], "media_incomplete") || jx.Bool(p[0], "text_post") {
		t.Fatalf("Threads lost missing video state: %v %v", p, err)
	}
	ig := []byte(`{"business_discovery":{"username":"cats","media":{"data":[{"id":"1","media_type":"CAROUSEL_ALBUM","media_url":"https://cdn.example/cover.jpg","permalink":"https://www.instagram.com/p/ABC123/","timestamp":"2026-11-01T10:00:00Z","children":{"data":[{"media_type":"IMAGE","media_url":"https://cdn.example/a.jpg"},{"media_type":"VIDEO","thumbnail_url":"https://cdn.example/b.jpg"}]}}]}}}`)
	p, err = instagramBusinessPosts(ig, "cats", 10, false)
	if err != nil || len(p) != 1 || !jx.Bool(p[0], "media_incomplete") {
		t.Fatalf("Instagram lost missing carousel member: %v %v", p, err)
	}
}

func TestDzenStreamFallbackAndMissingVideo(t *testing.T) {
	body := []byte(`{"items":[{"publisher_id":"abc","title":"Video","link":"https://dzen.ru/a/123","publication_date":"1793534400","video":{"id":"not-a-url","streams":["https://vd123.okcdn.ru/video.m3u8?token=x"]}},{"publisher_id":"abc","title":"Missing video","link":"https://dzen.ru/a/456","publication_date":"1793534400","video":{"id":"not-a-url"}}]}`)
	p, err := dzenChannelPosts(body, "abc", 10, false, time.Now())
	if err != nil || len(p) != 2 || jx.Str(p[0], "video_via") != "ytdlp" || !jx.Bool(p[1], "media_incomplete") {
		t.Fatalf("Dzen video state: %v %v", p, err)
	}
}

func TestRedditPartialGalleryAndCommunityURL(t *testing.T) {
	if redditSourceName("https://www.reddit.com/r/Cats") != "cats" {
		t.Fatal("community URL without trailing slash rejected")
	}
	body := []byte(`{"data":{"children":[{"data":{"title":"Album","subreddit":"cats","permalink":"/r/cats/comments/abc/album/","created_utc":1793534400,"gallery_data":{"items":[{"media_id":"a"},{"media_id":"b"}]},"media_metadata":{"a":{"status":"valid","s":{"u":"https://i.redd.it/a.jpg"}}}}}]}}`)
	p, err := redditListingPosts(body, "cats", 10, false)
	if err != nil || len(p) != 1 || !jx.Bool(p[0], "media_incomplete") || jx.Bool(p[0], "text_post") {
		t.Fatalf("Reddit partial gallery: %v %v", p, err)
	}
}
