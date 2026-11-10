package posta

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"time"

	"posta/internal/jx"
)

// История ограничена по размеру и принадлежит пользователю и теме.
var rejectedPostsMu sync.Mutex

func (u *U) rejectedPostsKey(topic string) string {
	return fmt.Sprintf("rejected_posts:%s:%s", u.ID, TopicNorm(topic))
}

func (u *U) RejectedPosts(topic string) []jx.M {
	records := KV(u.App, u.Ctx, u.rejectedPostsKey(topic), []jx.M{})
	sort.SliceStable(records, func(i, j int) bool { return jx.Int(records[i], "at") > jx.Int(records[j], "at") })
	return records
}

func (u *U) rememberRejected(topic, stage string, item jx.M, reason string, score *int) {
	key := CandidatePostKey(item)
	if key == "" {
		key = ContentItemKey(item)
	}
	if key == "" {
		// Даже короткие тексты и записи без медиа должны попадать в историю.
		raw, err := json.Marshal(item)
		if err != nil {
			u.Log("Не удалось сохранить отсеянный пост: %v", err)
			return
		}
		key = fmt.Sprintf("item:%x", sha256.Sum256(raw))
	}
	rejectedPostsMu.Lock()
	defer rejectedPostsMu.Unlock()
	records := u.RejectedPosts(topic)
	filtered := make([]jx.M, 0, min(len(records)+1, 200))
	entry := jx.M{"key": key, "item": item, "stage": stage, "reason": reason, "at": now()}
	if score != nil {
		entry["score"] = *score
	}
	filtered = append(filtered, entry)
	for _, record := range records {
		if jx.Str(record, "key") != key && len(filtered) < 200 {
			filtered = append(filtered, record)
		}
	}
	if err := u.KVSet(u.Ctx, u.rejectedPostsKey(topic), filtered, 30*24*time.Hour); err != nil {
		u.Log("Не удалось сохранить отсеянный пост: %v", err)
	}
}
