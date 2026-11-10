package posta

import (
	"strings"
	"testing"

	"posta/internal/jx"
)

func TestTaxonomyStructureAndMigrationTargets(t *testing.T) {
	keys, titles := map[string]bool{}, map[string]string{}
	check := func(key, title, about, hint, rubric string) {
		t.Helper()
		if !topicKeyRe.MatchString(key) || keys[key] {
			t.Errorf("invalid or duplicate key %q", key)
		}
		keys[key] = true
		if previous := titles[TopicNorm(title)]; previous != "" {
			t.Errorf("ambiguous title %q: %s and %s", title, previous, key)
		}
		titles[TopicNorm(title)] = key
		if title == "" || runeLen(title) > topicTitleMax || about == "" || runeLen(about) > 200 || runeLen(hint) > 120 || rubric == "" {
			t.Errorf("incomplete or truncated topic %s: title=%d about=%d hint=%d", key, runeLen(title), runeLen(about), runeLen(hint))
		}
	}
	leaves := 0
	for _, branch := range TopicTree {
		check(branch.Key, branch.Title, branch.About, "", branch.Rubric)
		if branch.Parent != "" && !keys[branch.Parent] {
			t.Errorf("branch %s has missing or later parent %s", branch.Key, branch.Parent)
		}
		if len(branch.Children) == 0 {
			t.Errorf("empty branch %s", branch.Key)
		}
		for _, leaf := range branch.Children {
			leaves++
			check(leaf.Key, leaf.Title, leaf.About, leaf.Hint, leaf.Rubric)
			if !strings.HasPrefix(leaf.Key, branch.Key+".") || strings.Count(leaf.Key, ".") != 1 {
				t.Errorf("leaf %s outside branch %s", leaf.Key, branch.Key)
			}
		}
	}
	for old, target := range LegacyTagKeys {
		if target != "" && !keys[target] {
			t.Errorf("migration %s -> %s has no current target", old, target)
		}
		if keys[old] && target != old {
			t.Errorf("active topic %s would be retired on reseed", old)
		}
	}
	if got := MigrateTagKey("memes.dark"); got != "humor.dark" {
		t.Errorf("old dark-humor tag migrated to %q", got)
	}
	for old, want := range map[string]string{"celebs.stars": "celebs", "travel.nature": "nature.landscapes", "hobby.quotes": ""} {
		if got := MigrateTagKey(old); got != want {
			t.Errorf("old tag %s migrated to %q, want %q", old, got, want)
		}
	}
	t.Logf("%d branches, %d leaves", len(TopicTree), leaves)
}

func TestTaxonomyAliasesRemainSearchable(t *testing.T) {
	u := taxonomyTestUser()
	for _, topic := range u.TopicRegistry() {
		for _, alias := range topic.Aliases {
			if u.TopicLookup(alias) == nil {
				t.Errorf("ambiguous alias %q for %s", alias, topic.Key)
			}
		}
	}
	for alias, key := range map[string]string{
		"птицы": "animals.birds", "морские животные": "animals.sea",
		"налоги": "finance.taxes", "местные новости": "news.local",
		"образование": "science.education", "техника": "tech", "искусство": "culture.art",
		"искусство и книги": "culture", "манга": "culture.comics", "здоровье": "health", "ux": "design.interfaces", "раздельный сбор": "environment.waste",
	} {
		if topic := u.TopicLookup(alias); topic == nil || topic.Key != key {
			t.Errorf("%q resolved to %v, want %s", alias, topic, key)
		}
	}
}

func TestTaxonomyBoundaryHintsReachAnalysis(t *testing.T) {
	u := taxonomyTestUser()
	for _, topic := range u.TopicRegistry() {
		if topic.Hint == "" {
			continue
		}
		for _, prompt := range []string{
			u.ContentAnalysisPrompt(topic.Title, "", EmptyHistory(), nil, ""),
			u.TextBatchPrompt(topic.Title, []string{"Пост"}, EmptyHistory()),
		} {
			if !strings.Contains(prompt, topic.Hint) {
				t.Errorf("boundary hint missing for %s", topic.Key)
			}
		}
	}
	for _, key := range []string{"self.esoteric", "expert.esoteric"} {
		if got := MigrateTagKey(key); got != "culture.esoteric" {
			t.Errorf("%s migrated to %s", key, got)
		}
	}
}

func TestInformationalTopicsAcceptTextAndVisualPosts(t *testing.T) {
	u := taxonomyTestUser()
	for _, title := range []string{"Новости", "Технологии", "Наука и образование", "Животные", "Уход за питомцами", "Дом и сад", "Ремонт и стройка", "Медицина", "Сад и огород", "Дизайн", "Экология"} {
		f := u.TopicFormatOf(title)
		if f.Media != "optional" || f.Text != "optional" || f.Judge != "both" {
			t.Errorf("topic %q arbitrarily restricts format: %+v", title, f)
		}
	}
	if !u.TopicIsHumor("Пошлый юмор") || !u.TopicIsRestricted("Пошлый юмор") {
		t.Fatal("lewd humor must keep both humor rules and sensitivity")
	}
	if u.TopicIsRestricted("Эзотерика и астрология") || u.TopicIsRestricted("Психология") {
		t.Fatal("ordinary taxonomy topic incorrectly restricted")
	}
	if u.TopicParent(u.TopicLookup("Эзотерика и астрология")).Key != "culture" {
		t.Fatal("esotericism still grouped under psychology")
	}
	// Public tags must retain the restored specific leaves instead of collapsing them.
	if got := u.ContentTagsClean([]string{"animals.birds", "animals.sea"}); !jx.Equal(got, []string{"animals.birds", "animals.sea"}) {
		t.Fatalf("specific animal topics lost: %v", got)
	}
}

func TestHistoricalSeedAliasesKeepTheirNewMeaning(t *testing.T) {
	u := taxonomyTestUser()
	// Reseeding retains old aliases along with user-added aliases. They must not
	// make the new specific topics disappear from lookup after an upgrade.
	for key, alias := range map[string]string{
		"screen.anime": "манга", "health.medicine": "здоровье",
		"science": "образование", "transport": "техника", "culture": "искусство",
	} {
		for _, topic := range u.TopicRegistry() {
			if topic.Key == key {
				topic.Aliases = append(topic.Aliases, alias)
			}
		}
	}
	u.catalog.idx = buildTopicIndex(u.TopicRegistry())
	for alias, key := range map[string]string{
		"манга": "culture.comics", "здоровье": "health", "образование": "science.education", "техника": "tech", "искусство": "culture.art",
	} {
		if got := u.TopicLookup(alias); got == nil || got.Key != key {
			t.Errorf("historical alias %q resolved to %v, want %s", alias, got, key)
		}
	}
}
