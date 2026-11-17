package posta

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"posta/internal/jx"
	"posta/internal/llm"
	"strings"
	"testing"
)

func TestMediaClassificationNeedsContentNotCaptionLength(t *testing.T) {
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 16, 16))); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, observation string
		want              bool
		unsafe            bool
	}{
		{"visible_content_without_caption", `{"description":"Написал программу на Python для обработки файлов","sufficient":true,"is_meme":false,"safety":[]}`, true, false},
		{"generic_video_cover", `{"description":"Мужчина стоит у стены","sufficient":false,"is_meme":false,"safety":[]}`, false, false},
		{"safety", `{"description":"","sufficient":false,"is_meme":false,"safety":["adult"]}`, false, true},
		{"malformed", `{}`, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selections := 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req jx.M
				json.NewDecoder(r.Body).Decode(&req)
				messages := jx.Maps(req, "messages")
				parts := jx.Maps(messages[len(messages)-1], "content")
				prompt := jx.Str(parts[0], "text")
				answer := tc.observation
				if len(parts) == 1 {
					selections++
					if strings.HasPrefix(prompt, "Проверь каждую") {
						answer = `{"verified":[{"tag":"tech.code","matches":true,"basis":"subject","aspect":"Публикация раскрывает действие из определения темы","evidence":"Написал программу на Python"}]}`
					} else {
						answer = `{"sufficient":true,"selected":[{"topic":"Программирование","evidence":"Написал программу на Python"}]}`
					}
				} else if strings.Contains(prompt, "tech.code") {
					t.Error("taxonomy leaked into observation")
				}
				json.NewEncoder(w).Encode(jx.M{"choices": []any{jx.M{"message": jx.M{"content": answer}}}, "usage": jx.M{"cost": 0.001}})
			}))
			defer srv.Close()
			router := llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
			router.LoadSettings = func() llm.Settings { return llm.Preset("free") }
			u := taxonomyTestUser()
			u.Ctx = context.Background()
			u.LLM = router
			tags, unsafe := u.classifyPublicImageBytes(img.Bytes(), "")
			if unsafe != tc.unsafe || (len(tags) > 0) != tc.want {
				t.Fatalf("tags=%v unsafe=%v", tags, unsafe)
			}
			if !tc.want && selections != 0 {
				t.Fatal("classified an insufficient or unsafe observation")
			}
		})
	}
}

func TestMediaObservationRequiresSafetyAndMemeStatus(t *testing.T) {
	for _, tc := range []struct {
		name  string
		data  jx.M
		valid bool
	}{
		{"complete", jx.M{"description": "мем", "sufficient": true, "is_meme": true, "safety": []any{}}, true},
		{"missing safety", jx.M{"description": "мем", "sufficient": true, "is_meme": true}, false},
		{"unknown safety", jx.M{"description": "мем", "sufficient": true, "is_meme": true, "safety": []any{"other"}}, false},
		{"missing meme", jx.M{"description": "мем", "sufficient": true, "safety": []any{}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, _, got := validPublicMediaObservation(tc.data)
			if got != tc.valid {
				t.Fatalf("valid=%v", got)
			}
		})
	}
}

func TestMemeWithoutMemeTopicRemainsUnresolved(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls++
		answer := `{"sufficient":true,"selected":[{"topic":"Кибербезопасность","evidence":"новый VPN обходит блокировки"}]}`
		if calls == 2 {
			answer = `{"verified":[{"tag":"tech.security","matches":true,"basis":"subject","aspect":"Обход блокировок","evidence":"новый VPN обходит блокировки"}]}`
		}
		json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": answer}}}})
	}))
	defer srv.Close()
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	u.LLM.LoadSettings = func() llm.Settings { return llm.Preset("free") }
	tags := u.classifyMediaDescription("Шутка: новый VPN обходит блокировки", "Когда вышел VPN", true)
	if tags != nil || u.classificationReason != "media_meme_unresolved" {
		t.Fatalf("meme lost: %v, reason=%s", tags, u.classificationReason)
	}
}
