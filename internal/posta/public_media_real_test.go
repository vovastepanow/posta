package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"posta/internal/config"
	"posta/internal/llm"
	"testing"
	"time"
)

// Explicit opt-in. Calls only the configured free model on two known-public,
// previously saved originals. Does not write to the database or alter labels.
func TestPublicMediaRealProbe(t *testing.T) {
	dir := os.Getenv("POSTA_MEDIA_PROBE_DIR")
	if dir == "" {
		t.Skip("live probe not requested")
	}
	if _, e := os.Stat(filepath.Join(dir, "plan.json")); e == nil {
		t.Fatal("output directory already used")
	}
	if e := os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	save := func(name string, v any) {
		b, e := json.MarshalIndent(v, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, name), b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	cases := []string{"VPN image", "Amazon silent video"}
	if os.Getenv("POSTA_MEDIA_ONLY") == "image" {
		cases = []string{"VPN image"}
	}
	if os.Getenv("POSTA_MEDIA_ONLY") == "newimage" {
		cases = []string{"Nvidia image 3251770"}
	}
	if os.Getenv("POSTA_MEDIA_ONLY") == "audio" {
		cases = []string{"public audio video 17891"}
	}
	if os.Getenv("POSTA_MEDIA_ONLY") == "audio2" {
		cases = []string{"public audio video 17885"}
	}
	save("plan.json", map[string]any{"max_physical_calls": 10, "model": "nvidia/nemotron-3.5-super-vl:free", "paid_allowed": false, "cases": cases, "label_writes": false})
	cfg := config.LoadServer()
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	old := http.DefaultTransport
	probe := &classificationProbeTransport{base: old, dir: dir, limit: 10, cache: map[string][]byte{}, failedModels: map[string]bool{}}
	http.DefaultTransport = probe
	defer func() { http.DefaultTransport = old }()
	router := llm.NewRouter(llm.NewOllama(cfg.OllamaURL), llm.NewOpenRouter(cfg.OpenRouterURL, cfg.OpenRouterKey, cfg.OpenRouterTitle, cfg.PublicURL))
	router.LoadSettings = func() llm.Settings {
		s := llm.Preset("free")
		for _, r := range []llm.Role{llm.RoleAnalysis, llm.RoleClassify, llm.RoleClassifyVerify} {
			s.Roles[r] = llm.RoleConfig{Primary: llm.FreeVision, Fallback: llm.Off}
		}
		return s
	}
	u := taxonomyTestUser()
	u.Ctx = ctx
	u.Cfg = cfg
	u.LLM = router
	if os.Getenv("POSTA_MEDIA_ONLY") == "newimage" {
		img, e := os.ReadFile(filepath.Join(cfg.Root, "data/backups/classification-multimedia-20261113/new-image.image"))
		if e != nil {
			t.Fatal(e)
		}
		tags, unsafe := u.classifyPublicImageBytes(img, "Дженсен Хуанг покинул пост гендиректора Nvidia и вышел из совета директоров. Компания назначила временного главу и начала искать постоянного преемника.")
		save("results.json", []any{map[string]any{"case": "Nvidia image 3251770", "tags": tags, "unsafe": unsafe, "reason": u.classificationReason, "physical_calls": probe.calls}})
		save("summary.json", map[string]any{"physical_calls": probe.calls, "records_planned": 1, "records_attempted": 1, "paid_cost_usd": 0})
		fmt.Printf("Nvidia image: tags=%v unsafe=%v reason=%s calls=%d\n", tags, unsafe, u.classificationReason, probe.calls)
		return
	}
	if mode := os.Getenv("POSTA_MEDIA_ONLY"); mode == "audio" || mode == "audio2" {
		id, caption := "17891", "Главная ошибка при запуске сайта и рекламы"
		if mode == "audio2" {
			id, caption = "17885", "Потребительская модель поведения у разных поколений #digital #маркетинг #продажи"
		}
		path := filepath.Join(cfg.Root, "data/backups/classification-benchmark-v2-20261112", id, "video.mp4")
		tags, unsafe := u.classifyPublicVideoFile(path, caption)
		result := map[string]any{"case": "public video with audio", "tags": tags, "unsafe": unsafe, "reason": u.classificationReason, "physical_calls": probe.calls}
		save("results.json", []any{result})
		save("summary.json", map[string]any{"physical_calls": probe.calls, "records_planned": 1, "records_attempted": 1, "paid_cost_usd": 0})
		fmt.Printf("Public video with audio: tags=%v unsafe=%v reason=%s calls=%d\n", tags, unsafe, u.classificationReason, probe.calls)
		return
	}
	img, e := os.ReadFile(filepath.Join(cfg.Root, "data/backups/classification-workflow-media-20261113/source-image"))
	if e != nil {
		t.Fatal(e)
	}
	var results []map[string]any
	tags, unsafe := u.classifyPublicImageBytes(img, "Когда вышел новый VPN")
	results = append(results, map[string]any{"case": "VPN image", "tags": tags, "unsafe": unsafe, "reason": u.classificationReason, "calls": probe.calls})
	save("results.json", results)
	fmt.Printf("VPN image: tags=%v unsafe=%v reason=%s calls=%d\n", tags, unsafe, u.classificationReason, probe.calls)
	if os.Getenv("POSTA_MEDIA_ONLY") == "image" {
		save("summary.json", map[string]any{"physical_calls": probe.calls, "records_planned": 1, "records_attempted": 1, "upstream_stopped": probe.stopped, "paid_cost_usd": 0})
		return
	}
	if !probe.stopped {
		before := probe.calls
		u.classificationErr = nil
		u.classificationReason = ""
		video := filepath.Join(cfg.Root, "data/backups/classification-multimedia-20261113/previously-untested-video.mp4")
		tags, unsafe = u.classifyPublicVideoFile(video, "Amazon представила дрон для видеонаблюдения и патрулирования дома и робота-охранника на колёсах. Они умеют работать в связке.")
		results = append(results, map[string]any{"case": "Amazon silent video", "tags": tags, "unsafe": unsafe, "reason": u.classificationReason, "calls": probe.calls - before})
		save("results.json", results)
		fmt.Printf("Amazon silent video: tags=%v unsafe=%v reason=%s calls=%d\n", tags, unsafe, u.classificationReason, probe.calls-before)
	}
	save("summary.json", map[string]any{"physical_calls": probe.calls, "records_planned": 2, "records_attempted": len(results), "upstream_stopped": probe.stopped, "paid_cost_usd": 0})
}
