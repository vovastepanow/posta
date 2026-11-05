package llm

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Target — где выполнять задачу роли.
type Target struct {
	Provider string `json:"provider"` // ollama | openrouter | off
	Model    string `json:"model"`
}

func (t Target) Empty() bool { return t.Provider == "" || t.Provider == "off" || t.Model == "" }

// Free — бесплатная цель: локальная модель или :free-модель OpenRouter.
func (t Target) Free() bool {
	return t.Provider == "ollama" || strings.HasSuffix(t.Model, ":free") || t.Model == "openrouter/free"
}

func (t Target) String() string {
	if t.Empty() {
		return "выключено"
	}
	return t.Provider + ":" + t.Model
}

type RoleConfig struct {
	Primary  Target `json:"primary"`
	Fallback Target `json:"fallback"`
	// Threshold — порог «по теме» для роли analysis под эту модель (0 — общий 80): у моделей разная шкала.
	Threshold int `json:"threshold,omitempty"`
}

// Settings — настройки моделей (kv admin:llm), меняются в админке.
type Settings struct {
	Preset         string              `json:"preset"` // local | cloud | hybrid | custom
	Roles          map[Role]RoleConfig `json:"roles"`
	FastOnly       bool                `json:"fast_only"`        // все задачи vision — на модель быстрой оценки (локальный режим слабой видеокарты)
	DailyBudgetUSD float64             `json:"daily_budget_usd"` // платные модели: 0 — не вызываются вовсе; превышен — задача идёт в резерв
	FreeDailyLimit int                 `json:"free_daily_limit"` // бесплатных запросов OpenRouter в сутки (у ключа 1000); исчерпаны — резерв
	ThinkInParser  bool                `json:"think_in_parser"`
}

// Модели по умолчанию. Облачные выбраны по замеру на истории оценок (posta ai-bench) — см. docs/MODELS.md.
var (
	LocalFast   = Target{"ollama", "qwen3-vl:2b-instruct"}
	LocalSmart  = Target{"ollama", "qwen3.5-vision:latest"}
	LocalEmbed  = Target{"ollama", "bge-m3"}
	FreeVision  = Target{"openrouter", "nvidia/nemotron-3.5-super-vl:free"}
	CloudFast   = Target{"openrouter", "qwen/qwen3.7-flash"}
	CloudVision = Target{"openrouter", "google/gemini-3.1-flash-lite"}
	CloudJudge  = Target{"openrouter", "qwen/qwen3.8-flash"}
	CloudSmart  = Target{"openrouter", "anthropic/claude-sonnet-5.5"}
	CloudText   = Target{"openrouter", "deepseek/deepseek-v4.1-flash"}
	Off         = Target{"off", ""}

	// Модели быстрой оценки Ollama по порядку: берётся первая установленная.
	LocalFastCandidates = []string{"qwen3-vl:2b-instruct", "qwen3-vl:2b", "qwen2.5vl:3b"}
)

// Preset — готовый набор:
//
//	free   — бесплатно: отсев локально (2B), анализ и умные задачи — бесплатная модель OpenRouter, резерв — Ollama;
//	local  — всё на Ollama;
//	hybrid — отсев локально, остальное — дешёвые платные модели (нужен бюджет);
//	cloud  — запросы в OpenRouter, без локальных моделей.
func Preset(name string) Settings {
	s := Settings{Preset: name, Roles: map[Role]RoleConfig{}, DailyBudgetUSD: 0, FreeDailyLimit: 1000}
	set := func(r Role, p, f Target) { s.Roles[r] = RoleConfig{Primary: p, Fallback: f} }
	switch name {
	case "free":
		set(RolePrescreen, LocalFast, Off)
		set(RoleAnalysis, FreeVision, LocalSmart)
		s.Roles[RoleAnalysis] = RoleConfig{Primary: FreeVision, Fallback: LocalSmart, Threshold: 65}
		set(RoleJudge, FreeVision, LocalSmart)
		set(RoleSmart, FreeVision, LocalSmart)
		set(RoleText, FreeVision, LocalSmart)
		set(RoleRadar, FreeVision, LocalSmart)
		set(RoleTranslate, Off, Off)
		set(RoleEmbed, LocalEmbed, Off)
	case "cloud":
		s.DailyBudgetUSD = 0.5
		set(RolePrescreen, CloudFast, Off)
		set(RoleAnalysis, CloudVision, Off)
		set(RoleJudge, CloudJudge, Off)
		set(RoleSmart, CloudSmart, Off)
		set(RoleText, CloudText, Off)
		set(RoleRadar, CloudText, Off)
		set(RoleTranslate, CloudText, Off)
		set(RoleEmbed, Off, Off)
	case "hybrid":
		s.DailyBudgetUSD = 0.3
		set(RolePrescreen, LocalFast, CloudFast)
		set(RoleAnalysis, CloudVision, LocalSmart)
		set(RoleJudge, CloudJudge, LocalSmart)
		set(RoleSmart, CloudSmart, LocalSmart)
		set(RoleText, CloudText, LocalSmart)
		set(RoleRadar, CloudText, LocalSmart)
		set(RoleTranslate, Off, Off)
		set(RoleEmbed, LocalEmbed, Off)
	default:
		s.Preset = "local"
		set(RolePrescreen, LocalFast, Off)
		set(RoleAnalysis, LocalSmart, Off)
		set(RoleJudge, LocalSmart, Off)
		set(RoleSmart, LocalSmart, Off)
		set(RoleText, LocalSmart, Off)
		set(RoleRadar, LocalSmart, Off)
		set(RoleTranslate, Off, Off)
		set(RoleEmbed, LocalEmbed, Off)
	}
	return s
}

// Normalize — пропущенные роли берутся из пресета local.
func (s Settings) Normalize() Settings {
	base := Preset("free")
	if s.Roles == nil {
		s.Roles = map[Role]RoleConfig{}
	}
	for _, r := range Roles {
		if _, ok := s.Roles[r]; !ok {
			s.Roles[r] = base.Roles[r]
		}
	}
	if s.Preset == "" {
		s.Preset = "custom"
	}
	return s
}

// Call — журнал вызова: для журнала сборщика, ClickHouse и дневного бюджета.
type Call struct {
	Role     Role
	Target   Target
	Req      *Request
	Resp     *Response
	Err      error
	Fallback bool
	Started  time.Time
}

type Router struct {
	Ollama     *Ollama
	OpenRouter *OpenRouter
	// LoadSettings — актуальные настройки (кэширует вызывающий); nil — пресет local.
	LoadSettings func() Settings
	// Spent — потрачено в OpenRouter за сегодня (долл.), с учётом этого процесса; nil — бюджет не проверяется.
	Spent func() float64
	// OnCall — после каждого вызова (журнал, ClickHouse, учёт расходов).
	OnCall func(Call)
	// Log — строки в журнал сборщика или ответа API.
	Log func(userID, line string)

	mu                    sync.Mutex
	freeDay               string
	freeUsed              int
	freeDailyBlockedUntil time.Time
	freeBlocked           map[string]time.Time // бесплатная модель → до какого времени её не трогаем (429 от поставщика)
	timeouts              map[Role]int
	slowTill              map[Role]time.Time
	fast                  string
}

func NewRouter(ol *Ollama, or *OpenRouter) *Router {
	r := &Router{Ollama: ol, OpenRouter: or, timeouts: map[Role]int{}, slowTill: map[Role]time.Time{}}
	ol.ProjectModels = r.projectModels
	return r
}

func (r *Router) Settings() Settings {
	if r.LoadSettings == nil {
		return Preset("free")
	}
	return r.LoadSettings().Normalize()
}

// projectModels — все модели Ollama из настроек (их можно выгружать друг ради друга).
func (r *Router) projectModels() []string {
	s := r.Settings()
	var out []string
	for _, rc := range s.Roles {
		for _, t := range []Target{rc.Primary, rc.Fallback} {
			if t.Provider == "ollama" && t.Model != "" {
				out = append(out, t.Model)
			}
		}
	}
	return append(out, LocalFastCandidates...)
}

// localFast — модель быстрой оценки Ollama: из настроек, если установлена, иначе первая установленная из кандидатов.
func (r *Router) localFast(ctx context.Context, want string) string {
	r.mu.Lock()
	cached := r.fast
	r.mu.Unlock()
	if cached != "" && (want == "" || cached == want) {
		return cached
	}
	pick := want
	if want == "" || !r.Ollama.HasModel(ctx, want) {
		pick = ""
		for _, c := range LocalFastCandidates {
			if r.Ollama.HasModel(ctx, c) {
				pick = c
				break
			}
		}
	}
	if pick == "" {
		pick = want
	}
	r.mu.Lock()
	r.fast = pick
	r.mu.Unlock()
	return pick
}

// Resolve — куда пойдёт задача роли сейчас: с учётом «только быстрой» и временного перехода на резерв.
// Деньги и бесплатная квота проверяются в Generate для каждой цели.
func (r *Router) Resolve(ctx context.Context, role Role) (primary, fallback Target, note string) {
	s := r.Settings()
	rc := s.Roles[role]
	primary, fallback = rc.Primary, rc.Fallback
	if primary.Provider == "ollama" && role == RolePrescreen {
		primary.Model = r.localFast(ctx, primary.Model)
	}
	// «Только быстрая модель»: vision-задачи и умные — на модель быстрой оценки, если основная локальная
	if s.FastOnly && role != RolePrescreen && role != RoleEmbed && primary.Provider == "ollama" {
		fast := s.Roles[RolePrescreen].Primary
		if fast.Provider == "ollama" {
			fast.Model = r.localFast(ctx, fast.Model)
		}
		primary, note = fast, "режим «только быстрая модель»"
	}
	r.mu.Lock()
	till := r.slowTill[role]
	r.mu.Unlock()
	if time.Now().Before(till) && !fallback.Empty() {
		primary, fallback, note = fallback, Off, "основная модель не укладывалась во время, до "+till.Format("15:04")+" — резерв"
	}
	return primary, fallback, note
}

// blocked — почему цель сейчас нельзя использовать ("" — можно): приватный запрос, деньги, бесплатная квота.
func (r *Router) blocked(t Target, req *Request, s Settings) string {
	if t.Empty() {
		return "выключено"
	}
	if t.Provider != "openrouter" {
		return ""
	}
	if req.Private {
		return "18+ — только локальная модель"
	}
	if !t.Free() {
		if s.DailyBudgetUSD <= 0 {
			return "платные модели выключены (бюджет $0)"
		}
		if r.Spent != nil && r.Spent() >= s.DailyBudgetUSD {
			return fmt.Sprintf("дневной бюджет $%.2f исчерпан", s.DailyBudgetUSD)
		}
		return ""
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if till, ok := r.freeBlocked[t.Model]; ok && time.Now().Before(till) {
		return "у бесплатной модели перегрузка (429) до " + till.Format("15:04")
	}
	if time.Now().Before(r.freeDailyBlockedUntil) {
		return "OpenRouter сообщил об исчерпании суточного лимита; повторная проверка после " + r.freeDailyBlockedUntil.Format("15:04")
	}
	return ""
}

func (r *Router) countFree(t Target, err error) {
	if t.Provider != "openrouter" || !t.Free() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	day := time.Now().Format("2006-01-02")
	if r.freeDay != day {
		r.freeDay, r.freeUsed = day, 0
	}
	r.freeUsed++
	if err != nil && strings.Contains(strings.ToLower(err.Error()), "free-models-per-day") {
		// Периодически пробуем снова: время сброса может измениться.
		r.freeDailyBlockedUntil = time.Now().Add(15 * time.Minute)
		return
	}
	if err != nil && (strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "rate limit")) {
		if r.freeBlocked == nil {
			r.freeBlocked = map[string]time.Time{}
		}
		pause := 2 * time.Minute // «в минуту» — короткая пауза; перегрузка поставщика — дольше
		if !strings.Contains(err.Error(), "per-min") {
			pause = 15 * time.Minute
		}
		r.freeBlocked[t.Model] = time.Now().Add(pause)
	}
}

// SetFreeUsed — сколько бесплатных запросов уже сделано сегодня (восстановление после перезапуска).
func (r *Router) SetFreeUsed(n int) {
	r.mu.Lock()
	r.freeDay, r.freeUsed = time.Now().Format("2006-01-02"), n
	r.mu.Unlock()
}

// FreeUsed — бесплатных запросов за сегодня.
func (r *Router) FreeUsed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.freeDay != time.Now().Format("2006-01-02") {
		return 0
	}
	return r.freeUsed
}

func (r *Router) provider(t Target) (Provider, error) {
	switch t.Provider {
	case "ollama":
		return r.Ollama, nil
	case "openrouter":
		if !r.OpenRouter.Configured() {
			return nil, errors.New("OpenRouter не настроен: добавьте OPENROUTER_API_KEY в posta.env")
		}
		return r.OpenRouter, nil
	}
	return nil, fmt.Errorf("модель для этой задачи выключена")
}

// Generate — запрос к модели роли; нельзя или не вышло — к резервной; 18+ и пустые цели — к локальной быстрой.
func (r *Router) Generate(ctx context.Context, req *Request) (*Response, error) {
	s := r.Settings()
	primary, fallback, note := r.Resolve(ctx, req.Role)
	logf := func(line string) {
		if !req.Quiet && r.Log != nil {
			r.Log(req.UserID, line)
		}
	}
	if note != "" {
		logf("-> Модели: " + note + ".")
	}
	cands := []Target{primary, fallback}
	if req.Private && primary.Provider != "ollama" && fallback.Provider != "ollama" {
		// приватное, а локальной модели у роли нет — модель анализа или быстрой оценки, если она локальная
		for _, role := range []Role{RoleAnalysis, RolePrescreen} {
			if t := s.Roles[role].Primary; t.Provider == "ollama" {
				cands = append(cands, t)
				break
			}
			if t := s.Roles[role].Fallback; t.Provider == "ollama" {
				cands = append(cands, t)
				break
			}
		}
	}
	var lastErr error
	for i, t := range cands {
		if why := r.blocked(t, req, s); why != "" {
			if !t.Empty() {
				logf(fmt.Sprintf("-> %s пропущена: %s.", t, why))
			}
			if lastErr == nil {
				lastErr = errors.New("нет доступной модели: " + why)
			}
			continue
		}
		if i > 0 && lastErr != nil && !strings.HasPrefix(lastErr.Error(), "нет доступной модели") {
			logf(fmt.Sprintf("-> пробую резерв %s.", t))
		}
		resp, err := r.try(ctx, req, t, i > 0)
		r.countFree(t, err)
		if err == nil {
			if i == 0 {
				r.noteSuccess(req.Role)
			}
			return resp, nil
		}
		if i == 0 {
			r.noteFailure(req.Role, err, primary, fallback)
		}
		lastErr = err
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = errors.New("модель для этой задачи выключена")
	}
	return nil, lastErr
}

func shortErr(err error) string {
	s := err.Error()
	if len([]rune(s)) > 160 {
		s = string([]rune(s)[:160]) + "…"
	}
	return s
}

func (r *Router) try(ctx context.Context, req *Request, t Target, isFallback bool) (*Response, error) {
	p, err := r.provider(t)
	if err != nil {
		return nil, err
	}
	call := Call{Role: req.Role, Target: t, Req: req, Fallback: isFallback, Started: time.Now()}
	if !req.Quiet && r.Log != nil {
		r.Log(req.UserID, exchangeLine("→", t.Model, requestSummary(req)))
	}
	resp, err := p.Generate(ctx, t.Model, req)
	if err == nil && strings.TrimSpace(resp.Text) == "" && len(resp.Logprobs) == 0 {
		err = errors.New("модель вернула пустой ответ")
	}
	call.Resp, call.Err = resp, err
	if r.OnCall != nil {
		r.OnCall(call)
	}
	if !req.Quiet && r.Log != nil {
		reply := ""
		if err != nil {
			reply = "ошибка: " + err.Error()
		} else {
			reply = resp.Text
			if resp.CostUSD > 0 {
				reply += fmt.Sprintf(" [$%.5f]", resp.CostUSD)
			}
		}
		r.Log(req.UserID, exchangeLine("←", t.Model, reply))
	}
	return resp, err
}

func requestSummary(req *Request) string {
	prefix := ""
	if !req.Think {
		prefix = "[без размышлений] "
	}
	if n := len(req.Images); n > 0 {
		prefix += fmt.Sprintf("[изображений: %d] ", n)
	}
	return prefix + req.Prompt
}

// exchangeLine — строка журнала обмена: «[ИИ → модель] текст» (сайт показывает её в консоли).
func exchangeLine(dir, model, text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if rs := []rune(text); len(rs) > 1500 {
		text = string(rs[:1497]) + "..."
	}
	return "[ИИ " + dir + " " + model + "] " + text
}

func (r *Router) noteSuccess(role Role) {
	r.mu.Lock()
	r.timeouts[role] = 0
	r.mu.Unlock()
}

// noteFailure: MainModelSlowTimeouts таймаутов основной локальной модели подряд — роль на 6 часов уходит в резерв.
func (r *Router) noteFailure(role Role, err error, primary, fallback Target) {
	if primary.Provider != "ollama" || fallback.Empty() || !strings.Contains(err.Error(), "timed out") {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.timeouts[role]++
	if r.timeouts[role] >= 3 {
		r.slowTill[role] = time.Now().Add(6 * time.Hour)
		r.timeouts[role] = 0
		go r.Ollama.Unload(context.Background(), primary.Model)
	}
}

// Embed — эмбеддинги роли embed.
func (r *Router) Embed(ctx context.Context, texts []string) ([][]float64, error) {
	primary, fallback, _ := r.Resolve(ctx, RoleEmbed)
	for _, t := range []Target{primary, fallback} {
		if t.Empty() {
			continue
		}
		p, err := r.provider(t)
		if err != nil {
			continue
		}
		if out, err := p.Embed(ctx, t.Model, texts); err == nil {
			return out, nil
		}
	}
	return nil, errors.New("эмбеддинги недоступны")
}

// AnalysisThreshold — порог «по теме» для модели, которая сейчас делает анализ.
func (r *Router) AnalysisThreshold(ctx context.Context, def int) int {
	s := r.Settings()
	rc := s.Roles[RoleAnalysis]
	p, _, _ := r.Resolve(ctx, RoleAnalysis)
	if rc.Threshold > 0 && p == rc.Primary && r.blocked(p, &Request{}, s) == "" {
		return rc.Threshold
	}
	return def
}

// ModelFor — какая модель сейчас выполняет роль (для журнала и админки).
func (r *Router) ModelFor(ctx context.Context, role Role) string {
	p, _, _ := r.Resolve(ctx, role)
	return p.Model
}

// Enabled — есть ли модель у роли.
func (r *Router) Enabled(ctx context.Context, role Role) bool {
	p, f, _ := r.Resolve(ctx, role)
	return !p.Empty() || !f.Empty()
}

// ThresholdForModel — порог «по теме» для оценки, которую дала эта модель (кэшированные оценки старой модели — общий порог).
func (r *Router) ThresholdForModel(model string, def int) int {
	rc := r.Settings().Roles[RoleAnalysis]
	if rc.Threshold > 0 && model != "" && model == rc.Primary.Model {
		return rc.Threshold
	}
	return def
}
