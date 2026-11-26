package posta

import (
	"strings"

	"posta/internal/jx"
)

// These are conservative Posta limits. A provider can impose tighter
// restrictions based on the account or API grant.
func networkPollLimits(platform string) jx.M {
	switch platform {
	case "telegram":
		return jx.M{"question_max": 300, "options_max": 10, "option_max": 100, "multiple": true}
	case "ok", "vk":
		return jx.M{"question_max": 300, "options_max": 10, "option_max": 100, "multiple": true}
	case "x":
		return jx.M{"question_max": 280, "options_max": 4, "option_max": 25, "duration": true}
	}
	return nil
}

// NetworkPollCheck validates the actual destination before queueing or sending.
func NetworkPollCheck(platform string, poll jx.M) (jx.M, error) {
	if platform == "" {
		platform = "telegram"
	}
	limits := networkPollLimits(platform)
	if limits == nil {
		return nil, Bad("Опросы пока не поддерживаются для этой площадки.")
	}
	question := strings.TrimSpace(jx.Str(poll, "question"))
	if question == "" || runeLen(question) > int(jx.Int(limits, "question_max")) {
		return nil, Bad("Вопрос должен содержать от 1 до %d символов.", jx.Int(limits, "question_max"))
	}
	var opts []string
	seen := map[string]bool{}
	for _, raw := range jx.List(poll, "options") {
		s, ok := raw.(string)
		if !ok {
			return nil, Bad("Вариант ответа должен быть текстом.")
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if runeLen(s) > int(jx.Int(limits, "option_max")) {
			return nil, Bad("Вариант ответа длиннее %d символов.", jx.Int(limits, "option_max"))
		}
		if seen[strings.ToLower(s)] {
			return nil, Bad("Варианты ответа должны различаться.")
		}
		seen[strings.ToLower(s)] = true
		opts = append(opts, s)
	}
	if len(opts) < 2 || len(opts) > int(jx.Int(limits, "options_max")) {
		return nil, Bad("Допустимо от 2 до %d вариантов ответа.", jx.Int(limits, "options_max"))
	}
	multiple := jx.Bool(poll, "multiple")
	if multiple && !jx.Bool(limits, "multiple") {
		return nil, Bad("Несколько ответов в одном голосе не поддерживаются.")
	}
	out := jx.M{"question": question, "options": opts, "multiple": multiple}
	if platform == "x" {
		duration := jx.Int(poll, "duration_minutes")
		if duration == 0 {
			duration = 1440
		}
		if duration < 5 || duration > 10080 {
			return nil, Bad("Длительность опроса X — от 5 минут до 7 дней.")
		}
		out["duration_minutes"] = duration
	}
	return out, nil
}

 // VK polls.create requires a user token; a community token alone is insufficient.
func (u *U) ChannelPollAvailable(c *Channel) bool {
	if c == nil { return false }
	if c.Platform != "vk" { return networkPollLimits(c.Platform) != nil || c.Platform == "" }
	if jx.Bool(c.Meta,"poll_user_token") && c.Secrets["token"] != "" { return true }
	t := u.App.vkTokens(u.Ctx)
	return jx.Str(t,"access_token") != "" && (jx.Int(t,"expires_at") > now()+60 || jx.Str(t,"refresh_token") != "")
}

func (u *U) ChannelPollCheck(c *Channel, poll jx.M) (jx.M,error) {
	if !u.ChannelPollAvailable(c) { return nil,Bad("Опрос для этого ресурса недоступен. Для VK подключите пользовательский токен с доступом к polls.create.") }
	return NetworkPollCheck(c.Platform,poll)
}

// ChannelEditorCapabilities returns no credentials and reflects the connected resource.
func (u *U) ChannelEditorCapabilities(c *Channel) jx.M {
	platform := c.Platform
	if platform == "" { platform = "telegram" }
	m := jx.ToMap(u.App.NetworksPublic()[platform])
	if m != nil { m["poll"] = u.ChannelPollAvailable(c) }
	return m
}
