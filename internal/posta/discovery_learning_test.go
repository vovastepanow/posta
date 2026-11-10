package posta

import (
	"testing"
)

func TestDiscoveryQueryLearning(t *testing.T) {
	p := discoveryProgress{}
	c := DiscoveryCandidate{Platform: PlatformTelegram, ID: "example", Origin: "поиск Telegram «котики»"}
	discoveryRecordQuery(&p, c, 1, 0, 0)
	discoveryRecordQuery(&p, c, 0, 1, 1)
	key, _ := discoveryQueryKey(c)
	s := p.Queries[key]
	if s.Found != 1 || s.Checked != 1 || s.Accepted != 1 {
		t.Fatalf("unexpected stats: %+v", s)
	}
	discoveryRecordQuery(&p, DiscoveryCandidate{Platform: PlatformTelegram, Origin: "похож на @foo"}, 1, 0, 0)
	if len(p.Queries) != 1 {
		t.Fatal("unattributed candidate should not affect a query")
	}
	stats := map[string]discoveryQueryStats{"a": {Query: "добрый", Checked: 10, Accepted: 9}, "b": {Query: "плохой", Checked: 10, Accepted: 0}}
	ranked := discoveryRankQueries([]string{"плохой", "добрый", "новый"}, stats, 0)
	if ranked[0] != "добрый" || len(ranked) != 3 {
		t.Fatalf("ranking lost evidence or exploration: %v", ranked)
	}
}

func TestDiscoveryAutoNeedsEvidence(t *testing.T) {
	ev := &sourceEvaluation{Goodness: &GoodnessResult{Score: 75}}
	for _, tc := range []struct {
		score, threshold, posts int
		evaluation              *sourceEvaluation
		want                    bool
	}{
		{95, 90, 3, ev, true}, {89, 90, 3, ev, false}, {95, 90, 2, ev, false}, {95, 90, 3, nil, false},
		{95, 90, 3, &sourceEvaluation{Goodness: &GoodnessResult{Score: 69}}, false},
	} {
		if got := discoveryAutoEligible(tc.score, tc.threshold, tc.evaluation, tc.posts); got != tc.want {
			t.Fatalf("unexpected auto decision for %+v: %v", tc, got)
		}
	}
}

func TestDiscoveryRetryScheduleAndRemove(t *testing.T) {
	c := DiscoveryCandidate{Platform: PlatformTelegram, ID: "retry_test"}
	const at = int64(1800000000)
	r := discoveryScheduleRetry(nil, c, at)
	if len(r) != 1 || r[0].NextAt != at+discoveryRetryDelay(1) || discoveryRetryDue(r, at) || !discoveryRetryDue(r, r[0].NextAt) {
		t.Fatalf("bad first retry: %+v", r)
	}
	r = discoveryScheduleRetry(r, c, r[0].NextAt)
	if len(r) != 1 || r[0].Attempts != 2 || discoveryRetryDelay(2) != 2*discoveryRetryDelay(1) {
		t.Fatalf("bad backoff: %+v", r)
	}
	if len(discoveryRemoveRetry(r, c)) != 0 || discoveryRetryDelay(10) != 7*discoveryRetryDelay(1) {
		t.Fatal("retry removal or cap failed")
	}
}

func TestDiscoverySearchOutcomes(t *testing.T) {
	p := discoveryProgress{}
	discoveryObserveQuery(&p, "vk", "собаки", "unavailable")
	discoveryObserveQuery(&p, "vk", "собаки", "empty")
	discoveryObserveQuery(&p, "vk", "собаки", "ok")
	s := p.Queries["vk:"+TopicNorm("собаки")]
	if s.Searches != 2 || s.Empty != 1 || s.Unavailable != 1 || s.LastStatus != "ok" {
		t.Fatalf("unavailable must not count as empty: %+v", s)
	}
}

func TestDiscoveryQueueDiversifiesEqualCandidates(t *testing.T) {
	q := newDiscoveryQueue()
	for _, origin := range []string{"поиск Telegram «кошки»", "поиск Telegram «кошки»", "поиск Telegram «собаки»"} {
		q.add("telegram", DiscoveryCandidate{Platform: PlatformTelegram, ID: origin, Origin: origin}, false)
	}
	first, second := q.next(), q.next()
	_, a := discoveryQueryKey(*first)
	_, b := discoveryQueryKey(*second)
	if a == b {
		t.Fatalf("same query twice despite an alternative: %q", a)
	}
}
