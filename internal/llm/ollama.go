package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Ollama — локальные модели. Видеопамяти мало (GTX 1060, 3 ГБ): перед запросом выгружаются другие модели проекта,
// а разросшийся процесс модели перезапускается (утечка памяти у llama-server за часы работы).
type Ollama struct {
	URL       string
	NumCtx    int
	KeepAlive string
	// ProjectModels — модели Posta: их можно выгружать ради другой модели проекта; чужие модели не трогаем.
	ProjectModels func() []string

	mu          sync.Mutex
	installed   []string
	installedAt time.Time
	canThink    map[string]bool
	lastRecycle time.Time
	recycleMu   sync.Mutex
	prepareMu   sync.Mutex
	client      *http.Client
}

func NewOllama(url string) *Ollama {
	return &Ollama{URL: strings.TrimRight(url, "/"), NumCtx: 4096, KeepAlive: "30m", canThink: map[string]bool{},
		client: &http.Client{}}
}

func (o *Ollama) Name() string { return "ollama" }

func (o *Ollama) getJSON(ctx context.Context, path string, timeout time.Duration, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", o.URL+path, nil)
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

func (o *Ollama) postJSON(ctx context.Context, path string, payload any, timeout time.Duration, out any) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, _ := json.Marshal(payload)
	req, _ := http.NewRequestWithContext(ctx, "POST", o.URL+path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

// Installed — установленные модели (кэш минуту).
func (o *Ollama) Installed(ctx context.Context) []string {
	o.mu.Lock()
	if time.Since(o.installedAt) < time.Minute && o.installed != nil {
		defer o.mu.Unlock()
		return o.installed
	}
	o.mu.Unlock()
	var r struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if err := o.getJSON(ctx, "/api/tags", 5*time.Second, &r); err != nil {
		return nil
	}
	names := make([]string, 0, len(r.Models))
	for _, m := range r.Models {
		names = append(names, m.Name)
	}
	o.mu.Lock()
	o.installed, o.installedAt = names, time.Now()
	o.mu.Unlock()
	return names
}

func (o *Ollama) HasModel(ctx context.Context, model string) bool {
	for _, n := range o.Installed(ctx) {
		if n == model || n == model+":latest" {
			return true
		}
	}
	return false
}

type LoadedModel struct {
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SizeVRAM int64  `json:"size_vram"`
	ExpiresAt string `json:"expires_at"`
	Context   int    `json:"context_length"`
}

func (o *Ollama) Loaded(ctx context.Context) []LoadedModel {
	var r struct {
		Models []LoadedModel `json:"models"`
	}
	o.getJSON(ctx, "/api/ps", 3*time.Second, &r)
	return r.Models
}

// Unload выгружает модель и ждёт, пока она пропадёт из /api/ps (иначе следующая загрузится «рядом»).
func (o *Ollama) Unload(ctx context.Context, model string) {
	o.postJSON(ctx, "/api/generate", map[string]any{"model": model, "keep_alive": 0}, 10*time.Second, nil)
	for i := 0; i < 50; i++ {
		found := false
		for _, m := range o.Loaded(ctx) {
			if m.Name == model {
				found = true
			}
		}
		if !found {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Load — загрузить модель заранее (кнопка в админке).
func (o *Ollama) Load(ctx context.Context, model string) error {
	o.prepare(ctx, model)
	return o.postJSON(ctx, "/api/generate", map[string]any{"model": model, "keep_alive": o.KeepAlive, "options": map[string]any{"num_ctx": o.NumCtx}}, 3*time.Minute, nil)
}

func normModel(name string) string { return strings.TrimSuffix(name, ":latest") }

// prepare выгружает другие модели проекта перед запросом: на 3 ГБ видеопамяти две не помещаются.
func (o *Ollama) prepare(ctx context.Context, model string) {
	if o.ProjectModels == nil {
		return
	}
	ours := map[string]bool{}
	for _, m := range o.ProjectModels() {
		ours[normModel(m)] = true
	}
	if !ours[normModel(model)] {
		return
	}
	o.prepareMu.Lock()
	defer o.prepareMu.Unlock()
	o.RecycleIfNeeded(ctx)
	for _, l := range o.Loaded(ctx) {
		if normModel(l.Name) != normModel(model) && ours[normModel(l.Name)] {
			o.Unload(ctx, l.Name)
		}
	}
}

// CanThink — умеет ли модель размышлять (capabilities в /api/show).
func (o *Ollama) CanThink(ctx context.Context, model string) bool {
	o.mu.Lock()
	v, ok := o.canThink[model]
	o.mu.Unlock()
	if ok {
		return v
	}
	var r struct {
		Capabilities []string `json:"capabilities"`
	}
	o.postJSON(ctx, "/api/show", map[string]any{"model": model}, 5*time.Second, &r)
	can := false
	for _, c := range r.Capabilities {
		if c == "thinking" {
			can = true
		}
	}
	o.mu.Lock()
	o.canThink[model] = can
	o.mu.Unlock()
	return can
}

// Generate — потоковый запрос /api/generate. Размышления приходят полем thinking или текстом до </think>
// (шаблон Qwen 3.5 сам открывает <think>) — они уходят в req.OnThinking.
func (o *Ollama) Generate(ctx context.Context, model string, req *Request) (*Response, error) {
	o.prepare(ctx, model)
	started := time.Now()
	think := req.Think && o.CanThink(ctx, model)
	payload := map[string]any{"model": model, "prompt": req.Prompt, "stream": true, "keep_alive": o.KeepAlive}
	if req.System != "" {
		payload["system"] = req.System
	}
	opts := map[string]any{"num_ctx": o.NumCtx, "temperature": req.Temperature}
	if req.MaxTokens > 0 {
		opts["num_predict"] = req.MaxTokens
	}
	if req.Seed != nil {
		opts["seed"] = *req.Seed
	}
	payload["options"] = opts
	if o.CanThink(ctx, model) {
		payload["think"] = think
	}
	if !think { // format=json отключает размышления
		if req.Schema != nil {
			payload["format"] = req.Schema
		} else if req.JSON {
			payload["format"] = "json"
		}
	}
	if len(req.Images) > 0 {
		imgs := make([]string, len(req.Images))
		for i, b := range req.Images {
			imgs[i] = base64.StdEncoding.EncodeToString(b)
		}
		payload["images"] = imgs
	}
	if req.Logprobs {
		payload["logprobs"] = true
		payload["top_logprobs"] = max(req.TopLogprobs, 1)
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 180 * time.Second
		if think {
			timeout = 420 * time.Second
		}
	}
	r, err := o.stream(ctx, payload, timeout, think, req.OnThinking)
	if err != nil && think && strings.Contains(strings.ToLower(err.Error()), "think") {
		delete(payload, "think") // модель не поддерживает размышления — повтор без них
		r, err = o.stream(ctx, payload, timeout, false, req.OnThinking)
	}
	if r != nil {
		r.Provider, r.Model, r.Duration = "ollama", model, time.Since(started)
	}
	return r, err
}

func (o *Ollama) stream(ctx context.Context, payload map[string]any, timeout time.Duration, think bool, onThinking func(string)) (*Response, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, _ := json.Marshal(payload)
	hreq, _ := http.NewRequestWithContext(ctx, "POST", o.URL+"/api/generate", bytes.NewReader(body))
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := o.client.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	r := &Response{}
	var raw strings.Builder
	answerStarted := !think
	emit := func(s string) {
		if onThinking != nil && s != "" {
			onThinking(s)
		}
	}
	apiErr := ""
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var d struct {
			Response           string         `json:"response"`
			Thinking           string         `json:"thinking"`
			Error              string         `json:"error"`
			Done               bool           `json:"done"`
			Logprobs           []TokenLogprob `json:"logprobs"`
			TotalDuration      int64          `json:"total_duration"`
			LoadDuration       int64          `json:"load_duration"`
			PromptEvalDuration int64          `json:"prompt_eval_duration"`
			PromptEvalCount    int            `json:"prompt_eval_count"`
			EvalDuration       int64          `json:"eval_duration"`
			EvalCount          int            `json:"eval_count"`
		}
		if json.Unmarshal(sc.Bytes(), &d) != nil {
			continue
		}
		if d.Error != "" {
			apiErr = d.Error
		}
		r.Logprobs = append(r.Logprobs, d.Logprobs...)
		if d.Done && d.TotalDuration > 0 {
			r.LoadSeconds = float64(d.LoadDuration) / 1e9
			r.PromptSeconds = float64(d.PromptEvalDuration) / 1e9
			r.AnswerSeconds = float64(d.EvalDuration) / 1e9
			r.PromptTokens, r.CompletionTokens = d.PromptEvalCount, d.EvalCount
		}
		if d.Thinking != "" {
			r.Thinking += d.Thinking
			emit(d.Thinking)
			answerStarted = true
		}
		if d.Response == "" {
			continue
		}
		before := raw.Len()
		raw.WriteString(d.Response)
		if answerStarted {
			r.Text += d.Response
			continue
		}
		all := raw.String()
		close := strings.Index(all, "</think>")
		if close < 0 {
			emit(strings.ReplaceAll(d.Response, "<think>", ""))
			continue
		}
		answerStarted = true
		if close > before {
			emit(all[before:close])
		}
		r.Thinking += strings.ReplaceAll(all[:close], "<think>", "")
		r.Text = strings.TrimLeft(all[close+len("</think>"):], " \n")
	}
	if err := sc.Err(); err != nil && ctx.Err() != nil {
		return r, fmt.Errorf("timed out: %w", ctx.Err())
	}
	if !answerStarted { // </think> не было: всё — ответ
		r.Thinking = ""
		r.Text = strings.TrimSpace(strings.ReplaceAll(raw.String(), "<think>", ""))
	}
	if apiErr != "" {
		return r, fmt.Errorf("%s", apiErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return r, fmt.Errorf("Ошибка локальной Ollama (HTTP %d).", resp.StatusCode)
	}
	return r, nil
}

func (o *Ollama) Embed(ctx context.Context, model string, texts []string) ([][]float64, error) {
	var r struct {
		Embeddings [][]float64 `json:"embeddings"`
		Error      string      `json:"error"`
	}
	// num_gpu 0: эмбеддинги на процессоре, видеопамять — под vision-модель
	err := o.postJSON(ctx, "/api/embed", map[string]any{"model": model, "input": texts, "keep_alive": "5m", "options": map[string]any{"num_gpu": 0}}, 60*time.Second, &r)
	if err != nil {
		return nil, err
	}
	if r.Error != "" {
		return nil, fmt.Errorf("%s", r.Error)
	}
	return r.Embeddings, nil
}

// ---- Присмотр за памятью: llama-server разрастается за часы, и компьютер зависает ----

const (
	runnerMaxRSS            = 4 << 30
	runnerMaxAge            = 3 * 3600
	runnerLowMemory         = 1536 << 20
	runnerPeriod            = 600
	runnerFallbackLowMemory = 3.5 * (1 << 30)
)

type runnerProc struct {
	PID int
	RSS int64
	Age int64
}

var vmRSSRe = regexp.MustCompile(`VmRSS:\s+(\d+) kB`)
var memAvailRe = regexp.MustCompile(`MemAvailable:\s+(\d+) kB`)

func runnerProcesses() []runnerProc {
	upRaw, _ := os.ReadFile("/proc/uptime")
	uptime, _ := strconv.ParseFloat(strings.Fields(string(upRaw) + " 0")[0], 64)
	var list []runnerProc
	files, _ := filepath.Glob("/proc/[0-9]*/cmdline")
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		args := strings.Split(string(raw), "\x00")
		base := filepath.Base(args[0])
		if base != "llama-server" && !(base == "ollama" && len(args) > 1 && args[1] == "runner") {
			continue
		}
		pid, _ := strconv.Atoi(strings.Split(f, "/")[2])
		p := runnerProc{PID: pid}
		if st, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", pid)); err == nil {
			if m := vmRSSRe.FindSubmatch(st); m != nil {
				kb, _ := strconv.ParseInt(string(m[1]), 10, 64)
				p.RSS = kb * 1024
			}
		}
		if stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid)); err == nil {
			s := string(stat)
			if i := strings.LastIndex(s, ")"); i > 0 {
				fields := strings.Fields(s[i+2:])
				if len(fields) > 19 {
					start, _ := strconv.ParseFloat(fields[19], 64)
					p.Age = int64(max(0, uptime-start/100))
				}
			}
		}
		list = append(list, p)
	}
	return list
}

func availableMemory() int64 {
	raw, _ := os.ReadFile("/proc/meminfo")
	if m := memAvailRe.FindSubmatch(raw); m != nil {
		kb, _ := strconv.ParseInt(string(m[1]), 10, 64)
		return kb * 1024
	}
	return 1 << 62
}

func gb(b int64) string { return fmt.Sprintf("%.1f ГБ", float64(b)/(1<<30)) }

// runnerProblem — причина перезапуска процесса или "". Пороги — от размера самой модели (6 ГБ у 9B — норма).
func runnerProblem(p runnerProc, modelSize int64) string {
	grown := max(int64(2<<30), modelSize+(1<<30))
	switch {
	case p.RSS > max(int64(runnerMaxRSS), modelSize+(2<<30)):
		return "процесс модели занимает " + gb(p.RSS) + " памяти"
	case p.Age > runnerMaxAge && p.RSS > grown:
		return fmt.Sprintf("процесс модели работает %.1f ч и занимает %s", float64(p.Age)/3600, gb(p.RSS))
	case p.RSS > grown && availableMemory() < runnerLowMemory:
		return "в системе мало памяти, а процесс модели занимает " + gb(p.RSS)
	}
	return ""
}

// RecycleIfNeeded выгружает модели, если процесс модели разросся (Ollama поднимет свежий). Не чаще раза в 30 с.
func (o *Ollama) RecycleIfNeeded(ctx context.Context) bool {
	o.recycleMu.Lock()
	defer o.recycleMu.Unlock()
	if time.Since(o.lastRecycle) < 30*time.Second {
		return false
	}
	o.lastRecycle = time.Now()
	loaded := o.Loaded(ctx)
	var size int64
	for _, m := range loaded {
		size = max(size, m.Size)
	}
	reason := ""
	procs := runnerProcesses()
	if len(procs) > 0 {
		for _, p := range procs {
			if reason = runnerProblem(p, size); reason != "" {
				break
			}
		}
	} else if len(loaded) > 0 && float64(availableMemory()) < runnerFallbackLowMemory {
		// процессы Ollama не видны (другой пользователь и ProtectProc): судим по свободной памяти
		reason = fmt.Sprintf("в системе мало свободной памяти (%.1f ГБ)", float64(availableMemory())/(1<<30))
	}
	if reason == "" {
		return false
	}
	log.Printf("Память: %s — перезапускаю модель, чтобы компьютер не завис (утечка памяти в Ollama).", reason)
	for _, m := range loaded {
		o.Unload(ctx, m.Name)
	}
	for i := 0; i < 100; i++ {
		big := false
		for _, p := range runnerProcesses() {
			if p.RSS > 1<<30 {
				big = true
			}
		}
		if !big {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	for _, p := range runnerProcesses() {
		if runnerProblem(p, 0) != "" {
			log.Printf("-> Процесс модели не завершился сам. Выполните: sudo systemctl restart ollama")
			return false
		}
	}
	return true
}

// Version — версия Ollama или "" (не отвечает).
func (o *Ollama) Version(ctx context.Context) string {
	var r struct {
		Version string `json:"version"`
	}
	if o.getJSON(ctx, "/api/version", 3*time.Second, &r) != nil {
		return ""
	}
	return r.Version
}

// UnloadAll — выгрузить все загруженные модели.
func (o *Ollama) UnloadAll(ctx context.Context) {
	for _, m := range o.Loaded(ctx) {
		o.Unload(ctx, m.Name)
	}
}
