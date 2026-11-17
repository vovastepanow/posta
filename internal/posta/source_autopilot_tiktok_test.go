package posta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

func tiktokTestPage(stream string) []byte {
	return []byte(fmt.Sprintf(`<script id="__UNIVERSAL_DATA_FOR_REHYDRATION__" type="application/json">{"__DEFAULT_SCOPE__":{"webapp.video-detail":{"statusCode":0,"itemInfo":{"itemStruct":{"id":"6718335390845095173","desc":"cats","createTime":"1560000000","secret":false,"privateItem":false,"forFriend":false,"isProhibited":false,"isReviewing":false,"takeDown":0,"warnInfo":[],"author":{"uniqueId":"scout2015","privateAccount":false},"video":{"duration":10,"size":"1000","format":"mp4","playAddr":%q}}}}}}</script>`, stream))
}

func TestAutopilotTikTokPublicVideo(t *testing.T) {
	stream := "https://v16-webapp.tiktok.com/video?token=ephemeral"
	page := tiktokTestPage(stream)
	post, got, err := parseAutopilotTikTokPage(page, "scout2015", "6718335390845095173")
	if err != nil || got != stream || jx.Str(post, "video_page") != "https://www.tiktok.com/@scout2015/video/6718335390845095173" || strings.Contains(string(jx.Marshal(post)), "token=") {
		t.Fatalf("valid video: %v", err)
	}
	for _, bad := range [][]byte{
		tiktokTestPage("https://tiktok.com.evil.test/video"),
		[]byte(strings.Replace(string(page), `"privateItem":false`, `"privateItem":true`, 1)),
		[]byte(strings.Replace(string(page), `"secret":false,`, ``, 1)),
		[]byte(strings.Replace(string(page), `"takeDown":0`, `"takeDown":1`, 1)),
		[]byte(strings.Replace(string(page), `"size":"1000"`, `"size":"999999999"`, 1)),
		[]byte(strings.Replace(string(page), `"uniqueId":"scout2015"`, `"uniqueId":"other"`, 1)),
	} {
		if _, _, err := parseAutopilotTikTokPage(bad, "scout2015", "6718335390845095173"); err == nil {
			t.Fatal("unsafe video accepted")
		}
	}
	for _, bad := range []string{"https://www.tiktok.com.evil.test/@scout2015/video/6718335390845095173", "https://www.tiktok.com/@scout2015/video/6718335390845095173?token=x", "http://www.tiktok.com/@scout2015/video/6718335390845095173"} {
		if _, _, ok := autopilotTikTokVideoParts(bad); ok {
			t.Fatalf("accepted %s", bad)
		}
	}
	if id, ok := autopilotCandidateID(DiscoveryCandidate{Platform: PlatformTikTok, ID: "Scout2015"}); !ok || id != "tiktok:scout2015" {
		t.Fatalf("source id %q", id)
	}
}

func TestAutopilotTikTokLiveDownload(t *testing.T) {
	if os.Getenv("POSTA_TEST_TIKTOK_LIVE") != "1" {
		t.Skip("set POSTA_TEST_TIKTOK_LIVE=1")
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	base, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	ctx := contextWithAutopilotLease(base, &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error {
		calls++
		if calls > 8 {
			return errors.New("test request budget exhausted")
		}
		return nil
	})
	a := &App{Cfg: config.Server{Root: root}}
	path := filepath.Join(t.TempDir(), "video.mp4")
	err = a.User(ctx, "", nil).autopilotDownloadTikTok("https://www.tiktok.com/@scout2015/video/6718335390845095173", path)
	if err != nil {
		t.Fatalf("download failed after %d budgeted requests: %v", calls, err)
	}
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 || len(data) > publicClassificationVideoMaxBytes || http.DetectContentType(data) != "video/mp4" {
		t.Fatalf("invalid video: %v", err)
	}
	t.Logf("public video bytes=%d, budgeted requests=%d", len(data), calls)
}

func TestAutopilotTikTokDownloadUsesBudgetedRequests(t *testing.T) {
	pageURL := "https://www.tiktok.com/@scout2015/video/6718335390845095173"
	calls := 0
	pageFetch := func(raw string, limit int64, timeout time.Duration, ua string) FetchResultURL {
		calls++
		if raw != pageURL || limit != 3<<20 {
			t.Fatal("unexpected page request")
		}
		return FetchResultURL{URL: pageURL, FetchResult: FetchResult{OK: true, Bytes: tiktokTestPage("https://v16-webapp.tiktok.com/video?token=temporary")}}
	}
	fetch := func(raw string, opts FetchOpts) (FetchResult, error) {
		calls++
		if opts.MaxBytes != publicClassificationVideoMaxBytes || opts.Dest != "/tmp/test-video" {
			t.Fatal("request not bounded")
		}
		if calls == 2 {
			return FetchResult{Status: 302, Location: "https://cdn.tiktokcdn.com/video"}, nil
		}
		if raw != "https://cdn.tiktokcdn.com/video" {
			t.Fatal("bad redirect")
		}
		return FetchResult{OK: true, Type: "video/mp4"}, nil
	}
	if err := autopilotDownloadTikTokWith(pageURL, "/tmp/test-video", pageFetch, fetch); err != nil || calls != 3 {
		t.Fatalf("requests=%d err=%v", calls, err)
	}
	calls = 0
	evil := func(string, FetchOpts) (FetchResult, error) {
		calls++
		return FetchResult{Status: 302, Location: "https://evil.test/video"}, nil
	}
	if err := autopilotDownloadTikTokWith(pageURL, "/tmp/test-video", pageFetch, evil); err == nil || calls != 2 {
		t.Fatal("unsafe redirect accepted")
	}
}

func TestAutopilotTikTokAlternativeStreams(t *testing.T) {
	primary := "https://v16.tiktok.com/video"
	page := strings.Replace(string(tiktokTestPage(primary)), `"format":"mp4"`, `"format":"mp4","PlayAddrStruct":{"UrlList":["https://evil.test/video","https://v19.tiktok.com/video","https://v20.tiktok.com/video","https://v21.tiktok.com/video"]}`, 1)
	const pageURL = "https://www.tiktok.com/@scout2015/video/6718335390845095173"
	pageFetch := func(string, int64, time.Duration, string) FetchResultURL {
		return FetchResultURL{URL: pageURL, FetchResult: FetchResult{OK: true, Bytes: []byte(page)}}
	}
	var urls []string
	fetch := func(raw string, _ FetchOpts) (FetchResult, error) {
		urls = append(urls, raw)
		if len(urls) == 1 {
			return FetchResult{Status: 403}, nil
		}
		if len(urls) == 2 {
			return FetchResult{Status: 200, OK: true, Type: "text/html"}, nil
		}
		return FetchResult{Status: 200, OK: true, Type: "video/mp4"}, nil
	}
	if err := autopilotDownloadTikTokWith(pageURL, "/tmp/test-video", pageFetch, fetch); err != nil {
		t.Fatal(err)
	}
	if len(urls) != 3 || urls[1] != "https://v19.tiktok.com/video" || urls[2] != "https://v20.tiktok.com/video" {
		t.Fatalf("unexpected requests: %v", urls)
	}
	urls = nil
	rateLimited := func(raw string, _ FetchOpts) (FetchResult, error) {
		urls = append(urls, raw)
		return FetchResult{Status: 429}, nil
	}
	if err := autopilotDownloadTikTokWith(pageURL, "/tmp/test-video", pageFetch, rateLimited); err == nil || len(urls) != 1 {
		t.Fatal("rate limit should stop alternatives")
	}
}

func TestAutopilotTikTokUnavailableReason(t *testing.T) {
	if got := autopilotStreamUnavailableReason(fmt.Errorf("wrapped: %w", errAutopilotTikTokStreamUnavailable)); got != "tiktok_stream_unavailable" {
		t.Fatalf("TikTok reason: %q", got)
	}
	if got := autopilotStreamUnavailableReason(errAutopilotYouTubeStreamUnavailable); got != "youtube_stream_unavailable" {
		t.Fatalf("YouTube reason: %q", got)
	}
	if got := autopilotStreamUnavailableReason(errors.New("model unavailable")); got != "" {
		t.Fatalf("unexpected reason: %q", got)
	}
}

func TestAutopilotTikTokDownloadAddressCandidate(t *testing.T) {
	primary := "https://v16.tiktok.com/video"
	page := strings.Replace(string(tiktokTestPage(primary)), `"format":"mp4"`, `"format":"mp4","downloadAddr":"https://v19.tiktok.com/public","PlayAddrStruct":{"UrlList":["https://v20.tiktok.com/video"]}`, 1)
	streams := autopilotTikTokStreams([]byte(page), primary)
	if len(streams) != 3 || streams[1] != "https://v19.tiktok.com/public" {
		t.Fatalf("unexpected streams: %v", streams)
	}
	bad := strings.Replace(page, "https://v19.tiktok.com/public", "https://tiktok.com.evil.test/public", 1)
	streams = autopilotTikTokStreams([]byte(bad), primary)
	if len(streams) != 2 || streams[1] != "https://v20.tiktok.com/video" {
		t.Fatal("untrusted download address accepted")
	}
}

func TestAutopilotTikTokGenericBinaryVideoResponse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "media")
	mp4 := []byte{0, 0, 0, 24, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm', 0, 0, 0, 0, 'i', 's', 'o', 'm', 'm', 'p', '4', '2'}
	if err := os.WriteFile(path, mp4, 0600); err != nil {
		t.Fatal(err)
	}
	if !autopilotTikTokVideoResponse("application/octet-stream", path) {
		t.Fatal("valid generic MP4 rejected")
	}
	if err := os.WriteFile(path, []byte("<html>access denied</html>"), 0600); err != nil {
		t.Fatal(err)
	}
	if autopilotTikTokVideoResponse("application/octet-stream", path) || autopilotTikTokVideoResponse("text/html", path) {
		t.Fatal("HTML accepted")
	}
}
