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

type callGateKey struct{}
type callReportKey struct{}
type autopilotFreeFallbackKey struct{}
type autopilotPaidNemotronFallbackKey struct{}

// WithSourceAutopilotPaidNemotronFallback permits a small paid Nemotron
// reserve ONLY after the public autopilot encounters a free-model rate limit.
// The caller must also install the database-backed paid spend gate.
func WithSourceAutopilotPaidNemotronFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, autopilotPaidNemotronFallbackKey{}, true)
}

// WithSourceAutopilotFreeFallback allows a single no-charge OpenRouter free
// router attempt when a configured OpenRouter model returned HTTP 403.
// This opt-in is used only by the public source-autopilot worker, never by
// ordinary chat, publication or private requests.
func WithSourceAutopilotFreeFallback(ctx context.Context) context.Context {
	return context.WithValue(ctx, autopilotFreeFallbackKey{}, true)
}

// WithCallReport receives the actual provider-reported cost for each attempt.
// It is scoped to the request context and is called even when the provider
// returns an error. A nil response reports zero (cost is unknown).
func WithCallReport(ctx context.Context, report func(context.Context, Target, *Response, error) error) context.Context {
	return context.WithValue(ctx, callReportKey{}, report)
}

func reportCall(ctx context.Context, target Target, resp *Response, err error) error {
	if report, ok := ctx.Value(callReportKey{}).(func(context.Context, Target, *Response, error) error); ok && report != nil {
		return report(ctx, target, resp, err)
	}
	return nil
}

// WithCallGate checks every actual provider attempt, including fallbacks.
// A failed check prevents the provider call; existing router limits still apply.
func WithCallGate(ctx context.Context, gate func(context.Context, Target) error) context.Context {
	return context.WithValue(ctx, callGateKey{}, gate)
}

func checkCallGate(ctx context.Context, target Target) error {
	if gate, ok := ctx.Value(callGateKey{}).(func(context.Context, Target) error); ok && gate != nil {
		return gate(ctx, target)
	}
	return nil
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
	ProviderOrder []string `json:"provider_order,omitempty"`
	Primary       Target   `json:"primary"`
	Fallback      Target   `json:"fallback"`
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
//	free   — бесплатная модель OpenRouter, без обязательной локальной модели и повторного быстрого анализа;
//	local  — всё на Ollama;
//	hybrid — отсев локально, остальное — дешёвые платные модели (нужен бюджет);
//	cloud  — запросы в OpenRouter, без локальных моделей.
func Preset(name string) Settings {
	s := Settings{Preset: name, Roles: map[Role]RoleConfig{}, DailyBudgetUSD: 0, FreeDailyLimit: 1000}
	set := func(r Role, p, f Target) { s.Roles[r] = RoleConfig{Primary: p, Fallback: f} }
	set(RoleClassify, Off, Off)
	set(RoleClassifyVerify, Off, Off)
	switch name {
	case "free":
		set(RoleClassify, FreeVision, Off)
		// Avoid buying a second inference for the same post. The collector
		// proceeds to its full analysis when the quick local screen is off.
		set(RolePrescreen, Off, Off)
		s.Roles[RoleAnalysis] = RoleConfig{Primary: FreeVision, Fallback: Off, Threshold: 65}
		set(RoleJudge, FreeVision, Off)
		set(RoleSmart, FreeVision, Off)
		set(RoleText, FreeVision, Off)
		set(RoleRadar, FreeVision, Off)
		set(RoleTranslate, Off, Off)
		set(RoleEmbed, Off, Off)
	case "cloud":
		set(RoleClassify, Target{"openrouter", "nvidia/nemotron-3.5-super-vl"}, Off)
		set(RoleClassifyVerify, CloudSmart, Off)
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

// Normalize fills missing roles from the free preset, migrating only exact
// legacy defaults. Explicit custom model choices remain unchanged.
func (s Settings) Normalize() Settings {
	base := Preset("free")
	roles := make(map[Role]RoleConfig, len(s.Roles))
	for role, cfg := range s.Roles {
		roles[role] = cfg
	}
	s.Roles = roles
	if s.Preset == "free" {
		for _, role := range []Role{RoleAnalysis, RoleJudge, RoleSmart, RoleText, RoleRadar} {
			cfg := s.Roles[role]
			if cfg.Primary == FreeVision && cfg.Fallback == LocalSmart {
				cfg.Fallback = Off
				s.Roles[role] = cfg
			}
		}
		for role, legacy := range map[Role]Target{RolePrescreen: LocalFast, RoleEmbed: LocalEmbed} {
			cfg := s.Roles[role]
			if cfg.Primary == legacy && cfg.Fallback.Empty() {
				cfg.Primary, cfg.Fallback = Off, Off
				s.Roles[role] = cfg
			}
		}
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
	// ReserveFree persists the attempt before sending it to the provider.
	// It returns the authoritative UTC daily count, or denies the attempt.
	ReserveFree func(context.Context, string, int) (int, error)
	// OnCall — после каждого вызова (журнал, ClickHouse, учёт расходов).
	OnCall func(Call)
	// Log — строки в журнал сборщика или ответа API.
	Log func(userID, line string)

	classificationSlots   chan struct{}
	paidOnce              sync.Once
	paidSlots             chan struct{}
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
	r := &Router{classificationSlots: make(chan struct{}, 1), Ollama: ol, OpenRouter: or, timeouts: map[Role]int{}, slowTill: map[Role]time.Time{}}
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
	if req.FreeOnly && !t.Free() {
		return "для запроса разрешены только бесплатные модели"
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
	if s.FreeDailyLimit > 0 && r.freeDay == time.Now().UTC().Format("2006-01-02") && r.freeUsed >= s.FreeDailyLimit {
		return "достигнут заданный лимит бесплатных запросов"
	}
	return ""
}

func (r *Router) countFree(t Target, err error) {
	if t.Provider != "openrouter" || !t.Free() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

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
		if !strings.Contains(err.Error(), "per-min") && !strings.Contains(err.Error(), "tpm_rate_limit_exceeded") {
			pause = 15 * time.Minute
		}
		r.freeBlocked[t.Model] = time.Now().Add(pause)
	}
}

// reserveFreeAttempt holds the local lock across the durable reservation.
// Failed transport attempts remain counted; a rejected call gate does not
// consume the provider quota. A zero configured limit retains unlimited mode.
func (r *Router) reserveFreeAttempt(ctx context.Context, t Target, limit int) error {
	if t.Provider != "openrouter" || !t.Free() {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	day := time.Now().UTC().Format("2006-01-02")
	if r.freeDay != day {
		r.freeDay, r.freeUsed = day, 0
	}
	if limit > 0 && r.freeUsed >= limit {
		return errors.New("достигнут заданный лимит бесплатных запросов")
	}
	if r.ReserveFree != nil {
		count, err := r.ReserveFree(ctx, day, limit)
		if err != nil {
			return err
		}
		r.freeUsed = count
	} else {
		r.freeUsed++
	}
	return nil
}

// SetFreeUsed — сколько бесплатных запросов уже сделано сегодня (восстановление после перезапуска).
func (r *Router) SetFreeUsed(n int) {
	r.mu.Lock()
	r.freeDay, r.freeUsed = time.Now().UTC().Format("2006-01-02"), n
	r.mu.Unlock()
}

// FreeUsed — бесплатных запросов за сегодня.
func (r *Router) FreeUsed() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.freeDay != time.Now().UTC().Format("2006-01-02") {
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
	// Classification sweeps must not burst into a shared free-provider pool.
	if (req.Role == RoleClassify || req.Role == RoleClassifyVerify) && r.classificationSlots != nil {
		select {
		case r.classificationSlots <- struct{}{}:
			defer func() { <-r.classificationSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
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
	forbiddenOpenRouter := false
	freeRateLimited := false
	nemotronFreeSelected := false
	for _, candidate := range cands {
		if candidate.Provider == "openrouter" && candidate.Model == "nvidia/nemotron-3.5-super-vl:free" {
			nemotronFreeSelected = true
		}
	}
	for i, t := range cands {
		if why := r.blocked(t, req, s); why != "" {
			if t.Provider == "openrouter" && t.Free() && (strings.Contains(why, "429") || strings.Contains(why, "суточного лимита")) {
				freeRateLimited = true
			}
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
		if t.Provider == "openrouter" && err != nil && strings.Contains(err.Error(), "HTTP 403") {
			forbiddenOpenRouter = true
		}
		if t.Provider == "openrouter" && t.Free() && err != nil &&
			!strings.Contains(err.Error(), "HTTP 401") &&
			(strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "HTTP 403") ||
				strings.Contains(strings.ToLower(err.Error()), "rate limit") || strings.Contains(err.Error(), "free-models-per-day")) {
			// Free-model access may fail with HTTP 403 as well as 429.
			// The paid reserve remains scoped to the public autopilot, the
			// selected free Nemotron, and the independently enforced spend gate.
			freeRateLimited = true
		}
		if ctx.Err() != nil {
			break
		}
	}
	// Some account/model-specific 403s do not affect the free router. This
	// attempt costs $0 and goes through the same per-attempt call gate,
	// budget ledger and structured-output validation as the selected model.
	// No paid fallback or automatic approval is authorized by this path.
	if allow, _ := ctx.Value(autopilotFreeFallbackKey{}).(bool); allow && forbiddenOpenRouter && !req.Private && ctx.Err() == nil {
		free := Target{Provider: "openrouter", Model: "openrouter/free"}
		alreadyTried := false
		for _, candidate := range cands {
			if candidate == free {
				alreadyTried = true
				break
			}
		}
		if !alreadyTried && r.OpenRouter != nil && r.OpenRouter.Configured() && r.blocked(free, req, s) == "" {
			logf("-> выбранная модель вернула 403; пробую бесплатный резерв OpenRouter.")
			resp, err := r.try(ctx, req, free, true)
			if err == nil {
				return resp, nil
			}
			lastErr = err
		}
	}
	// Use the SAME multimodal Nemotron on its paid endpoint after the free
	// variant is rate-limited. Never use it for ordinary tasks, private requests,
	// FreeOnly requests, or when the scoped database spend gate is exhausted.
	if allow, _ := ctx.Value(autopilotPaidNemotronFallbackKey{}).(bool); allow && hasCallGate(ctx) && freeRateLimited && nemotronFreeSelected && !req.Private && !req.FreeOnly && ctx.Err() == nil &&
		r.OpenRouter != nil && r.OpenRouter.Configured() {
		paid := Target{Provider: "openrouter", Model: "nvidia/nemotron-3.5-super-vl"}
		logf("-> бесплатный Nemotron ограничен; пробую платную версию в пределах бюджета автопилота.")
		if resp, err := r.tryWithPaidReserve(ctx, req, paid, true, true); err == nil {
			return resp, nil
		} else {
			lastErr = err
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
	return r.tryWithPaidReserve(ctx, req, t, isFallback, false)
}

func hasCallGate(ctx context.Context) bool {
	gate, ok := ctx.Value(callGateKey{}).(func(context.Context, Target) error)
	return ok && gate != nil
}

// Serialize paid calls per router. Recheck spend after waiting, and keep the
// slot until the provider's cost has been recorded. This limits overshoot to
// one in-flight charge per process; an exact prepaid cap still needs the
// provider's own spending limit, because failed calls can have unknown cost.
func (r *Router) tryWithPaidReserve(ctx context.Context, req *Request, t Target, isFallback, scopedReserve bool) (*Response, error) {
	if t.Provider == "openrouter" && !t.Free() {
		r.paidOnce.Do(func() { r.paidSlots = make(chan struct{}, 1) })
		select {
		case r.paidSlots <- struct{}{}:
			defer func() { <-r.paidSlots }()
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if scopedReserve {
			if !hasCallGate(ctx) || req.Private || req.FreeOnly {
				return nil, errors.New("платный резерв требует проверки отдельного бюджета")
			}
		} else if why := r.blocked(t, req, r.Settings()); why != "" {
			return nil, errors.New(why)
		}
	}
	p, err := r.provider(t)
	if err != nil {
		return nil, err
	}
	// Both configured and scoped paid attempts must honor a caller's
	// classification budget. Reserve inside the serialized provider boundary
	// so blocked or unconfigured targets never reserve classification funds.
	var settle func(float64)
	if !t.Free() && req.ReservePaid != nil {
		var ok bool
		settle, ok = req.ReservePaid()
		if !ok {
			return nil, errors.New("исчерпан бюджет классификации")
		}
	}
	if err := checkCallGate(ctx, t); err != nil {
		return nil, err
	}
	if err := r.reserveFreeAttempt(ctx, t, r.Settings().FreeDailyLimit); err != nil {
		return nil, err
	}
	call := Call{Role: req.Role, Target: t, Req: req, Fallback: isFallback, Started: time.Now()}
	if !req.Quiet && r.Log != nil {
		r.Log(req.UserID, exchangeLine("→", t.Model, requestSummary(req, t)))
	}
	resp, err := p.Generate(ctx, t.Model, req)
	r.countFree(t, err)
	if err == nil && strings.TrimSpace(resp.Text) == "" && len(resp.Logprobs) == 0 {
		err = errors.New("модель вернула пустой ответ")
	}
	call.Resp, call.Err = resp, err
	// Record spend before delivering the response. This cannot undo a charge
	// already incurred at the provider, but blocks subsequent requests.
	if reportErr := reportCall(ctx, t, resp, err); reportErr != nil && err == nil {
		err = reportErr
		call.Err = err
	}
	if r.OnCall != nil {
		r.OnCall(call)
	}
	if err == nil && settle != nil {
		settle(resp.CostUSD)
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

func requestSummary(req *Request, target Target) string {
	prefix := ""
	if !req.Think && !(target.Provider == "openrouter" && req.Role == RoleClassify && strings.HasPrefix(target.Model, "baidu/ernie-5.5")) {
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

// SourceEmbeddingTarget is deliberately independent of the legacy local
// embedding role. Public autopilot never starts Ollama.
func (r *Router) SourceEmbeddingTarget() Target {
	if !r.OpenRouter.Configured() {
		return Off
	}
	return Target{"openrouter", "nvidia/nemotron-3.5-embed-1b:free"}
}

// EmbedSource uses only the bounded OpenRouter route, with no paid fallback.
func (r *Router) EmbedSource(ctx context.Context, target Target, texts []string) ([][]float64, error) {
	if target.Empty() || r.SourceEmbeddingTarget() != target {
		return nil, errors.New("OpenRouter source embedding unavailable")
	}
	if reason := r.blocked(target, &Request{FreeOnly: true}, r.Settings()); reason != "" {
		return nil, errors.New(reason)
	}
	limit := r.Settings().FreeDailyLimit
	if limit > 0 {
		limit = limit * 8 / 10
		if limit == 0 {
			return nil, errors.New("free quota reserved for content evaluation")
		}
	}
	if err := r.reserveFreeAttempt(ctx, target, limit); err != nil {
		return nil, err
	}
	started := time.Now()
	vectors, err := r.OpenRouter.embedSource(ctx, target.Model, texts)
	r.countFree(target, err)
	if r.OnCall != nil {
		r.OnCall(Call{Role: RoleEmbed, Target: target, Started: started, Err: err})
	}
	return vectors, err
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
		settings := r.Settings()
		if r.blocked(t, &Request{}, settings) != "" {
			continue
		}
		if err := checkCallGate(ctx, t); err != nil {
			return nil, err
		}
		if err := r.reserveFreeAttempt(ctx, t, settings.FreeDailyLimit); err != nil {
			return nil, err
		}
		started := time.Now()
		out, err := p.Embed(ctx, t.Model, texts)
		r.countFree(t, err)
		if r.OnCall != nil {
			r.OnCall(Call{Role: RoleEmbed, Target: t, Started: started, Err: err})
		}
		if err == nil {
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
