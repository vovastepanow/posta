package posta

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"posta/internal/jx"
)

// Simulated external HTTP failures must not prevent healthy backends from
// contributing candidates. The test intentionally uses HTTP responses (not
// only hardcoded result slices) without contacting external services.
func TestAutopilotSearchSurvivesHTTPProviderFailures(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusTooManyRequests, http.StatusServiceUnavailable} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/broken" {
					w.WriteHeader(status)
					return
				}
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"feed":"https://example.org/good.xml"}`))
			}))
			defer server.Close()
			statuses := map[string]string{}
			var visited []string
			makeBackend := func(name, path string) func() []DiscoveryCandidate {
				return func() []DiscoveryCandidate {
					visited = append(visited, name)
					resp, err := http.Get(server.URL + path)
					if err != nil {
						recordAutopilotBackendStatus(statuses, name, "unavailable")
						return nil
					}
					defer resp.Body.Close()
					if resp.StatusCode != http.StatusOK {
						recordAutopilotBackendStatus(statuses, name, "unavailable")
						return nil
					}
					recordAutopilotBackendStatus(statuses, name, "ok")
					return []DiscoveryCandidate{{Platform: PlatformRSS, ID: "https://example.org/good.xml"}}
				}
			}
			found := autopilotRunSearchBackendsAdaptive(context.Background(), 0,
				[]func() []DiscoveryCandidate{makeBackend("blocked", "/broken"), makeBackend("healthy", "/good")},
				2, 1)
			if len(found) != 1 || statuses["blocked"] != "unavailable" || statuses["healthy"] != "ok" {
				t.Fatalf("http %d: candidates=%v statuses=%v", status, found, statuses)
			}
			if !reflect.DeepEqual(visited, []string{"blocked", "healthy"}) {
				t.Fatalf("http %d: visited=%v", status, visited)
			}
			if got := autopilotDiscoveryOutcome(2, len(found), 0, true, true, false); got != "results" {
				t.Fatalf("http %d: outcome=%s", status, got)
			}
			job := &autopilotJob{Checkpoint: []byte(`{}`)}
			autopilotRecordDiscoveryMetrics(job, len(found), 0, 2, "results", 1, statuses)
			checkpoint := jx.Unmarshal(job.Checkpoint)
			if jx.Int(checkpoint, "last_backends_ok") != 1 || jx.Int(checkpoint, "last_backends_unavailable") != 1 {
				t.Fatalf("http %d: checkpoint=%v", status, checkpoint)
			}
		})
	}
}

func TestAutopilotBackendStatusDoesNotLoseSuccess(t *testing.T) {
	statuses := map[string]string{}
	for _, value := range []string{"unavailable", "empty", "ok", "unavailable", "empty", "unknown"} {
		recordAutopilotBackendStatus(statuses, "reddit", value)
	}
	if statuses["reddit"] != "ok" {
		t.Fatalf("later error overwrote success: %v", statuses)
	}
	recordAutopilotBackendStatus(statuses, "another", "unavailable")
	recordAutopilotBackendStatus(statuses, "another", "empty")
	if statuses["another"] != "empty" {
		t.Fatalf("empty response was classified unavailable: %v", statuses)
	}
}
