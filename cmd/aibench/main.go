// aibench — замер моделей полного анализа на истории оценок: насколько оценка модели отделяет лайки от жалоб и
// дизлайков, сколько стоит и как быстро. Набор (bench_set.json) — записи истории с картинкой и промптом, как в работе.
//
//	go run ./cmd/aibench -set bench_set.json -n 30 -models google/gemini-3.1-flash-lite,anthropic/claude-haiku-5.5
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"posta/internal/config"
	"posta/internal/imgx"
	"posta/internal/llm"
)

type item struct {
	Img    string         `json:"img"`
	Label  string         `json:"label"`
	Reason string         `json:"reason"`
	Topic  string         `json:"topic"`
	Humor  bool           `json:"humor"`
	Prompt string         `json:"prompt"`
	Schema map[string]any `json:"schema"`
}

type result struct {
	Rel      int
	OK       bool
	Cost     float64
	Latency  time.Duration
	Evidence string
	Err      string
}

func main() {
	setPath := flag.String("set", "bench_set.json", "набор записей")
	n := flag.Int("n", 30, "записей каждого класса (лайки / жалобы+дизлайки)")
	models := flag.String("models", "", "модели OpenRouter через запятую; ollama:<модель> — локальная")
	par := flag.Int("par", 4, "запросов одновременно")
	seed := flag.Int64("seed", 1, "выборка")
	out := flag.String("out", "", "сохранить ответы (json)")
	flag.Parse()
	srv := config.LoadServer()
	raw, err := os.ReadFile(*setPath)
	if err != nil {
		panic(err)
	}
	var all []item
	json.Unmarshal(raw, &all)
	rng := rand.New(rand.NewSource(*seed))
	rng.Shuffle(len(all), func(i, j int) { all[i], all[j] = all[j], all[i] })
	var pos, neg []item
	for _, it := range all {
		switch it.Label {
		case "liked":
			if len(pos) < *n {
				pos = append(pos, it)
			}
		case "complaints", "disliked":
			if len(neg) < *n {
				neg = append(neg, it)
			}
		}
	}
	set := append(pos, neg...)
	fmt.Printf("Набор: %d лайков, %d жалоб/дизлайков\n", len(pos), len(neg))
	imgs := make([][]byte, len(set))
	for i, it := range set {
		b, err := os.ReadFile(it.Img)
		if err != nil {
			panic(err)
		}
		imgs[i] = imgx.Shrink(b, 1024, 88, false)
	}
	or := llm.NewOpenRouter(srv.OpenRouterURL, srv.OpenRouterKey, "Posta bench", "")
	ol := llm.NewOllama(srv.OllamaURL)
	dump := map[string][]result{}
	for _, model := range strings.Split(*models, ",") {
		model = strings.TrimSpace(model)
		if model == "" {
			continue
		}
		var p llm.Provider = or
		name := model
		if strings.HasPrefix(model, "ollama:") {
			p, name = ol, strings.TrimPrefix(model, "ollama:")
		}
		res := make([]result, len(set))
		var wg sync.WaitGroup
		sem := make(chan struct{}, *par)
		for i := range set {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()
				s := config.AnalysisSeed
				req := &llm.Request{Role: llm.RoleAnalysis, Prompt: set[i].Prompt, Images: [][]byte{imgs[i]}, Schema: set[i].Schema,
					MaxTokens: config.AnalysisMaxTokens, Temperature: 0, Seed: &s, Timeout: 150 * time.Second}
				t0 := time.Now()
				r, err := p.Generate(context.Background(), name, req)
				res[i].Latency = time.Since(t0)
				if err != nil {
					res[i].Err = err.Error()
					return
				}
				res[i].Cost = r.CostUSD
				a := llm.DecodeJSON(r.Text)
				rel, ok := a["relevance"].(float64)
				if !ok {
					res[i].Err = "не JSON: " + trim(r.Text, 80)
					return
				}
				res[i].OK, res[i].Rel = true, llm.ClampScore(rel)
				ev, _ := a["evidence"].(string)
				res[i].Evidence = strings.TrimSpace(ev)
				if res[i].Evidence == "" { // как в работе: без цитаты оценка не выше 40
					res[i].Rel = min(res[i].Rel, config.QualityNoEvidenceCap)
				}
			}(i)
		}
		wg.Wait()
		dump[model] = res
		report(model, set, res)
	}
	if *out != "" {
		b, _ := json.MarshalIndent(dump, "", " ")
		os.WriteFile(*out, b, 0o644)
	}
}

func trim(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}

// report: AUC (вероятность, что лайк получит оценку выше жалобы), доля прошедших порог 80 по классам, цена, задержка.
func report(model string, set []item, res []result) {
	var p, q []int
	var cost float64
	var lat []time.Duration
	fails := 0
	keepPos, keepNeg := 0, 0
	for i, r := range res {
		cost += r.Cost
		lat = append(lat, r.Latency)
		if !r.OK {
			fails++
			continue
		}
		if set[i].Label == "liked" {
			p = append(p, r.Rel)
			if r.Rel >= config.RelevanceThreshold {
				keepPos++
			}
		} else {
			q = append(q, r.Rel)
			if r.Rel >= config.RelevanceThreshold {
				keepNeg++
			}
		}
	}
	auc := 0.0
	for _, a := range p {
		for _, b := range q {
			switch {
			case a > b:
				auc++
			case a == b:
				auc += 0.5
			}
		}
	}
	if len(p)*len(q) > 0 {
		auc /= float64(len(p) * len(q))
	}
	sort.Slice(lat, func(i, j int) bool { return lat[i] < lat[j] })
	med := time.Duration(0)
	if len(lat) > 0 {
		med = lat[len(lat)/2]
	}
	per1k := 0.0
	if len(res) > 0 {
		per1k = cost / float64(len(res)) * 1000
	}
	fmt.Printf("%-40s AUC %.3f | порог 80: лайков %3.0f%%, жалоб/дизл. %3.0f%% | ср. лайк %4.1f, ср. плохие %4.1f | ошибок %d | $%.4f всего, $%.2f за 1000 | медиана %.1f с\n",
		model, auc, pct(keepPos, len(p)), pct(keepNeg, len(q)), mean(p), mean(q), fails, cost, per1k, med.Seconds())
	if fails > 0 {
		for _, r := range res {
			if r.Err != "" {
				fmt.Println("   пример ошибки:", trim(r.Err, 200))
				break
			}
		}
	}
}

func pct(a, b int) float64 {
	if b == 0 {
		return 0
	}
	return 100 * float64(a) / float64(b)
}

func mean(v []int) float64 {
	if len(v) == 0 {
		return 0
	}
	s := 0
	for _, x := range v {
		s += x
	}
	return float64(s) / float64(len(v))
}
