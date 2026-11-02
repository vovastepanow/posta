package posta

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"posta/internal/config"
	"posta/internal/jx"
)

// Параметры поведения приложения для всех пользователей: правятся в админке («Управление → Параметры»), хранятся в kv
// admin:tunables и действуют без перезапуска (перечитываются раз в 15 с). Значение по умолчанию — из config.

type Tunable struct {
	Key     string   `json:"key"`
	Group   string   `json:"group"`
	Title   string   `json:"title"`
	Help    string   `json:"help"`
	Kind    string   `json:"kind"` // int | float | bool | choice | text
	Def     any      `json:"default"`
	Min     float64  `json:"min,omitempty"`
	Max     float64  `json:"max,omitempty"`
	Unit    string   `json:"unit,omitempty"`
	Choices []string `json:"choices,omitempty"`
}

var Tunables = []Tunable{
	// Сборщик
	{"pool_target", "Сборщик", "Записей в ленте на тему", "Сколько свежих записей держать в пуле каждой темы.", "int", config.ContentPoolTarget, 5, 500, "", nil},
	{"added_per_run", "Сборщик", "Новых записей за проход", "После стольких добавленных записей проход заканчивается.", "int", config.ContentAddedPerRun, 1, 300, "", nil},
	{"query_search_seconds", "Сборщик", "Поиск по запросу, сек", "Бюджет на все площадки за проход, когда задан запрос. Каталог темы тоже входит.", "int", 120, 30, 600, "сек", nil},
	{"tg_post_search_per_hour", "Сборщик", "Поисков постов Telegram в час", "Лимит поисков по словам через аккаунт владельца. Больше 20 — риск FloodWait.", "int", 5, 1, 20, "", nil},
	{"suggestion_sources_per_run", "Сборщик", "Находок автоподбора за проход", "С каждой находки берётся один лучший пост; предел на проход.", "int", 100, 0, 200, "", nil},
	{"post_search_queries", "Сборщик", "Запросов встроенного поиска за проход", "Сколько запросов по теме (слова профиля, хэштеги) уходит в площадки за проход.", "int", 6, 1, 20, "", nil},
	{"per_source_pool", "Сборщик", "Записей одного источника в ленте темы", "Обычный предел для одного источника. Пока лента заполнена меньше чем наполовину, сборщик может взять ещё одну запись, чтобы не оставить её пустой.", "int", 1, 1, 10, "", nil},
	{"source_cooldown_hours", "Сборщик", "Отдых источника после оценки, ч", "Пока идёт, новые посты оценённого источника в тему не берутся. 0 — без отдыха.", "int", 24, 0, 168, "ч", nil},
	{"analyze_per_run", "Сборщик", "Полных анализов за проход", "Лимит вызовов модели полного анализа за один проход.", "int", config.ContentAnalyzePerRun, 1, 1000, "", nil},
	{"parser_minutes", "Сборщик", "Длительность прохода", "Проход сборщика не дольше стольких минут.", "int", config.ParserMaxSeconds / 60, 2, 240, "мин", nil},
	{"parser_cooldown", "Сборщик", "Пауза между проходами", "Пул полон — следующий проход не раньше чем через столько минут.", "int", config.ParserCooldownSeconds / 60, 0, 240, "мин", nil},
	{"max_parsers", "Сборщик", "Сборщиков одновременно", "Сколько пользователей собирают контент параллельно; остальные ждут очереди.", "int", config.MaxParallelParsers, 1, 32, "", nil},
	{"pool_max_age", "Сборщик", "Срок жизни записи в ленте", "Запись старше этого просрочена и уступает место новой.", "int", config.PoolMaxAgeSeconds / 60, 10, 10080, "мин", nil},
	{"fetches_per_pass", "Сборщик", "Источников в сеть за проход", "Остальные берутся из кэша: бережём площадки от частых запросов.", "int", config.SourceFetchesPerPass, 1, 200, "", nil},
	{"poll_min", "Сборщик", "Опрос источника не чаще", "Минимальный интервал повторного опроса одного источника.", "int", config.SourcePollMin / 60, 1, 1440, "мин", nil},
	{"hot_shortlist", "Сборщик", "Горячих кандидатов в шортлисте", "Сколько самых обсуждаемых постов идёт на оценку первыми.", "int", config.HotShortlist, 5, 500, "", nil},
	{"prescreen_junk", "Сборщик", "Уверенность отсева мусора", "Быстрая оценка отбрасывает запись как мусор, только если уверена не меньше чем на столько.", "int", int(config.PrescreenJunkP * 100), 50, 100, "%", nil},
	{"text_posts", "Сборщик", "Текстовые посты", "Собирать посты без картинки для тем, где медиа необязательно (новости, бизнес).", "bool", config.TextPostsEnabled, 0, 0, "", nil},
	{"text_per_pass", "Сборщик", "Текстовых анализов за проход", "", "int", config.TextAnalysesPerPass, 0, 200, "", nil},
	{"post_search", "Сборщик", "Поиск постов площадок", "Искать посты по теме встроенными поисками YouTube, Telegram и VK рядом с источниками.", "bool", true, 0, 0, "", nil},
	{"search_telegram", "Сборщик", "Telegram по запросу", "Искать посты Telegram по запросу.", "bool", true, 0, 0, "", nil},
	{"search_vk", "Сборщик", "VK по запросу", "Искать посты VK по запросу (нужен ключ VK).", "bool", true, 0, 0, "", nil},
	{"search_youtube", "Сборщик", "YouTube по запросу", "Искать ролики YouTube по запросу.", "bool", true, 0, 0, "", nil},
	{"search_news", "Сборщик", "Новости по запросу", "Google News и Bing News RSS по запросу.", "bool", true, 0, 0, "", nil},
	{"search_vc", "Сборщик", "vc.ru и dtf.ru по запросу", "Поиск статей vc.ru и dtf.ru.", "bool", true, 0, 0, "", nil},
	{"search_tiktok", "Сборщик", "TikTok по хэштегу", "Поиск TikTok по хэштегу (выключено: медленно и нестабильно).", "bool", false, 0, 0, "", nil},
	{"search_rutube", "Сборщик", "Rutube по запросу", "Поиск видео Rutube (без ключа).", "bool", true, 0, 0, "", nil},
	{"search_9gag", "Сборщик", "9GAG по запросу", "Поиск постов 9GAG (без ключа, в основном по-английски).", "bool", true, 0, 0, "", nil},
	{"search_lemmy", "Сборщик", "Lemmy по запросу", "Поиск постов Lemmy (lemmy.world, без ключа).", "bool", true, 0, 0, "", nil},
	{"search_mastodon", "Сборщик", "Mastodon по хэштегу", "Статусы Mastodon по хэштегу запроса (без ключа).", "bool", true, 0, 0, "", nil},
	{"search_joyreactor", "Сборщик", "Joyreactor по тегу", "Страница тега Joyreactor по запросу (без ключа).", "bool", true, 0, 0, "", nil},
	{"content_max_age", "Сборщик", "Возраст постов", "Посты старше не собираются.", "int", config.ContentMaxAgeDays, 1, 365, "дн", nil},
	// Отбор
	{"relevance_threshold", "Отбор", "Порог «по теме» (общий)", "Для моделей без своего порога. У бесплатной модели анализа порог задан в «Моделях».", "int", config.RelevanceThreshold, 0, 100, "", nil},
	{"no_evidence_cap", "Отбор", "Без цитаты — не выше", "Модель не нашла в записи примету темы — оценка не выше этого.", "int", config.QualityNoEvidenceCap, 0, 100, "", nil},
	{"joke_check", "Отбор", "Проверка шутки в юморе", "Отдельный вопрос модели «есть ли шутка» для тем «Мемы» и «Юмор».", "bool", true, 0, 0, "", nil},
	{"subtopic_check", "Отбор", "Выбор среди соседних подтем", "Для частных тем: модель выбирает, к какой соседней подтеме относится запись.", "bool", true, 0, 0, "", nil},
	{"complaints_learn", "Отбор", "Учиться на жалобах", "Жалобы «не по теме» уточняют профиль темы и попадают в промпт анализа.", "bool", true, 0, 0, "", nil},
	// Подбор источников
	{"discovery", "Подбор источников", "Автоподбор источников", "Искать новые каналы и паблики по теме.", "bool", config.SourceDiscoveryEnabled, 0, 0, "", nil},
	{"discovery_interval", "Подбор источников", "Подбор не чаще", "Повторный автоподбор по теме, когда источников уже достаточно.", "int", config.SourceDiscoveryInterval / 3600, 1, 168, "ч", nil},
	{"discovery_checks", "Подбор источников", "Проверок за прогон", "Сколько кандидатов проверяется за один прогон подбора.", "int", config.SourceDiscoveryMaxChecks, 1, 300, "", nil},
	{"discovery_minutes", "Подбор источников", "Длительность прогона", "", "int", config.SourceDiscoveryMaxSeconds / 60, 1, 120, "мин", nil},
	{"suggest_score", "Подбор источников", "Порог предложения", "Оценка, с которой найденный источник предлагается пользователю.", "int", config.SourceSuggestScore, 0, 100, "", nil},
	{"auto_add_score", "Подбор источников", "Порог автодобавления", "С этой оценки источник добавляется без вопроса. Больше 100 — выключено.", "int", config.SourceAutoAddScore, 0, 101, "", nil},
	{"min_subscribers", "Подбор источников", "Минимум подписчиков", "Каналы меньше не рассматриваются.", "int", config.DiscoveryMinSubscribers, 0, 100000, "", nil},
	{"model_handles", "Подбор источников", "Каналы, которые знает модель", "Спрашивать у умной модели адреса каналов по теме (каждый проверяется через аккаунт Telegram).", "bool", true, 0, 0, "", nil},
	{"min_sources", "Подбор источников", "Источников темы для сбора", "Меньше — сначала автоподбор, потом сбор.", "int", 40, 0, 100, "", nil},
	{"registry_recheck_days", "Подбор источников", "Перепроверка отказов через", "Отказ по теме можно проверить заново через столько дней. 0 — никогда.", "int", 0, 0, 3650, "дн", nil},
	{"registry_sample_days", "Подбор источников", "Срок сохранённых постов", "Источник, отклонённый по другой теме, оценивается по сохранённым постам, пока они не старше этого срока.", "int", 60, 1, 365, "дн", nil},
	{"goodness_min", "Подбор источников", "Порог годноты", "Предлагаются только источники с годнотой не ниже этого: живой, с откликом, оригинальный, без рекламы и во вкусе владельца.", "int", 55, 0, 100, "", nil},
	{"suggestions_pending_max", "Подбор источников", "Очередь предложений на тему", "Сколько предложений ждут решения по теме. Сверх — остаются лучшие по баллу, остальные — в «Проверенных».", "int", 60, 1, 500, "", nil},
	// Видео
	{"video_max_mb", "Видео", "Размер видео для анализа", "Ролики тяжелее не скачиваются для раскадровки.", "int", config.VideoMaxBytes >> 20, 1, 500, "МБ", nil},
	{"video_frames", "Видео", "Кадров раскадровки (макс.)", "", "int", config.VideoFramesMax, 2, 16, "", nil},
	// Пользователи
	{"default_topic", "Пользователи", "Тема новых пользователей", "", "text", config.DefaultTopic, 0, 0, "", nil},
	{"default_fresh_only", "Пользователи", "«Только новый контент» по умолчанию", "", "bool", false, 0, 0, "", nil},
	{"default_auto_translate", "Пользователи", "Автоперевод по умолчанию", "", "bool", true, 0, 0, "", nil},
	{"adult_available", "Пользователи", "Разрешить включать 18+", "Выключено — галочка 18+ скрыта у всех, темы 18+ недоступны.", "bool", true, 0, 0, "", nil},
	{"registration", "Пользователи", "Новые пользователи", "Выключено — сайт работает только для уже вошедших (логин, Telegram).", "bool", false, 0, 0, "", nil},
	// Публикация и тарифы
	{"billing_enforce", "Публикация", "Лимиты тарифов", "Включено — лимиты постов и каналов проверяются при публикации.", "bool", config.BillingEnforce, 0, 0, "", nil},
	{"min_delay", "Публикация", "Отложенный пост не раньше", "", "int", config.TelegramMinDelaySeconds, 0, 3600, "с", nil},
	{"captions_per_hour", "Публикация", "Подписей от ИИ в час", "На пользователя.", "int", 30, 0, 1000, "", nil},
}

func tunableByKey(key string) *Tunable {
	for i := range Tunables {
		if Tunables[i].Key == key {
			return &Tunables[i]
		}
	}
	return nil
}

type tunableCache struct {
	sync.Mutex
	vals jx.M
	read time.Time
}

var tunables tunableCache

func (a *App) tunableValues() jx.M {
	tunables.Lock()
	defer tunables.Unlock()
	if tunables.vals != nil && time.Since(tunables.read) < 15*time.Second {
		return tunables.vals
	}
	v := jx.M{}
	a.KVGet(a.ctxOrBg(), "admin:tunables", &v)
	tunables.vals, tunables.read = v, time.Now()
	return v
}

func (a *App) tunable(key string) any {
	t := tunableByKey(key)
	if t == nil {
		panic("неизвестный параметр " + key)
	}
	if v, ok := a.tunableValues()[key]; ok && v != nil {
		return v
	}
	return t.Def
}

// TInt — целый параметр (админка или умолчание).
func (a *App) TInt(key string) int { return int(jx.ToInt(a.tunable(key))) }

// TFloat — дробный параметр.
func (a *App) TFloat(key string) float64 { return jx.ToFloat(a.tunable(key)) }

// TBool — переключатель.
func (a *App) TBool(key string) bool { return jx.Truthy(a.tunable(key)) }

// TStr — строковый параметр.
func (a *App) TStr(key string) string { return jx.ToStr(a.tunable(key)) }

// TunablesPublic — параметры для админки: описание и текущее значение.
func (a *App) TunablesPublic() []jx.M {
	vals := a.tunableValues()
	var out []jx.M
	for _, t := range Tunables {
		v, set := vals[t.Key]
		if !set || v == nil {
			v = t.Def
		}
		m := jx.M{"key": t.Key, "group": t.Group, "title": t.Title, "help": t.Help, "kind": t.Kind, "default": t.Def, "value": v, "changed": set && !jx.Equal(v, t.Def)}
		if t.Kind == "int" || t.Kind == "float" {
			m["min"], m["max"], m["unit"] = t.Min, t.Max, t.Unit
		}
		out = append(out, m)
	}
	return out
}

// TunablesSave — проверить и сохранить изменения; значение null — вернуть умолчание.
func (a *App) TunablesSave(ctx context.Context, changes jx.M) error {
	clean := jx.M{}
	for k, v := range changes {
		t := tunableByKey(k)
		if t == nil {
			return Bad("Неизвестный параметр: %s", k)
		}
		if v == nil {
			clean[k] = nil
			continue
		}
		switch t.Kind {
		case "int", "float":
			f := jx.ToFloat(v)
			if math.IsNaN(f) || f < t.Min || f > t.Max {
				return Bad("«%s»: допустимо от %v до %v.", t.Title, t.Min, t.Max)
			}
			if t.Kind == "int" {
				clean[k] = int64(math.Round(f))
			} else {
				clean[k] = f
			}
		case "bool":
			clean[k] = jx.Truthy(v)
		case "text":
			s := trimRunes(collapseSpaces(jx.ToStr(v)), 200)
			if s == "" {
				return Bad("«%s»: пустое значение.", t.Title)
			}
			clean[k] = s
		default:
			return Bad("«%s»: неизвестный вид параметра.", t.Title)
		}
	}
	_, err := KVUpdate(a, ctx, "admin:tunables", 0, func(cur jx.M, _ bool) *jx.M {
		if cur == nil {
			cur = jx.M{}
		}
		for k, v := range clean {
			if v == nil {
				delete(cur, k)
			} else {
				cur[k] = v
			}
		}
		return &cur
	})
	tunables.Lock()
	tunables.vals = nil
	tunables.Unlock()
	return err
}

var _ = fmt.Sprint
