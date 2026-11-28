package web

import (
	"net/http"
	"testing"

	"posta/internal/config"
	"posta/internal/jx"
)

func TestAccountCookieDetailsDoNotExposeValues(t *testing.T) {
	rows := accountCookieDetails([]*http.Cookie{
		{Name: config.SessionCookie, Value: "session-secret"},
		{Name: "other", Value: "unknown-secret"},
	})
	if len(rows) != 2 || !jx.Bool(rows[0], "http_only") || !jx.Bool(rows[0], "known") || jx.Bool(rows[1], "known") {
		t.Fatalf("unexpected cookie metadata: %v", rows)
	}
	for _, row := range rows {
		if _, ok := row["value"]; ok {
			t.Fatal("account API must not reflect cookie values")
		}
	}
}
