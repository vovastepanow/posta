package llm

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// OpenRouter — облачные модели через https://openrouter.ai (OpenAI-совместимый /chat/completions).
type OpenRouter struct {
	URL, Key, Title, Referer string
	client                   *http.Client

	mu       sync.Mutex
	models   map[string]ORModel
	loadedAt time.Time
}

// ORModel — модель из каталога OpenRouter: цены в долларах за токен.
type ORModel struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	ContextLength  int      `json:"context_length"`
	PromptPrice    float64  `json:"prompt_price"`
	CompletionPrice float64 `json:"completion_price"`
	ImagePrice     float64  `json:"image_price"`
	Vision         bool     `json:"vision"`
	Params         []string `json:"params"`
}

func (m ORModel) Supports(p string) bool {
	for _, x := range m.Params {
		if x == p {
			return true
		}
	}
	return false
}

func NewOpenRouter(url, key, title, referer string) *OpenRouter {
	return &OpenRouter{URL: strings.TrimRight(url, "/"), Key: key, Title: title, Referer: referer, client: &http.Client{}}
}

func (o *OpenRouter) Name() string { return "openrouter" }

func (o *OpenRouter) Configured() bool { return o != nil && o.Key != "" }

func (o *OpenRouter) do(ctx context.Context, path string, payload any, timeout time.Duration) ([]byte, int, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	method := "GET"
	if payload != nil {
		b, _ := json.Marshal(payload)
		body, method = bytes.NewReader(b), "POST"
	}
	req, _ := http.NewRequestWithContext(ctx, method, o.URL+path, body)
	req.Header.Set("Content-Type", "application/json")
	if o.Key != "" {
		req.Header.Set("Authorization", "Bearer "+o.Key)
	}
	if o.Title != "" {
		req.Header.Set("X-Title", o.Title)
	}
	if o.Referer != "" {
		req.Header.Set("HTTP-Referer", o.Referer)
	}
	resp, err := o.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, 0, fmt.Errorf("timed out: %w", ctx.Err())
		}
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return raw, resp.StatusCode, err
}

// Models — каталог моделей (кэш 6 часов).
func (o *OpenRouter) Models(ctx context.Context) (map[string]ORModel, error) {
	o.mu.Lock()
	if o.models != nil && time.Since(o.loadedAt) < 6*time.Hour {
		defer o.mu.Unlock()
		return o.models, nil
	}
	o.mu.Unlock()
	raw, status, err := o.do(ctx, "/models", nil, 30*time.Second)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("OpenRouter /models: HTTP %d", status)
	}
	var r struct {
		Data []struct {
			ID            string `json:"id"`
			Name          string `json:"name"`
			ContextLength int    `json:"context_length"`
			Pricing       struct {
				Prompt, Completion, Image string
			} `json:"pricing"`
			Architecture struct {
				InputModalities []string `json:"input_modalities"`
			} `json:"architecture"`
			SupportedParameters []string `json:"supported_parameters"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	out := map[string]ORModel{}
	for _, d := range r.Data {
		m := ORModel{ID: d.ID, Name: d.Name, ContextLength: d.ContextLength, Params: d.SupportedParameters}
		m.PromptPrice, _ = strconv.ParseFloat(d.Pricing.Prompt, 64)
		m.CompletionPrice, _ = strconv.ParseFloat(d.Pricing.Completion, 64)
		m.ImagePrice, _ = strconv.ParseFloat(d.Pricing.Image, 64)
		for _, mod := range d.Architecture.InputModalities {
			if mod == "image" {
				m.Vision = true
			}
		}
		out[d.ID] = m
	}
	o.mu.Lock()
	o.models, o.loadedAt = out, time.Now()
	o.mu.Unlock()
	return out, nil
}

// SortedModels — модели для списка в админке: сначала с картинками, внутри — по цене.
func (o *OpenRouter) SortedModels(ctx context.Context, vision bool) []ORModel {
	all, err := o.Models(ctx)
	if err != nil {
		return nil
	}
	var list []ORModel
	for _, m := range all {
		if m.PromptPrice < 0 || (vision && !m.Vision) {
			continue
		}
		list = append(list, m)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].PromptPrice != list[j].PromptPrice {
			return list[i].PromptPrice < list[j].PromptPrice
		}
		return list[i].ID < list[j].ID
	})
	return list
}

func imageMime(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0x89, 'P', 'N', 'G'}):
		return "image/png"
	case bytes.HasPrefix(b, []byte("GIF8")):
		return "image/gif"
	case len(b) > 12 && string(b[8:12]) == "WEBP":
		return "image/webp"
	}
	return "image/jpeg"
}

type orError struct {
	Error *struct {
		Message string `json:"message"`
		Code    any    `json:"code"`
	} `json:"error"`
}

func (o *OpenRouter) Generate(ctx context.Context, model string, req *Request) (*Response, error) {
	if !o.Configured() {
		return nil, fmt.Errorf("OpenRouter не настроен: нет ключа OPENROUTER_API_KEY")
	}
	started := time.Now()
	content := []map[string]any{{"type": "text", "text": req.Prompt}}
	for _, img := range req.Images {
		content = append(content, map[string]any{"type": "image_url", "image_url": map[string]any{
			"url": "data:" + imageMime(img) + ";base64," + base64.StdEncoding.EncodeToString(img)}})
	}
	var messages []map[string]any
	if req.System != "" {
		messages = append(messages, map[string]any{"role": "system", "content": req.System})
	}
	messages = append(messages, map[string]any{"role": "user", "content": content})
	payload := map[string]any{"model": model, "messages": messages, "temperature": req.Temperature, "usage": map[string]any{"include": true}}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	if req.Seed != nil {
		payload["seed"] = *req.Seed
	}
	if req.Think {
		payload["reasoning"] = map[string]any{"effort": "medium"}
		if req.MaxTokens > 0 {
			payload["max_tokens"] = req.MaxTokens + 2000 // размышления тоже тратят токены ответа
		}
	} else {
		payload["reasoning"] = map[string]any{"enabled": false, "exclude": true}
	}
	if req.Schema != nil {
		payload["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "answer", "strict": false, "schema": req.Schema}}
	} else if req.JSON {
		payload["response_format"] = map[string]any{"type": "json_object"}
	}
	if req.Logprobs {
		payload["logprobs"] = true
		payload["top_logprobs"] = max(min(req.TopLogprobs, 20), 1)
		payload["provider"] = map[string]any{"require_parameters": true}
	}
	timeout := req.Timeout
	if timeout == 0 {
		timeout = 120 * time.Second
	}
	var raw []byte
	var status int
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		raw, status, err = o.do(ctx, "/chat/completions", payload, timeout)
		if err == nil && status == 404 && req.Logprobs {
			// у модели нет поставщика с logprobs — тот же запрос без них (ответ словом)
			delete(payload, "logprobs")
			delete(payload, "top_logprobs")
			delete(payload, "provider")
			req.Logprobs = false
			continue
		}
		if err != nil || status == 429 || status >= 500 {
			if ctx.Err() != nil {
				break
			}
			time.Sleep(time.Duration(attempt+1) * 2 * time.Second)
			continue
		}
		break
	}
	if err != nil {
		return nil, err
	}
	var e orError
	if json.Unmarshal(raw, &e) == nil && e.Error != nil {
		return nil, fmt.Errorf("OpenRouter: %s (HTTP %d)", e.Error.Message, status)
	}
	if status != 200 {
		return nil, fmt.Errorf("OpenRouter: HTTP %d: %.200s", status, raw)
	}
	var r struct {
		Model   string `json:"model"`
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				Reasoning string `json:"reasoning"`
			} `json:"message"`
			Logprobs *struct {
				Content []TokenLogprob `json:"content"`
			} `json:"logprobs"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens        int     `json:"prompt_tokens"`
			CompletionTokens    int     `json:"completion_tokens"`
			Cost                float64 `json:"cost"`
			PromptTokensDetails struct {
				CachedTokens int `json:"cached_tokens"`
			} `json:"prompt_tokens_details"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("OpenRouter: ответ не JSON: %w", err)
	}
	if len(r.Choices) == 0 {
		return nil, fmt.Errorf("OpenRouter: пустой ответ")
	}
	c := r.Choices[0]
	resp := &Response{Text: strings.TrimSpace(c.Message.Content), Thinking: c.Message.Reasoning, Provider: "openrouter", Model: model,
		PromptTokens: r.Usage.PromptTokens, CompletionTokens: r.Usage.CompletionTokens, CachedTokens: r.Usage.PromptTokensDetails.CachedTokens,
		CostUSD: r.Usage.Cost, Duration: time.Since(started)}
	if c.Logprobs != nil {
		resp.Logprobs = c.Logprobs.Content
	}
	if resp.CostUSD == 0 {
		if all, err := o.Models(ctx); err == nil {
			if m, ok := all[model]; ok {
				resp.CostUSD = float64(resp.PromptTokens)*m.PromptPrice + float64(resp.CompletionTokens)*m.CompletionPrice
			}
		}
	}
	if req.OnThinking != nil && resp.Thinking != "" {
		req.OnThinking(resp.Thinking)
	}
	return resp, nil
}

func (o *OpenRouter) Embed(ctx context.Context, model string, texts []string) ([][]float64, error) {
	if !o.Configured() {
		return nil, fmt.Errorf("OpenRouter не настроен")
	}
	raw, status, err := o.do(ctx, "/embeddings", map[string]any{"model": model, "input": texts}, 60*time.Second)
	if err != nil {
		return nil, err
	}
	var r struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if status != 200 || json.Unmarshal(raw, &r) != nil {
		return nil, fmt.Errorf("OpenRouter embeddings: HTTP %d", status)
	}
	out := make([][]float64, len(r.Data))
	for i, d := range r.Data {
		out[i] = d.Embedding
	}
	return out, nil
}

// Credits — остаток и расход ключа (для админки).
func (o *OpenRouter) Credits(ctx context.Context) (total, used float64, err error) {
	raw, status, err := o.do(ctx, "/credits", nil, 15*time.Second)
	if err != nil {
		return 0, 0, err
	}
	var r struct {
		Data struct {
			TotalCredits float64 `json:"total_credits"`
			TotalUsage   float64 `json:"total_usage"`
		} `json:"data"`
	}
	if status != 200 || json.Unmarshal(raw, &r) != nil {
		return 0, 0, fmt.Errorf("OpenRouter /credits: HTTP %d", status)
	}
	return r.Data.TotalCredits, r.Data.TotalUsage, nil
}
