package posta

import (
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// Выдача подборки: записи текущей темы и фильтров, по порядку показа.

func (u *U) poolItemMatchesTopic(i jx.M, topic, key string) bool {
	t := jx.Str(i, "topic")
	return t == "" || u.TopicSame(t, topic) || u.TopicSame(t, key)
}

// PoolItemInTopic — запись можно показать: подходит теме, не скрыта фильтром Украины и не 18+ без флага.
func (u *U) PoolItemInTopic(i jx.M, topic, key string, adult bool) bool {
	if u.UARecordHidden(i) || (!adult && u.ContentIsAdult(i)) {
		return false
	}
	return u.poolItemMatchesTopic(i, topic, key)
}

// PoolTopicPositions — позиции записей выбранной темы (по ним правятся записи в «Базах данных»).
func (u *U) PoolTopicPositions(pool []jx.M) []int {
	topic, key, adult := u.CurrentTopic(), u.SearchTopicKey(), u.AdultAllowed()
	out := []int{}
	for i, r := range pool {
		if u.PoolItemInTopic(r, topic, key, adult) {
			out = append(out, i)
		}
	}
	return out
}

// FrozenSourceLabels — подписи замороженных источников, как они стоят в записях.
func (u *U) FrozenSourceLabels() map[string]bool {
	labels := map[string]bool{}
	d := u.Catalog()
	for _, ck := range CatalogKinds {
		for _, e := range *d.List(ck.Kind) {
			if IsSourceFrozen(e, now()) {
				name := jx.Str(e, "name")
				if name == "" {
					name = jx.Str(e, "id")
				}
				labels[strings.ToLower(SourceDisplayName(ck.Platform, name))] = true
			}
		}
	}
	return labels
}

// PoolForCurrentTopic — пул без записей другой темы и не подходящих под фильтры; фильтры медиа прячут при выдаче.
func (u *U) PoolForCurrentTopic() []jx.M {
	topic, key, f := u.CurrentTopic(), u.SearchTopicKey(), u.ContentFilterValues()
	frozen := map[string]bool{}
	if jx.Bool(f, "active_sources_only") {
		frozen = u.FrozenSourceLabels()
	}
	pool := u.Pool()
	adult := u.AdultAllowed()
	return selectPoolItems(pool, func(i jx.M) bool {
		return u.PoolItemInTopic(i, topic, key, adult) && u.poolItemFresh(i) && poolMatchesDisplayFilters(i, f, frozen)
	})
}

// ApplyVote — новый пул после голоса или nil (записи уже нет). save — до удаления записи.
func (a *App) ApplyVote(pool []jx.M, image, vote string, reason any, save func(record jx.M, collection string)) []jx.M {
	item := PoolFindItem(pool, image)
	if item == nil {
		return nil
	}
	if vote == "postpone" {
		moved := jx.Copy(item)
		moved["postponed_at"] = float64(now())
		return append(PoolWithoutImage(pool, image), moved)
	}
	save(a.ContentHistoryRecord(item, reason), config.ContentVoteCollections[vote])
	return PoolWithoutImage(pool, image)
}

// poolMatchesDisplayFilters applies view filters without changing the stored pool.
func poolMatchesDisplayFilters(i, f jx.M, frozen map[string]bool) bool {
	return (!jx.Bool(f, "video_only") || PostItemHasVideo(i)) &&
		(!jx.Bool(f, "fresh_only") || !IsPostOutdated(jx.Int(i, "posted_at"), 0, config.ContentFreshMaxAgeDays)) &&
		!frozen[strings.ToLower(jx.Str(i, "source"))] && ContentPassesMediaFilters(i, f)
}

// PoolStaleForCurrentTopic provides a fallback while the collector finds replacements.
func (u *U) PoolStaleForCurrentTopic() []jx.M {
	topic, key, f := u.CurrentTopic(), u.SearchTopicKey(), u.ContentFilterValues()
	frozen := map[string]bool{}
	if jx.Bool(f, "active_sources_only") {
		frozen = u.FrozenSourceLabels()
	}
	adult := u.AdultAllowed()
	return selectPoolItems(u.Pool(), func(item jx.M) bool {
		return !u.poolItemFresh(item) && u.PoolItemInTopic(item, topic, key, adult) && poolMatchesDisplayFilters(item, f, frozen)
	})
}

func selectPoolItems(pool []jx.M, matches func(jx.M) bool) []jx.M {
	var shown []jx.M
	for _, item := range pool {
		if matches(item) {
			shown = append(shown, item)
		}
	}
	return PoolOrder(shown)
}
