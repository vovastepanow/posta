package posta

import (
	"testing"

	"posta/internal/jx"
)

func TestVisiblePoolQuotaKeepsHiddenFreshRecords(t *testing.T) {
	same := func(it jx.M) bool { return jx.Str(it, "topic") == "SMM" }
	visible := func(it jx.M) bool { return !jx.Bool(it, "hidden") }
	fresh := func(it jx.M) bool { return !jx.Bool(it, "expired") }
	hidden := jx.M{"topic": "SMM", "hidden": true, "pooled_at": 10}
	shown := jx.M{"topic": "SMM", "pooled_at": 20}
	other := jx.M{"topic": "Other", "pooled_at": 1}
	pool := []jx.M{hidden, hidden, other}
	if remove, ok := poolQuotaInsertion(pool, 2, same, visible, fresh); !ok || remove != -1 {
		t.Fatal("hidden records blocked a usable candidate or were deleted")
	}
	pool = append(pool, shown, shown)
	if _, ok := poolQuotaInsertion(pool, 2, same, visible, fresh); ok {
		t.Fatal("visible target was exceeded")
	}
	if _, ok := poolQuotaInsertion([]jx.M{hidden, hidden, hidden, hidden, other}, 2, same, visible, fresh); ok {
		t.Fatal("storage reserve was unbounded")
	}
	expired := jx.Merge(hidden, jx.M{"expired": true, "pooled_at": 2})
	if remove, ok := poolQuotaInsertion([]jx.M{hidden, other, expired, shown}, 2, same, visible, fresh); !ok || remove != 2 {
		t.Fatalf("oldest expired same-profile record was not replaced: %d %t", remove, ok)
	}
}
