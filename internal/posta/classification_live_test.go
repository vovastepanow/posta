package posta

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/llm"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type classificationProbeTransport struct {
	base          http.RoundTripper
	dir           string
	mu            sync.Mutex
	calls         int
	stopped       bool
	cache         map[string][]byte
	reused        int
	last          time.Time
	limit         int
	allowFallback bool
	failedModels  map[string]bool
	allowPaid     bool
	paidReserved  float64
	paidCost      float64
}

func (p *classificationProbeTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
		return p.base.RoundTrip(r)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, err
	}
	r.Body = io.NopCloser(bytes.NewReader(raw))
	var body map[string]any
	if json.Unmarshal(raw, &body) != nil {
		return nil, fmt.Errorf("bad payload")
	}
	model, _ := body["model"].(string)
	if strings.Contains(model, "baidu/") {
		return nil, fmt.Errorf("Baidu is not permitted in this probe")
	}
	paid := !strings.HasSuffix(model, ":free")
	if paid && (!p.allowPaid || model != "qwen/qwen3.7-flash") {
		return nil, fmt.Errorf("probe refuses paid model")
	}
	if cached, ok := p.cache[string(raw)]; ok {
		p.reused++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(bytes.NewReader(cached)), Request: r}, nil
	}
	if p.failedModels[model] {
		return nil, fmt.Errorf("probe route already failed")
	}
	if p.stopped || p.calls >= p.limit {
		return nil, fmt.Errorf("probe physical-call limit or upstream failure")
	}
	if paid {
		provider, _ := body["provider"].(map[string]any)
		caps, _ := provider["max_price"].(map[string]any)
		in, _ := caps["prompt"].(float64)
		out, _ := caps["completion"].(float64)
		tokens, _ := body["max_tokens"].(float64)
		if in <= 0 || in > 0.15 || out <= 0 || out > 0.5 || tokens <= 0 {
			return nil, fmt.Errorf("missing/excessive price caps")
		}
		// Upper bound: one token per UTF-8 payload byte plus protocol overhead.
		bound := float64(len(raw)+4096)*in/1e6 + tokens*out/1e6
		if p.paidReserved+bound > 0.02 {
			p.stopped = true
			return nil, fmt.Errorf("probe $0.02 hard reservation cap")
		}
		p.paidReserved += bound
	}
	if wait := time.Until(p.last.Add(15 * time.Second)); wait > 0 {
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-time.After(wait):
		}
	}
	p.last = time.Now()
	p.calls++
	prefix := filepath.Join(p.dir, fmt.Sprintf("%02d", p.calls))
	// Persist and count BEFORE forwarding; never save request headers.
	if err = os.WriteFile(prefix+"-request.json", raw, 0600); err != nil {
		return nil, err
	}
	resp, err := p.base.RoundTrip(r)
	if err != nil {
		p.stopped = true
		return nil, err
	}
	data, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(data))
	if e := os.WriteFile(prefix+"-response.json", data, 0600); e != nil {
		return nil, e
	}
	var cost struct {
		Usage struct {
			Cost float64 `json:"cost"`
		} `json:"usage"`
	}
	json.Unmarshal(data, &cost)
	p.paidCost += cost.Usage.Cost
	if resp.StatusCode != 200 {
		if p.allowFallback {
			p.failedModels[model] = true
		} else {
			p.stopped = true
		}
	}
	return resp, err
}

// Explicitly opted in; ordinary go test never uses secrets, DB, or cloud here.
func TestClassificationBoundedLive(t *testing.T) {
	dir := os.Getenv("POSTA_CLASSIFICATION_PROBE_DIR")
	if dir == "" {
		t.Skip("opt-in live probe")
	}
	cfg := config.LoadServer()
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Minute)
	defer cancel()
	d, err := db.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		t.Fatal("database connection unavailable")
	}
	defer d.Close()
	a := &App{DB: d}
	settings := a.LLMSettings()
	role := settings.Roles[llm.RoleClassify]
	allowPaid := os.Getenv("POSTA_CLASSIFICATION_PAID") == "1"
	if model := os.Getenv("POSTA_CLASSIFICATION_MODEL"); model != "" {
		role.Primary = llm.Target{Provider: "openrouter", Model: model}
	}
	if role.Primary.Provider != "openrouter" || !strings.HasSuffix(role.Primary.Model, ":free") {
		t.Fatal("configured classifier is not a free cloud model")
	}
	keepFallback := os.Getenv("POSTA_CLASSIFICATION_KEEP_FALLBACK") == "1"
	if !keepFallback {
		role.Fallback = llm.Off
	}
	if !role.Fallback.Empty() && !role.Fallback.Free() && (!allowPaid || role.Fallback.Model != "qwen/qwen3.7-flash") {
		t.Fatal("paid fallback forbidden")
	}
	settings.Roles[llm.RoleClassify] = role
	if err = os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
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
	if _, err := os.Stat(filepath.Join(dir, "plan.json")); err == nil {
		t.Fatal("choose a new output directory; existing run is immutable")
	}
	limit := 15
	if s := os.Getenv("POSTA_CLASSIFICATION_MAX_CALLS"); s != "" {
		n, e := strconv.Atoi(s)
		if e != nil || n < 1 || n > 15 {
			t.Fatal("invalid physical call limit")
		}
		limit = n
	}
	save("plan.json", map[string]any{"model": role.Primary.Model, "fallback": role.Fallback, "verifier": settings.Roles[llm.RoleClassifyVerify], "max_physical_calls": limit, "paid_allowed": allowPaid, "paid_cap_usd": 0.02, "database_writes": false, "started": time.Now().UTC(), "cache_dir": os.Getenv("POSTA_CLASSIFICATION_CACHE_DIR"), "minimum_spacing_seconds": 15})
	old := http.DefaultTransport
	probe := &classificationProbeTransport{base: old, dir: dir, cache: map[string][]byte{}, limit: limit, allowFallback: keepFallback, failedModels: map[string]bool{}, allowPaid: allowPaid}
	if prior := os.Getenv("POSTA_CLASSIFICATION_PRIOR_SUMMARY"); prior != "" {
		b, e := os.ReadFile(prior)
		if e != nil {
			t.Fatal(e)
		}
		var s struct {
			Reserved float64 `json:"paid_reserved_upper_bound_usd"`
			Cost     float64 `json:"paid_cost_usd"`
		}
		if e = json.Unmarshal(b, &s); e != nil {
			t.Fatal(e)
		}
		probe.paidReserved = s.Reserved
		probe.paidCost = s.Cost
	}
	if cachedir := os.Getenv("POSTA_CLASSIFICATION_CACHE_DIR"); cachedir != "" {
		files, _ := filepath.Glob(filepath.Join(cachedir, "*-request.json"))
		for _, f := range files {
			req, e := os.ReadFile(f)
			if e != nil {
				t.Fatal(e)
			}
			resp, e := os.ReadFile(strings.TrimSuffix(f, "-request.json") + "-response.json")
			if e != nil {
				continue
			}
			var obj map[string]any
			if json.Unmarshal(resp, &obj) == nil && obj["choices"] != nil && obj["error"] == nil {
				probe.cache[string(req)] = resp
			}
		}
	}
	http.DefaultTransport = probe
	defer func() { http.DefaultTransport = old }()
	router := llm.NewRouter(llm.NewOllama(cfg.OllamaURL), llm.NewOpenRouter(cfg.OpenRouterURL, cfg.OpenRouterKey, cfg.OpenRouterTitle, cfg.PublicURL))
	router.LoadSettings = func() llm.Settings { return settings }
	var cases []struct {
		ID, Kind, Caption, Description string
		Required, Allowed              []string
		Abstain                        bool
	}
	raw, err := os.ReadFile(filepath.Join(dir, "cases.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &cases) != nil {
		t.Fatal("invalid cases")
	}
	var results []map[string]any
	for _, c := range cases {
		if probe.stopped {
			break
		}
		u := taxonomyTestUser()
		u.Ctx = ctx
		u.LLM = router
		before := probe.calls
		tags := u.classifyContent(c.Description, c.Caption, false)
		row := map[string]any{"id": c.ID, "kind": c.Kind, "tags": tags, "pending": tags == nil, "reason": u.classificationReason, "physical_calls": probe.calls - before, "required": c.Required, "allowed": c.Allowed}
		if u.classificationErr != nil {
			row["error"] = u.classificationErr.Error()
		}
		results = append(results, row)
		save("results.json", results)
		t.Logf("%s: tags=%v pending=%v reason=%s calls=%d", c.ID, tags, tags == nil, u.classificationReason, probe.calls-before)
	}
	save("summary.json", map[string]any{"records_planned": len(cases), "records_attempted": len(results), "physical_calls": probe.calls, "reused_responses": probe.reused, "paid_cost_usd": probe.paidCost, "paid_reserved_upper_bound_usd": probe.paidReserved, "upstream_stopped": probe.stopped})
}
