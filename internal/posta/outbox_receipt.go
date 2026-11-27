package posta

import (
	"context"
	"encoding/json"
	"strings"

	"posta/internal/jx"
)

// Contains no credentials: the target identity is only needed to reconstruct
// a journal entry when the resource was disconnected after the send.
type outboxReceipt struct {
	ChannelID string      `json:"channel_id"`
	Title     string      `json:"title"`
	Platform  string      `json:"platform"`
	Sent      *SentResult `json:"sent"`
}

func outboxConfirmedResult(sent *SentResult) bool {
	if sent == nil || len(sent.MessageIDs)+len(sent.ExternalIDs) == 0 {
		return false
	}
	for _, id := range sent.MessageIDs {
		if id <= 0 {
			return false
		}
	}
	for _, id := range sent.ExternalIDs {
		id = strings.TrimSpace(id)
		if id == "" || id == "0" || id == "null" {
			return false
		}
	}
	return true
}

func (a *App) outboxStoreReceipt(ctx context.Context, id, uid string, post jx.M, c *Channel, sent *SentResult) error {
	if c == nil || c.ID == "" || c.ID != jx.Str(post, "channel") || !outboxConfirmedResult(sent) {
		return Bad("Публикация не подтверждена.")
	}
	receipt := outboxReceipt{ChannelID: c.ID, Title: c.ChatTitle, Platform: c.Platform, Sent: sent}
	tag, err := a.DB.Pool.Exec(ctx, `UPDATE outbox
		SET data=jsonb_set(data,'{confirmed_receipt}',$1::jsonb,true)
		WHERE id=$2 AND user_id=$3 AND status='sending'
		AND COALESCE(data->>'sending_at','')=$4 AND COALESCE(data->>'attempts','')=$5
		AND NOT (data ? 'confirmed_receipt')`,
		jx.Marshal(receipt), id, uid, jx.Str(post, "sending_at"), jx.Str(post, "attempts"))
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errSkip
	}
	return nil
}

func (a *App) outboxRecoverReceipt(ctx context.Context, id, uid string, post jx.M) error {
	var receipt outboxReceipt
	if err := json.Unmarshal(jx.Marshal(post["confirmed_receipt"]), &receipt); err != nil ||
		receipt.ChannelID == "" || receipt.ChannelID != jx.Str(post, "channel") || !outboxConfirmedResult(receipt.Sent) {
		// A damaged receipt remains locked against automatic or manual
		// resend. It must not be interpreted as a failed network send.
		return Bad("Сохранённое подтверждение публикации повреждено.")
	}
	c := &Channel{ID: receipt.ChannelID, ChatTitle: receipt.Title, Platform: receipt.Platform}
	if err := a.outboxFinishSent(ctx, id, uid, post, c, receipt.Sent); err != nil {
		return err
	}
	if rec := jx.Str(post, "record"); rec != "" {
		a.User(ctx, uid, nil).ContentMarkPosted(rec)
	}
	return nil
}
