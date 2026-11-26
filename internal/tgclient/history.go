package tgclient

import (
	"bytes"
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
)

// PublicHistoryMessage retains the identity of each album member and, when
// downloaded within the bounded photo budget, its image bytes.
type PublicHistoryMessage struct {
	ID              int
	GroupID         int64
	Date            int64
	Text            string
	Kind            string
	Views           *int
	Photo           []byte
	PhotoURL        string
	GroupIncomplete bool
}

type PublicHistory struct {
	Username  string
	Title     string
	Messages  []PublicHistoryMessage
	FloodWait int
}

// ReadPublicChannelHistory does not join channels or read private peers.
// Protected channels and individual protected messages are not collected.
func ReadPublicChannelHistory(ctx context.Context, acc *Account, username string, limit int) (PublicHistory, error) {
	var result PublicHistory
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" || limit <= 0 {
		return result, fail("Нужен публичный канал и положительный лимит.")
	}
	err := RunAuthorized(ctx, acc, func(ctx context.Context, api *tg.Client) error {
		ch, err := resolveChannel(ctx, api, username)
		if err != nil {
			return err
		}
		if !ch.Broadcast || ch.Username == "" || !strings.EqualFold(ch.Username, username) || ch.Noforwards {
			return fail("Канал не является доступным публичным каналом для сбора.")
		}
		res, err := api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{Peer: inputPeer(ch), Limit: min(limit, 100)})
		if err != nil {
			return err
		}
		m, ok := res.AsModified()
		if !ok {
			return fail("Telegram не вернул историю канала.")
		}
		result.Username, result.Title = ch.Username, ch.Title
		messages := m.GetMessages()
		result.Messages = publicHistoryMessages(messages, ch.ID)
		// The history window may end in the middle of an album. Do not
		// advertise that album as complete until all members are available.
		if len(messages) >= min(limit, 100) && len(result.Messages) > 0 {
			last := result.Messages[len(result.Messages)-1].GroupID
			if last != 0 {
				for i := range result.Messages {
					if result.Messages[i].GroupID == last {
						result.Messages[i].GroupIncomplete = true
					}
				}
			}
		}
		byID := map[int]*tg.Message{}
		for _, mc := range messages {
			if msg, ok := mc.(*tg.Message); ok {
				byID[msg.ID] = msg
			}
		}
		// Bound both the number of downloads and bytes per RPC batch. An
		// unavailable photo remains metadata instead of disappearing.
		remaining, attempted := 24<<20, 0
		for i := range result.Messages {
			row := &result.Messages[i]
			if row.Kind != "photo" || row.GroupIncomplete || attempted >= 12 || remaining <= 0 || ctx.Err() != nil {
				continue
			}
			attempted++
			photo, err := downloadPublicHistoryPhoto(ctx, api, byID[row.ID], min(remaining, 8<<20))
			if d, ok := tgerr.AsFloodWait(err); ok {
				result.FloodWait = int(d.Seconds())
				break
			}
			if err == nil {
				row.Photo = photo
				remaining -= len(photo)
			}
		}
		return nil
	})
	return result, err
}

func publicHistoryMessages(messages []tg.MessageClass, channelID int64) []PublicHistoryMessage {
	// If an album includes protected content, skip the whole group rather
	// than reconstructing a partial protected album from its other members.
	protectedGroups := map[int64]bool{}
	for _, mc := range messages {
		if msg, ok := mc.(*tg.Message); ok && msg.Noforwards && msg.GroupedID != 0 {
			protectedGroups[msg.GroupedID] = true
		}
	}
	var out []PublicHistoryMessage
	for _, mc := range messages {
		msg, ok := mc.(*tg.Message)
		if !ok || msg.ID <= 0 || msg.Noforwards || protectedGroups[msg.GroupedID] {
			continue
		}
		peer, ok := msg.PeerID.(*tg.PeerChannel)
		if !ok || peer.ChannelID != channelID {
			continue
		}
		kind := "other"
		switch msg.Media.(type) {
		case nil, *tg.MessageMediaEmpty, *tg.MessageMediaWebPage:
			kind = "text"
		case *tg.MessageMediaPhoto:
			kind = "photo"
		case *tg.MessageMediaPoll:
			kind = "poll"
		case *tg.MessageMediaDocument:
			kind = "document"
			if videoDocument(msg) != nil {
				kind = "video"
			}
		}
		row := PublicHistoryMessage{ID: msg.ID, GroupID: msg.GroupedID, Date: int64(msg.Date), Text: msg.Message, Kind: kind}
		if views, ok := msg.GetViews(); ok {
			row.Views = &views
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].ID > out[j].ID })
	return out
}

type historyPhotoSize struct {
	kind string
	size int
	area int64
}

// Select the highest-resolution downloadable photo within the actual budget,
// not a stripped preview. Progressive sizes describe successive JPEG prefixes.
func bestHistoryPhotoSize(photo *tg.Photo, limit int) historyPhotoSize {
	var best historyPhotoSize
	for _, item := range photo.Sizes {
		var size historyPhotoSize
		switch s := item.(type) {
		case *tg.PhotoSize:
			size = historyPhotoSize{s.Type, s.Size, int64(s.W) * int64(s.H)}
		case *tg.PhotoSizeProgressive:
			if len(s.Sizes) > 0 {
				size = historyPhotoSize{s.Type, s.Sizes[len(s.Sizes)-1], int64(s.W) * int64(s.H)}
			}
		}
		if size.kind != "" && size.size > 0 && size.size <= limit && size.area > best.area {
			best = size
		}
	}
	return best
}

type historyPhotoBuffer struct {
	bytes.Buffer
	limit int
}

func (b *historyPhotoBuffer) Write(p []byte) (int, error) {
	if len(p) > b.limit-b.Len() {
		return 0, errors.New("photo exceeds download budget")
	}
	return b.Buffer.Write(p)
}

func downloadPublicHistoryPhoto(ctx context.Context, api *tg.Client, msg *tg.Message, limit int) ([]byte, error) {
	if msg == nil || msg.Noforwards {
		return nil, fail("Нет доступного фото.")
	}
	media, ok := msg.Media.(*tg.MessageMediaPhoto)
	if !ok {
		return nil, fail("Нет фото.")
	}
	raw, ok := media.GetPhoto()
	if !ok {
		return nil, fail("Нет фото.")
	}
	photo, ok := raw.(*tg.Photo)
	if !ok {
		return nil, fail("Нет фото.")
	}
	best := bestHistoryPhotoSize(photo, limit)
	if best.kind == "" {
		return nil, fail("Фото превышает бюджет или недоступно.")
	}
	loc := &tg.InputPhotoFileLocation{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference, ThumbSize: best.kind}
	buf := &historyPhotoBuffer{limit: min(limit, best.size)}
	if _, err := downloader.NewDownloader().Download(api, loc).Stream(ctx, buf); err != nil {
		return nil, err
	}
	if buf.Len() != best.size {
		return nil, fail("Фото загружено не полностью.")
	}
	return buf.Bytes(), nil
}
