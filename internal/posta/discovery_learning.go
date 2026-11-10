package posta

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"posta/internal/jx"
	"posta/internal/llm"
)

// Query results are attributed only when a finder identifies the query in its origin.
type discoveryQueryStats struct {
	Query       string `json:"query"`
	Platform    string `json:"platform"`
	Found       int    `json:"found"`
	Checked     int    `json:"checked"`
	Accepted    int    `json:"accepted"`
	LastSeen    int64  `json:"last_seen"`
	Searches    int    `json:"searches,omitempty"`
	Empty       int    `json:"empty,omitempty"`
	Unavailable int    `json:"unavailable,omitempty"`
	LastStatus  string `json:"last_status,omitempty"`
	SearchAt    int64  `json:"search_at,omitempty"`
}

func discoveryQueryKey(c DiscoveryCandidate) (string, string) {
	origin := strings.ToLower(c.Origin)
	if !strings.Contains(origin, "поиск") && !strings.Contains(origin, "запрос") && !strings.Contains(origin, "авторы видео") {
		return "", ""
	}
	start := strings.Index(c.Origin, "«")
	end := strings.Index(c.Origin, "»")
	if start < 0 || end <= start {
		return "", ""
	}
	q := DiscoveryCleanQuery(c.Origin[start+len("«") : end])
	if q == "" {
		return "", ""
	}
	platform := DiscoveryGroupOf(c.Platform, c.ID)
	return platform + ":" + TopicNorm(q), q
}

func discoveryRecordQuery(p *discoveryProgress, c DiscoveryCandidate, found, checked, accepted int) {
	key, query := discoveryQueryKey(c)
	if key == "" {
		return
	}
	if p.Queries == nil {
		p.Queries = map[string]discoveryQueryStats{}
	}
	s := p.Queries[key]
	s.Query, s.Platform, s.LastSeen = query, DiscoveryGroupOf(c.Platform, c.ID), now()
	s.Found += found
	s.Checked += checked
	s.Accepted += accepted
	p.Queries[key] = s
}

func discoveryObserveQuery(p *discoveryProgress, platform, query, status string) {
	query = DiscoveryCleanQuery(query)
	if query == "" {
		return
	}
	if p.Queries == nil {
		p.Queries = map[string]discoveryQueryStats{}
	}
	key := platform + ":" + TopicNorm(query)
	s := p.Queries[key]
	s.Query, s.Platform, s.LastSeen, s.LastStatus, s.SearchAt = query, platform, now(), status, now()
	switch status {
	case "empty":
		s.Searches++
		s.Empty++
	case "ok":
		s.Searches++
	case "unavailable":
		s.Unavailable++
	}
	p.Queries[key] = s
}

// Half the leading slots favour proven queries; the rotating half keeps exploring.
func discoveryRankQueries(queries []string, stats map[string]discoveryQueryStats, turn int) []string {
	rotated := DiscoveryRotate(queries, turn*3)
	score := func(q string) float64 {
		var checked, accepted, searches, empty int
		for _, s := range stats {
			if TopicNorm(s.Query) == TopicNorm(q) {
				checked += s.Checked
				accepted += s.Accepted
				searches += s.Searches
				empty += s.Empty
			}
		}
		value := float64(accepted+1) / float64(checked+3)
		if searches >= 3 && empty == searches {
			value *= 0.5
		}
		return value
	}
	ranked := append([]string{}, rotated...)
	sort.SliceStable(ranked, func(i, j int) bool { return score(ranked[i]) > score(ranked[j]) })
	var out []string
	seen := map[string]bool{}
	add := func(q string) {
		k := TopicNorm(q)
		if !seen[k] {
			seen[k] = true
			out = append(out, q)
		}
	}
	for _, q := range ranked[:min(2, len(ranked))] {
		add(q)
	}
	for _, q := range rotated {
		add(q)
	}
	return out
}

// Model expansion is rate limited per topic; generated queries still pass the usual source checks.
func (u *U) discoveryExpandQueries(topic string, existing []string) []string {
	prompt := fmt.Sprintf("Предложи до 8 коротких поисковых запросов для поиска источников по теме %q: синонимы, подтемы, разговорные названия. Только релевантные теме запросы из 1–4 слов. Не придумывай адреса. Уже использованы: %s. Ответь JSON: {\"queries\":[\"...\"]}.", topic, strings.Join(existing, ", "))
	schema := jx.M{"type": "object", "properties": jx.M{"queries": jx.M{"type": "array", "items": jx.M{"type": "string"}}}, "required": []string{"queries"}}
	ctx, cancel := context.WithTimeout(u.Ctx, 90*time.Second)
	defer cancel()
	resp, err := u.LLM.Generate(ctx, &llm.Request{Role: llm.RoleSmart, Prompt: prompt, Schema: schema, MaxTokens: 300, Temperature: 0.4, UserID: u.ID})
	if err != nil {
		u.Log("-> Расширение запросов недоступно: %v", err)
		return nil
	}
	data := llm.DecodeJSON(resp.Text)
	seen := map[string]bool{}
	for _, q := range existing {
		seen[TopicNorm(q)] = true
	}
	var out []string
	for _, q := range jx.Strs(data, "queries") {
		q = DiscoveryCleanQuery(q)
		if q == "" || TextIsUkrainian(q) || seen[TopicNorm(q)] {
			continue
		}
		seen[TopicNorm(q)] = true
		out = append(out, q)
		if len(out) == 8 {
			break
		}
	}
	return out
}

// discoveryQueryInsight describes observed candidate checks, not owner approval.
func discoveryQueryInsight(stats map[string]discoveryQueryStats) string {
	var best discoveryQueryStats
	for _, s := range stats {
		if s.Checked < 3 {
			continue
		}
		if best.Checked == 0 || float64(s.Accepted+1)/float64(s.Checked+3) > float64(best.Accepted+1)/float64(best.Checked+3) ||
			(float64(s.Accepted+1)/float64(s.Checked+3) == float64(best.Accepted+1)/float64(best.Checked+3) && s.Query < best.Query) {
			best = s
		}
	}
	if best.Checked == 0 {
		return "Поисковые запросы ещё набирают статистику."
	}
	return fmt.Sprintf("Запрос «%s» (%s): подходящих %d из %d проверенных.", best.Query, discoveryGroupNames[best.Platform], best.Accepted, best.Checked)
}

func discoveryAutoEligible(score, threshold int, ev *sourceEvaluation, posts int) bool {
	return score >= threshold && ev != nil && ev.Goodness != nil && ev.Goodness.Score >= 70 && posts >= 3
}

func discoveryLastSearch(stats map[string]discoveryQueryStats) string {
	var last discoveryQueryStats
	for _, s := range stats {
		if s.LastStatus != "" && (s.SearchAt > last.SearchAt || s.SearchAt == last.SearchAt && s.Query < last.Query) {
			last = s
		}
	}
	if last.LastStatus == "" {
		return ""
	}
	result := map[string]string{"ok": "получен ответ", "empty": "пустой результат", "unavailable": "площадка недоступна"}[last.LastStatus]
	return fmt.Sprintf("%s · «%s»: %s.", discoveryGroupNames[last.Platform], last.Query, result)
}
