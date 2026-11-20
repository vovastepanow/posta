package posta

import (
	"fmt"
	"testing"

	"posta/internal/jx"
)

func TestAutopilotSMMCanonicalKeyKeepsLegacySources(t *testing.T) {
	legacy := fmt.Sprintf("v%d:%s", TopicProfileVersion, repeatDigest("smm"))
	for _, title := range []string{"smm", "SMM", "смм", "SMM и блогинг", "  smm  "} {
		if got := autopilotTopicKey(title); got != legacy {
			t.Errorf("%q got topic key %q, want existing %q", title, got, legacy)
		}
		if got := autopilotCanonicalTopic(title); got != "SMM и блогинг" {
			t.Errorf("%q canonical title = %q", title, got)
		}
	}
	if autopilotTopicKey("Маркетинг и реклама") == legacy {
		t.Fatal("separate marketing topic was merged into SMM")
	}
	if autopilotTopicKey("Блог врача") == legacy {
		t.Fatal("unrelated blog was merged into SMM")
	}
}

func TestAutopilotGeneralTaxonomyAliases(t *testing.T) {
	for _, tc := range []struct{ input, expected string }{
		{"маркетинг", "Маркетинг и реклама"},
		{"реклама", "Маркетинг и реклама"},
		{"Маркетинг и реклама", "Маркетинг и реклама"},
		{"smm", "SMM и блогинг"},
		{"блогинг", "SMM и блогинг"},
		{"Криптовалюта", "Криптовалюта"},
		{"крипта", "Криптовалюта"},
		{"Совершенно новая пользовательская тема", "Совершенно новая пользовательская тема"},
	} {
		if got := autopilotCanonicalTopic(tc.input); got != tc.expected {
			t.Errorf("%q canonicalized to %q, want %q", tc.input, got, tc.expected)
		}
		if autopilotTopicKey(tc.input) != autopilotTopicKey(tc.expected) {
			t.Errorf("different autopilot identity for %q and %q", tc.input, tc.expected)
		}
	}
	if autopilotTopicKey("SMM и блогинг") == autopilotTopicKey("Маркетинг и реклама") {
		t.Fatal("distinct tree categories were merged")
	}
}

func TestOKDiscoveryRequiresProfessionalIdentity(t *testing.T) {
	for _, tc := range []struct {
		title string
		want  bool
	}{
		{"Клуб вязания и рукоделия", false},
		{"Блог врача и здоровье", false},
		{"Новости знаменитостей и блогеров", false},
		{"SMM и продвижение в соцсетях", true},
		{"Таргетолог и реклама", true},
		{"Интернет-маркетинг для бизнеса", true},
	} {
		if got := okDiscoveryGroupTitleMatches(tc.title, "smm", []string{"блогинг", "бизнес"}); got != tc.want {
			t.Errorf("OK group title %q: got %t, want %t", tc.title, got, tc.want)
		}
	}
	for _, tc := range []struct {
		text string
		want bool
	}{
		{"Вязание, посты и видео из жизни блогера", false},
		{"Врач ведёт блог о пациентах", false},
		{"Скидка на рекламу в группе: обычный рекламный пост", false},
		{"Новый контент-план: анализ роста подписчиков в SMM", true},
		{"Кейс по таргетингу и продвижению соцсетей", true},
	} {
		// The matcher must never accept unrelated posts just because the
		// generated query contains generic words such as blog or business.
		if got := okDiscoveryTopicTextMatches(tc.text, "smm", []string{"блогинг", "бизнес"}); got != tc.want {
			t.Errorf("OK feed text %q: got %t, want %t", tc.text, got, tc.want)
		}
	}
}

func TestAutopilotOKSMMTopicReasonRequiresRealNegativeEvidence(t *testing.T) {
	const id = "12345678901234"
	post := func(text string) jx.M { return jx.M{"text": text} }
	unrelated := []jx.M{
		post("Собрали новые образцы вязания и схемы для рукоделия."),
		post("Новые советы по вязанию и вышивке крестиком."),
		post("Сегодня выбираем цвет пряжи для зимнего свитера."),
	}
	for _, tc := range []struct {
		name string
		title string
		posts []jx.M
		want string
	}{
		{"missing mobile title", id, unrelated, "source_metadata_unavailable"},
		{"off-topic title but no readable posts", "Клуб вязания", nil, "source_metadata_unavailable"},
		{"off-topic title and confirmed off-topic posts", "Клуб вязания", unrelated, "off_topic_group"},
		{"relevant group title", "SMM и продвижение в соцсетях", unrelated, ""},
		{"two independently relevant captions", "Новости сообщества", []jx.M{
			post("Новый контент-план: анализ роста подписчиков в SMM."),
			post("Разбор таргетинга и продвижения социальных сетей."),
			unrelated[0],
		}, ""},
		{"one relevant post is uncertain, not rejected", "Новости сообщества", []jx.M{
			post("Новый контент-план: анализ роста подписчиков в SMM."),
			unrelated[0], unrelated[1],
		}, "source_metadata_unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := autopilotOKSMMTopicReason(tc.title, id, tc.posts); got != tc.want {
				t.Fatalf("reason=%q, want=%q", got, tc.want)
			}
		})
	}
}
