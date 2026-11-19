package posta

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strings"

	"posta/internal/db"
)

type autopilotDemandSettings struct {
	Topic, Mode              string
	PostsPerDay, HorizonDays float64
	Paused                   bool
}

var errAutopilotInvalidDemand = errors.New("invalid autopilot demand")

// The original "smm" topic has a live discovery queue. Reuse its existing
// key for the taxonomy title and its explicit aliases so no source state,
// evidence, or history is orphaned by the canonical display name.
func autopilotCanonicalTopic(topic string) string {
	switch TopicNorm(topic) {
	case "smm", "смм", "smm и блогинг":
		return "SMM и блогинг"
	default:
		return strings.TrimSpace(topic)
	}
}

func autopilotTopicKey(topic string) string {
	normalized := strings.ToLower(collapseSpaces(strings.TrimSpace(topic)))
	if autopilotCanonicalTopic(topic) == "SMM и блогинг" {
		normalized = "smm" // historical key; existing Posta data stays reachable
	}
	return fmt.Sprintf("v%d:%s", TopicProfileVersion, repeatDigest(normalized))
}

func saveAutopilotDemand(ctx context.Context, q db.Querier, userID string, demand autopilotDemandSettings, updatedAt int64) error {
	topic := autopilotCanonicalTopic(demand.Topic)
	if userID == "" || topic == "" || TopicNorm(topic) == "" || (demand.Mode != "off" && demand.Mode != "shadow" && demand.Mode != "auto") || math.IsNaN(demand.PostsPerDay) || math.IsInf(demand.PostsPerDay, 0) || demand.PostsPerDay < 0 || demand.PostsPerDay > 1000 || math.IsNaN(demand.HorizonDays) || math.IsInf(demand.HorizonDays, 0) || demand.HorizonDays <= 0 || demand.HorizonDays > 365 {
		return errAutopilotInvalidDemand
	}
	_, err := q.Exec(ctx, `INSERT INTO source_autopilot_demands(user_id,topic_key,topic,mode,posts_per_day,horizon_days,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(user_id,topic_key) DO UPDATE SET
		topic=EXCLUDED.topic,mode=EXCLUDED.mode,posts_per_day=EXCLUDED.posts_per_day,
		horizon_days=EXCLUDED.horizon_days,updated_at=EXCLUDED.updated_at,paused=false`,
		userID, autopilotTopicKey(topic), topic, demand.Mode, demand.PostsPerDay, demand.HorizonDays, updatedAt)
	return err
}

func listAutopilotDemands(ctx context.Context, q db.Querier, userID string) ([]autopilotDemandSettings, error) {
	rows, err := q.Query(ctx, `SELECT topic,mode,posts_per_day,horizon_days,paused FROM source_autopilot_demands WHERE user_id=$1 ORDER BY topic_key`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []autopilotDemandSettings
	for rows.Next() {
		var d autopilotDemandSettings
		if err := rows.Scan(&d.Topic, &d.Mode, &d.PostsPerDay, &d.HorizonDays, &d.Paused); err != nil {
			return nil, err
		}
		d.Topic = autopilotCanonicalTopic(d.Topic)
		result = append(result, d)
	}
	return result, rows.Err()
}
