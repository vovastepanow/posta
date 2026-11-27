package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBuildRevisionOnlyOnLocalHealthRequest(t *testing.T) {
	rev := strings.Repeat("a", 40)
	h := withBuildRevision(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }), rev)
	for _, tc := range []struct{ host, remote, forwarded, want string }{
		{"127.0.0.1:8090", "127.0.0.1:1234", "", rev},
		{"127.0.0.1:8090", "127.0.0.1:1234", "203.0.113.1", ""},
		{"posta.example", "127.0.0.1:1234", "", ""},
		{"127.0.0.1:8090", "203.0.113.1:1234", "", ""},
	} {
		r := httptest.NewRequest("GET", "http://"+tc.host+"/", nil)
		r.RemoteAddr = tc.remote
		if tc.forwarded != "" { r.Header.Set("X-Forwarded-For",tc.forwarded) }
		w := httptest.NewRecorder()
		h.ServeHTTP(w,r)
		if got := w.Header().Get("X-Posta-Revision"); got != tc.want { t.Fatalf("%+v: %q",tc,got) }
	}
}
