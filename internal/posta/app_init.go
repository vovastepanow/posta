package posta

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/jx"
	"posta/internal/llm"
	"posta/internal/secret"
)

// New — ресурсы процесса: Postgres (обязателен), ClickHouse (если задан), ключ шифрования, модели, каталог.
func New(ctx context.Context, cfg config.Server) (*App, error) {
	d, err := db.Open(ctx, cfg.PostgresDSN)
	if err != nil {
		return nil, fmt.Errorf("Postgres: %w", err)
	}
	box, err := secret.Load(cfg.SecretKeyFile, true)
	if err != nil {
		d.Close()
		return nil, err
	}
	var sink *ch.Sink
	if cfg.ClickHouse != "" {
		if sink, err = ch.Open(ctx, cfg.ClickHouse, cfg.CHDatabase); err != nil {
			log.Printf("ClickHouse недоступен (%v): аналитика не пишется, приложение работает", err)
			sink = nil
		} else if err := sink.Migrate(ctx); err != nil {
			log.Printf("ClickHouse: схема: %v", err)
		}
	}
	a := &App{Cfg: cfg, DB: d, CH: sink, Box: box, Mem: NewMemCache(4000), HTTP: defaultClient}
	a.ctx, a.cancel = context.WithCancel(context.Background())
	a.catalog = &Catalog{app: a}
	a.parsers = newParserManager(a)
	a.LLM = llm.NewRouter(llm.NewOllama(cfg.OllamaURL), llm.NewOpenRouter(cfg.OpenRouterURL, cfg.OpenRouterKey, cfg.OpenRouterTitle, cfg.PublicURL))
	a.LLM.LoadSettings = a.LLMSettings
	a.LLM.Spent = a.LLMSpentToday
	a.LLM.OnCall = a.onLLMCall
	a.LLM.Log = func(uid, line string) {
		if uid != "" && a.ParserRunning(uid) {
			a.ParserLogger(uid)(line)
		}
	}
	a.LLM.SetFreeUsed(KV(a, ctx, "llm_free_used:"+time.Now().UTC().Format("2006-01-02"), 0))
	for _, dir := range []string{cfg.DataDir, a.UsersDir(), cfg.MediaDir} {
		os.MkdirAll(dir, 0o775)
	}
	if err := a.LoadCatalog(ctx); err != nil {
		a.Close()
		return nil, fmt.Errorf("каталог: %w", err)
	}
	if err := a.MigrateSourceRegistry(ctx); err != nil {
		log.Printf("Реестр источников: перенос не выполнен, повторится при следующем запуске: %v", err)
	}
	return a, nil
}

// Close — остановить фоновые задачи и сборщики, дописать ClickHouse.
func (a *App) Close() {
	if a.cancel != nil {
		a.cancel()
	}
	if a.parsers != nil {
		a.StopAllParsers()
	}
	done := make(chan struct{})
	go func() { a.bg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second):
		log.Print("фоновые задачи не остановились за 15 с")
	}
	a.CH.Close()
	a.DB.Close()
}

// StartBackground — фоновые задачи сервера (бывшие cron-скрипты).
func (a *App) StartBackground() {
	a.Go("publisher", a.RunPublisher)
	a.Go("video-worker", func(ctx context.Context) { a.KickVideoWorker() })
	a.Every("cache-gc", time.Hour, func(ctx context.Context) {
		a.CacheGC(ctx)
		a.ThumbGC(ctx)
		a.UsersGC(ctx)
		a.DB.Pool.Exec(ctx, `DELETE FROM login_requests WHERE created_at < $1`, now()-86400)
		a.DB.Pool.Exec(ctx, `DELETE FROM login_failures WHERE at < $1`, now()-86400)
		a.DB.Pool.Exec(ctx, `DELETE FROM post_usage WHERE at < $1`, now()-2*86400)
		a.DB.Pool.Exec(ctx, `DELETE FROM telegram_lead_queue q WHERE status = 'processed' AND processed_at < now() - interval '30 days'
			AND NOT EXISTS (SELECT 1 FROM telegram_leads l WHERE l.queue_id = q.id)`)
	})
	a.Every("outbox-settle", time.Minute, func(ctx context.Context) {
		rows, err := a.DB.Pool.Query(ctx, `SELECT DISTINCT user_id FROM outbox WHERE status = 'telegram' AND publish_at < $1`, now()-120)
		if err != nil {
			return
		}
		var ids []string
		for rows.Next() {
			var id string
			rows.Scan(&id)
			ids = append(ids, id)
		}
		rows.Close()
		for _, id := range ids {
			a.User(ctx, id, nil).TelegramSettleNativePosts()
		}
	})
	a.Every("radar", 5*time.Minute, func(ctx context.Context) {
		if uid := a.RadarAccountUID(ctx); uid != "" {
			s := a.RadarSettings(ctx, uid)
			if jx.Bool(s, "enabled") && now()-jx.Int(s, "last_run") > 600 {
				a.RadarRun(ctx, uid, nil)
			}
		}
	})
	a.Every("llm-models", 6*time.Hour, func(ctx context.Context) {
		if a.Cfg.OpenRouterKey != "" {
			a.LLM.OpenRouter.Models(ctx)
		}
	})
}

// ---- Настройки моделей (админка → «Модели») ----

// LLMSettings — настройки из kv admin:llm (кэш 15 с); по умолчанию — бесплатный пресет.
func (a *App) LLMSettings() llm.Settings {
	a.llmMu.Lock()
	defer a.llmMu.Unlock()
	if time.Since(a.llmReadAt) < 15*time.Second && a.llmSettings.Roles != nil {
		return a.llmSettings
	}
	s := llm.Preset("free")
	a.KVGet(a.ctxOrBg(), "admin:llm", &s)
	a.llmSettings, a.llmReadAt = s.Normalize(), time.Now()
	return a.llmSettings
}

func (a *App) LLMSettingsSave(ctx context.Context, s llm.Settings) error {
	s = s.Normalize()
	if s.DailyBudgetUSD < 0 || s.DailyBudgetUSD > 50 {
		return Bad("Дневной бюджет — от 0 до 50 долларов.")
	}
	if s.FreeDailyLimit < 0 || s.FreeDailyLimit > 100000 {
		return Bad("Лимит бесплатных запросов — от 0 до 100000.")
	}
	for role, rc := range s.Roles {
		for _, t := range []llm.Target{rc.Primary, rc.Fallback} {
			if !contains([]string{"", "off", "ollama", "openrouter"}, t.Provider) {
				return Bad("Роль %s: неизвестный поставщик %s.", role, t.Provider)
			}
			if t.Provider == "openrouter" && a.Cfg.OpenRouterKey == "" {
				return Bad("Ключ OpenRouter не задан на сервере (OPENROUTER_API_KEY в posta.env).")
			}
		}
		if rc.Threshold < 0 || rc.Threshold > 100 {
			return Bad("Порог — от 0 до 100.")
		}
	}
	if err := a.KVSet(ctx, "admin:llm", s, 0); err != nil {
		return err
	}
	a.llmMu.Lock()
	a.llmReadAt = time.Time{}
	a.llmMu.Unlock()
	return nil
}

func spendKey() string { return "llm_spent:" + time.Now().UTC().Format("2006-01-02") }

// LLMSpentToday — потрачено в OpenRouter за сутки (UTC), долл.
func (a *App) LLMSpentToday() float64 {
	a.spentMu.Lock()
	defer a.spentMu.Unlock()
	day := time.Now().UTC().Format("2006-01-02")
	if a.spentDay != day {
		a.spentDay = day
		a.spentUSD = KV(a, a.ctxOrBg(), spendKey(), 0.0)
	}
	return a.spentUSD
}

func (a *App) onLLMCall(c llm.Call) {
	rec := ch.AICall{TS: c.Started, Provider: c.Target.Provider, Model: c.Target.Model, Role: string(c.Role), OK: c.Err == nil,
		LatencyMS: int(time.Since(c.Started).Milliseconds())}
	if c.Req != nil {
		rec.UserID, rec.Topic, rec.Images = c.Req.UserID, c.Req.Topic, len(c.Req.Images)
	}
	if c.Err != nil {
		rec.Error = trimRunes(c.Err.Error(), 300)
	}
	if r := c.Resp; r != nil {
		rec.PromptTokens, rec.CompletionTokens, rec.CachedTokens, rec.CostUSD = r.PromptTokens, r.CompletionTokens, r.CachedTokens, r.CostUSD
	}
	a.CH.AICall(rec)
	ctx := a.ctxOrBg()
	if rec.CostUSD > 0 {
		a.LLMSpentToday()
		a.spentMu.Lock()
		a.spentUSD += rec.CostUSD
		total := a.spentUSD
		a.spentMu.Unlock()
		a.KVSet(ctx, spendKey(), total, 3*24*time.Hour)
	}
	if c.Target.Provider == "openrouter" && c.Target.Free() {
		a.KVSet(ctx, "llm_free_used:"+time.Now().UTC().Format("2006-01-02"), a.LLM.FreeUsed(), 3*24*time.Hour)
	}
}

// LLMPublic — настройки, расход и доступные модели для админки.
func (a *App) LLMPublic(ctx context.Context) jx.M {
	s := a.LLMSettings()
	roles := []jx.M{}
	for _, r := range llm.Roles {
		rc := s.Roles[r]
		roles = append(roles, jx.M{"role": r, "title": llm.RoleTitles[r], "primary": rc.Primary, "fallback": rc.Fallback, "threshold": rc.Threshold})
	}
	out := jx.M{"settings": s, "roles": roles, "spent_today": a.LLMSpentToday(), "free_used": a.LLM.FreeUsed(), "openrouter_key": a.Cfg.OpenRouterKey != "",
		"presets": []jx.M{{"id": "free", "title": "Бесплатно", "note": "Отсев на своём компьютере, анализ — бесплатная модель OpenRouter, резерв — Ollama."},
			{"id": "local", "title": "Только свой компьютер", "note": "Всё на Ollama: бесплатно, но медленно на слабой видеокарте."},
			{"id": "hybrid", "title": "Гибрид", "note": "Отсев локально, остальное — дешёвые платные модели (нужен бюджет)."},
			{"id": "cloud", "title": "Облако", "note": "Всё в OpenRouter на платных моделях, резерв — Ollama."}}}
	out["ollama_models"] = orStrs(a.LLM.Ollama.Installed(ctx))
	if a.Cfg.OpenRouterKey != "" {
		out["openrouter_models"] = a.LLM.OpenRouter.SortedModels(ctx, false)
		if total, used, err := a.LLM.OpenRouter.Credits(ctx); err == nil {
			out["openrouter_credits"] = jx.M{"total": total, "used": used}
		}
	}
	return out
}

var _ = http.StatusOK
