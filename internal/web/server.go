// Package web — HTTP: тот же JSON API, что был у index.php (?action=…), статика интерфейса и медиа.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"mime"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/posta"
)

type Server struct {
	A *posta.App
}

// Ctx — один запрос API.
type Ctx struct {
	W      http.ResponseWriter
	R      *http.Request
	A      *posta.App
	U      *posta.U
	In     jx.M
	SID    string
	Action string
	done   bool
}

// Resp — ответ с кодом, отличным от 200.
type Resp struct {
	Status int
	Body   any
}

type handler struct {
	post bool
	fn   func(c *Ctx) (any, error)
	// noUser — не нужен вошедший пользователь (вход, проверка доступа)
	noUser bool
}

var routes = map[string]handler{}

func get(name string, fn func(c *Ctx) (any, error))  { routes[name] = handler{fn: fn} }
func post(name string, fn func(c *Ctx) (any, error)) { routes[name] = handler{post: true, fn: fn} }

func ok(extra jx.M) jx.M {
	out := jx.M{"status": "success"}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func fail(status int, msg string) error { return &posta.Error{Status: status, Msg: msg} }

func (c *Ctx) Q(k string) string { return c.R.URL.Query().Get(k) }

func (c *Ctx) Str(k string) string { return jx.Str(c.In, k) }

func New(a *posta.App) *Server { return &Server{A: a} }

// Handler — сайт целиком: API, медиа, страницы и статика из web/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	cfg := s.A.Cfg
	mux.HandleFunc("/index.php", s.api)
	mux.HandleFunc("/api", s.api)
	if cfg.Dev {
		// режим разработки: войти в браузере без кук (headless-проверки) — только на 127.0.0.1
		mux.HandleFunc("/dev-login", func(w http.ResponseWriter, r *http.Request) {
			http.SetCookie(w, &http.Cookie{Name: config.SessionCookie, Value: r.URL.Query().Get("sid"), Path: "/", HttpOnly: true})
			http.Redirect(w, r, r.URL.Query().Get("to"), http.StatusFound)
		})
	}
	mediaURL := "/" + strings.Trim(cfg.MediaURL, "/") + "/"
	mux.Handle(mediaURL, http.StripPrefix(mediaURL, staticFiles(cfg.MediaDir, 7*24*time.Hour)))
	web := filepath.Join(cfg.Root, "web")
	if _, err := os.Stat(web); err != nil {
		web = cfg.Root // до переезда интерфейса в web/: index.html, js/, css/ лежат в корне
	}
	mux.Handle("/", staticSite(web))
	var h http.Handler = mux
	if base := strings.TrimRight(cfg.BasePath, "/"); base != "" {
		h = http.StripPrefix(base, mux)
	}
	return securityHeaders(h)
}

func securityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("X-Frame-Options", "SAMEORIGIN")
		h.ServeHTTP(w, r)
	})
}

// staticSite — только файлы интерфейса: без скрытых файлов, исходников и данных.
var staticExt = map[string]bool{".html": true, ".js": true, ".mjs": true, ".css": true, ".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true,
	".svg": true, ".ico": true, ".woff": true, ".woff2": true, ".ttf": true, ".json": true, ".webmanifest": true, ".txt": true, ".mp4": true}

func staticSite(root string) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if p == "/" || p == "" {
			p = "/index.html" // единая страница приложения и студии
		}
		clean := filepath.Clean(p)
		for _, part := range strings.Split(clean, "/") {
			if strings.HasPrefix(part, ".") {
				http.NotFound(w, r)
				return
			}
		}
		top := strings.SplitN(strings.TrimPrefix(clean, "/"), "/", 2)[0]
		if contains([]string{"data", "users", "lib", "handlers", "tools", "internal", "cmd", "docs", "media_cache", "vendor"}, top) || !staticExt[strings.ToLower(filepath.Ext(clean))] {
			http.NotFound(w, r)
			return
		}
		// no-cache: браузер переспрашивает каждый раз (ответ 304, если файл не менялся) — правки интерфейса видны сразу
		w.Header().Set("Cache-Control", "no-cache")
		if strings.HasSuffix(clean, "/index.html") {
			// FileServer перенаправляет index.html на папку: отдаём сами
			f, err := os.Open(filepath.Join(root, clean))
			if err != nil {
				http.NotFound(w, r)
				return
			}
			defer f.Close()
			st, _ := f.Stat()
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			http.ServeContent(w, r, "index.html", st.ModTime(), f)
			return
		}
		r2 := r.Clone(r.Context())
		r2.URL.Path = clean
		files.ServeHTTP(w, r2)
	})
}

func staticFiles(root string, maxAge time.Duration) http.Handler {
	files := http.FileServer(http.Dir(root))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/.") || strings.HasPrefix(r.URL.Path, ".") {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age="+strconv.Itoa(int(maxAge.Seconds())))
		files.ServeHTTP(w, r)
	})
}

func contains(l []string, s string) bool {
	for _, x := range l {
		if x == s {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	enc.Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, jx.M{"status": "error", "message": msg})
}

// authActions — работают без профиля: вход и проверка, что за браузер.
var authActions = map[string]bool{"auth_status": true, "password_login": true, "login_start": true, "login_status": true, "radar_access": true,
	"network_oauth_callback": true, "vk_oauth_callback": true}

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	action := r.URL.Query().Get("action")
	h, known := routes[action]
	if !known {
		writeError(w, 404, "Неизвестное действие API.")
		return
	}
	want := "GET"
	if h.post {
		want = "POST"
	}
	if r.Method != want {
		w.Header().Set("Allow", want)
		writeError(w, 405, "Метод "+r.Method+" не поддерживается для этого действия.")
		return
	}
	c := &Ctx{W: w, R: r, A: s.A, Action: action, In: jx.M{}}
	defer func() {
		if rec := recover(); rec != nil {
			log.Printf("API %s: паника: %v", action, rec)
			if !c.done {
				writeError(w, 500, "Внутренняя ошибка сервера.")
			}
		}
	}()
	if action == "api_add_post" {
		s.finish(c, apiAddPost(c))
		return
	}
	if h.post {
		if r.Header.Get("X-Posta") != "1" {
			writeError(w, 403, "Запрос отклонён: отсутствует заголовок X-Posta.")
			return
		}
		if strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "application/json") {
			body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
			if json.Unmarshal(body, &c.In) != nil || c.In == nil {
				writeError(w, 400, "Тело запроса должно быть JSON-объектом.")
				return
			}
		}
	}
	if err := s.bindUser(c); err != nil {
		writeError(w, posta.StatusOf(err), err.Error())
		return
	}
	s.finish(c, func() (any, error) { return h.fn(c) })
}

func (s *Server) finish(c *Ctx, fn func() (any, error)) {
	res, err := fn()
	if c.done {
		return
	}
	c.done = true
	if err != nil {
		status := posta.StatusOf(err)
		msg := err.Error()
		if status == 500 {
			log.Printf("API %s: %v", c.Action, err)
			msg = "Внутренняя ошибка сервера: " + msg
		}
		writeError(c.W, status, msg)
		return
	}
	if r, isResp := res.(Resp); isResp {
		writeJSON(c.W, r.Status, r.Body)
		return
	}
	if res == nil {
		res = ok(nil)
	}
	writeJSON(c.W, 200, res)
}

// bindUser — пользователь из куки сессии; без профиля (регистрация закрыта) — только вход.
func (s *Server) bindUser(c *Ctx) error {
	ctx := c.R.Context()
	sid := cookie(c.R, config.SessionCookie)
	legacy := cookie(c.R, config.UserCookie)
	res, err := s.A.SessionResolve(ctx, sid, legacy)
	if err != nil {
		return err
	}
	c.SID = sid
	if res.NewSID != "" {
		c.SID = res.NewSID
		s.setSession(c, res.NewSID)
	}
	if res.DropLegacy {
		http.SetCookie(c.W, &http.Cookie{Name: config.UserCookie, Value: "", Path: s.cookiePath(), MaxAge: -1})
	}
	if res.UID == "" {
		if !authActions[c.Action] {
			return fail(401, "Войдите: логин и пароль или Telegram. Новые профили на этом сайте сейчас не создаются.")
		}
		if c.Action == "login_start" {
			// вход через Telegram ведёт запрос от временного профиля; пустой профиль удалит сборка мусора
			uid := randUID()
			t, err := s.A.SessionCreate(ctx, uid)
			if err != nil {
				return err
			}
			c.SID = t
			s.setSession(c, t)
			res.UID = uid
		}
	}
	if res.UID != "" {
		c.U = s.A.User(ctx, res.UID, nil)
	}
	return nil
}

func (s *Server) cookiePath() string { return strings.TrimRight(s.A.Cfg.BasePath, "/") + "/" }

func (s *Server) setSession(c *Ctx, token string) {
	secure := c.R.TLS != nil || strings.EqualFold(c.R.Header.Get("X-Forwarded-Proto"), "https")
	http.SetCookie(c.W, &http.Cookie{Name: config.SessionCookie, Value: token, Path: s.cookiePath(), MaxAge: config.UserCookieDays * 86400,
		HttpOnly: true, Secure: secure, SameSite: http.SameSiteLaxMode})
}

func cookie(r *http.Request, name string) string {
	if c, err := r.Cookie(name); err == nil {
		return c.Value
	}
	return ""
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	// за обратным прокси на этой же машине — адрес клиента из заголовка прокси
	if (host == "127.0.0.1" || host == "::1") && r.Header.Get("X-Forwarded-For") != "" {
		return strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	}
	return host
}

// detached — контекст, который не обрывается, если браузер закрыл соединение (публикация, загрузки).
func (c *Ctx) detached(d time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(c.R.Context()), d)
}

// userDetached — пользователь с контекстом, переживающим разрыв соединения.
func (c *Ctx) userDetached(d time.Duration) (*posta.U, context.CancelFunc) {
	ctx, cancel := c.detached(d)
	return c.A.User(ctx, c.U.ID, nil), cancel
}

func (c *Ctx) serveFile(path, contentType, cache string) {
	c.done = true
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(c.W, c.R)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	if contentType == "" {
		contentType = mime.TypeByExtension(filepath.Ext(path))
	}
	if contentType != "" {
		c.W.Header().Set("Content-Type", contentType)
	}
	c.W.Header().Set("Cache-Control", cache)
	http.ServeContent(c.W, c.R, filepath.Base(path), st.ModTime(), f)
}

func (c *Ctx) redirect(url string) {
	c.done = true
	http.Redirect(c.W, c.R, url, http.StatusFound)
}

var errNotFound = errors.New("not found")
