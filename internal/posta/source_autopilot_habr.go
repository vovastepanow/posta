package posta

import (
	"fmt"
	"strings"
	"time"
)

// The ordinary Habr finder appends a language filter to the feed URL. The
// shared catalog excludes query strings, so auto uses the public unfiltered
// feed and still subjects each post to its normal language and safety checks.
func autopilotHabrCandidates(page string, query string) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	for _, c := range discoveryHabrHubs(page, []string{query}) {
		c.ID = strings.TrimSuffix(c.ID, "?fl=ru")
		if _, ok := autopilotPublicRSSID(c.ID); ok {
			out = append(out, c)
		}
	}
	return out
}

func (u *U) autopilotFindHabr(query string, turn int) []DiscoveryCandidate {
	var out []DiscoveryCandidate
	for i := 0; i < 2; i++ {
		page := (turn*2+i)%11 + 1
		r := u.RSSGet(fmt.Sprintf("https://habr.com/ru/hubs/page%d/", page), 3<<20, 15*time.Second, BrowserUA)
		status := "unavailable"
		if r.OK && strings.Contains(string(r.Bytes), "/ru/hubs/") {
			found := autopilotHabrCandidates(string(r.Bytes), query)
			status = "ok"
			out = append(out, found...)
		}
		if u.discoverySearchObservation != nil {
			u.discoverySearchObservation("habr", query, status)
		}
		if u.Ctx.Err() != nil || autopilotBudgetError(u.Ctx) != nil {
			break
		}
	}
	return out
}
