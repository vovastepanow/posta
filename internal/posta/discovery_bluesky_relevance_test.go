package posta

import (
	"fmt"
	"testing"
)

func TestBlueskyQueryEvidenceIsWholeWord(t *testing.T) {
	for _, tc := range []struct {
		query, evidence string
		want bool
	}{
		{"SMM", "Womens MMA and MMA fights", false},
		{"SMM", "Practical SMM case studies", true},
		{"маркетинг", "Кейсы по маркетингу", false},
		{"маркетинг", "Маркетинг: реальные кейсы", true},
		{"social media", "daily life and MMA highlights", false},
		{"social media", "Social media analytics", true},
	} {
		if got := blueskyQueryEvidenceMatch(tc.query, tc.evidence); got != tc.want {
			t.Errorf("query=%q evidence=%q got=%v want=%v", tc.query, tc.evidence, got, tc.want)
		}
	}
}

func TestDiscoveryBlueskyPostSearchRejectsIncidentalMentions(t *testing.T) {
	const expert = "did:plc:oky5czdrnfjpqslsw2a5iclo"
	const unrelated = "did:plc:3tkb3equ4w4nz3hiplaner7d"
	const specialist = "did:plc:z72i7hdynmk6r22z27h6tvur"
	body := []byte(fmt.Sprintf(`{"posts":[
		{"author":{"did":%q,"displayName":"SMM insights"},"record":{"text":"An SMM case study"}},
		{"author":{"did":%q,"displayName":"MMA highlights"},"record":{"text":"MMA fight card"}},
		{"author":{"did":%q,"displayName":"MMA highlights"},"record":{"text":"I saw an SMM post"}},
		{"author":{"did":%q,"displayName":"MMA highlights"},"record":{"text":"More MMA highlights"}},
		{"author":{"did":%q,"displayName":"Life notes"},"record":{"text":"An SMM report"}},
		{"author":{"did":%q,"displayName":"Life notes"},"record":{"text":"Another SMM report"}}
	]}`, expert, unrelated, unrelated, unrelated, specialist, specialist))
	found, err := discoveryBlueskyRelevantPostAuthors(body, "SMM")
	if err != nil {
		t.Fatal(err)
	}
	if len(found) != 2 {
		t.Fatalf("got %d topical authors, want 2: %#v", len(found), found)
	}
	for _, c := range found {
		if c.ID == "https://bsky.app/profile/"+unrelated+"/rss" {
			t.Fatal("incidental, unrelated MMA author admitted")
		}
	}
}
