package posta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestTextVerdictKeySeparatesScopeAndEvidence(t *testing.T) {
	key := textVerdictKey("", "full", "prompt", "text", "models")
	for _, other := range []string{
		textVerdictKey("owner:a", "full", "prompt", "text", "models"),
		textVerdictKey("", "caption", "prompt", "text", "models"),
		textVerdictKey("", "full", "new prompt", "text", "models"),
		textVerdictKey("", "full", "prompt", "other text", "models"),
		textVerdictKey("", "full", "prompt", "text", "other models"),
	} {
		if key == other {
			t.Fatal("different text verdict input reused cache")
		}
	}
}

func TestVisionCacheSeparatesPosterAndStoryboard(t *testing.T) {
	key := visionEvidenceKey("post", "prompt", "model", [][]byte{[]byte("poster")})
	for _, other := range []string{
		visionEvidenceKey("post", "prompt", "model", [][]byte{[]byte("different poster")}),
		visionEvidenceKey("post", "prompt", "model", [][]byte{[]byte("poster"), []byte("frame")}),
		visionEvidenceKey("post", "new prompt", "model", [][]byte{[]byte("poster")}),
		visionEvidenceKey("post", "prompt", "new model", [][]byte{[]byte("poster")}),
	} {
		if key == other {
			t.Fatal("changed visual input reused a verdict")
		}
	}
}

func TestCollectorTextCacheReusesPublicVerdictsPostgres(t *testing.T) {
	dsn := os.Getenv("POSTA_TEST_PG")
	if dsn == "" {
		t.Skip("POSTA_TEST_PG not set")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if _, err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		answer := `{"items":[{"index":0,"relevance":90,"advertising":false,"topic_match":true,"evidence":"Пример расчёта налогов","description":"Расчёт налогов","safety":[]}]}`
		json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": answer}}}})
	}))
	defer srv.Close()
	u := taxonomyTestUser()
	u.Ctx, u.DB, u.Mem = ctx, d, NewMemCache(100)
	u.ID = randHex(16)
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	u.LLM.LoadSettings = func() llm.Settings { return llm.Preset("free") }
	topic := "Бизнес"
	text := "Пример расчёта налогов " + randHex(16)
	h := EmptyHistory()
	key := u.collectorTextKey(text, topic, h, false, false, false)
	defer d.Pool.Exec(ctx, `DELETE FROM analysis_cache WHERE key=$1`, key)
	result := u.cachedCollectorTextBatch([]string{text, text}, topic, h, false, false, false)
	if len(result) != 2 || result[0] == nil || result[1] == nil || calls.Load() != 1 {
		t.Fatalf("identical texts not deduplicated: calls=%d results=%v", calls.Load(), result)
	}
	result[0]["relevance"] = 0
	if jx.Int(result[1], "relevance") != 90 {
		t.Fatal("result maps share mutable state")
	}
	u.ID = randHex(16)
	if got := u.cachedCollectorTextBatch([]string{text}, topic, h, false, false, false); got[0] == nil || jx.Int(got[0], "relevance") != 90 || calls.Load() != 1 {
		t.Fatalf("same public text was analyzed again: calls=%d result=%v", calls.Load(), got)
	}
	privateKey := u.collectorTextKey(text, topic, h, true, false, false)
	u.ID = randHex(16)
	if privateKey == u.collectorTextKey(text, topic, h, true, false, false) || privateKey == key {
		t.Fatal("private verdict is not owner scoped")
	}
	if got := u.readAnalysisCache([]string{privateKey}); got[privateKey] != nil {
		t.Fatal("private read reused public cache")
	}
}

func TestPublicEvidenceRequiresSamePostAndContent(t *testing.T) {
	item := jx.M{"source_key": "rss:https://example.org/feed", "url": "https://example.org/1", "text": "Public article", "text_post": true}
	if !publicEvidenceMatches(item, jx.Merge(item, jx.M{"views": 500})) {
		t.Fatal("engagement refresh invalidated identical evidence")
	}
	for _, stored := range []jx.M{
		jx.Merge(item, jx.M{"text": "Edited article"}),
		jx.Merge(item, jx.M{"url": "https://example.org/2"}),
		jx.Merge(item, jx.M{"source_key": "own:private"}),
		jx.Merge(item, jx.M{"source_sensitive": true}),
		jx.Merge(item, jx.M{"video": "https://example.org/1.mp4"}),
	} {
		if publicEvidenceMatches(item, stored) {
			t.Fatal("non-identical or sensitive catalog entry authorized shared cache")
		}
	}
}
