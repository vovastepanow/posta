package posta

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"posta/internal/jx"
	"posta/internal/llm"
	"testing"
)

func TestRealAudioResponseReplayIndependentEvidence(t *testing.T) {
	b, e := os.ReadFile("../../tools/fixtures/classification_audio_real_replay.json")
	if e != nil {
		t.Fatal(e)
	}
	var cases []struct {
		Name, Text, Answer   string
		Candidates, Expected []string
	}
	if e = json.Unmarshal(b, &cases); e != nil {
		t.Fatal(e)
	}
	for _, c := range cases {
		t.Run(c.Name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/chat/completions" {
					http.NotFound(w, r)
					return
				}
				json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": c.Answer}}}})
			}))
			defer srv.Close()
			u := taxonomyTestUser()
			u.Ctx = context.Background()
			u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
			u.LLM.LoadSettings = func() llm.Settings { return llm.Preset("free") }
			var labels []contentLabel
			for _, p := range TopicTree {
				for _, l := range p.Children {
					if contains(c.Candidates, l.Key) {
						labels = append(labels, contentLabel{l.Key, l.Title, l.About + " " + l.Hint})
					}
				}
			}
			got := u.verifySelectedLabels(c.Text, labels, c.Candidates)
			if (got == nil) != (c.Expected == nil) || !jx.Equal(got, c.Expected) {
				t.Fatalf("got %v want %v reason=%s", got, c.Expected, u.classificationReason)
			}
		})
	}
}
