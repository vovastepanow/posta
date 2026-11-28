package posta

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"posta/internal/jx"
)

// ---- Общий HTTP: только https и публичные адреса; секреты не попадают в ошибки ----

type netResp struct {
	Status  int
	Data    jx.M
	Raw     []byte
	Headers http.Header
}

var networkRejectStatuses = []int{400, 401, 403, 404, 410, 422}

// Resolve once and pin the connection to that validated answer. API requests
// carry credentials and must never follow redirects or environment proxies.
func networkResolve(raw string, lookup func(string) ([]net.IP, error)) (*url.URL, []string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" {
		return nil, nil, Bad("Нужен публичный HTTPS-адрес API.")
	}
	return publicResolveAddresses(raw, lookup)
}

type networkRequestFunc func(context.Context, string, string, map[string]string, io.Reader) (*netResp, error)

func networkHTTP(ctx context.Context, method, rawURL string, headers map[string]string, body io.Reader) (*netResp, error) {
	ctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	u, addresses, err := networkResolve(rawURL, func(host string) ([]net.IP, error) {
		return net.DefaultResolver.LookupIP(ctx, "ip4", host)
	})
	if err != nil {
		return nil, fmt.Errorf("Адрес API отклонён или не разрешается: %w", ErrNetworkRejected)
	}
	dialer := &net.Dialer{Timeout: 8*time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return dialPublicAddresses(ctx, addresses, dialer.DialContext)
		},
		TLSHandshakeTimeout: 8*time.Second,
	}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error {return http.ErrUseLastResponse}}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), body)
	if err != nil {
		return nil, Bad("Неверный адрес запроса.")
	}
	if file, ok := body.(*os.File); ok {
		if info, err := file.Stat(); err == nil {
			req.ContentLength = info.Size()
		}
	}
	req.Header.Set("User-Agent", HTTPUA)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		if method != http.MethodGet && method != http.MethodHead {
			return nil, uncertainPublish("Связь с сервисом прервалась.")
		}
		return nil, Bad("Нет связи с сервисом.")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(raw)>8<<20 {
		if method != http.MethodGet && method != http.MethodHead {
			return nil, uncertainPublish("Ответ сервиса получен не полностью.")
		}
		return nil, Bad("Ответ сервиса получен не полностью.")
	}
	return &netResp{Status: resp.StatusCode, Data: jx.Unmarshal(raw), Raw: raw, Headers: resp.Header}, nil
}

func jsonBody(v any) io.Reader { return bytes.NewReader(jx.Marshal(v)) }

func formBody(v url.Values) io.Reader { return strings.NewReader(v.Encode()) }

const (
	ctJSON = "application/json"
	ctForm = "application/x-www-form-urlencoded"
)

// networkExpect — данные при ожидаемом коде, иначе ошибка с текстом сервиса; отказ, который повтор не исправит, — ErrNetworkRejected.
func networkExpect(r *netResp, err error, statuses []int, what string) (jx.M, error) {
	if err != nil {
		return nil, err
	}
	if r == nil { return nil, fmt.Errorf("%s: пустой ответ API",what) }
	if contains(statuses, r.Status) {
		return orEmpty(r.Data), nil
	}
	d := orEmpty(r.Data)
	msg := ""
	for _, k := range []string{"error_description", "error", "error_msg", "message", "detail"} {
		if v, ok := d[k]; ok {
			if m := jx.ToMap(v); m != nil {
				msg = jx.Str(m, "message")
				if msg == "" {
					msg = jx.Str(m, "error_msg")
				}
			} else {
				msg = jx.ToStr(v)
			}
			if msg != "" {
				break
			}
		}
	}
	text := fmt.Sprintf("%s: код %d.", what, r.Status)
	if msg != "" {
		text = what + ": " + trimRunes(msg, 200) + "."
	}
	if r.Status >= 500 || r.Status == 408 {
		return nil, uncertainPublish(fmt.Sprintf("%s: HTTP %d.", what, r.Status))
	}
	if contains(networkRejectStatuses, r.Status) {
		return nil, fmt.Errorf("%s%w", text, errRejectedSilent)
	}
	return nil, errors.New(text)
}

// errRejectedSilent — ErrNetworkRejected без добавки к тексту ошибки.
var errRejectedSilent = &silentErr{ErrNetworkRejected}

type silentErr struct{ e error }

func (s *silentErr) Error() string { return "" }
func (s *silentErr) Unwrap() error { return s.e }

func bearer(t string) map[string]string { return map[string]string{"Authorization": "Bearer " + t} }

func withCT(h map[string]string, ct string) map[string]string {
	out := map[string]string{"Content-Type": ct}
	for k, v := range h {
		out[k] = v
	}
	return out
}
