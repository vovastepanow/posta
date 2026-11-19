package posta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestSourcePostsHaveImages(t *testing.T) {
	cases := []struct {
		name  string
		posts []jx.M
		want  bool
	}{
		{name: "empty feed", posts: nil, want: false},
		{name: "text only", posts: []jx.M{{"text": "useful text"}, {"text": "another post", "image": ""}}, want: false},
		{name: "media", posts: []jx.M{{"text": "first"}, {"image": "https://example.org/photo.jpg"}}, want: true},
		{name: "blank media ref", posts: []jx.M{{"image": "  "}}, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sourcePostsHaveImages(tc.posts); got != tc.want {
				t.Fatalf("sourcePostsHaveImages() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEvaluateTextOnlySourceWithoutImage(t *testing.T) {
	dsn := os.Getenv("POSTA_AUTOPILOT_TEST_DSN")
	if dsn == "" {
		t.Skip("requires an isolated PostgreSQL database for prompt storage")
	}
	database, err := db.Open(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	if _, err := database.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls++
		_ = json.NewEncoder(w).Encode(jx.M{"choices": []any{jx.M{"message": jx.M{"content": `{"score":92,"reason":"По теме"}`}}}})
	}))
	defer srv.Close()

	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.App.DB = database
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	u.LLM.LoadSettings = func() llm.Settings {
		s := llm.Preset("free")
		s.Roles[llm.RoleSmart] = llm.RoleConfig{Primary: llm.Target{Provider: "openrouter", Model: "example:free"}, Fallback: llm.Off}
		return s
	}
	posts := []jx.M{{"text": "Подробный практический разбор рекламной кампании с измеримыми результатами."}}
	ev := u.evaluateSource(jx.M{"title": "Практика маркетинга"}, posts, "Маркетинг и реклама")
	if ev == nil || ev.Score != 92 || ev.Text == nil || *ev.Text != 92 {
		t.Fatalf("text-only feed did not receive text model score: %+v", ev)
	}
	if calls != 1 {
		t.Fatalf("text-only source should use one text model call, got %d", calls)
	}
	// A mixed SMM publisher can advertise images even when the host
	// blocks their retrieval. A valid text assessment must not be erased
	// merely because the optional storyboard cannot be downloaded.
	withBlockedImage := []jx.M{{
		"text":  "Подробный разбор SMM-стратегии и измерения охвата аудитории.",
		"image": "http://127.0.0.1:1/unavailable.png",
	}}
	ev = u.evaluateSource(jx.M{"title": "Практика SMM"}, withBlockedImage, "SMM и блогинг")
	if ev == nil || ev.Score != 92 || ev.Text == nil || !ev.TextOnly {
		t.Fatalf("mixed source lost successful text score when an image is blocked: %+v", ev)
	}
	if calls != 2 {
		t.Fatalf("optional blocked image should not trigger extra AI attempts: %d", calls)
	}
	// A text-rich SMM candidate should be pre-screened with exactly one
	// text request, without trying the advertised (unavailable) images.
	// This protects the public-fetch budget without skipping post-level checks.
	var rich []jx.M
	for i := 0; i < 3; i++ {
		rich = append(rich, jx.M{
			"text":  "Подробный практический разбор SMM-стратегии и результатов рекламной кампании.",
			"image": "http://127.0.0.1:1/unavailable.png",
		})
	}
	ev = u.autopilotEvaluateSource(jx.M{"title": "Практика SMM"}, rich, "SMM и блогинг")
	if ev == nil || ev.Score != 92 || ev.Text == nil || !ev.TextOnly {
		t.Fatalf("text-first prescreen failed for image-advertising feed: %+v", ev)
	}
	if calls != 3 {
		t.Fatalf("text-rich autopilot prescreen should use one model request, got %d total", calls)
	}
}
