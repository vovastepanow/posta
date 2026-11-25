package posta

import (
	"strings"

	"posta/internal/config"
	"posta/internal/jx"
)

// Выдача подборки: записи текущей темы и фильтров, по порядку показа.

// A base-topic verdict is not evidence for a narrower query or the
// conjunction of two topics. Compare the actual analyzed profile, allowing
// aliases only for the topic components, not for an unrelated query.
func (u *U) poolProfileMatches(record, key string) bool {
	if !strings.EqualFold(NormalizeQuery(TopicQueryOf(record)), NormalizeQuery(TopicQueryOf(key))) {
		return false
	}
	want, have := TopicParts(key), TopicParts(record)
	if len(want) == 0 || len(have) != len(want) {
		return false
	}
	for i, part := range want {
		if !u.TopicSame(have[i], part) {
			return false
		}
	}
	return true
}

func (u *U) poolItemMatchesTopic(i jx.M, topic, key string) bool {
	if key == "" {
		key = topic
	}
	t := jx.Str(i, "topic")
	if t == "" {
		// Unstamped legacy records cannot satisfy a specific search.
		return TopicQueryOf(key) == "" && len(TopicParts(key)) == 1
	}
	return u.poolProfileMatches(t, key)
}

// PoolItemInTopic — запись можно показать: подходит теме, не скрыта фильтром Украины и не 18+ без флага.
func (u *U) PoolItemInTopic(i jx.M, topic, key string, adult bool) bool {
	if PostIsAdvertisement(i) || u.UARecordHidden(i) || (!adult && u.ContentIsAdult(i)) {
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

// PoolForCurrentTopic — весь сохранённый пул текущей темы (в пределах
// срока хранения), кроме явно скрытого фильтрами. Часовая свежесть
// используется для пополнения, но не должна скрывать записи Предложки.
func (u *U) PoolForCurrentTopic() []jx.M {
	topic, key, f := u.CurrentTopic(), u.SearchTopicKey(), u.ContentFilterValues()
	frozen := map[string]bool{}
	if jx.Bool(f, "active_sources_only") {
		frozen = u.FrozenSourceLabels()
	}
	pool := u.Pool()
	adult := u.AdultAllowed()
	blogs, err := u.poolPersonalBlogFilter(topic)
	if err != nil {
		return nil
	}
	return u.selectPersonalPoolItems(pool, topic, key, func(i jx.M) bool {
		return blogs(i) && u.PoolItemInTopic(i, topic, key, adult) && poolMatchesDisplayFilters(i, f, frozen)
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
	blogs, err := u.poolPersonalBlogFilter(topic)
	if err != nil {
		return nil
	}
	return u.selectPersonalPoolItems(u.Pool(), topic, key, func(item jx.M) bool {
		return blogs(item) && !u.poolItemFresh(item) && u.PoolItemInTopic(item, topic, key, adult) && poolMatchesDisplayFilters(item, f, frozen)
	})
}

func matchingPoolItems(pool []jx.M, matches func(jx.M) bool) []jx.M {
	var shown []jx.M
	for _, item := range pool {
		if matches(item) {
			shown = append(shown, item)
		}
	}
	return shown
}

func selectPoolItems(pool []jx.M, matches func(jx.M) bool) []jx.M {
	return PoolOrder(matchingPoolItems(pool, matches))
}

func (u *U) selectPersonalPoolItems(pool []jx.M, topic, key string, matches func(jx.M) bool) []jx.M {
	shown := matchingPoolItems(pool, matches)
	if len(shown) < 2 {
		return shown
	}
	affinity := u.loadPersonalPoolAffinity(topic, key)
	professional := u.professionalValueTopic(topic) && !u.TopicIsHumor(topic)
	return poolOrderWithPreference(shown, func(item jx.M) int64 {
		return poolEditorialBonus(item, professional) + affinity.bonus(item)
	})
}
