package posta

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"
)

const (
	HTTPUA    = "Posta/1.0"
	BrowserUA = "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Safari/537.36"
)

var defaultClient = &http.Client{
	Timeout: 15 * time.Second,
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 3 {
			return errors.New("слишком много перенаправлений")
		}
		return nil
	},
}

// HTTPGet — тело ответа 2xx или nil (бывший http_get: 15 с, до трёх перенаправлений).
func (a *App) HTTPGet(raw string, timeout time.Duration) []byte {
	b, _, _ := a.HTTPGetStatus(raw, timeout, nil)
	return b
}

// HTTPGetStatus — тело, код ответа и ошибка; headers — дополнительные заголовки.
func (a *App) HTTPGetStatus(raw string, timeout time.Duration, headers map[string]string) ([]byte, int, error) {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(a.ctxOrBg(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", raw, nil)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", HTTPUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := defaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return body, resp.StatusCode, nil
}

func (a *App) ctxOrBg() context.Context {
	if a.ctx != nil {
		return a.ctx
	}
	return context.Background()
}

// HTTPGetJSON — JSON-объект или nil.
func (a *App) HTTPGetJSON(raw string, timeout time.Duration, dest any) bool {
	b := a.HTTPGet(raw, timeout)
	return b != nil && json.Unmarshal(b, dest) == nil
}

// HTTPPost — POST с телом; contentType пустой — form-urlencoded.
func (a *App) HTTPPost(raw string, body []byte, contentType string, headers map[string]string, timeout time.Duration) ([]byte, int, error) {
	if timeout == 0 {
		timeout = 15 * time.Second
	}
	ctx, cancel := context.WithTimeout(a.ctxOrBg(), timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "POST", raw, strings.NewReader(string(body)))
	if err != nil {
		return nil, 0, err
	}
	if contentType == "" {
		contentType = "application/x-www-form-urlencoded"
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", HTTPUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := defaultClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	return b, resp.StatusCode, err
}

// HTTPPostJSON — POST JSON и разбор ответа в dest.
func (a *App) HTTPPostJSON(raw string, payload any, timeout time.Duration, dest any) (int, error) {
	body, _ := json.Marshal(payload)
	b, status, err := a.HTTPPost(raw, body, "application/json", nil, timeout)
	if err != nil {
		return status, err
	}
	if dest != nil {
		json.Unmarshal(b, dest)
	}
	return status, nil
}

// ---- Публичные адреса (защита от обращений во внутреннюю сеть) ----

func publicIP(ip net.IP) bool {
	return ip != nil && !ip.IsLoopback() && !ip.IsPrivate() && !ip.IsLinkLocalUnicast() && !ip.IsLinkLocalMulticast() &&
		!ip.IsUnspecified() && !ip.IsMulticast() && !ip.IsInterfaceLocalMulticast() && ip.To4() != nil &&
		!(ip[len(ip)-4] == 100 && ip[len(ip)-3]&0xC0 == 64) // 100.64.0.0/10
}

// publicResolve проверяет ссылку: только http(s) на стандартном порту и публичные IPv4. Возвращает адрес для подключения.
func publicResolve(raw string) (*url.URL, string, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil {
		return nil, "", Bad("Укажите прямую публичную ссылку на изображение.")
	}
	port := u.Port()
	std := map[string]string{"http": "80", "https": "443"}[u.Scheme]
	if port != "" && port != std {
		return nil, "", Bad("Ссылка должна использовать стандартный порт HTTP или HTTPS.")
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if ip := net.ParseIP(host); ip != nil {
		if !publicIP(ip) {
			return nil, "", Bad("Ссылка должна вести на публичный IPv4-адрес или домен.")
		}
		return u, net.JoinHostPort(host, std), nil
	}
	ips, err := net.LookupIP(host)
	if err != nil || len(ips) == 0 {
		return nil, "", Bad("Не удалось найти адрес сайта из ссылки.")
	}
	var first net.IP
	for _, ip := range ips {
		if ip.To4() == nil {
			continue
		}
		if !publicIP(ip) {
			return nil, "", Bad("Ссылка ведёт на непубличный адрес.")
		}
		if first == nil {
			first = ip
		}
	}
	if first == nil {
		return nil, "", Bad("Ссылка ведёт на непубличный адрес.")
	}
	return u, net.JoinHostPort(first.String(), std), nil
}

type FetchResult struct {
	OK           bool
	Bytes        []byte
	Type         string
	Error        string
	TooBig       bool
	Status       int
	Location     string
	LastModified string
}

type FetchOpts struct {
	MaxBytes  int64
	Dest      string // путь файла: тело пишется в него, а не в память
	Timeout   time.Duration
	UserAgent string
	Headers   map[string]string
}

// FetchPublic скачивает публичный файл без перенаправлений и не больше MaxBytes, подключаясь к проверенному адресу.
func (a *App) FetchPublic(raw string, o FetchOpts) (FetchResult, error) {
	u, addr, err := publicResolve(raw)
	if err != nil {
		return FetchResult{}, err
	}
	if o.Timeout == 0 {
		o.Timeout = 20 * time.Second
	}
	if o.UserAgent == "" {
		o.UserAgent = HTTPUA
	}
	dialer := &net.Dialer{Timeout: 8 * time.Second}
	client := &http.Client{
		Timeout: o.Timeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) { return dialer.DialContext(ctx, "tcp4", addr) },
			TLSHandshakeTimeout: 8 * time.Second,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	ctx, cancel := context.WithTimeout(a.ctxOrBg(), o.Timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	req.Header.Set("User-Agent", o.UserAgent)
	for k, v := range o.Headers {
		req.Header.Set(k, v)
	}
	r := FetchResult{}
	resp, err := client.Do(req)
	if err != nil {
		r.Error = err.Error()
		return r, nil
	}
	defer resp.Body.Close()
	r.Status, r.Type = resp.StatusCode, strings.ToLower(resp.Header.Get("Content-Type"))
	r.Location, r.LastModified = resp.Header.Get("Location"), resp.Header.Get("Last-Modified")
	limited := io.LimitReader(resp.Body, o.MaxBytes+1)
	var size int64
	if o.Dest != "" {
		f, err := os.Create(o.Dest)
		if err != nil {
			return r, Bad("Не удалось сохранить файл во временную папку.")
		}
		size, err = io.Copy(f, limited)
		f.Close()
		if err != nil {
			r.Error = err.Error()
		}
	} else {
		r.Bytes, err = io.ReadAll(limited)
		size = int64(len(r.Bytes))
		if err != nil {
			r.Error = err.Error()
		}
	}
	r.TooBig = size > o.MaxBytes
	if r.TooBig {
		r.Bytes = nil
	}
	r.OK = r.Error == "" && !r.TooBig && r.Status >= 200 && r.Status < 300 && size > 0
	if !r.OK && o.Dest != "" {
		os.Remove(o.Dest)
	}
	return r, nil
}

// FetchPublicImage — картинка по прямой ссылке.
func (a *App) FetchPublicImage(raw string) ([]byte, error) {
	r, err := a.FetchPublic(raw, FetchOpts{MaxBytes: 10 << 20})
	if err != nil {
		return nil, err
	}
	if !r.OK || !strings.HasPrefix(r.Type, "image/") {
		if r.Error != "" {
			return nil, Bad("Не удалось скачать изображение: %s", r.Error)
		}
		return nil, Bad("Ссылка должна вести прямо на файл изображения, без страницы или редиректа.")
	}
	return r.Bytes, nil
}

// ---- Вежливость: не чаще одного запроса за gap к хосту (в процессе — общий счётчик) ----

var throttle = struct {
	sync.Mutex
	next map[string]time.Time
}{next: map[string]time.Time{}}

func HostThrottle(host string, gap time.Duration) {
	host = strings.ToLower(host)
	throttle.Lock()
	at := throttle.next[host]
	t := time.Now()
	if at.Before(t) {
		at = t
	}
	throttle.next[host] = at.Add(gap)
	throttle.Unlock()
	if d := time.Until(at); d > 0 {
		time.Sleep(d)
	}
}
