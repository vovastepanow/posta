package posta

import (
	"sort"

	"posta/internal/jx"
)

// Правило «один пост с источника»: в ленте темы не больше per_source_pool записей одного источника,
// а после оценки источник отдыхает source_cooldown_hours часов. Отметки об оценке — в kv по пользователю и теме.

// sourceRestKeepHours — отметки старше этого срока не храним (больше отдыха не бывает).
const sourceRestKeepHours = 168

func sourceRestKey(uid, topic string) string { return "source_rest:" + uid + ":" + TopicNorm(topic) }

// sourcePoolCount — сколько записей этой темы из этого источника уже в пуле.
func sourcePoolCount(pool []jx.M, topic, src string) int {
	t, n := TopicNorm(topic), 0
	for _, it := range pool {
		if TopicNorm(jx.Str(it, "topic")) == t && ContentSourceKey(it) == src {
			n++
		}
	}
	return n
}

// sourceBusy — источники, которых сейчас не берём в тему: записей в ленте уже per_source_pool или идёт отдых после оценки.
func (u *U) sourceBusy(topic string) map[string]bool {
	busy := map[string]bool{}
	counts := map[string]int{}
	t := TopicNorm(topic)
	for _, it := range u.Pool() {
		if TopicNorm(jx.Str(it, "topic")) == t {
			counts[ContentSourceKey(it)]++
		}
	}
	perSource := u.TInt("per_source_pool")
	for src, n := range counts {
		if n >= perSource {
			busy[src] = true
		}
	}
	cutoff := now() - int64(u.TInt("source_cooldown_hours"))*3600
	for src, at := range KV(u.App, u.Ctx, sourceRestKey(u.ID, topic), map[string]int64{}) {
		if at > cutoff {
			busy[src] = true
		}
	}
	return busy
}

// MarkSourceRest — источник записи оценён: отдыхает source_cooldown_hours. Вызывается при любом голосе, кроме «отложить».
func (u *U) MarkSourceRest(topic string, item jx.M) {
	src := ContentSourceKey(item)
	_, _ = KVUpdate(u.App, u.Ctx, sourceRestKey(u.ID, topic), 0, func(cur map[string]int64, exists bool) *map[string]int64 {
		m := map[string]int64{src: now()}
		for k, at := range cur {
			if k != src && now()-at <= sourceRestKeepHours*3600 {
				m[k] = at
			}
		}
		return &m
	})
}

// PruneSourceDuplicates — одноразово при первом проходе: в каждой теме у источника остаются только per_source_pool самых свежих записей.
func (u *U) PruneSourceDuplicates() int {
	flag := "source_pool_pruned:" + u.ID
	if KV(u.App, u.Ctx, flag, false) {
		return 0
	}
	keep := u.TInt("per_source_pool")
	before := u.Pool()
	after, _ := u.PoolUpdate(func(pool []jx.M) []jx.M {
		groups := map[string][]int{}
		for i, it := range pool {
			k := TopicNorm(jx.Str(it, "topic")) + "\x00" + ContentSourceKey(it)
			groups[k] = append(groups[k], i)
		}
		drop := map[int]bool{}
		for _, idx := range groups {
			if len(idx) <= keep {
				continue
			}
			sort.SliceStable(idx, func(a, b int) bool { return jx.Int(pool[idx[a]], "posted_at") > jx.Int(pool[idx[b]], "posted_at") })
			for _, i := range idx[keep:] {
				drop[i] = true
			}
		}
		if len(drop) == 0 {
			return nil
		}
		out := []jx.M{}
		for i, it := range pool {
			if !drop[i] {
				out = append(out, it)
			}
		}
		return out
	})
	u.MediaReleaseRemoved(u.Ctx, before, after)
	_ = u.KVSet(u.Ctx, flag, true, 0)
	return len(before) - len(after)
}

// dropTakenSources — убирает кандидатов источников, которые уже взяты в этом проходе.
func dropTakenSources(items []jx.M, taken map[string]bool) []jx.M {
	if len(taken) == 0 {
		return items
	}
	out := []jx.M{}
	for _, it := range items {
		if !taken[ContentSourceKey(it)] {
			out = append(out, it)
		}
	}
	return out
}
