package posta

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"posta/internal/jx"
	"posta/internal/tgclient"
)

// Account-scoped fallback; do not use it for anonymous discovery or shared
// collection. Throttle Telegram RPCs after FloodWait, not just one channel.
var telegramHistoryPause sync.Map

func (u *U) telegramUserHistoryPosts(channel, name string, count int) ([]jx.M, error) {
	channel = strings.ToLower(strings.TrimLeft(strings.TrimSpace(channel), "@"))
	if u.ID == "" || count <= 0 || !IsValidTelegramChannel(channel) || !u.TelegramUserConnected() {
		return nil, Bad("Для резервного чтения нужен подключённый аккаунт Telegram.")
	}
	var result tgclient.PublicHistory
	err := u.tguCall(30*time.Second, func(ctx context.Context, acc *tgclient.Account) error {
		if until, ok := telegramHistoryPause.Load(u.ID); ok && time.Now().Before(until.(time.Time)) {
			return Bad("Telegram ограничил запросы истории; повтор будет позже.")
		}
		var err error
		result, err = tgclient.ReadPublicChannelHistory(ctx, acc, channel, min(count+10, 100))
		var flood *tgclient.FloodError
		if errors.As(err, &flood) {
			telegramHistoryPause.Store(u.ID, time.Now().Add(time.Duration(flood.Seconds)*time.Second))
		} else if result.FloodWait > 0 {
			telegramHistoryPause.Store(u.ID, time.Now().Add(time.Duration(result.FloodWait)*time.Second))
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	for i := range result.Messages {
		row := &result.Messages[i]
		if len(row.Photo) != 0 {
			if local, err := u.OwnStoreImage(row.Photo); err == nil {
				row.PhotoURL = local
				mediaProtect.Store(filepath.Base(local), time.Now())
			}
			row.Photo = nil
		}
	}
	return normalizeTelegramHistory(result, name, count), nil
}

// Keep metadata for every member in source order. Undownloaded photos and
// unsupported attachments stay explicitly incomplete; never call them text.
func normalizeTelegramHistory(history tgclient.PublicHistory, name string, count int) []jx.M {
	if history.Username == "" || count <= 0 {
		return nil
	}
	if name == "" {
		name = SourceDisplayName(PlatformTelegram, history.Title)
	}
	type group struct {
		messages []tgclient.PublicHistoryMessage
	}
	var groups []*group
	byAlbum := map[int64]*group{}
	for _, msg := range history.Messages {
		g := byAlbum[msg.GroupID]
		if msg.GroupID == 0 || g == nil {
			g = &group{}
			groups = append(groups, g)
			if msg.GroupID != 0 {
				byAlbum[msg.GroupID] = g
			}
		}
		g.messages = append(g.messages, msg)
	}
	var out []jx.M
	for _, g := range groups {
		var texts []string
		var attachments, gallery []jx.M
		var images []string
		var views any
		incomplete := false
		// The native history arrives newest first; album members are reversed.
		oldest := g.messages[len(g.messages)-1]
		for i := len(g.messages)-1; i >= 0; i-- {
			m := g.messages[i]
			if strings.TrimSpace(m.Text) != "" {
				texts = append(texts, m.Text)
			}
			if views == nil && m.Views != nil {
				views = *m.Views
			}
			if m.GroupIncomplete {
				incomplete = true
			}
			if m.Kind != "text" {
				attachment := jx.M{"kind": m.Kind, "message_id": m.ID, "url": fmt.Sprintf("https://t.me/%s/%d", history.Username, m.ID)}
				if m.Kind == "photo" && m.PhotoURL != "" {
					attachment["image_local"] = m.PhotoURL
					images = append(images, m.PhotoURL)
					gallery = append(gallery, jx.M{"image": m.PhotoURL, "image_local": m.PhotoURL})
				} else {
					incomplete = true
				}
				attachments = append(attachments, attachment)
			}
		}
		text := strings.Join(texts, "\n\n")
		if text == "" && len(attachments) == 0 {
			continue
		}
		p := jx.M{"source": name, "source_key": SourceIDFor(PlatformTelegram, history.Username), "post_id": strconv.Itoa(oldest.ID),
			"url": fmt.Sprintf("https://t.me/%s/%d", history.Username, oldest.ID), "posted_at": oldest.Date,
			"text": text, "text_post": len(attachments) == 0, "source_attachments": attachments, "views": views,
			"fetched_via": "telegram_mtproto", "media_incomplete": incomplete}
		if len(images) > 0 {
			p["image"], p["image_local"], p["images"] = images[0], images[0], images
			if len(gallery) > 1 {
				p["gallery"] = gallery
			}
		}
		// Only a single video is supported by the existing preview queue.
		if oldest.GroupID == 0 && len(attachments) == 1 && jx.Str(attachments[0], "kind") == "video" {
			p["video_page"], p["video_via"], p["media_incomplete"] = jx.Str(p, "url"), "tgu", false
		}
		out = append(out, p)
		if len(out) >= count {
			break
		}
	}
	return out
}
