package posta

import (
	"context"
	"encoding/base64"
	"fmt"
	"html"
	"net/url"
	"strconv"
	"strings"
	"time"

	"posta/internal/jx"
)

// Self-hosted WordPress REST API. Use application passwords, not login
// passwords. Requests are DNS-pinned and never follow redirects.
func wordpressSite(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || (u.Port() != "" && u.Port() != "443") {
		return "", Bad("WordPress: укажите HTTPS-адрес сайта без логина, параметров или фрагмента.")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path = strings.TrimRight(u.Path, "/")
	u.RawPath = ""
	return u.String(), nil
}

func wordpressRequest(a *App, ctx context.Context, site, username, password, method, endpoint, body string, status int) (jx.M, error) {
	auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
	r, err := a.fetchPublicContext(ctx, site+"/wp-json/wp/v2/"+endpoint, FetchOpts{
		Method: method, Body: body, Timeout: 30 * time.Second, MaxBytes: 2 << 20,
		Headers: map[string]string{"Authorization": "Basic " + auth, "Content-Type": ctJSON, "Accept": ctJSON},
	})
	if err != nil {
		return nil, Bad("WordPress: адрес недоступен или не прошёл проверку безопасности.")
	}
	if r.Status == 401 || r.Status == 403 || r.Status == 404 || r.Status == 400 {
		return nil, fmt.Errorf("WordPress: HTTP %d; проверьте адрес REST API, пароль приложения и права: %w", r.Status, ErrNetworkRejected)
	}
	if !r.OK || r.Status != status {
		return nil, fmt.Errorf("WordPress: результат запроса не подтверждён (HTTP %d).", r.Status)
	}
	data := jx.Unmarshal(r.Bytes)
	if jx.Int(data, "id") <= 0 {
		return nil, Bad("WordPress: ответ API не содержит ID.")
	}
	return data, nil
}

func wordpressConnect(a *App, ctx context.Context, in map[string]string) (*NetResource, error) {
	site, err := wordpressSite(in["site"])
	if err != nil {
		return nil, err
	}
	username, password := strings.TrimSpace(in["username"]), strings.TrimSpace(in["password"])
	if username == "" || strings.Contains(username, ":") || password == "" {
		return nil, Bad("WordPress: укажите имя пользователя и пароль приложения.")
	}
	info, err := wordpressRequest(a, ctx, site, username, password, "GET", "users/me?context=edit", "", 200)
	if err != nil {
		return nil, err
	}
	if !jx.Bool(jx.Map(info, "capabilities"), "publish_posts") {
		return nil, Bad("WordPress: у пользователя нет права publish_posts.")
	}
	return &NetResource{Ref: site, Title: site, Secrets: map[string]string{"username": username, "password": password}}, nil
}

func wordpressPostBody(text string) jx.M {
	title, _ := titleAndBody(text)
	// Derive a display title, but keep the entire original text in content.
	content := strings.ReplaceAll(html.EscapeString(text), "\n", "<br>\n")
	return jx.M{"title": trimRunes(title, 160), "content": content, "status": "publish"}
}

func wordpressPublish(a *App, ctx context.Context, c *Channel, p NetPost) (*NetSent, error) {
	if len(p.Media) != 0 || p.Poll != nil || strings.TrimSpace(p.Text) == "" {
		return nil, fmt.Errorf("WordPress: текущий адаптер принимает только непустой текст: %w", ErrNetworkRejected)
	}
	site, err := wordpressSite(c.Ref)
	if err != nil {
		return nil, err
	}
	info, err := wordpressRequest(a, ctx, site, c.Secrets["username"], c.Secrets["password"], "POST", "posts", string(jx.Marshal(wordpressPostBody(p.Text))), 201)
	if err != nil {
		return nil, err
	}
	if jx.Str(info, "status") != "publish" {
		return nil, Bad("WordPress: API не подтвердил опубликованный статус.")
	}
	link := jx.Str(info, "link")
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Hostname() == "" || u.User != nil {
		link = ""
	}
	id := strconv.FormatInt(jx.Int(info, "id"), 10)
	return &NetSent{URL: link, MessageIDs: []string{id}}, nil
}
