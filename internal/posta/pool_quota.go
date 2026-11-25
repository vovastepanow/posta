package posta

import "posta/internal/jx"

// Keep up to target visible fresh posts and one target-sized reserve of
// hidden records. Never discard a fresh post merely because filters changed.
// Expired records can be replaced as before; this bounds per-profile storage.
func poolQuotaInsertion(pool []jx.M, target int, same, visible, fresh func(jx.M) bool) (remove int, allowed bool) {
	if target <= 0 {
		return -1, false
	}
	total, available, oldest := 0, 0, -1
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
		return -1, false
	}
	if total < target {
		return -1, true
	}
	if oldest >= 0 {
		return oldest, true
	}
	return -1, total < 2*target
}

// poolRefillState is shared by the background launch condition and the
// collector stop condition, so a full hidden reserve does not trigger
// repeated futile collection runs.
func (u *U) poolRefillState(key string, target int) (available int, allowed bool, err error) {
	visible, err := u.poolVisibilityFilter(key)
	if err != nil {
		return 0, false, err
	}
	pool := u.Pool()
	available = len(u.visibleFreshPool(pool, key, visible))
	if available >= target {
		return available, false, nil
	}
	_, allowed = poolQuotaInsertion(pool, target, func(record jx.M) bool {
		return u.poolProfileMatches(jx.Str(record, "topic"), key)
	}, visible, u.poolItemFresh)
	return available, allowed, nil
}
