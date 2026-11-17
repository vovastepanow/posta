package posta

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"posta/internal/jx"
	"posta/internal/llm"
	"strings"
	"testing"
)

func TestCollectorVideoClassificationDoesNotKeepStoryboardLabels(t *testing.T) {
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	result := jx.M{"tags": []string{"business.marketing"}}
	u.applyVideoClassification(result, jx.M{"video": ""}, "", "подпись", false)
	if !jx.Bool(result, "tags_classification_pending") || len(jx.ToStrs(result["tags"])) != 0 {
		t.Fatalf("unavailable video must not keep storyboard labels: %v", result)
	}
	result = jx.M{"tags": []string{"adult", "business.marketing"}}
	u.applyVideoClassification(result, jx.M{"video": "https://example.test/video.mp4"}, "", "", false)
	if !jx.Bool(result, "tags_classification_pending") || !jx.Equal(jx.ToStrs(result["tags"]), []string{"adult"}) {
		t.Fatalf("safety label must survive and block external classification: %v", result)
	}
}

func TestPublicVideoSendsWholeSilentFileAndRejectsAudio(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	dir := t.TempDir()
	silent := filepath.Join(dir, "silent.mp4")
	audio := filepath.Join(dir, "audio.mp4")
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1", "-c:v", "mpeg4", "-y", silent).CombinedOutput(); err != nil {
		t.Fatalf("create silent video: %s %v", out, err)
	}
	if out, err := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1", "-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-shortest", "-c:v", "mpeg4", "-c:a", "aac", "-y", audio).CombinedOutput(); err != nil {
		t.Fatalf("create audio video: %s %v", out, err)
	}
	actual, err := os.ReadFile(silent)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls++
		var req jx.M
		json.NewDecoder(r.Body).Decode(&req)
		messages := jx.Maps(req, "messages")
		parts := jx.Maps(messages[len(messages)-1], "content")
		prompt := jx.Str(parts[0], "text")
		answer := `{"sufficient":true,"selected":[{"topic":"Гаджеты","evidence":"умный домашний робот"}]}`
		if len(parts) > 1 {
			if jx.Str(parts[1], "type") != "video_url" {
				t.Error("video was not sent")
			}
			encoded := strings.TrimPrefix(jx.Str(jx.Map(parts[1], "video_url"), "url"), "data:video/mp4;base64,")
			decoded, e := base64.StdEncoding.DecodeString(encoded)
			if e != nil || string(decoded) != string(actual) {
				t.Error("not the complete original video")
			}
			answer = `{"description":"Показан умный домашний робот на колёсах.","sufficient":true,"is_meme":false,"safety":[]}`
		} else if strings.HasPrefix(prompt, "Проверь каждую") {
			answer = `{"verified":[{"tag":"tech.gadgets","matches":true,"basis":"subject","aspect":"Новинка умного устройства","evidence":"умный домашний робот"}]}`
		}
		json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": answer}}}})
	}))
	defer srv.Close()
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.Cfg.FFprobe = ffprobe
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	u.LLM.LoadSettings = func() llm.Settings { return llm.Preset("free") }
	tags, unsafe := u.classifyPublicVideoFile(silent, "Робот дома")
	if unsafe || !jx.Equal(tags, []string{"tech.gadgets"}) || calls != 3 {
		t.Fatalf("tags=%v unsafe=%v calls=%d reason=%s", tags, unsafe, calls, u.classificationReason)
	}
	tags, unsafe = u.classifyPublicVideoFile(audio, "Робот дома")
	if tags != nil || unsafe || calls != 3 || u.classificationReason != "video_audio_unavailable" {
		t.Fatalf("audio silently ignored: %v %v %d %s", tags, unsafe, calls, u.classificationReason)
	}
}

func TestPublicVideoUnavailableAndSensitiveNeverSend(t *testing.T) {
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	if tags, _ := u.classifyPublicItem(jx.M{"video": "https://example.test/x.mp4", "nsfw": true}, "hello", ""); tags != nil {
		t.Fatal("sensitive video classified")
	}
	if tags, unsafe := u.classifyPublicItem(jx.M{"video": "https://example.test/x.mp4", "tags": []string{"adult"}}, "hello", ""); tags != nil || !unsafe {
		t.Fatal("existing mandatory safety label not respected")
	}
	if tags, _ := u.classifyPublicItem(jx.M{"video_via": "ytdlp", "video_page": "https://example.test/x"}, "hello", ""); tags != nil || u.classificationReason != "video_unavailable" {
		t.Fatal("poster or caption accepted without video")
	}
}

func TestPublicAudioVideoUsesCompleteLocalTranscriptAndSilentVisual(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	ffprobe, err := exec.LookPath("ffprobe")
	if err != nil {
		t.Skip("ffprobe unavailable")
	}
	dir := t.TempDir()
	video := filepath.Join(dir, "audio.mp4")
	if out, e := exec.Command(ffmpeg, "-v", "error", "-f", "lavfi", "-i", "color=c=blue:s=32x32:d=1", "-f", "lavfi", "-i", "sine=frequency=1000:duration=1", "-shortest", "-c:v", "mpeg4", "-c:a", "aac", "-y", video).CombinedOutput(); e != nil {
		t.Fatalf("create media: %s %v", out, e)
	}
	modeldir := filepath.Join(dir, "data/local-asr-build")
	if err = os.MkdirAll(filepath.Join(modeldir, "whisper.cpp-1.5.5"), 0700); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
while [ "$#" -gt 0 ]; do
 if [ "$1" = "-of" ]; then shift; dest="$1"; fi
 shift
done
printf '%s' '{"transcription":[{"offsets":{"from":0,"to":1000},"text":"умный домашний робот", "tokens":[{"id":123,"p":0.9}]}]}' > "$dest.json"
`
	if err = os.WriteFile(filepath.Join(modeldir, "whisper.cpp-1.5.5/main"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(modeldir, "ggml-small-q5_1.bin"), []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			http.NotFound(w, r)
			return
		}
		calls++
		var req jx.M
		json.NewDecoder(r.Body).Decode(&req)
		messages := jx.Maps(req, "messages")
		parts := jx.Maps(messages[len(messages)-1], "content")
		prompt := jx.Str(parts[0], "text")
		answer := `{"sufficient":true,"selected":[{"topic":"Гаджеты","evidence":"умный домашний робот"}]}`
		if len(parts) > 1 {
			if !strings.Contains(prompt, "умный домашний робот") {
				t.Error("full local transcript not provided")
			}
			raw, e := base64.StdEncoding.DecodeString(strings.TrimPrefix(jx.Str(jx.Map(parts[1], "video_url"), "url"), "data:video/mp4;base64,"))
			if e != nil {
				t.Error(e)
			}
			visual := filepath.Join(dir, "sent.mp4")
			os.WriteFile(visual, raw, 0600)
			out, e := exec.Command(ffprobe, "-v", "error", "-select_streams", "a", "-show_entries", "stream=codec_type", "-of", "csv=p=0", visual).Output()
			if e != nil || len(strings.TrimSpace(string(out))) > 0 {
				t.Error("audio was sent to external provider")
			}
			answer = `{"description":"Показан умный домашний робот.","sufficient":true,"is_meme":false,"safety":[]}`
		} else if strings.HasPrefix(prompt, "Проверь каждую") {
			answer = `{"verified":[{"tag":"tech.gadgets","matches":true,"basis":"subject","aspect":"Новинка умного устройства","evidence":"умный домашний робот"}]}`
		}
		json.NewEncoder(w).Encode(jx.M{"choices": []jx.M{{"message": jx.M{"content": answer}}}})
	}))
	defer srv.Close()
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.Cfg.Root = dir
	u.Cfg.FFprobe = ffprobe
	u.Cfg.FFmpeg = ffmpeg
	u.LLM = llm.NewRouter(llm.NewOllama("http://127.0.0.1:1"), llm.NewOpenRouter(srv.URL, "test", "", ""))
	u.LLM.LoadSettings = func() llm.Settings { return llm.Preset("free") }
	tags, unsafe := u.classifyPublicVideoFile(video, "Робот дома")
	if unsafe || !jx.Equal(tags, []string{"tech.gadgets"}) || calls != 3 {
		t.Fatalf("tags=%v unsafe=%v calls=%d reason=%s", tags, unsafe, calls, u.classificationReason)
	}
}
