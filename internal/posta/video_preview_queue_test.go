package posta

import "testing"

func TestVideoPreviewQueueRequiresRealPageJob(t *testing.T) {
	page := "https://t.me/example/12"
	jobs := []VideoJob{{UID: "u", Key: VideoPreviewKey(page), Page: page}}
	if !videoPreviewQueued(jobs, page, "") || !videoPreviewQueued(jobs, page, "u") {
		t.Fatal("existing job not recognized")
	}
	if videoPreviewQueued(jobs, page, "other") || videoPreviewQueued(nil, page, "") || videoPreviewQueued(jobs, "https://t.me/example/13", "") {
		t.Fatal("phantom job recognized")
	}
}

func TestTelegramPreviewStateIsAccountScoped(t *testing.T) {
	page := "https://t.me/example/12"
	a, b := videoPreviewStateKey(page, "owner-a"), videoPreviewStateKey(page, "owner-b")
	if a == "" || b == "" || a == b || videoPreviewStateKey(page, "") != "" {
		t.Fatal("account-native preview shares state")
	}
	public := "https://www.youtube.com/shorts/abcdefghijk"
	if videoPreviewStateKey(public, "owner-a") != videoPreviewStateKey(public, "owner-b") {
		t.Fatal("public previews should keep their shared cache")
	}
}
