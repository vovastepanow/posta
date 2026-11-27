package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestSourceEmbeddingUsesFreeNemotronOnly(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		calls++
		if req.URL.Path != "/embeddings" {
			t.Errorf("unexpected endpoint: %s", req.URL.Path)
		}
		var payload map[string]any
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(400)
			return
		}
		if payload["model"] != "nvidia/nemotron-3.5-embed-1b:free" {
			t.Error("not using free embedding model")
		}
		v := make([]float64, 2048)
		v[0] = 1
		json.NewEncoder(w).Encode(map[string]any{"data": []any{
			map[string]any{"index": 1, "embedding": v},
			map[string]any{"index": 0, "embedding": v},
		}})
	}))
	defer server.Close()
	r := &Router{OpenRouter: NewOpenRouter(server.URL, "test", "", ""), LoadSettings: func() Settings {
		return Preset("free")
	}}
	target := r.SourceEmbeddingTarget()
	if !target.Free() {
		t.Fatal("paid source embedding")
	}
	out, err := r.EmbedSource(context.Background(), target, []string{"first", "second"})
	if err != nil || len(out) != 2 || len(out[0]) != 512 || calls != 1 {
		t.Fatalf("embedding batch: %v, %d calls", err, calls)
	}
	if _, err := r.EmbedSource(context.Background(), Target{"openrouter", "openai/text-embedding-3-small"}, []string{"test"}); err == nil || calls != 1 {
		t.Fatal("paid model fallback allowed")
	}
}
