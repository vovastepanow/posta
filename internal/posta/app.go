// Package posta — предметная логика Posta: пользователи, темы, источники, сборщик, подбор, публикация.
// Файлы повторяют прежние PHP-модули (lib/*.php): core, sources, topics, parser…
package posta

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"posta/internal/ch"
	"posta/internal/config"
	"posta/internal/db"
	"posta/internal/llm"
	"posta/internal/secret"
)

// App — общие ресурсы процесса.
type App struct {
	Cfg  config.Server
	DB   *db.DB
	CH   *ch.Sink
	LLM  *llm.Router
	Box  *secret.Box
	Mem  *MemCache
	HTTP *http.Client

	catalog   *Catalog
	parsers   *ParserManager
	userLocks sync.Map // id пользователя → *sync.Mutex: пул и история пользователя меняются по очереди
	bg        sync.WaitGroup
	ctx       context.Context
	cancel    context.CancelFunc

	llmMu       sync.Mutex
	llmSettings llm.Settings
	llmReadAt   time.Time
	spentMu     sync.Mutex
	spentDay    string
	spentUSD    float64
	vkMu        sync.Mutex
}

// Error — ожидаемая ошибка с текстом для пользователя и HTTP-статусом (было: InvalidArgumentException/RuntimeException → 400,
// DomainException → 409).
type Error struct {
	Status int
	Msg    string
}

func (e *Error) Error() string { return e.Msg }

// Bad — неверный ввод или ожидаемый сбой (400).
func Bad(format string, a ...any) error { return &Error{400, fmt.Sprintf(format, a...)} }

// Conflict — конфликт состояния (409).
func Conflict(format string, a ...any) error { return &Error{409, fmt.Sprintf(format, a...)} }

// NotFound — 404.
func NotFound(format string, a ...any) error { return &Error{404, fmt.Sprintf(format, a...)} }

// StatusOf — HTTP-статус ошибки: ожидаемые — свой, прочие — 500.
func StatusOf(err error) int {
	var e *Error
	if errors.As(err, &e) {
		return e.Status
	}
	return 500
}

// U — работа от имени пользователя: запрос API, сборщик, фоновая задача. Бывший app_user_bind().
type U struct {
	*App
	ID  string
	Ctx context.Context
	// log — журнал: сборщик пишет в консоль пользователя, запрос API собирает ai_log для ответа.
	log func(string)
	run *Run // состояние прохода сборщика (бывшие $GLOBALS); nil вне сборщика
}

func (a *App) User(ctx context.Context, uid string, logf func(string)) *U {
	return &U{App: a, ID: uid, Ctx: context.WithValue(ctx, vkUserContextKey{}, uid), log: logf}
}

type vkUserContextKey struct{}

// Log — строка журнала (бывший cli_echo): в сборщике — в консоль сайта, иначе — в журнал сервера только в режиме разработки.
func (u *U) Log(format string, a ...any) {
	line := format
	if len(a) > 0 {
		line = fmt.Sprintf(format, a...)
	}
	if u.log != nil {
		u.log(line)
	} else if u.Cfg.Dev {
		log.Print(line)
	}
}

// Alive — продолжать ли работу: вкладка открыта и процесс не останавливается (бывший parser_session_alive).
func (u *U) Alive() bool { return u.Ctx.Err() == nil }

func (a *App) Context() context.Context { return a.ctx }

var userIDRe = regexp.MustCompile(`^[a-f0-9]{32}$`)

func IsValidUserID(uid string) bool { return userIDRe.MatchString(uid) }

// userLock — мьютекс пользователя: пул и история меняются по одному писателю (сайт и сборщик в одном процессе).
func (a *App) userLock(uid string) *sync.Mutex {
	m, _ := a.userLocks.LoadOrStore(uid, &sync.Mutex{})
	return m.(*sync.Mutex)
}

// Go — фоновая задача процесса: дождётся её Close.
func (a *App) Go(name string, fn func(ctx context.Context)) {
	a.bg.Add(1)
	go func() {
		defer a.bg.Done()
		defer func() {
			if r := recover(); r != nil {
				log.Printf("%s: паника: %v", name, r)
			}
		}()
		fn(a.ctx)
	}()
}

// Every — периодическая фоновая задача.
func (a *App) Every(name string, period time.Duration, fn func(ctx context.Context)) {
	a.Go(name, func(ctx context.Context) {
		t := time.NewTicker(period)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				func() {
					defer func() {
						if r := recover(); r != nil {
							log.Printf("%s: паника: %v", name, r)
						}
					}()
					fn(ctx)
				}()
			}
		}
	})
}

// UsersDir — users/<id>/: журналы сборщика (консоль сайта).
func (a *App) UsersDir() string { return filepath.Join(a.Cfg.DataDir, "users") }

func (a *App) UserDir(uid string) string {
	dir := filepath.Join(a.UsersDir(), uid)
	os.MkdirAll(dir, 0o775)
	return dir
}

func now() int64 { return time.Now().Unix() }

// trimRunes — первые n символов (mb_substr).
func trimRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// ellipsis — mb_strimwidth с «…».
func ellipsis(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:max(0, n-1)]) + "…"
}

func runeLen(s string) int { return len([]rune(s)) }

var spaceRe = regexp.MustCompile(`\s+`)

func collapseSpaces(s string) string { return strings.TrimSpace(spaceRe.ReplaceAllString(s, " ")) }
