package posta

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
)

func TestAutopilotYouTubeSafeStream(t *testing.T) {
	id := "Abcdefgh123"
	page := func(url, length, status, videoID string) []byte {
		return []byte(fmt.Sprintf(`var ytInitialPlayerResponse = {"playabilityStatus":{"status":%q},"videoDetails":{"videoId":%q,"lengthSeconds":"120","isLiveContent":false},"streamingData":{"formats":[{"url":%q,"mimeType":"video/mp4; codecs=avc1","audioQuality":"AUDIO_QUALITY_LOW","contentLength":%q}]}};`, status, videoID, url, length))
	}
	stream := "https://r1.googlevideo.com/videoplayback?signature=ephemeral"
	if got, err := autopilotYouTubeStream(page(stream, "1000", "OK", id), id); err != nil || got != stream {
		t.Fatalf("stream rejected: %v", err)
	}
	for _, bad := range [][]byte{
		page("https://googlevideo.com.evil.test/v", "1000", "OK", id),
		page(stream, "999999999", "OK", id),
		page(stream, "1000", "LOGIN_REQUIRED", id),
		page(stream, "1000", "OK", "otherID1234"),
		[]byte(strings.Replace(string(page(stream, "1000", "OK", id)), `,"streamingData"`, `,"microformat":{"playerMicroformatRenderer":{"isFamilySafe":false}},"streamingData"`, 1)),
		[]byte(`ytInitialPlayerResponse = {"broken":`),
	} {
		if _, err := autopilotYouTubeStream(bad, id); err == nil {
			t.Fatal("invalid or unavailable stream accepted")
		}
	}
	if autopilotYouTubeVideoID("https://www.youtube.com/watch?v="+id) != id || autopilotYouTubeVideoID("https://www.youtube.com.evil.test/watch?v="+id) != "" || autopilotYouTubeVideoID("https://www.youtube.com/watch?v="+id+"&token=secret") != "" {
		t.Fatal("watch URL validation failed")
	}
}

func TestAutopilotYouTubeBundledSolverLiveFixture(t *testing.T) {
	pagePath := os.Getenv("POSTA_TEST_YOUTUBE_PAGE")
	playerPath := os.Getenv("POSTA_TEST_YOUTUBE_PLAYER")
	if pagePath == "" || playerPath == "" {
		t.Skip("set public YouTube page and player fixture paths")
	}
	page, err := os.ReadFile(pagePath)
	if err != nil {
		t.Fatal(err)
	}
	player, err := os.ReadFile(playerPath)
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	a := &App{Cfg: config.Server{Root: root}}
	calls := 0
	stream, err := resolveAutopilotYouTubeStream(page, "dQw4w9WgXcQ", func(raw string, opts FetchOpts) (FetchResult, error) {
		calls++
		if !strings.HasPrefix(raw, "https://www.youtube.com/s/player/") || opts.MaxBytes != 5<<20 {
			t.Fatal("unexpected player request")
		}
		return FetchResult{OK: true, Bytes: player}, nil
	}, func(player []byte, requests []youtubeChallenge) ([]map[string]string, error) {
		return a.solveAutopilotYouTube(context.Background(), player, requests)
	})
	if err != nil || calls != 1 || !autopilotYouTubeStreamHost(stream) {
		t.Fatalf("solver err=%v player requests=%d", err, calls)
	}
}

// Optional public smoke: no user cookies, model calls, catalog writes or
// publications. The gate counts each watch, player and media request.
func TestAutopilotYouTubeLiveDownload(t *testing.T) {
	if os.Getenv("POSTA_TEST_YOUTUBE_LIVE") != "1" {
		t.Skip("set POSTA_TEST_YOUTUBE_LIVE=1 for a public network smoke")
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
	if err = a.User(ctx, "", nil).autopilotDownloadYouTube("https://www.youtube.com/watch?v=dQw4w9WgXcQ", path); err != nil {
		t.Fatalf("public download failed after %d requests: %v", calls, err)
	}
	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 || info.Size() > publicClassificationVideoMaxBytes {
		t.Fatalf("download size invalid: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil || (http.DetectContentType(data) != "video/mp4" && http.DetectContentType(data) != "video/webm") {
		t.Fatalf("download was not a supported video: %v", err)
	}
	t.Logf("public video bytes=%d, budgeted requests=%d", info.Size(), calls)
}

func TestAutopilotYouTubeUnavailableReleasesTrialPostgres(t *testing.T) {
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
	const owner, topic = "autopilot-youtube-unavailable", "Тема недоступного видео"
	if _, err = d.Pool.Exec(ctx, `INSERT INTO users(id,created_at) VALUES($1,0) ON CONFLICT DO NOTHING`, owner); err != nil {
		t.Fatal(err)
	}
	defer d.Pool.Exec(context.Background(), `DELETE FROM users WHERE id=$1`, owner)
	if err = saveAutopilotDemand(ctx, d.Pool, owner, autopilotDemandSettings{Topic: topic, Mode: "auto", PostsPerDay: 5, HorizonDays: 3}, time.Now().Unix()); err != nil {
		t.Fatal(err)
	}
	key := autopilotTopicKey(topic)
	for _, table := range []string{"source_autopilot_collection_passes", "source_autopilot_decisions", "source_autopilot_sources", "source_autopilot_jobs"} {
		defer d.Pool.Exec(context.Background(), `DELETE FROM `+table+` WHERE scope='public' AND topic_key=$1`, key)
	}
	a := &App{DB: d}
	if err = a.LoadCatalog(ctx); err != nil {
		t.Fatal(err)
	}
	if err = a.reconcileAutopilotMode(ctx, true, "auto", autopilotAutoScope, autopilotAutoKind); err != nil {
		t.Fatal(err)
	}
	job, err := claimAutopilotJobKind(ctx, d.Pool, time.Now().Add(time.Second), time.Minute, autopilotAutoKind)
	if err != nil || job == nil || job.TopicKey != key {
		t.Fatalf("claim=%v err=%v", job, err)
	}
	job.MaxProbation = 1
	feed := YouTubeChannelFeedURL("UCabcdefghijklmnopqrstuv")
	source := SourceIDFor(PlatformRSS, feed)
	if err = a.autopilotShadowSearchWith(ctx, job, func(context.Context) ([]DiscoveryCandidate, error) {
		return []DiscoveryCandidate{{Platform: PlatformRSS, ID: feed}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	var items strings.Builder
	for i := 0; i < 3; i++ {
		fmt.Fprintf(&items, `<item><title>Video %d</title><link>https://example.org/video/%d</link><pubDate>%s</pubDate><description>Test video</description></item>`, i, i, time.Now().UTC().Format(time.RFC1123Z))
	}
	body := []byte(`<rss><channel><title>Test</title>` + items.String() + `</channel></rss>`)
	policy := sourcePolicy{MinEvaluated: 10, MinPasses: 2, MinFreshUnique: 3, PromoteRate: .85, DemoteRate: .5, MaxAdvertisingRate: .2, MaxDuplicateRate: .5}
	if err = a.autopilotReviewShadowCandidatesWith(ctx, job, 1, 85, func(string) FetchResultURL {
		return FetchResultURL{URL: feed, FetchResult: FetchResult{OK: true, Bytes: body}}
	}, func(jx.M, []jx.M, string) *sourceEvaluation { return &sourceEvaluation{Score: 90} }, func(context.Context, string, []jx.M) error { return errAutopilotYouTubeStreamUnavailable }, policy); err != nil {
		t.Fatal(err)
	}
	var state, resume string
	if err = d.Pool.QueryRow(ctx, `SELECT state,resume_state FROM source_autopilot_sources WHERE scope='public' AND topic_key=$1 AND source_id=$2`, key, source).Scan(&state, &resume); err != nil || state != "cooldown" || resume != "candidate" {
		t.Fatalf("state=%s resume=%s err=%v", state, resume, err)
	}
}

func TestAutopilotYouTubeDownloadChecksEveryHop(t *testing.T) {
	const id = "Abcdefgh123"
	stream := "https://r1.googlevideo.com/videoplayback?signature=ephemeral"
	page := []byte(fmt.Sprintf(`ytInitialPlayerResponse = {"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":%q,"lengthSeconds":"120"},"streamingData":{"formats":[{"url":%q,"mimeType":"video/mp4","audioQuality":"low","contentLength":"1000"}]}};`, id, stream))
	path := t.TempDir() + "/video"
	calls := 0
	err := autopilotDownloadYouTubeWith("https://www.youtube.com/watch?v="+id, path, func(raw string, opts FetchOpts) (FetchResult, error) {
		calls++
		switch calls {
		case 1:
			if raw != "https://www.youtube.com/watch?v="+id || opts.MaxBytes != 5<<20 {
				t.Fatal("watch request not bounded")
			}
			return FetchResult{OK: true, Bytes: page}, nil
		case 2:
			if raw != stream || opts.Dest != path || opts.MaxBytes != publicClassificationVideoMaxBytes {
				t.Fatal("stream request not bounded")
			}
			return FetchResult{Status: 302, Location: "https://r2.googlevideo.com/next?sig=temporary"}, nil
		case 3:
			if !autopilotYouTubeStreamHost(raw) {
				t.Fatal("invalid redirect host")
			}
			if err := os.WriteFile(opts.Dest, []byte("video"), 0600); err != nil {
				return FetchResult{}, err
			}
			return FetchResult{OK: true}, nil
		default:
			t.Fatal("too many fetches")
			return FetchResult{}, nil
		}
	})
	if err != nil || calls != 3 {
		t.Fatalf("download error=%v calls=%d", err, calls)
	}
	contents, err := os.ReadFile(path)
	if err != nil || string(contents) != "video" {
		t.Fatalf("file error=%v", err)
	}
	calls = 0
	err = autopilotDownloadYouTubeWith("https://www.youtube.com/watch?v="+id, path, func(_ string, _ FetchOpts) (FetchResult, error) {
		calls++
		if calls == 1 {
			return FetchResult{OK: true, Bytes: page}, nil
		}
		return FetchResult{Status: 302, Location: "https://evil.test/steal"}, nil
	})
	if err == nil || calls != 2 {
		t.Fatalf("foreign redirect accepted: err=%v calls=%d", err, calls)
	}
}

func TestAutopilotYouTubeCipherUsesBudgetedPlayerFetch(t *testing.T) {
	const id = "Abcdefgh123"
	values := url.Values{"url": {"https://r1.googlevideo.com/videoplayback?n=challenge"}, "s": {"signed-challenge"}, "sp": {"sig"}}
	page := []byte(fmt.Sprintf(`{"jsUrl":"/s/player/abcd/player_ias.vflset/en_US/base.js"}; var ytInitialPlayerResponse = {"playabilityStatus":{"status":"OK"},"videoDetails":{"videoId":%q,"lengthSeconds":"120"},"streamingData":{"formats":[{"signatureCipher":%q,"mimeType":"video/mp4","audioQuality":"low"}]}};`, id, values.Encode()))
	calls := 0
	path := filepath.Join(t.TempDir(), "video")
	err := autopilotDownloadYouTubeWithSolver("https://www.youtube.com/watch?v="+id, path, func(raw string, opts FetchOpts) (FetchResult, error) {
		calls++
		switch calls {
		case 1:
			return FetchResult{OK: true, Bytes: page}, nil
		case 2:
			if raw != "https://www.youtube.com/s/player/abcd/player_ias.vflset/en_US/base.js" || opts.MaxBytes != 5<<20 {
				t.Fatal("player fetch was not bounded")
			}
			return FetchResult{OK: true, Bytes: []byte("player")}, nil
		case 3:
			u, err := url.Parse(raw)
			if err != nil || !autopilotYouTubeStreamHost(raw) || u.Query().Get("sig") != "signature-result" || u.Query().Get("n") != "n-result" || opts.Dest != path || opts.MaxBytes != publicClassificationVideoMaxBytes {
				t.Fatal("resolved stream request was not bounded")
			}
			return FetchResult{OK: true}, nil
		default:
			t.Fatal("unexpected fetch")
			return FetchResult{}, nil
		}
	}, func(player []byte, requests []youtubeChallenge) ([]map[string]string, error) {
		if string(player) != "player" || len(requests) != 2 || requests[0].Type != "sig" || requests[1].Type != "n" {
			t.Fatal("unexpected solver input")
		}
		return []map[string]string{{"signed-challenge": "signature-result"}, {"challenge": "n-result"}}, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("error=%v requests=%d", err, calls)
	}
}

func TestAutopilotYouTubeDownloadRequiresBudget(t *testing.T) {
	ctx := contextWithAutopilotLease(context.Background(), &autopilotJob{Scope: autopilotAutoScope})
	calls := 0
	ctx = context.WithValue(ctx, autopilotNetworkGateKey{}, func(context.Context) error { calls++; return errors.New("budget exhausted") })
	u := (&App{}).User(ctx, "", nil)
	err := u.autopilotDownloadYouTube("https://www.youtube.com/watch?v=Abcdefgh123", t.TempDir()+"/video")
	if err == nil || !strings.Contains(err.Error(), "budget exhausted") || calls != 1 {
		t.Fatalf("err=%v calls=%d", err, calls)
	}
}
