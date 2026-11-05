package posta

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/jx"
)

// Сборщик пользователя — горутина сервера (раньше — процесс `php cron_parser.php`). Вкладка при открытии запускает его,
// сигнал вкладки раз в 5 с держит сессию; закрытие вкладки сборщик не останавливает: он доработает проход сам.

const (
	TabSessionTTL         = 150 * time.Second
	parserQueueMax        = 10 * time.Minute
	parserExpectedSeconds = 1200
	ParserSessionEnded    = "Окно приложения закрыто или открыто заново — работа остановлена."
	parserLogLines        = 400
)

// Run — состояние одного прохода сборщика (бывшие $GLOBALS).
type Run struct {
	TextAnalysesLeft     int
	TextAnalysesDeferred int
	SourceMisses         map[string]int
	HotStats             jx.M
	FourchanAPI          int
	FourchanAPICached    int
	FourchanMedia        int
	NewsEnrichLeft       int
	DiscoveryCheap       map[string]map[string]int
	FixedTopic           bool // CLI: тема зафиксирована, смена настроек не прерывает подбор
}

func newRun(a *App) *Run {
	return &Run{TextAnalysesLeft: a.TInt("text_per_pass"), SourceMisses: map[string]int{}, NewsEnrichLeft: NewsEnrichPerPass, DiscoveryCheap: map[string]map[string]int{}}
}

type userParser struct {
	mu        sync.Mutex
	running   bool
	paused    bool // остановлен администратором до нового открытия вкладки
	cancel    context.CancelFunc
	startedAt time.Time
	tab       string
	tabSeen   time.Time
	tabOpened time.Time
	phase     string
	progress  jx.M
	lines     []string
	stage     string      // текущий шаг сборщика (ключ для журнала студии); задаёт ParserStage
	lineStage []string    // шаг, который был текущим в момент записи строки (параллельно lines)
	lineAt    []time.Time // время записи строки (параллельно lines)
	file      *os.File
}

// ParserManager — сборщики всех пользователей процесса.
type ParserManager struct {
	app          *App
	mu           sync.Mutex
	users        map[string]*userParser
	slots        chan struct{}
	named        sync.Map // блокировки по имени: подбор темы, уточнение
	sharedPaused bool
	sharedCancel context.CancelFunc
	sharedDone   chan struct{}
}

func newParserManager(a *App) *ParserManager {
	return &ParserManager{app: a, users: map[string]*userParser{}, slots: make(chan struct{}, 64)}
}

func (m *ParserManager) user(uid string) *userParser {
	m.mu.Lock()
	defer m.mu.Unlock()
	p := m.users[uid]
	if p == nil {
		p = &userParser{progress: jx.M{}}
		m.users[uid] = p
	}
	return p
}

func (a *App) namedLock(name string) *sync.Mutex {
	l, _ := a.parsers.named.LoadOrStore(name, &sync.Mutex{})
	return l.(*sync.Mutex)
}

var logSpaceRe = regexp.MustCompile(`\s+`)

// ParserLogger — запись в журнал сборщика пользователя: консоль сайта (память и файл) и ClickHouse.
func (a *App) ParserLogger(uid string) func(string) {
	p := a.parsers.user(uid)
	return func(line string) {
		line = strings.TrimRight(line, "\n")
		p.mu.Lock()
		p.lines = append(p.lines, line)
		p.lineStage = append(p.lineStage, p.stage)
		p.lineAt = append(p.lineAt, time.Now())
		if len(p.lines) > parserLogLines {
			cut := len(p.lines) - parserLogLines
			p.lines, p.lineStage, p.lineAt = p.lines[cut:], p.lineStage[cut:], p.lineAt[cut:]
		}
		if p.file != nil {
			p.file.WriteString(line + "\n")
		}
		p.mu.Unlock()
		if strings.TrimSpace(line) != "" {
			a.CH.Log(ch.LogLine{TS: time.Now(), UserID: uid, Line: line})
		}
		if a.Cfg.Dev {
			log.Printf("[%s] %s", uid[:min(6, len(uid))], line)
		}
	}
}

// ParserActivity — последние строки журнала для консоли сайта.
func (a *App) ParserActivity(uid string) []string {
	p := a.parsers.user(uid)
	p.mu.Lock()
	lines := append([]string{}, p.lines...)
	p.mu.Unlock()
	if len(lines) == 0 { // после перезапуска сервера — из файла
		if b, err := os.ReadFile(filepath.Join(a.UserDir(uid), "parser-v3.log")); err == nil {
			all := strings.Split(string(b), "\n")
			lines = all[max(0, len(all)-parserLogLines):]
		}
	}
	out := []string{}
	for _, l := range lines {
		if l = strings.TrimSpace(logSpaceRe.ReplaceAllString(l, " ")); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// ParserStage — шаг сборщика для журнала студии: collect, filter, prescreen, analysis, pool, discovery.
func (u *U) ParserStage(key string) {
	p := u.parsers.user(u.ID)
	p.mu.Lock()
	p.stage = key
	p.mu.Unlock()
}

// ParserJournal — последние n строк журнала с шагом и временем: {t — текст, s — шаг, at — HH:MM}.
// Строки из файла после перезапуска сервера без шага и времени.
func (a *App) ParserJournal(uid string, n int) []jx.M {
	p := a.parsers.user(uid)
	p.mu.Lock()
	lines := append([]string{}, p.lines...)
	stages := append([]string{}, p.lineStage...)
	ats := append([]time.Time{}, p.lineAt...)
	p.mu.Unlock()
	if len(lines) == 0 { // после перезапуска сервера — из файла, без шага и времени
		lines = a.ParserActivity(uid)
		stages, ats = make([]string, len(lines)), make([]time.Time, len(lines))
	}
	out := []jx.M{}
	for i, l := range lines {
		l = strings.TrimSpace(logSpaceRe.ReplaceAllString(l, " "))
		if l == "" {
			continue
		}
		row := jx.M{"t": l, "s": stages[i], "at": ""}
		if !ats[i].IsZero() {
			row["at"] = ats[i].Format("15:04")
		}
		out = append(out, row)
	}
	return out[max(0, len(out)-n):]
}

// ParserStageOf — текущий шаг сборщика (пусто — сборщик не в работе или шаг не задан).
func (a *App) ParserStageOf(uid string) string {
	p := a.parsers.user(uid)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stage
}

func (u *U) ParserPhase(text string) {
	p := u.parsers.user(u.ID)
	p.mu.Lock()
	p.phase = text
	p.mu.Unlock()
}

func (a *App) parserPhaseOf(uid string) string {
	p := a.parsers.user(uid)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.phase
}

// ParserProgress — ход сбора для полосы прогресса; reset — новый проход.
func (u *U) ParserProgress(update jx.M, reset bool) {
	p := u.parsers.user(u.ID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if reset {
		p.progress = jx.M{}
	}
	for k, v := range update {
		p.progress[k] = v
	}
}

func (a *App) ParserRunning(uid string) bool {
	p := a.parsers.user(uid)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.running
}

func (a *App) ParserPaused(uid string) bool {
	p := a.parsers.user(uid)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.paused
}

func (a *App) ParserRuntimeSeconds(uid string) *int64 {
	p := a.parsers.user(uid)
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.running {
		return nil
	}
	s := int64(time.Since(p.startedAt).Seconds())
	return &s
}

// ParserProgressPublic — share (0–1, nil — очередь собирается), eta, added.
func (a *App) ParserProgressPublic(uid string) jx.M {
	p := a.parsers.user(uid)
	p.mu.Lock()
	pr := jx.Copy(p.progress)
	running, started := p.running, p.startedAt
	p.mu.Unlock()
	t := now()
	maxSec := int64(a.TInt("parser_minutes") * 60)
	if jx.Bool(pr, "shared_wait") {
		return jx.M{"share": nil, "eta": nil, "added": 0, "queued": true}
	}
	if !jx.Bool(pr, "queued_at") && !jx.Bool(pr, "deadline") && running {
		pr["deadline"] = started.Unix() + maxSec
	}
	if !jx.Bool(pr, "deadline") {
		return jx.M{"share": nil, "eta": nil, "added": 0, "queued": jx.Bool(pr, "queued_at"), "funnel": pr["funnel"]}
	}
	leftCap := max(0, jx.Int(pr, "deadline")-t)
	var share float64
	var eta int64
	have := false
	if q := jx.Int(pr, "queue_at"); q > 0 {
		ratio := func(x, y float64) float64 {
			if y > 0 {
				return min(1, x/y)
			}
			return 0
		}
		s := 1.0
		if jx.Int(pr, "total") != 0 {
			s = max(ratio(jx.Float(pr, "done"), jx.Float(pr, "total")), ratio(jx.Float(pr, "analyzed"), float64(a.TInt("analyze_per_run"))), ratio(jx.Float(pr, "added"), jx.Float(pr, "need")))
		}
		spent := t - q
		if s >= 1 {
			share, eta, have = 1, 0, true
		} else if s > 0 && spent >= 5 {
			share, eta, have = s, int64(float64(spent)*(1-s)/s+0.5), true
		}
	}
	if !have {
		expected := a.parserExpectedSeconds(uid, 0)
		elapsed := t - (jx.Int(pr, "deadline") - maxSec)
		expected = max(expected, int64(float64(elapsed)/0.9))
		share, eta = float64(elapsed)/float64(max(1, expected)), max(30, expected-elapsed)
	}
	return jx.M{"share": float64(int(share*1000+0.5)) / 1000, "eta": min(eta, leftCap), "added": jx.Int(pr, "added"), "funnel": pr["funnel"]}
}

// parserExpectedSeconds — медиана последних 5 проходов; finished > 0 — записать новый.
func (a *App) parserExpectedSeconds(uid string, finished int64) int64 {
	key := "parser_durations:" + uid
	var list []int64
	if finished > 0 {
		list, _ = KVUpdate(a, a.ctxOrBg(), key, 0, func(l []int64, _ bool) *[]int64 {
			l = append(l, finished)
			l = l[max(0, len(l)-5):]
			return &l
		})
	} else {
		list = KV(a, a.ctxOrBg(), key, []int64{})
	}
	if len(list) == 0 {
		return parserExpectedSeconds
	}
	s := append([]int64{}, list...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return max(60, s[len(s)/2])
}

// StartParser — started | running | cooldown | no_session | forbidden | disabled.
func (u *U) StartParser(force bool) string {
	if u.Cfg.Dev && os.Getenv("POSTA_DEV_PARSERS") != "1" {
		return "disabled" // режим разработки: сборщики не запускаются
	}
	if u.TopicSaveDenied(u.CurrentTopic()) != "" {
		return "forbidden"
	}
	p := u.parsers.user(u.ID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.paused {
		return "paused"
	}
	if p.running {
		return "running"
	}
	if !force && time.Since(p.startedAt) < time.Duration(u.TInt("parser_cooldown"))*time.Minute {
		return "cooldown"
	}
	if p.tab == "" || time.Since(p.tabSeen) > TabSessionTTL {
		return "no_session"
	}
	u.RequestSharedCollection(u.Ctx, u.CurrentTopic(), 10)
	ctx, cancel := context.WithCancel(u.App.ctx)
	p.running, p.cancel, p.startedAt = true, cancel, time.Now()
	p.progress = jx.M{"queued_at": now()}
	if p.file != nil {
		p.file.Close()
	}
	p.lines, p.lineStage, p.lineAt, p.stage = nil, nil, nil, ""
	p.file, _ = os.Create(filepath.Join(u.UserDir(u.ID), "parser-v3.log"))
	uid := u.ID
	u.App.Go("сборщик "+uid, func(context.Context) {
		defer func() {
			p.mu.Lock()
			p.running = false
			if p.file != nil {
				p.file.Close()
				p.file = nil
			}
			p.mu.Unlock()
			cancel()
		}()
		w := u.App.User(ctx, uid, u.App.ParserLogger(uid))
		if !w.parserWaitForSlot() {
			return
		}
		defer func() { <-u.parsers.slots }()
		for pass := 0; pass < 3 && ctx.Err() == nil; pass++ {
			if !w.RunCollector() {
				break
			}
		}
	})
	return "started"
}

// parserWaitForSlot — не больше max_parsers сборщиков одновременно; ждём не дольше parserQueueMax.
func (u *U) parserWaitForSlot() bool {
	since := time.Now()
	u.ParserProgress(jx.M{"queued_at": since.Unix()}, true)
	notice := time.Time{}
	for {
		if len(u.parsers.slots) < u.TInt("max_parsers") {
			select {
			case u.parsers.slots <- struct{}{}:
				if time.Since(since) > 3*time.Second {
					u.Log("Очередь дошла, начинаю.")
				}
				return true
			default:
			}
		}
		if time.Since(since) > parserQueueMax {
			u.Log("Очередь сборщиков слишком долгая — сбор не запущен.")
			return false
		}
		if time.Since(notice) >= 20*time.Second {
			notice = time.Now()
			u.Log("Все сборщики заняты другими пользователями — жду очереди (%d с)...", int(time.Since(since).Seconds()))
		}
		select {
		case <-u.Ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
}

// StopParser — остановить сборщик пользователя (обрыв запроса останавливает и генерацию модели).
func (a *App) StopParser(uid string) bool {
	p := a.parsers.user(uid)
	p.mu.Lock()
	running, cancel := p.running, p.cancel
	p.mu.Unlock()
	if !running || cancel == nil {
		return false
	}
	cancel()
	for i := 0; i < 50 && a.ParserRunning(uid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	return true
}

func (u *U) RestartParser() string {
	u.StopParser(u.ID)
	return u.StartParser(true)
}

// StopAllParsers — админка: остановить всё.
func (a *App) StopAllParsers() int {
	a.parsers.mu.Lock()
	var ids []string
	for uid, p := range a.parsers.users {
		p.mu.Lock()
		if p.running {
			ids = append(ids, uid)
		}
		p.mu.Unlock()
	}
	a.parsers.mu.Unlock()
	for _, uid := range ids {
		a.StopParser(uid)
	}
	return len(ids)
}

// PauseAllParsers blocks automatic restarts from existing tab heartbeats before cancellation.
// Opening a new page explicitly clears that user's pause.
func (a *App) PauseAllParsers() int {
	a.parsers.mu.Lock()
	a.parsers.sharedPaused = true
	sharedCancel, sharedDone := a.parsers.sharedCancel, a.parsers.sharedDone
	var active []*userParser
	var cancels []context.CancelFunc
	for _, p := range a.parsers.users {
		p.mu.Lock()
		p.paused = true
		if p.running {
			active = append(active, p)
			if p.cancel != nil {
				cancels = append(cancels, p.cancel)
			}
		}
		p.mu.Unlock()
	}
	a.parsers.mu.Unlock()
	if sharedCancel != nil {
		sharedCancel()
	}
	for _, cancel := range cancels {
		cancel()
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		running := false
		if sharedDone != nil {
			select {
			case <-sharedDone:
			default:
				running = true
			}
		}
		for _, p := range active {
			p.mu.Lock()
			running = running || p.running
			p.mu.Unlock()
		}
		if !running {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	return len(active)
}

// RunningParsers — пользователи, у которых сейчас идёт сбор (админка).
func (a *App) RunningParsers() []jx.M {
	a.parsers.mu.Lock()
	defer a.parsers.mu.Unlock()
	var out []jx.M
	for uid, p := range a.parsers.users {
		p.mu.Lock()
		if p.running {
			out = append(out, jx.M{"uid": uid, "started_at": p.startedAt.Unix(), "phase": p.phase})
		}
		p.mu.Unlock()
	}
	return out
}

func (a *App) SharedCollectionRunning() bool {
	a.parsers.mu.Lock()
	defer a.parsers.mu.Unlock()
	return a.parsers.sharedDone != nil
}

// ---- Сессия вкладки ----

var tabIDRe = regexp.MustCompile(`^[a-f0-9-]{8,64}$`)

func IsValidTabID(t string) bool { return tabIDRe.MatchString(t) }

// TabOpen — вкладка заняла сессию; сборщик запускается (уже работающий не трогаем).
func (u *U) TabOpen(tab string) string {
	u.parsers.mu.Lock()
	u.parsers.sharedPaused = false
	u.parsers.mu.Unlock()
	p := u.parsers.user(u.ID)
	p.mu.Lock()
	p.tab, p.tabSeen, p.tabOpened = tab, time.Now(), time.Now()
	p.paused = false
	p.mu.Unlock()
	if u.Cfg.Dev && os.Getenv("POSTA_DEV_PARSERS") != "1" {
		return "disabled" // режим разработки: открытие страницы не запускает сборщик
	}
	return u.StartParser(true)
}

// TabHeartbeat — сигнал вкладки: owner — ведёт ли она сессию; уснувшая вкладка возобновляет сборщик.
func (u *U) TabHeartbeat(tab string) jx.M {
	p := u.parsers.user(u.ID)
	p.mu.Lock()
	stale := p.tab == "" || time.Since(p.tabSeen) > TabSessionTTL
	outcome := "other"
	switch {
	case p.tab == tab && stale:
		outcome = "resumed"
	case p.tab == tab:
		outcome = "owner"
	case stale:
		outcome = "claimed"
	}
	if outcome != "other" {
		if p.tab != tab {
			p.tabOpened = time.Now()
		}
		p.tab, p.tabSeen = tab, time.Now()
	}
	p.mu.Unlock()
	u.KickPublisher()
	if outcome == "other" {
		return jx.M{"owner": false, "parser": nil}
	}
	if u.Cfg.Dev && os.Getenv("POSTA_DEV_PARSERS") != "1" {
		return jx.M{"owner": true, "parser": nil}
	}
	var parser any
	switch outcome {
	case "claimed", "resumed":
		parser = u.StartParser(true)
	case "owner":
		if u.TopicIsHot(u.Ctx, u.CurrentTopic()) && u.ContentPoolNeedsFill(u.SearchTopicKey()) {
			parser = u.StartParser(false)
		}
	}
	return jx.M{"owner": true, "parser": parser}
}

// TabClose — сборщик не останавливаем: он доработает сам, журнал будет виден при следующем открытии.
func (u *U) TabClose(tab string) bool {
	p := u.parsers.user(u.ID)
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.tab != tab {
		return false
	}
	p.tabSeen = time.Time{}
	return true
}

func (a *App) TabSessionFresh(uid string) bool {
	p := a.parsers.user(uid)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.tab != "" && time.Since(p.tabSeen) <= TabSessionTTL
}

var _ = fmt.Sprint
var _ = config.DaySeconds
