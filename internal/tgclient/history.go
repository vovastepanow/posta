package tgclient

import (
	"context"
	"sort"
	"strings"

	"github.com/gotd/td/tg"
)

// PublicHistoryMessage is metadata, not a downloaded attachment. The
// message ID identifies each file in a grouped album.
type PublicHistoryMessage struct {
	ID int
	GroupID int64
	Date int64
	Text string
	Kind string
	Views *int
}

type PublicHistory struct {
	Username string
	Title string
	Messages []PublicHistoryMessage
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
		result.Messages = publicHistoryMessages(m.GetMessages(), ch.ID)
		return nil
	})
	return result, err
}

func publicHistoryMessages(messages []tg.MessageClass, channelID int64) []PublicHistoryMessage {
	var out []PublicHistoryMessage
	for _, mc := range messages {
		msg, ok := mc.(*tg.Message)
		if !ok || msg.ID <= 0 || msg.Noforwards {
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
