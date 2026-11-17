package posta

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"posta/internal/jx"
	"posta/internal/llm"
)

func TestPublicClassificationInput(t *testing.T) {
	for _, tc := range []struct {
		name string
		post jx.M
		want bool
	}{
		{"image without caption", jx.M{"image": "https://example.com/post.jpg"}, true},
		{"short caption without image", jx.M{"text": "кот"}, false},
		{"meaningful short title", jx.M{"text": "Как оформить налоговый вычет"}, true},
		{"long caption", jx.M{"text": "Подробное описание события, которого достаточно для определения темы записи."}, true},
		{"sensitive image", jx.M{"image": "https://example.com/post.jpg", "source_sensitive": true}, false},
		{"adult image", jx.M{"image": "https://example.com/post.jpg", "nsfw": true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, got := publicClassificationInput(tc.post)
			if got != tc.want {
				t.Fatalf("eligible=%v, want %v", got, tc.want)
			}
		})
	}
}

// Opt-in: fixture is a snapshot of an actual public Telegram post.
func TestPublicClassificationRealPost(t *testing.T) {
	if os.Getenv("POSTA_TEST_OLLAMA") == "" {
		t.Skip("POSTA_TEST_OLLAMA not set")
	}
	raw, err := os.ReadFile("../../tools/fixtures/source_preview_tg_text.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Discovered struct {
			Posts []struct {
				Text string `json:"text"`
			} `json:"posts"`
		} `json:"discovered"`
	}
	if err = json.Unmarshal(raw, &fixture); err != nil || len(fixture.Discovered.Posts) < 2 {
		t.Fatalf("fixture: %v", err)
	}
	var topics []*Topic
	for _, branch := range TopicTree {
		topics = append(topics, &Topic{Key: branch.Key, Title: branch.Title, About: branch.About, Aliases: branch.Aliases})
		for _, leaf := range branch.Children {
			topics = append(topics, &Topic{Key: leaf.Key, Title: leaf.Title, About: leaf.About, Aliases: leaf.Aliases})
		}
	}
	router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:11434"), llm.NewOpenRouter("", "", "", ""))
	router.LoadSettings = func() llm.Settings {
		s := llm.Preset("local")
		s.Roles[llm.RoleEmbed] = llm.RoleConfig{Primary: llm.Target{Provider: "ollama", Model: "bge-m3:latest"}}
		s.Roles[llm.RoleText] = llm.RoleConfig{Primary: llm.Target{Provider: "ollama", Model: "qwen3-vl:2b-instruct"}}
		s.Roles[llm.RoleClassify] = llm.RoleConfig{Primary: llm.Target{Provider: "ollama", Model: "qwen3-vl:2b-instruct"}}
		return s
	}
	a := &App{LLM: router, catalog: &Catalog{idx: buildTopicIndex(topics)}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	post := fixture.Discovered.Posts[1].Text
	u := a.User(ctx, "", nil)
	tags := u.classifyShortlist(post, "")
	t.Logf("tags: %v; reason: %s; error: %v", tags, u.classificationReason, u.classificationErr)
	found := false
	for _, tag := range tags {
		if tag == "news.politics" || tag == "news.world" || tag == "news" {
			found = true
		}
	}
	if !found {
		t.Fatal("expected a news or politics label for the post")
	}
}
