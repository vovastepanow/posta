package posta

import (
	"strings"

	"posta/internal/jx"
)

// Stable counters for measuring the selector without conflating model
// availability, content rejection and actual persistence in the pool.
func collectorAdmissionCode(reason string) string {
	switch {
	case reason == "":
		return "accepted"
	case reason == "реклама или не проверена на рекламу":
		return "advertising_or_unknown_ad"
	case reason == "спам":
		return "spam"
	case reason == "не подтверждены обязательные признаки темы":
		return "topic_requirements"
	case strings.HasPrefix(reason, "порог "):
		return "relevance"
	case reason == "нет конкретной пользы, фактов или интересного вывода":
		return "low_editorial_value"
	case reason == "обычное фото, а тема — мемы":
		return "format"
	case reason == "18+ и чувствительный контент выключены" || reason == "украинский контент":
		return "user_filter"
	case reason == "тема поиска сменилась":
		return "topic_changed"
	default:
		return "other"
	}
}

func incrementCollectorCount(counts *map[string]int, key string) {
	if *counts == nil {
		*counts = map[string]int{}
	}
	(*counts)[key]++
}

func collectorCountSnapshot(counts map[string]int) jx.M {
	out := jx.M{}
	for k, n := range counts {
		out[k] = n
	}
	return out
}

// The ordinary collector's source ranking is based on actual admission
// decisions, not on relevance alone. Partial media and user-specific
// filters are not evidence that a source is poor.
func collectorSourceVerdict(a jx.M, reason, evidence string) (passed, known bool) {
	if !validModelContentVerdict(a) {
		return false, false
	}
	switch evidence {
	case "text", "image", "album", "storyboard":
	default:
		return false, false
	}
	switch collectorAdmissionCode(reason) {
	case "accepted":
		return true, true
	case "advertising_or_unknown_ad", "spam", "topic_requirements", "relevance", "low_editorial_value", "format":
		return false, true
	default:
		return false, false
	}
}
