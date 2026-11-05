package posta

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"

	"posta/internal/jx"
)

var discordChannelPath = regexp.MustCompile(`^/channels/([0-9]{5,22})/([0-9]{5,22})/?$`)

func discordSource(raw string) (string, string) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() != "discord.com" || u.Port() != "" || u.User != nil {
		return "", ""
	}
	m := discordChannelPath.FindStringSubmatch(u.Path)
	if m == nil {
		return "", ""
	}
	return m[1], m[2]
}

func (u *U) discordRead(path string) ([]byte, error) {
	token := u.NetworkAppKey("DISCORD_BOT_TOKEN")
	if token == "" {
		return nil, Bad("Discord: нужен токен бота с доступом к каналу.")
	}
	ctx, cancel := context.WithTimeout(u.Ctx, 20*time.Second)
	defer cancel()
	r, err := networkHTTP(ctx, "GET", "https://discord.com/api/v10"+path, map[string]string{"Authorization": "Bot " + token}, nil)
	if err != nil {
		return nil, err
	}
	if r.Status != 200 {
		return nil, fmt.Errorf("Discord API: ответ %d", r.Status)
	}
	return r.Raw, nil
}

func discordMessages(data []byte, guild, channel string, count int, withText bool) ([]jx.M, error) {
	var messages []struct {
		ID          string `json:"id"`
		ChannelID   string `json:"channel_id"`
		Content     string `json:"content"`
		Timestamp   string `json:"timestamp"`
		Attachments []struct {
			URL  string `json:"url"`
			Type string `json:"content_type"`
		} `json:"attachments"`
	}
	if err := json.Unmarshal(data, &messages); err != nil {
		return nil, err
	}
	var out []jx.M
	for _, m := range messages {
		if m.ID == "" || m.ChannelID != channel {
			continue
		}
		at, err := time.Parse(time.RFC3339Nano, m.Timestamp)
		if err != nil {
			continue
		}
		var media []jx.M
		for _, a := range m.Attachments {
			if !strings.HasPrefix(a.URL, "https://") {
				continue
			}
			if strings.HasPrefix(a.Type, "image/") {
				media = append(media, jx.M{"image": a.URL, "video": ""})
			} else if strings.HasPrefix(a.Type, "video/") {
				media = append(media, jx.M{"image": "", "video": a.URL})
			}
		}
		if len(media) == 0 && (!withText || runeLen(strings.TrimSpace(m.Content)) < 120) {
			continue
		}
		link := "https://discord.com/channels/" + guild + "/" + channel + "/" + m.ID
		post := searchPost("Discord", "discord:"+channel, "https://discord.com/channels/"+guild+"/"+channel, trimRunes(m.Content, 80), m.Content, "", "", link, at.Unix())
		if len(media) > 0 {
			setPostMedia(post, media)
			post["text_post"] = false
		}
		out = append(out, post)
		if len(out) >= count {
			break
		}
	}
	return out, nil
}

func discordMatchingChannels(data []byte, guild string, queries []string) ([]DiscoveryCandidate, error) {
	var channels []struct {
		ID      string `json:"id"`
		GuildID string `json:"guild_id"`
		Name    string `json:"name"`
		Topic   string `json:"topic"`
		Type    int    `json:"type"`
		NSFW    bool   `json:"nsfw"`
	}
	if err := json.Unmarshal(data, &channels); err != nil {
		return nil, err
	}
	var out []DiscoveryCandidate
	for _, c := range channels {
		if c.GuildID != guild || c.NSFW || (c.Type != 0 && c.Type != 5 && c.Type != 10 && c.Type != 11 && c.Type != 12) {
			continue
		}
		id := "https://discord.com/channels/" + guild + "/" + c.ID
		if g, _ := discordSource(id); g == "" {
			continue
		}
		match := false
		for _, q := range queries {
			if QueryTextHit(strings.ToLower(c.Name+" "+c.Topic), strings.ToLower(q)) {
				match = true
				break
			}
		}
		if match {
			out = append(out, DiscoveryCandidate{Platform: PlatformRSS, ID: id, Info: jx.M{"title": c.Name}, Origin: "канал Discord, доступный боту", OriginKind: "other"})
		}
	}
	return out, nil
}

func (u *U) discoveryFindDiscord(queries []string) []DiscoveryCandidate {
	if u.NetworkAppKey("DISCORD_BOT_TOKEN") == "" {
		u.Log("-> Discord: поиск пропущен, токен бота не настроен.")
		return nil
	}
	data, err := u.discordRead("/users/@me/guilds?limit=100")
	if err != nil {
		u.Log("-> Discord: %s", err)
		return nil
	}
	var guilds []struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &guilds); err != nil {
		u.Log("-> Discord: неверный ответ списка серверов.")
		return nil
	}
	var out []DiscoveryCandidate
	for _, g := range guilds[:min(10, len(guilds))] {
		if !regexp.MustCompile(`^[0-9]{5,22}$`).MatchString(g.ID) {
			continue
		}
		body, err := u.discordRead("/guilds/" + g.ID + "/channels")
		if err != nil {
			u.Log("-> Discord: каналы сервера не загружены: %s", err)
			continue
		}
		found, err := discordMatchingChannels(body, g.ID, queries)
		if err == nil {
			out = append(out, found...)
		}
		if body, err = u.discordRead("/guilds/" + g.ID + "/threads/active"); err == nil {
			var result struct {
				Threads []json.RawMessage `json:"threads"`
			}
			if json.Unmarshal(body, &result) == nil {
				if raw, err := json.Marshal(result.Threads); err == nil {
					if threads, err := discordMatchingChannels(raw, g.ID, queries); err == nil {
						out = append(out, threads...)
					}
				}
			}
		}
	}
	u.Log("-> Discord: найдено подходящих каналов %d на доступных серверах.", len(out))
	return out
}
