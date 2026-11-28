package posta

import "posta/internal/jx"

// Retrieval rank is an ephemeral hint, not an AI verdict or admission score.
func withInventoryRank(item jx.M, query string, rank int) jx.M {
	out := jx.Copy(item)
	out["_inventory_rank_for"] = TopicNorm(query)
	out["_inventory_rank"] = rank
	return out
}

func inventoryRank(item jx.M, query string) int {
	if query == "" || jx.Str(item, "_inventory_rank_for") != TopicNorm(query) {
		return 0
	}
	n := int(jx.Int(item, "_inventory_rank"))
	if n < 1 || n > 240 {
		return 0
	}
	return n
}

func inventoryPriority(item jx.M, query string) bool {
	rank := inventoryRank(item, query)
	return rank > 0 && rank <= 60
}
