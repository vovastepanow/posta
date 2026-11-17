package llm

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSharedTPMErrorIsNotImmediatelyRetried(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"Provider returned error","metadata":{"provider_name":"Baidu","provider_error_code":"tpm_rate_limit_exceeded","limit_source":"upstream_provider_shared_pool"}}}`))
	}))
	defer server.Close()
	_, err := NewOpenRouter(server.URL, "test", "", "").Generate(context.Background(), "baidu/ernie-5.5-flash-preview:free", &Request{Role: RoleClassify, Prompt: "test"})
	if calls != 1 || err == nil || !strings.Contains(err.Error(), "upstream_provider_shared_pool") {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestPaidSharedQuotaErrorIsNotImmediatelyRetried(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(429)
		w.Write([]byte(`{"error":{"message":"Provider returned error","metadata":{"provider_name":"Alibaba","provider_error_code":"insufficient_quota","limit_source":"upstream_provider_shared_pool"}}}`))
	}))
	defer server.Close()
	_, err := NewOpenRouter(server.URL, "test", "", "").Generate(context.Background(), "qwen/qwen3.7-flash", &Request{Role: RoleClassifyVerify, Prompt: "test"})
	if calls != 1 || err == nil {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
