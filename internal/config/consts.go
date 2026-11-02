// Package config — пороги и настройки Posta (бывший config.php) и параметры сервера из окружения.
// Цифры и их обоснования перенесены из PHP-версии без изменений: их подбирали по истории оценок.
package config

import "regexp"

const (
	AppName = "Posta"

	// ---- Пользователи и сессии ----
	UserCookie                = "ct_uid"
	SessionCookie             = "pm_sid"
	UserCookieDays            = 365
	LoginRequestTTL           = 600 // 10 минут на подтверждение входа
	SessionTouchSeconds       = 600
	UserRetentionDays         = 60 // профили без заходов дольше удаляются
	EmptyUserRetentionHours   = 24 // пустые профили удаляются быстрее
	MaxParallelParsers        = 4
	SecretKeyVersion          = 1
	DefaultTopic              = "мемы"
	TopicMaxLength            = 200
	DaySeconds                = 86400
	AnalysisCacheLimit        = 2000
	AnalysisCacheDays         = 14
	ScreenedCacheLimit        = 5000
	ScreenedCacheDays         = 14
	MediaCacheGraceSeconds    = 600 // свежие файлы кэша не трогаем: сборщик мог ещё не записать запись
	MediaCacheCleanupMaxShare = 0.3 // если удалится больше доли файлов от всех, кэш признаётся повреждённым

	// ---- Модели (локально) ----
	OllamaNumCtx            = 4096 // один контекст для всех запросов: смена num_ctx перезагружает модель
	OllamaKeepAlive         = "30m"
	OllamaThinkingMaxTokens = 1500
	OllamaTimeout           = 180
	OllamaThinkingTimeout   = 420
	ParserThinking          = false // размышления в сборщике — в разы дольше; во «Тесте ИИ» включены всегда
	MainModelSlowTimeouts   = 3     // столько таймаутов основной подряд — анализ на быструю модель
	MainModelSlowHours      = 6
	AnalysisMaxTokens       = 450
	AnalysisDescriptionMax  = 120 // описание — одна фраза до стольких символов
	AnalysisEvidenceMax     = 80  // цитата или примета — она же объяснение «почему подходит»
	ComplaintExampleChars   = 140
	AnalysisImageMaxSide    = 768
	FixedCostImageMaxSide   = 1024 // малые qwen3-vl берут ~1040 токенов за картинку любого размера
	PrescreenMaxSide        = 768

	// ---- Сборщик ----
	ContentPoolTarget          = 50    // записей в пуле на тему
	SourcePollMin              = 1200  // опрос источника не чаще раза в 20 минут
	SourcePollMax              = 21600 // и не реже раза в 6 часов
	SourceFetchesPerPass       = 10    // источников в сеть за проход; остальные — из кэша
	BoardPollMin               = 600   // каталог доски Двача/4chan — не чаще раза в 10 минут
	PoolDiversityWindow        = 6     // если подряд тот же источник, ищем другой в следующих стольких записях
	PoolMaxAgeSeconds          = 3600  // запись старше часа просрочена: не показывается и не считается
	HotTopicsLimit             = 7
	HotTopicsWindowDays        = 7
	HotTopicsMinScore          = 3
	HotTopicsCacheSeconds      = 600
	ContentAddedPerRun         = 30
	ContentAnalyzePerRun       = 80
	ParserMaxSeconds           = 2400
	ParserCooldownSeconds      = 600
	ParserSlowThresholdSeconds = 30 // через сколько секунд показываем архивные посты с 🔥, если сборщик ещё работает
	RelevanceThreshold         = 80 // порог «по теме»: при 80 жалоб 34% против 58–78% у оценок 60–75

	// ---- Качество подбора (цифры — история оценок мемов, 2026-10-23, 610 карточек) ----
	AnalysisPromptVersion         = 4  // 4: анализ отличает готовый мем от шаблона; входит в ключ кэша
	TopicTagMissPenalty           = 15 // частная тема подбора, а меток записи её нет — оценка −15
	TranslateEmail                = "" // почта для MyMemory (лимит ~50 000 символов в сутки)
	TextPostsEnabled              = true
	TextAnalysesPerPass           = 12
	JokeYesMin                    = 50 // вероятность «да» на вопрос «есть ли шутка» ниже — шутки нет
	JokeNoCap                     = 40
	MemeTemplateCap               = 30
	SubtopicLostCap               = 40 // частная тема проиграла соседней подтеме
	SubtopicOffCap                = 20 // выбор «не юмор / не по теме»
	SourceVerdictWindow           = 30
	SourceVerdictMin              = 8
	SourceVerdictLow              = 0.12
	AnalysisSeed                  = 7 // одна и та же запись — одна и та же оценка
	QualityNoEvidenceCap          = 40
	QualityViewsMinRatio          = 0.7
	QualityCardMinRatings         = 5
	QualitySourceNeutralShare     = 0.4
	QualitySourceWeight           = 30
	QualitySourceCap              = 15
	QualityTagMinRatings          = 8
	QualityTagWeight              = 0 // выключено: проверка не дала выигрыша
	QualityTagCap                 = 15
	QualityPlatformMinRatings     = 30
	QualityPlatformComplaintShare = 0.5
	QualityPlatformSample         = 10
	PrescreenJunkP                = 0.8 // быстрая оценка отбрасывает только «мусор» с такой уверенностью
	TopicQueryMaxLen              = 80
	TopicQueryMinSources          = 3
	TopicQueryDiscoveryPerDay     = 10
	TopicQueryChannelsPerRun      = 12
	ScreenedCacheVersion          = 2
	PrescreenBatch                = 10
	AnalyzeBlock                  = 8 // прошедшие быструю оценку уходят на полный анализ блоком: меньше смен модели
	PrescreenSourceMissLimit      = 6

	// ---- Горячие посты первыми ----
	ContentMinSources        = 8
	DiscoveryBlockingSeconds = 600
	QueuePerSource           = 2 // в очереди проверки — лучший кандидат источника и запасной
	HotShortlist             = 60
	HotPlatformShare         = 0.4
	HotKeywordBoost          = 1.3
	HotTrustBoost            = 1.5
	HotQueryBoost            = 1.5
	HotTextMinChars          = 40
	HotTextBatch             = 15
	HotTextRejectScore       = 20
	HotCollageCheck          = false

	PhashSameDistance = 20 // из 256 бит: ближе — та же картинка

	// ---- Автоподбор источников ----
	SourceDiscoveryEnabled      = true
	SourceDiscoveryInterval     = 12 * 3600
	SourceDiscoveryMaxSeconds   = 900
	SourceDiscoveryMaxChecks    = 40
	SourceAutoAddScore          = 101 // >100: автодобавление выключено, всё решает пользователь
	SourceSuggestScore          = 55
	SourceReviewCaptions        = 10
	DvachBoardsPerTopic         = 4
	DvachBoardsPerRun           = 20
	DiscoverySnowballSeeds      = 8
	DiscoverySimilarSeeds       = 6
	DiscoveryDeepExtraScore     = 10
	DiscoveryDeferMax           = 20
	DiscoveryDeferDays          = 3
	DiscoverySnowballMaxDepth   = 2
	DiscoveryKeywordRecent      = 8
	DiscoveryKeywordMinTexts    = 2
	DiscoveryTextRejectScore    = 20
	DiscoveryMinSubscribers     = 30
	DiscoveryMinOwnSources      = 3
	DiscoveryShortageInterval   = 1800
	DiscoveryNamesPerRun        = 0 // названия каналов от модели выключены: дают бренды и несуществующие адреса
	DiscoveryNamesIfSearchBelow = 15
	DiscoveryNamesRebuildStep   = 5
	DiscoveryNamesMaxDays       = 14
	DiscoveryRefBonus           = 5
	DiscoveryRefsMax            = 10
	DiscoveryOriginMinVotes     = 8

	// ---- Свежесть, активность и качество источников ----
	ContentMaxAgeDays           = 30
	ContentFreshMaxAgeDays      = 1
	DvachThreadAliveHours       = 24
	DvachMaxAgeDays             = 120
	SourceStaleDays             = 14
	SourceMinPostsPerWeek       = 2
	SourceFrozenRecheckDays     = 3
	SourceWeightContent         = 0.45
	SourceWeightAudience        = 0.25
	SourceWeightViewRate        = 0.30
	SourceMinContentScore       = 68
	SourceDeadViewRate          = 0.015
	SourceMinSubscribersForRate = 300
	SourceFeedbackMinVotes      = 3
	SourceFeedbackBanVotes      = 8
	SourceFeedbackBanScore      = 0.2
	SourceMuteMinRatings        = 10
	SourceMuteLikeShare         = 0.25
	SourceMuteComplaintWeight   = 2
	DvachThreadMinPosts         = 5
	PoolLivenessInterval        = 7200
	PoolLivenessMaxThreads      = 40
	DvachThreadsPerBoard        = 8
	DvachThreadPool             = 30 // треды каталога, из которых выбираем по теме (посты грузим только у выбранных)
	DvachThreadTitleMax         = 36
	ThreadTopicSkip             = 20 // тред имиджборды с оценкой по теме ниже — посты не берём
	ThreadTopicTrust            = 70 // выше — посты треда идут в анализ первыми
	ThreadTrustPrescreenBonus   = 10
	ThreadTopicCacheSeconds     = 86400
	ThreadTitleMax              = 120
	ThreadOpMax                 = 400
	DvachPostsPerThread         = 60
	TelegramPostsPerChannel     = 40
	VKPostsPerPublic            = 60
	RSSPostsPerFeed             = 15

	// ---- Видео ----
	VideoMaxBytes            = 30 << 20
	VideoFramesMin           = 4
	VideoFramesMax           = 8
	VideoSecondsPerFrame     = 3.0
	VideoFrameMaxSide        = 384
	StoryboardFramesPerSheet = 4 // кадры уходят в модель листами 2×2
	VideoLightBytes          = 6 << 20
	VideoLightHeight         = 720
	ContentTagsMax           = 3

	// ---- История и публикация ----
	ProposalsLimit            = 200
	MaxImageBytes             = 10 << 20
	TelegramMaxPhotoBytes     = 10 << 20
	TelegramMaxMedia          = 10
	TelegramCaptionLimit      = 1024
	TelegramTextLimit         = 4096
	TelegramMinDelaySeconds   = 60
	TelegramMaxDelayDays      = 365
	TelegramPublishedLogLimit = 30
	TelegramUploadMaxBytes    = 64 << 20
	TelegramUserTimeout       = 300
	TGUVideoMaxBytes          = 150 << 20 // большое видео поста Telegram, которое скачивает личный аккаунт
	TGUVideoTimeout           = 900
	TelegramUserPendingTTL    = 900

	// ---- Тарифы ----
	BillingEnforce    = false // лимиты тарифов: false — только показываются (приёма платежей ещё нет)
	BillingPeriodDays = 30
	BillingFreePlan   = "free"
	BillingHourShare  = 4  // за час — не больше четверти дневного лимита (минимум 1 пост)
	ReferralPercent   = 20 // приглашённый оплатил тариф — пригласившему 20% от суммы
	ReferralBindDays  = 7
)

// Оговорки в why_relevant: 56 карточек, 0% лайков, 85% жалоб.
var AnalysisHedgeRe = regexp.MustCompile(`(?i)может|возможн|если |вероятно|потенциальн|можно использовать|подойти для`)

// Темы, где кэш анализа сброшен после правок выбора подтемы.
var SubtopicCacheTopics = []string{"абсурдный юмор", "абсурд и шитпост", "чёрный юмор"}

// Подтемы, где проверка «есть ли шутка» остаётся (общие темы «Мемы» и «Юмор» — тоже).
var SubtopicJokeKeep = []string{"humor.jokes", "humor.other", "humor.fails", "humor.funny_videos", "memes.other"}

// Темы, где имиджборды — родная площадка: посты без признаков темы идут в шортлист.
var ImageboardNativeTopicKeys = []string{"memes", "internet"}

// PostSearchGenericWords — общие слова, по которым встроенный поиск по теме ничего про тему не находит.
var PostSearchGenericWords = []string{"image", "images", "video", "videos", "photo", "photos", "post", "posts", "content", "picture", "pictures"}

var DvachAlwaysBoards = []string{"b"}

// Форма поста — отдельная ось от темы (картинка с котом — форма content, тема «Кошки»). Порядок важен для промпта.
var ContentFormats = []struct{ Key, About string }{
	{"content", "картинка или видео с наложенным текстом-шуткой"},
	{"photo", "обычное фото без текста-шутки"},
	{"video", "обычное видео без текста-шутки"},
	{"text", "в основном текст: скриншот переписки, цитата, пост"},
	{"art", "рисунок, комикс, 3D или сгенерированная картинка"},
}

func IsContentFormat(f string) bool {
	for _, c := range ContentFormats {
		if c.Key == f {
			return true
		}
	}
	return false
}

var HistoryCollections = []string{"liked", "disliked", "undecided", "complaints"}
var ProposalCollections = []string{"liked", "undecided"}
var ContentVoteCollections = map[string]string{"like": "liked", "dislike": "disliked", "maybe": "undecided", "wrong": "complaints"}
var ComplaintReasons = []string{"match_error", "broken", "other"}

var TelegramLimitDefaults = map[string]int64{"bot": 50 << 20, "account": 2000 << 20}

// Тарифы: цены в рублях за период; совпадают с ценниками на лендинге.
type Plan struct {
	Key         string `json:"key"`
	Title       string `json:"title"`
	Tag         string `json:"tag"`
	Price       int    `json:"price"`
	Channels    int    `json:"channels"`
	PostsPerDay int    `json:"posts_per_day"`
	Schedule    bool   `json:"schedule"`
	Days        int    `json:"days,omitempty"`
	Once        bool   `json:"once,omitempty"`
}

var BillingPlans = []Plan{
	{Key: "free", Title: "Смотреть", Tag: "TITTA", Price: 0, Channels: 1, PostsPerDay: 3},
	// «Хот-дог»: неделя «Автопилота» за 49 ₽ — один раз и только до первой покупки любого тарифа
	{Key: "trial", Title: "Хот-дог", Tag: "KORV", Price: 49, Channels: 1, PostsPerDay: 10, Schedule: true, Days: 7, Once: true},
	{Key: "autopilot", Title: "Автопилот", Tag: "AUTÖPILÖT", Price: 390, Channels: 1, PostsPerDay: 10, Schedule: true},
	{Key: "network", Title: "Сетка", Tag: "SJÄTKÅ", Price: 990, Channels: 5, PostsPerDay: 50, Schedule: true},
}

func PlanByKey(key string) (Plan, bool) {
	for _, p := range BillingPlans {
		if p.Key == key {
			return p, true
		}
	}
	return Plan{}, false
}

// «Свой тариф»: цена «Автопилота» + за каждый канал сверх первого + за каждые 10 постов в день сверх 10.
var BillingCustom = struct {
	Base, Channel, PostsStep, PostsStepPrice, MaxChannels, MaxPostsPerDay int
}{390, 150, 10, 60, 10, 100}

// Годнота источника: веса компонентов в итоговом балле (см. posta/source_goodness.go). Без данных компонент не считается.
var GoodnessWeights = map[string]float64{"отклик": 25, "жизнь": 20, "оригинальность": 20, "реклама": 15, "вкус": 20}
