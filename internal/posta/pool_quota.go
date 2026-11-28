package posta

import "posta/internal/jx"

// Keep up to target visible fresh posts and one target-sized reserve of
// hidden records. Never discard a fresh post merely because filters changed.
// Expired records can be replaced as before; this bounds per-profile storage.
func poolQuotaInsertion(pool []jx.M, target int, same, visible, fresh func(jx.M) bool) (remove int, allowed bool) {
	_, remove, allowed = poolQuotaState(pool, target, same, visible, fresh)
	return
}

func poolQuotaState(pool []jx.M, target int, same, visible, fresh func(jx.M) bool) (available, remove int, allowed bool) {
	total, oldest := 0, -1
	for i, item := range pool {
		if !same(item) {
			continue
		}
		total++
		if fresh(item) {
			if visible(item) {
				available++
			}
		} else if oldest < 0 || jx.Int(item, "pooled_at") < jx.Int(pool[oldest], "pooled_at") {
			oldest = i
		}
	}
	if available >= target {
		return available, -1, false
	}
	if total < target {
		return available, -1, true
	}
	if oldest >= 0 {
		return available, oldest, true
	}
	return available, -1, total < 2*target
}

// poolRefillState is shared by the background launch condition and the
// collector stop condition, so a full hidden reserve does not trigger
// repeated futile collection runs.
func (u *U) poolRefillState(key string, target int) (available int, allowed bool, err error) {
	visible, err := u.poolVisibilityFilter(key)
	if err != nil {
		return 0, false, err
	}
	available, _, allowed = poolQuotaState(u.Pool(), target, func(record jx.M) bool {
		return u.poolProfileMatches(jx.Str(record, "topic"), key)
	}, visible, u.poolItemFresh)
	return available, allowed, nil
}

func (u *U) sourceVisibleQuotaReached(key, source string) bool {
	visible, err := u.poolVisibilityFilter(key)
	if err != nil {
		return false
	}
	pool := u.visibleFreshPool(u.Pool(), key, visible)
	n := 0
	for _, item := range pool {
		if ContentSourceKey(item) == source {
			n++
		}
	}
	return n >= sourcePoolLimit(u.TInt("per_source_pool"), u.TInt("pool_target"), len(pool))
}
