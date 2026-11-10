package posta

import "posta/internal/config"

// Retries remain attached to the topic until they succeed; next_at survives restarts.
type discoveryRetry struct {
	Candidate DiscoveryCandidate `json:"candidate"`
	Attempts  int                `json:"attempts"`
	NextAt    int64              `json:"next_at"`
}

func discoveryRetryDelay(attempts int) int64 {
	delay := int64(config.DaySeconds) * int64(1<<min(max(attempts-1, 0), 3))
	return min(delay, 7*int64(config.DaySeconds))
}
func discoveryRetryDue(retries []discoveryRetry, at int64) bool {
	for _, r := range retries {
		if r.NextAt <= at {
			return true
		}
	}
	return false
}
func discoveryRemoveRetry(retries []discoveryRetry, c DiscoveryCandidate) []discoveryRetry {
	id := SourceIDFor(c.Platform, c.ID)
	out := retries[:0]
	for _, r := range retries {
		if SourceIDFor(r.Candidate.Platform, r.Candidate.ID) != id {
			out = append(out, r)
		}
	}
	return out
}
func discoveryScheduleRetry(retries []discoveryRetry, c DiscoveryCandidate, at int64) []discoveryRetry {
	attempts := 1
	id := SourceIDFor(c.Platform, c.ID)
	for _, r := range retries {
		if SourceIDFor(r.Candidate.Platform, r.Candidate.ID) == id {
			attempts = r.Attempts + 1
			break
		}
	}
	retries = discoveryRemoveRetry(retries, c)
	return append(retries, discoveryRetry{Candidate: c, Attempts: attempts, NextAt: at + discoveryRetryDelay(attempts)})
}
