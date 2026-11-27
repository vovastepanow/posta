package posta

import "testing"

func TestSourceTextReviewCacheRequiresSameEvidence(t *testing.T) {
	caps := []string{"one", "two", "three"}
	models := []string{"openrouter:free", "off"}
	base := sourceTextReviewKey("SMM", "definition", "rules", "publisher", caps, models)
	for _, key := range []string{
		sourceTextReviewKey("fitness", "definition", "rules", "publisher", caps, models),
		sourceTextReviewKey("SMM", "updated", "rules", "publisher", caps, models),
		sourceTextReviewKey("SMM", "definition", "rules", "publisher", []string{"one", "two", "changed"}, models),
		sourceTextReviewKey("SMM", "definition", "rules", "publisher", caps, []string{"another", "off"}),
	} {
		if key == base {
			t.Fatal("different review inputs reused")
		}
	}
	ev := autopilotSourceTopicVerdicts(`{"verdicts":["relevant","relevant","off_topic"]}`, 3)
	if !validCachedSourceTextReview(ev, 3) {
		t.Fatal("completed review not cacheable")
	}
	ev.Quality["topic_unknown"] = 1
	if validCachedSourceTextReview(ev, 3) {
		t.Fatal("inconsistent evidence cached")
	}
	if validCachedSourceTextReview(nil, 3) {
		t.Fatal("provider failure cached")
	}
}
