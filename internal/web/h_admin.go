package web

import (
	"encoding/json"
	"net/url"
	"strings"
	"time"

	"posta/internal/jx"
	"posta/internal/llm"
	"posta/internal/posta"
)

// Админка (только владелец): радар лидов, поведение приложения для всех пользователей, модели; соцсети для автопостинга.

func init() {
	routes["radar_access"] = handler{noUser: true, fn: func(c *Ctx) (any, error) {
		return jx.M{"owner": c.U != nil && c.A.IsOwner(c.R.Context(), c.U.ID)}, nil
	}}
	get("radar", func(c *Ctx) (any, error) { return radarDispatch(c, c.Q("op"), queryMap(c), false) })
	post("radar_do", func(c *Ctx) (any, error) { return radarDispatch(c, c.Str("op"), c.In, true) })

	// Поведение приложения для всех пользователей: сборщик, отбор, подбор источников, видео, пользователи, публикация
	get("admin_tunables", func(c *Ctx) (any, error) {
		if err := ownerOnly(c); err != nil {
			return nil, err
		}
		return jx.M{"tunables": c.A.TunablesPublic()}, nil
	})
	post("admin_tunables_save", func(c *Ctx) (any, error) {
		if err := ownerOnly(c); err != nil {
			return nil, err
		}
		if err := c.A.TunablesSave(c.R.Context(), jx.Map(c.In, "values")); err != nil {
			return nil, err
		}
		return ok(jx.M{"tunables": c.A.TunablesPublic()}), nil
	})
	// Модели: поставщик и модель по ролям, пресеты, бюджет, расход
	get("admin_llm", func(c *Ctx) (any, error) {
		if err := ownerOnly(c); err != nil {
			return nil, err
		}
		return c.A.LLMPublic(c.R.Context()), nil
	})
	post("admin_llm_save", func(c *Ctx) (any, error) {
		if err := ownerOnly(c); err != nil {
			return nil, err
		}
		var s llm.Settings
		if p := c.Str("preset"); p != "" && p != "custom" {
			s = llm.Preset(p)
		} else {
			s = c.A.LLMSettings()
			raw, _ := json.Marshal(c.In["settings"])
			if err := json.Unmarshal(raw, &s); err != nil {
				return nil, fail(400, "Некорректные настройки моделей.")
			}
			s.Preset = "custom"
		}
		if err := c.A.LLMSettingsSave(c.R.Context(), s); err != nil {
			return nil, err
		}
		return ok(c.A.LLMPublic(c.R.Context())), nil
	})

	get("networks_list", func(c *Ctx) (any, error) {
		return ok(jx.M{"networks": c.A.NetworksPublic(), "channels": c.U.NetworksChannelsPublic()}), nil
	})
	post("network_connect", networkConnect)
	post("network_remove", func(c *Ctx) (any, error) {
		id := c.Str("id")
		ch := c.U.Channel(id)
		if ch == nil || ch.Platform == "telegram" {
			return nil, fail(404, "Такого ресурса нет.")
		}
		if d := c.U.ChannelDeleteDenied(id); d != "" {
			return nil, fail(409, d)
		}
		c.U.ChannelDelete(id)
		return ok(jx.M{"channels": c.U.NetworksChannelsPublic()}), nil
	})
	get("network_oauth_start", networkOAuthStart)
	routes["network_oauth_callback"] = handler{noUser: true, fn: networkOAuthCallback}
}

func queryMap(c *Ctx) jx.M {
	out := jx.M{}
	for k, v := range c.R.URL.Query() {
		if len(v) > 0 {
			out[k] = v[0]
		}
	}
	return out
}

func ownerOnly(c *Ctx) error {
	if c.U == nil || !c.A.IsOwner(c.R.Context(), c.U.ID) {
		return fail(404, "Неизвестное действие API.")
	}
	return nil
}

var radarGet = []string{"accounts", "settings", "scan_log", "leads", "stats", "rejected", "models", "ua_filter"}
var radarPost = []string{"account_select", "settings_save", "chats_search", "scan_start", "lead_status", "rejected_retry", "ua_filter_save", "ua_unblock"}

func radarDispatch(c *Ctx, op string, in jx.M, isPost bool) (any, error) {
	if err := ownerOnly(c); err != nil {
		return nil, err
	}
	allowed := radarGet
	if isPost {
		allowed = radarPost
	}
	if !contains(allowed, op) {
		return nil, fail(404, "Неизвестная операция радара.")
	}
	ctx, cancel := c.detached(3 * time.Minute)
	defer cancel()
	r, err := c.A.RadarAPI(ctx, op, in)
	if err != nil {
		return nil, err
	}
	return ok(r), nil
}

func networkConnect(c *Ctx) (any, error) {
	platform := c.Str("platform")
	n := c.A.Networks()[platform]
	if platform == "telegram" || n == nil || n.Connect == nil {
		return nil, fail(422, "Эту соцсеть пока нельзя подключить из Posta.")
	}
	if n.OAuth != nil {
		return nil, fail(422, n.Title+" подключается входом: нажмите «Войти».")
	}
	if n.Status == "beta" && !c.A.IsOwner(c.R.Context(), c.U.ID) {
		return nil, fail(403, n.Title+" в бете: подключает пока только владелец, пока не проверена реальная публикация.")
	}
	if n.Status != "ready" && n.Status != "beta" {
		return nil, fail(422, n.Title+" пока не подключается: "+n.Note)
	}
	fields := map[string]string{}
	for k, v := range jx.Map(c.In, "fields") {
		if s, isStr := v.(string); isStr {
			fields[k] = strings.TrimSpace(s)
		}
	}
	res, err := n.Connect(c.A, c.R.Context(), fields)
	if err != nil {
		return nil, fail(422, err.Error())
	}
	if err := c.U.NetworkSaveResource(platform, n, res); err != nil {
		return nil, err
	}
	return ok(jx.M{"channels": c.U.NetworksChannelsPublic()}), nil
}

func networkBack(c *Ctx, params url.Values) (any, error) {
	base := c.A.Cfg.PublicURL
	if base == "" {
		base = strings.TrimRight(c.A.Cfg.BasePath, "/")
	}
	c.redirect(base + "/?" + params.Encode())
	return nil, nil
}

func networkOAuthStart(c *Ctx) (any, error) {
	platform := c.Q("platform")
	n := c.A.Networks()[platform]
	if n == nil || n.OAuth == nil {
		return nil, fail(422, "Эта соцсеть подключается не входом.")
	}
	if n.Status == "needs_keys" {
		return nil, fail(422, n.Title+": не настроено на сервере (нужны ключи приложения).")
	}
	if n.Status == "beta" && !c.A.IsOwner(c.R.Context(), c.U.ID) {
		return nil, fail(403, n.Title+" в бете: подключает пока только владелец.")
	}
	verifier := ""
	if n.OAuth.PKCE {
		verifier = posta.PKCEVerifier()
	}
	state := c.A.NetworkOAuthStateCreate(c.R.Context(), c.U.ID, platform, verifier)
	c.redirect(n.OAuth.Authorize(c.A, state, c.A.NetworkRedirectURI(), verifier))
	return nil, nil
}

func networkOAuthCallback(c *Ctx) (any, error) {
	ctx := c.R.Context()
	q := c.R.URL.Query()
	data := c.A.NetworkOAuthStateTake(ctx, q.Get("state"))
	if data == nil {
		return networkBack(c, url.Values{"network_error": {"Вход устарел: начните подключение заново."}})
	}
	platform := jx.Str(data, "platform")
	n := c.A.Networks()[platform]
	if n == nil || n.OAuth == nil || q.Get("error") != "" || q.Get("code") == "" {
		title := platform
		if n != nil {
			title = n.Title
		}
		return networkBack(c, url.Values{"network_error": {title + ": вход отменён."}})
	}
	secrets, err := n.OAuth.Exchange(c.A, ctx, q.Get("code"), c.A.NetworkRedirectURI(), jx.Str(data, "verifier"))
	if err != nil {
		return networkBack(c, url.Values{"network_error": {err.Error()}})
	}
	res, err := n.OAuth.Identify(c.A, ctx, secrets)
	if err != nil {
		return networkBack(c, url.Values{"network_error": {err.Error()}})
	}
	res.Secrets = secrets
	if err := c.A.User(ctx, jx.Str(data, "uid"), nil).NetworkSaveResource(platform, n, res); err != nil {
		return networkBack(c, url.Values{"network_error": {err.Error()}})
	}
	return networkBack(c, url.Values{"network": {"ok"}, "platform": {platform}})
}
