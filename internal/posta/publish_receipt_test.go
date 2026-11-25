package posta

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestNetworkReceipt(t *testing.T) {
	for _, sent := range []*NetSent{nil, {}, {URL: "https://example.com/post"}, {MessageIDs: []string{""}}, {MessageIDs: []string{"0"}}} {
		if !errors.Is(validateNetworkReceipt(sent), ErrPublishUncertain) {
			t.Fatalf("unconfirmed receipt accepted: %+v", sent)
		}
	}
	if err := validateNetworkReceipt(&NetSent{MessageIDs: []string{"123"}}); err != nil {
		t.Fatal(err)
	}
}

func TestOKPublishedID(t *testing.T) {
	for _, raw := range []string{`"12345"`, `12345`} {
		id, err := okPublishedID(&netResp{Status: 200, Raw: []byte(raw)})
		if err != nil || id != "12345" {
			t.Fatalf("id=%q error=%v", id, err)
		}
	}
	raw := []byte(`{"error_code":10,"error_msg":"PERMISSION_DENIED"}`)
	if _, err := okPublishedID(&netResp{Status: 200, Raw: raw, Data: jx.Unmarshal(raw)}); !errors.Is(err, ErrNetworkRejected) {
		t.Fatalf("API error accepted: %v", err)
	}
	if _, err := okPublishedID(&netResp{Status: 200, Raw: []byte("null")}); !errors.Is(err, ErrPublishUncertain) {
		t.Fatalf("invalid receipt accepted: %v", err)
	}
}

func TestTelegramAmbiguousReceipt(t *testing.T) {
	for _, tc := range []struct {
		status int
		body string
	}{
		{200, `{"ok":true,"result":{}}`},
		{200, `{"ok":true,"result":{"message_id":0}}`},
		{502, "Bad Gateway"},
		{200, `{"ok":true,`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(tc.status)
			_, _ = w.Write([]byte(tc.body))
		}))
		t.Setenv("POSTA_TELEGRAM_API", srv.URL+"/bot")
		_, err := (&App{}).TelegramPublishPrepared(context.Background(), &Channel{Token: "secret-token", ChatID: 42}, "test", nil, nil)
		srv.Close()
		if !errors.Is(err, ErrPublishUncertain) || strings.Contains(err.Error(), "secret-token") {
			t.Fatalf("unexpected result for %s: %v", tc.body, err)
		}
	}
}
