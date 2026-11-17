package posta

import (
	"context"

	"os"
	"slices"
	"strings"

	"testing"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
	"posta/internal/llm"
)

func TestTagVerificationRejectsUngroundedAndUnrequestedLabels(t *testing.T) {
	u := taxonomyTestUser()
	text := "Разработка полуприцепа в Solidworks 2024, гидравлические системы и пневматическая подвеска."
	tags := []string{"science.engineering", "tech.code", "tech.hardware", "adult"}
	response := jx.M{"verified": []jx.M{
		{"tag": "science.engineering", "matches": true, "evidence": "гидравлические системы и пневматическая подвеска"},
		{"tag": "tech.code", "matches": true, "evidence": "Разработка программ на Python"},
		{"tag": "tech.hardware", "matches": true, "evidence": "2024"},
		{"tag": "tech.hardware", "matches": false, "evidence": "гидравлические системы и пневматическая подвеска"},
		{"tag": "business.owners", "matches": true, "evidence": "Разработка полуприцепа в Solidworks"},
	}}
	got := u.confirmedContentTags(tags, response, text)
	if !jx.Equal(got, []string{"science.engineering", "adult"}) {
		t.Fatalf("unverified tags survived: %v", got)
	}
	if got := u.confirmedContentTags(tags, nil, text); !jx.Equal(got, []string{"adult"}) {
		t.Fatalf("invalid response must preserve safety only: %v", got)
	}
	if got := u.contentAnalysisTags("Бизнес", nil, nil); len(got) != 0 {
		t.Fatalf("search topic injected as content label: %v", got)
	}
}

func TestEmptyVerifiedTagsDoNotProduceSearchTopicHashtag(t *testing.T) {
	u := taxonomyTestUser()
	if got := u.PostHashtags(jx.M{"topic": "Бизнес", "tags": []string{}}); len(got) != 0 {
		t.Fatalf("unconfirmed collection topic became a hashtag: %v", got)
	}
	if got := u.PostHashtags(jx.M{"topic": "Бизнес", "tags": []string{"science.engineering"}}); !jx.Equal(got, []string{"#инженерия"}) {
		t.Fatalf("content hashtag lost: %v", got)
	}
}

func TestContentClassificationRealPosts(t *testing.T) {
	if os.Getenv("POSTA_TEST_CLOUD") == "" {
		t.Skip("POSTA_TEST_CLOUD not set")
	}
	var router *llm.Router
	{
		cfg := config.LoadServer()
		router = llm.NewRouter(llm.NewOllama(cfg.OllamaURL), llm.NewOpenRouter(cfg.OpenRouterURL, cfg.OpenRouterKey, cfg.OpenRouterTitle, cfg.PublicURL))
		router.LoadSettings = func() llm.Settings {
			s := llm.Preset("free")
			s.DailyBudgetUSD = 0.05
			s.Roles[llm.RoleClassify] = llm.RoleConfig{Primary: llm.FreeVision}
			if model := os.Getenv("POSTA_CLASSIFIER_TEST_MODEL"); model != "" {
				s.Roles[llm.RoleClassify] = llm.RoleConfig{Primary: llm.Target{Provider: "openrouter", Model: model}}
			}
			return s
		}
	}
	spent := 0.0
	callErrors := 0
	router.Spent = func() float64 { return spent + 0.025 }
	router.OnCall = func(c llm.Call) {
		if c.Resp != nil {
			spent += c.Resp.CostUSD
		}
		if c.Err != nil {
			callErrors++
			t.Logf("error: %v", c.Err)
		} else if c.Resp != nil {
			t.Logf("response: %s", c.Resp.Text)
		}
		if delay := os.Getenv("POSTA_CLASSIFIER_TEST_DELAY"); delay != "" {
			if d, err := time.ParseDuration(delay); err == nil {
				time.Sleep(d)
			}
		}
	}
	u := taxonomyTestUser()
	u.Ctx = context.Background()
	u.LLM = router
	for _, tc := range []struct{ text, want string }{
		{"Главная ошибка при запуске сайта и рекламы", "business.marketing"},
		{"Реклама с Сидни Суини приносит миллионы просмотров и всплеск продаж. Но что остаётся у компании, когда хайп заканчивается? В карточках разбираемся, почему так происходит и что с этим делать.", "business.marketing"},
		{"Сингапур стал крупнейшей налоговой гаванью для Apple: компания провела через страну 50 млрд прибыли и сэкономила 2,7 млрд на налогах.", "finance.taxes"},
		{"Разработка полуприцепа в Solidworks 2024. Гидравлические системы, пневматическая подвеска и опоры. Грузоподъемность 55 тонн.", "science.engineering"},
		{"Написал программу на Python для обработки файлов: исходный код и алгоритм сортировки.", "tech.code"},
		{"Пластиковая игрушка, диван-кровать и кеды: где импортёры ошибаются с кодом ТН ВЭД при таможенном оформлении товаров.", "business.logistics"},
		{"Скриншот мобильного приложения с заголовком «На инвестиции» и суммой 35 000 ₽. В списке две операции пополнения брокерского счёта.", "finance.invest"},
		{"Текстовый пост о судебных исках к медиахолдингу и его структурам с перечислением судебных разбирательств.", "business.legal"},
		{"Статья о влиянии ИИ-агентов на разработку IT-продуктов и о том, что создание кода — лишь часть процесса разработки.", "tech.ai"},
		{"На слайде презентации заголовок «Инвестировать в привлечение новых клиентов» и схема: массовый охват плюс непрерывное продвижение.", "business.marketing"},
		{"Расследование Forbes о росте состояния Дональда Трампа после возвращения к власти: сделки с криптовалютами и лицензионные соглашения.", ""},
		{"За последние два месяца операторы наружной рекламы завалили исками медиабаинговую структуру группы Smart View. Другие юрлица группы столкнулись с исками в связи с неоплатой задолженности.", "business.legal"},
		{"Привезли инсайты из Минска: что остается важным в эпоху ИИ? На форуме маркетинга большинство кейсов рассказывали о внедрении ИИ-агентов и создании креативов с помощью нейросетей; говорили также о стратегии бренда.", "tech.ai"},
	} {
		t.Run(tc.want+tc.text[:6], func(t *testing.T) {
			beforeErrors := callErrors
			got := u.classifyContent("", tc.text, false)
			if callErrors != beforeErrors || got == nil {
				t.Fatalf("classification unavailable; result cannot be evaluated")
			}
			t.Logf("%s => %v", tc.text, got)
			if tc.want != "" && !slices.Contains(got, tc.want) {
				t.Fatalf("want %s, got %v", tc.want, got)
			}
			if strings.HasPrefix(tc.text, "За последние два месяца") && slices.Contains(got, "business.marketing") {
				t.Errorf("agency lawsuit misclassified as marketing: %v", got)
			}
			for _, bad := range []string{"humor", "memes", "tech.hardware", "style.tattoo"} {
				if slices.Contains(got, bad) {
					t.Errorf("unrelated tag %s", bad)
				}
			}
		})
	}
	for _, tc := range []struct {
		name, text string
		label      contentLabel
	}{
		{"customs_not_tattoo", "Пластиковая игрушка, диван-кровать и кеды: где импортёры ошибаются с кодом ТН ВЭД при таможенном оформлении товаров.", contentLabel{"style.tattoo", "Тату и пирсинг", "Татуировки и пирсинг."}},
		{"advertising_not_office", "Реклама с Сидни Суини приносит миллионы просмотров и всплеск продаж. В карточках разбираемся, почему так происходит.", contentLabel{"people.work", "Работа и офис", "Рабочие ситуации и офисная жизнь."}},
		{"engineering_not_programming", "Разработка полуприцепа в Solidworks. Гидравлические системы и пневматическая подвеска.", contentLabel{"tech.code", "Программирование", "Написание программного кода и разработка программ."}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			beforeErrors := callErrors
			got := u.verifySelectedLabels(tc.text, []contentLabel{tc.label}, []string{tc.label.key})
			if callErrors != beforeErrors || got == nil {
				t.Fatal("verification unavailable")
			}
			if len(got) != 0 {
				t.Fatalf("incorrect label accepted: %v", got)
			}
		})
	}
}
