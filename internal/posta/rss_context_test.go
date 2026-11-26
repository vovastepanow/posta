package posta

import (
	"context"
	"testing"
	"time"
)

func TestUserRSSGetHonorsCancellation(t *testing.T){
	ctx,cancel:=context.WithCancel(context.Background())
	cancel()
	u:=(&App{}).User(ctx,"",nil)
	got:=u.RSSGet("https://example.org/feed",rssMaxBytes,time.Second,HTTPUA)
	if got.OK||got.Error!=context.Canceled.Error(){t.Fatalf("unexpected cancellation outcome: %+v",got)}
}
