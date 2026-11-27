package posta

import "posta/internal/jx"

func autopilotProbationPoolAllowed(pool []jx.M, topic, source string, probation map[string]bool, target, percent int) bool {
	if !probation[source] {
		return true
	}
	limit := max(1, target*max(1, min(100, percent))/100)
	count := 0
	topicKey := autopilotTopicKey(topic)
	for _, item := range pool {
		if autopilotTopicKey(jx.Str(item, "topic")) != topicKey {
			continue
		}
		current := ContentSourceKey(item)
		if current == source {
			return false
		}
		if probation[current] {
			count++
		}
	}
	return count < limit
}
