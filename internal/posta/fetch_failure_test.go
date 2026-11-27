package posta

import (
	"strings"
	"testing"
)

func TestFetchFailureDetails(t *testing.T) {
	cases := []struct {
		result FetchResult
		want   string
	}{
		{FetchResult{Status: 404}, "HTTP 404"},
		{FetchResult{Error: "connection refused"}, "connection refused"},
		{FetchResult{Status: 200, Error: "unexpected EOF"}, "HTTP 200: unexpected EOF"},
		{FetchResult{Status: 200}, "HTTP 200: пустое тело ответа"},
		{FetchResult{Status: 200, TooBig: true}, "ответ превышает допустимый размер"},
		{FetchResult{}, "HTTP-ответ не получен"},
	}
	for _, tc := range cases {
		if got := fetchFailure(tc.result); !strings.Contains(got, tc.want) {
			t.Errorf("fetchFailure(%+v) = %q, want %q", tc.result, got, tc.want)
		}
	}
}

func TestDiscoveryErrorsReachJournalAndPanel(t *testing.T) {
	var journal, panel []string
	u := &U{log: func(line string) { journal = append(journal, line) }, discoveryIssue: func(line string) { panel = append(panel, line) }}
	u.discoveryNewsError("мемы", FetchResult{Status: 503})
	for _, want := range []string{"Bing News «мемы»: HTTP 503"} {
		if len(journal) != 1 || len(panel) != 1 || !strings.Contains(strings.Join(journal, "\n"), want) || !strings.Contains(strings.Join(panel, "\n"), want) {
			t.Fatalf("missing %q in journal %v or panel %v", want, journal, panel)
		}
	}
}
