<?php
// Настройки. Секреты сервера — в config.local.php (не в git), шаблон: config.local.example.php.
$local = is_file(__DIR__ . '/config.local.php') ? (require __DIR__ . '/config.local.php') : [];
foreach (['VK_CLIENT_ID', 'VK_REDIRECT_URI', 'VK_SERVICE_TOKEN', 'VK_OAUTH_SCOPE'] as $name) {
    if ((string)getenv($name) === '') putenv($name . '=' . (is_array($local) ? ($local[$name] ?? '') : ''));
}

const APP_NAME = 'Posta';
const ROOT = __DIR__;

// ---- Данные ----
const USERS_DIR = ROOT . '/users';                        // users/<id>/: только журнал и ход работы парсера (данные — в базе)
const SECRET_KEY_VERSION = 1;                            // версия ключа в формате секретов (lib/core.php, secret_encrypt)
const USER_COOKIE = 'ct_uid';
const SESSION_COOKIE = 'pm_sid';
const USER_COOKIE_DAYS = 365;
const LOGIN_REQUEST_TTL = 600;                            // 10 минут на подтверждение входа
const SESSION_TOUCH_SECONDS = 600;
const USER_RETENTION_DAYS = 60;                           // профили без заходов дольше удаляются
const EMPTY_USER_RETENTION_HOURS = 24;                   // пустые профили (зашёл без куки и ничего не сделал) удаляются быстрее
const MAX_PARALLEL_PARSERS = 4;  // Увеличено для лучшей GPU загрузки (от 6% до 70%+)
define('DB_FILE', getenv('POSTA_DB_FILE') ?: ROOT . '/data/posta.sqlite');   // POSTA_DB_FILE — копия базы для тестов             // вся база: пользователи, пулы, история, каталог, кэши
const DB_BUSY_TIMEOUT_MS = 15000;
const ANALYSIS_CACHE_LIMIT = 2000;
const ANALYSIS_CACHE_DAYS = 14;
const SCREENED_CACHE_LIMIT = 5000;
const SCREENED_CACHE_DAYS = 14;
const PARSER_SCRIPT = ROOT . '/cron_parser.php';
const PARSER_RUNTIME_VERSION = 'v3';                      // входит в имена журнала и состояния парсера
// Медиа: по умолчанию в папке проекта; на сервере — вне веб-корня, путь задаётся в config.local.php (MEDIA_CACHE_DIR, MEDIA_CACHE_URL)
define('MEDIA_CACHE_DIR', getenv('POSTA_MEDIA_CACHE_DIR') ?: ($local['MEDIA_CACHE_DIR'] ?? ROOT . '/media_cache'));
define('MEDIA_CACHE_URL', $local['MEDIA_CACHE_URL'] ?? 'media_cache');
const MEDIA_CACHE_GRACE_SECONDS = 600;                    // свежие файлы кэша не трогаем: парсер мог ещё не записать запись
const MEDIA_CACHE_CLEANUP_MAX_SHARE = 0.3;               // если удалится больше доли файлов от всех, кэш признаётся повреждённым
const DEFAULT_TOPIC = 'мемы';
const TOPIC_MAX_LENGTH = 200;
const DAY_SECONDS = 86400;

// ---- Модели (Ollama) ----
const OLLAMA_URL = 'http://localhost:11434';
const OLLAMA_GENERATE_URL = OLLAMA_URL . '/api/generate';
const VISION_MODEL = 'qwen3.5-vision:latest';
const ANALYSIS_MODEL = VISION_MODEL;                      // полный анализ отобранных
const SMART_MODEL = VISION_MODEL;                         // задачи «раз на тему»: профиль, классификация
const PRESCREEN_MODELS = ['qwen3-vl:2b-instruct', 'qwen3-vl:2b', 'qwen2.5vl:3b']; // быстрая оценка: первая установленная
const MAIN_MODEL_SLOW_TIMEOUTS = 3;                       // столько таймаутов основной подряд — анализ на быструю модель
const MAIN_MODEL_SLOW_HOURS = 6;
const MAIN_MODEL_SLOW_FLAG = USERS_DIR . '/.main-model-slow';
const OLLAMA_NUM_CTX = 4096;                              // один контекст для всех запросов: смена num_ctx перезагружает модель
const OLLAMA_KEEP_ALIVE = '30m';
const OLLAMA_THINKING_MODELS = [VISION_MODEL];
const OLLAMA_THINKING_MAX_TOKENS = 1500;
const OLLAMA_TIMEOUT = 180;
const OLLAMA_THINKING_TIMEOUT = 420;
const PARSER_THINKING = false;                            // размышления в парсере — в разы дольше; во «Тесте ИИ» включены всегда
const ANALYSIS_MAX_TOKENS = 450;
const ANALYSIS_DESCRIPTION_MAX = 120;                     // описание — одна фраза до стольких символов (обрезается в PHP)
const ANALYSIS_EVIDENCE_MAX = 80;                         // цитата или примета — до стольких символов; она же — объяснение «почему подходит»
const COMPLAINT_EXAMPLE_CHARS = 140;                      // жалобы в промпте обрезаются до стольких символов
const ANALYSIS_IMAGE_MAX_SIDE = 768;
const FIXED_COST_IMAGE_MAX_SIDE = 1024;                   // малые qwen3-vl берут ~1040 токенов за картинку любого размера

// ---- Парсер ----
const CONTENT_POOL_TARGET = 50;                        // записей в пуле на тему
const SOURCE_POLL_MIN = 1200;                          // опрос источника реже, чем раз в 20 минут, не бывает (сек., FRESH_ORDER_POLITE_FETCH_PROMPT.md, часть B)
const SOURCE_POLL_MAX = 21600;                         // и не реже, чем раз в 6 часов (сек.)
const SOURCE_FETCHES_PER_PASS = 10;                    // источников в сеть за проход; остальные — из кэша
const BOARD_POLL_MIN = 600;                            // каталог доски Двача/4chan — не чаще раза в 10 минут (сек.)
const POOL_DIVERSITY_WINDOW = 6;                       // порядок показа: если подряд тот же источник, ищем другой в следующих стольких записях (FRESH_ORDER_POLITE_FETCH_PROMPT.md, часть A)
const POOL_MAX_AGE_SECONDS = 3600;                     // запись старше часа просрочена: не показывается и не считается; удаляется только когда пул полон и нужна новая
const HOT_TOPICS_LIMIT = 7;                            // сколько горячих тем сервер подбирает всего (меньше, если меньше горячих)
const HOT_TOPICS_WINDOW_DAYS = 7;                      // окно активности: голоса и заходы за неделю
const HOT_TOPICS_MIN_SCORE = 3;                        // ниже этого балла тема не считается горячей
const HOT_TOPICS_CACHE_SECONDS = 600;                  // результат кэшируется, чтобы heartbeat не читал всю историю
const CONTENT_ADDED_PER_RUN = 30;
const CONTENT_ANALYZE_PER_RUN = 80;
const PARSER_MAX_SECONDS = 2400;
const PARSER_COOLDOWN_SECONDS = 600;
const PARSER_SLOW_THRESHOLD_SECONDS = 30;                // через сколько сек показываем архивные посты с 🔥 если парсер ещё работает
const RELEVANCE_THRESHOLD = 80;                           // порог «по теме» для попадания в пул: при 80 жалоб 34% против 58–78% у оценок 60–75 (QUALITY_PROMPT.md, часть A)
const PRESCREEN_MAX_SIDE = 768;
// ---- Качество подбора (QUALITY_PROMPT.md; цифры — история оценок мемов, 2026-10-23, 610 карточек) ----
const ANALYSIS_HEDGE_RE = '/может|возможн|если |вероятно|потенциальн|можно использовать|подойти для/iu';   // оговорки в why_relevant: 56 карточек, 0% лайков, 85% жалоб
const ANALYSIS_PROMPT_VERSION = 3;               // версия промпта оценки: входит в ключ кэша анализа, старые оценки не подмешиваются (QUALITY_PROMPT.md, часть C)
const TOPIC_TAG_MISS_PENALTY = 15;        // частная тема подбора, а меток записи её нет — оценка −15 (TOPICS_SEARCH_PROMPT.md, часть D)
const TRANSLATE_EMAIL = '';                // почта для MyMemory (лимит ~50 000 символов в сутки вместо ~5 000); пусто — без почты. Вписывает владелец
const TEXT_POSTS_ENABLED = true;          // текстовые посты (без картинки) для тем, где медиа необязательно (TOPICS_SEARCH_PROMPT.md, часть E); false — как раньше
const TEXT_ANALYSES_PER_PASS = 12;        // полных анализов текстовых записей (без картинки) за проход парсера; остальные — в следующий
const JOKE_YES_MIN = 50;                    // вопрос «есть ли шутка»: вероятность «да» (0–100) ниже этого — шутки нет (часть B)
const JOKE_NO_CAP = 40;                     // оценка записи юмора без шутки не выше этого
const SUBTOPIC_LOST_CAP = 40;               // частная тема проиграла соседней подтеме (выбор среди соседей, SUBTOPIC_PROMPT.md, часть A): оценка не выше
const SUBTOPIC_OFF_CAP = 20;                // выбор «не юмор / не по теме»: оценка не выше
const SOURCE_VERDICT_WINDOW = 30;            // итоги полного анализа по каналу и теме: окно последних записей (SUBTOPIC_PROMPT.md, часть F)
const SOURCE_VERDICT_MIN = 8;               // меньше стольких оценок по каналу — порядок по умолчанию
const SOURCE_VERDICT_LOW = 0.12;            // доля прошедших в пул ниже этой (при достаточных оценках) — канал в конец очереди
const SUBTOPIC_CACHE_TOPICS = ['абсурдный юмор', 'абсурд и шитпост', 'чёрный юмор'];   // темы, где кэш анализа сброшен после правок части A–B (SUBTOPIC_PROMPT.md, часть G)
const SUBTOPIC_JOKE_KEEP = ['humor.jokes', 'humor.other', 'humor.fails', 'humor.funny_videos', 'memes.other'];   // подтемы, где проверка «есть ли шутка» остаётся (общие темы «Мемы» и «Юмор» — тоже)
const ANALYSIS_SEED = 7;                    // фиксированный seed: одна и та же запись — одна и та же оценка
const QUALITY_NO_EVIDENCE_CAP = 40;         // без цитаты/предмета или без шутки в юморе оценка не выше этого: модель додумывала (часть C)
const QUALITY_VIEWS_MIN_RATIO = 0.7;        // просмотры / медиана канала: ниже — 31% лайков против 62–68% при ≥1,3×
const QUALITY_CARD_MIN_RATINGS = 5;         // оценок источника по теме, с которых его доля лайков входит в балл карточки (часть D)
const QUALITY_SOURCE_NEUTRAL_SHARE = 0.4;   // доля лайков, при которой источник не двигает балл
const QUALITY_SOURCE_WEIGHT = 30;           // (доля − 0,4) × 30, ограничено ±15
const QUALITY_SOURCE_CAP = 15;
const QUALITY_TAG_MIN_RATINGS = 8;         // оценок по первой метке, с которых её вкус входит в балл (часть E)
const QUALITY_TAG_WEIGHT = 0;               // часть E: выключено — честная проверка не дала выигрыша (разрыв верх−низ 52 против 53 без неё). Значение 40 — вариант из промпта
const QUALITY_TAG_CAP = 15;
const QUALITY_PLATFORM_MIN_RATINGS = 30;    // оценок площадки по теме, с которых её судим (меньше — шума много)
const QUALITY_PLATFORM_COMPLAINT_SHARE = 0.5; // доля жалоб, с которой площадка по теме режется (Двач в мемах — 63%)
const QUALITY_PLATFORM_SAMPLE = 10;         // такие площадки идут в полный анализ 1 из 10 кандидатов (модель не тратится впустую)
const PRESCREEN_JUNK_P = 0.8;                             // быстрая оценка 2B — только «мусор ли» (реклама, пустая картинка, скриншот): с такой уверенностью — отброс; тему 2B не видит (PRESCREEN_SOFTEN_PROMPT.md)
const TOPIC_QUERY_MAX_LEN = 60;                          // уточнение к теме (TOPIC_QUERY_PROMPT.md)
const TOPIC_QUERY_MIN_SOURCES = 3;                        // своих источников у пары «тема + запрос» меньше — подбор источников по запросу
const TOPIC_QUERY_DISCOVERY_PER_DAY = 10;                 // новых запросов с подбором источников на пользователя в сутки
const TOPIC_QUERY_CHANNELS_PER_RUN = 12;                  // каналов из поиска по уточнению, которые проверяем за прогон
const SCREENED_CACHE_VERSION = 2;                         // версия записей screened_cache: ver 1 — оценки по теме (до PRESCREEN_SOFTEN), не читаются
const PRESCREEN_BATCH = 10;
const ANALYZE_BLOCK = 8;                                  // прошедшие быструю оценку копятся до стольких и уходят на полный анализ разом: меньше смен модели
const PRESCREEN_SOURCE_MISS_LIMIT = 6;                    // промахов подряд — источник пропускается до конца запуска

// ---- Горячие посты первыми (HOT_FIRST_PROMPT.md) ----
const CONTENT_MIN_SOURCES = 8;                            // меньше своих источников темы (с высокими предложениями) — сначала автоподбор, потом сбор
const DISCOVERY_BLOCKING_SECONDS = 600;                   // автоподбор перед сбором: не дольше стольких секунд; остаток — в конце прохода
const HOT_PER_SOURCE = 3;                                 // в шортлист — не больше стольких горячих постов с одного источника
const HOT_SHORTLIST = 60;                                 // ≈ 2 × реальная ёмкость полного анализа за проход
const HOT_PLATFORM_SHARE = 0.4;                           // доля одной площадки в шортлисте (Двач не забивает список)
const HOT_KEYWORD_BOOST = 1.3;                            // пост со словом из keywords профиля темы поднимается в шортлисте
const HOT_TRUST_BOOST = 1.5;                              // пост из доверенного треда (thread_trust) — в общем отборе с этим множителем (CONTENT_FLOW_PROMPT.md, A2)
const HOT_QUERY_BOOST = 1.5;                              // уточнение сверх HOT_SHORTLIST / 2 — в общем отборе с этим множителем (CONTENT_FLOW_PROMPT.md, A1)
const IMAGEBOARD_NATIVE_TOPIC_KEYS = ['memes', 'internet'];   // темы, где имиджборды — родная площадка: посты без признаков темы в шортлист (CONTENT_FLOW_PROMPT.md, B2)
const HOT_TEXT_MIN_CHARS = 40;                            // подписи короче — без пакетной проверки по тексту
const HOT_TEXT_BATCH = 15;                                // подписей в одном вызове 9B
const HOT_TEXT_REJECT_SCORE = 20;                         // тема «текст»: подпись ниже этого балла — отброс
const HOT_COLLAGE_CHECK = false;                          // коллаж 3×2 для 9B: включать только после замера (см. отчёт HOT_FIRST_PROMPT.md, C3)

const PHASH_SAME_DISTANCE = 20;   // из 256 бит: ближе — та же картинка (пережатая, уменьшенная); подбирается командой phash-report

// ---- Автоподбор источников ----
const SOURCE_DISCOVERY_ENABLED = true;
const SOURCE_DISCOVERY_INTERVAL = 12 * 3600;
const SOURCE_DISCOVERY_MAX_SECONDS = 900;
const SOURCE_DISCOVERY_MAX_CHECKS = 40;
const SOURCE_AUTO_ADD_SCORE = 101;                        // >100: автодобавление выключено, всё решает пользователь
const SOURCE_SUGGEST_SCORE = 55;
const SOURCE_REVIEW_CAPTIONS = 10;                    // подписи последних постов канала в проверке умной моделью (PRESCREEN_SOFTEN_PROMPT.md, часть 4)
const DISCOVERY_RECHECK_DAYS = 30;                       // отклонённый канал перепроверяется через месяц
const DVACH_BOARDS_PER_TOPIC = 4;
const DVACH_BOARDS_PER_RUN = 20;
const DVACH_ALWAYS_BOARDS = ['b'];   // доски, которые берутся под любую тему: контент из «плохих» источников не отсекаем, а проверяем по теме
const DISCOVERY_CHECKED_LIMIT = 1000;
const DISCOVERY_SNOWBALL_SEEDS = 8;
const DISCOVERY_SIMILAR_SEEDS = 6;              // сидов для «похожих каналов» Telegram за прогон: свои каналы темы и высокие предложения, по кругу (HOT_FIRST_PROMPT.md, D1.1)
const DISCOVERY_DEEP_EXTRA_SCORE = 10;          // на глубине 2 порог принятия выше на столько пунктов (DISCOVERY_FIX_PROMPT.md, часть E)
const DISCOVERY_DEFER_MAX = 20;                 // отложенных кандидатов t.me на тему (часть D)
const DISCOVERY_DEFER_DAYS = 3;
const DISCOVERY_SNOWBALL_MAX_DEPTH = 2;        // «похож на» и «упомянут в» — не глубже 2 шагов от поиска (TOPICS_SEARCH_PROMPT.md, часть C)
const DISCOVERY_KEYWORD_RECENT = 8;            // последние подписи канала, по которым ищем слова темы
const DISCOVERY_KEYWORD_MIN_TEXTS = 2;         // столько подписей со словом темы — канал проходит фильтр
const DISCOVERY_TEXT_REJECT_SCORE = 20;
const DISCOVERY_MIN_SUBSCRIBERS = 30;
const DISCOVERY_MIN_OWN_SOURCES = 3;
const DISCOVERY_SHORTAGE_INTERVAL = 1800;
const DISCOVERY_NAMES_PER_RUN = 0;               // названий от модели за прогон: 0 — выключено (HOT_FIRST_PROMPT.md, часть D1.4: дают бренды и несуществующие адреса); код не удалён (DISCOVERY_NAMES_PROMPT.md, часть B)
const DISCOVERY_NAMES_IF_SEARCH_BELOW = 15;      // названия от модели ищем, только если поиск по запросам дал меньше стольких каналов Telegram
const DISCOVERY_NAMES_REBUILD_STEP = 5;          // одобренных стало на столько больше — названия от модели пересоздаются
const DISCOVERY_NAMES_MAX_DAYS = 14;             // старше этого — тоже пересоздаются
const DISCOVERY_REF_BONUS = 5;                   // балл предложения за каждую повторную находку (не больше 4)
const DISCOVERY_REFS_MAX = 10;                   // повторных находок, которые помним за предложением
const DISCOVERY_ORIGIN_MIN_VOTES = 8;            // решений одного вида, чтобы учесть его при порядке предложений

// ---- Свежесть, активность и качество источников ----
const CONTENT_MAX_AGE_DAYS = 30;
const CONTENT_FRESH_MAX_AGE_DAYS = 1;                     // «Только новый контент»
const DVACH_THREAD_ALIVE_HOURS = 24;                      // «Только актуальные источники»
const DVACH_MAX_AGE_DAYS = 120;
const SOURCE_STALE_DAYS = 14;
const SOURCE_MIN_POSTS_PER_WEEK = 2;
const SOURCE_FROZEN_RECHECK_DAYS = 3;
const SOURCE_WEIGHT_CONTENT = 0.45;                       // итог = по теме + аудитория + доля просмотров
const SOURCE_WEIGHT_AUDIENCE = 0.25;
const SOURCE_WEIGHT_VIEW_RATE = 0.30;
const SOURCE_MIN_CONTENT_SCORE = 68;
const SOURCE_DEAD_VIEW_RATE = 0.015;
const SOURCE_MIN_SUBSCRIBERS_FOR_RATE = 300;
const SOURCE_FEEDBACK_MIN_VOTES = 3;
const SOURCE_FEEDBACK_BAN_VOTES = 8;
const SOURCE_FEEDBACK_BAN_SCORE = 0.2;
const SOURCE_MUTE_MIN_RATINGS = 10;        // оценок источника по теме, с которых его выключаем у пользователя (TOPICS_SEARCH_PROMPT.md, часть F)
const SOURCE_MUTE_LIKE_SHARE = 0.25;       // доля лайков ниже — выключаем; жалоба «не по теме» считается как дизлайк ×2
const SOURCE_MUTE_COMPLAINT_WEIGHT = 2;
const DVACH_THREAD_MIN_POSTS = 5;
const POOL_LIVENESS_INTERVAL = 7200;
const POOL_LIVENESS_MAX_THREADS = 40;
const DVACH_THREADS_PER_BOARD = 8;
const DVACH_THREAD_TITLE_MAX = 36;
const THREAD_TOPIC_SKIP = 20;                    // тред имиджборды с оценкой по теме ниже — посты не берём (IMAGEBOARD_CONTEXT_HASHTAGS_PROMPT.md, часть A)
const THREAD_TOPIC_TRUST = 70;                   // выше — посты треда идут в анализ первыми
const THREAD_TRUST_PRESCREEN_BONUS = 10;         // для таких тредов порог «мусор» быстрой оценки выше на столько: доверенный тред отбрасывают реже
const THREAD_TOPIC_CACHE_SECONDS = 86400;        // оценка треда по теме держится сутки
const THREAD_TITLE_MAX = 120;                    // название треда в записи, символов
const THREAD_OP_MAX = 400;                       // шапка треда (текст ОП-поста) в записи, символов
const DVACH_POSTS_PER_THREAD = 60;
const TELEGRAM_POSTS_PER_CHANNEL = 40;
const VK_POSTS_PER_PUBLIC = 60;
const RSS_POSTS_PER_FEED = 15;

// ---- Видео ----
const VIDEO_MAX_BYTES = 30 * 1024 * 1024;
const VIDEO_FRAMES_MIN = 4;
const VIDEO_FRAMES_MAX = 8;
const VIDEO_SECONDS_PER_FRAME = 3.0;
const VIDEO_FRAME_MAX_SIDE = 384;
const STORYBOARD_FRAMES_PER_SHEET = 4;                    // кадры уходят в модель листами 2×2
const VIDEO_LIGHT_BYTES = 6 * 1024 * 1024;                // тяжелее — пережимается для браузера
const VIDEO_LIGHT_HEIGHT = 720;
// Форма поста: отдельная ось от темы (картинка с котом — форма content, тема «Кошки»)
const CONTENT_FORMATS = [
    'content' => 'картинка или видео с наложенным текстом-шуткой',
    'photo' => 'обычное фото без текста-шутки',
    'video' => 'обычное видео без текста-шутки',
    'text'  => 'в основном текст: скриншот переписки, цитата, пост',
    'art'   => 'рисунок, комикс, 3D или сгенерированная картинка',
];
const CONTENT_TAGS_MAX = 3;                                 // метки записи: частные темы дерева от модели полного анализа

// ---- История и публикация ----
const HISTORY_COLLECTIONS = ['liked', 'disliked', 'undecided', 'complaints'];
const PROPOSAL_COLLECTIONS = ['liked', 'undecided'];      // «Предложка»
const CONTENT_VOTE_COLLECTIONS = ['like' => 'liked', 'dislike' => 'disliked', 'maybe' => 'undecided', 'wrong' => 'complaints'];
const COMPLAINT_REASONS = ['match_error', 'broken', 'other'];
const PROPOSALS_LIMIT = 200;
const MAX_IMAGE_BYTES = 10 * 1024 * 1024;
const TELEGRAM_MAX_PHOTO_BYTES = 10 * 1024 * 1024;
const TELEGRAM_MAX_MEDIA = 10;
const TELEGRAM_CAPTION_LIMIT = 1024;
const TELEGRAM_TEXT_LIMIT = 4096;
const TELEGRAM_MIN_DELAY_SECONDS = 60;
const TELEGRAM_MAX_DELAY_DAYS = 365;
const TELEGRAM_PUBLISHED_LOG_LIMIT = 30;
const TELEGRAM_UPLOAD_MAX_BYTES = 64 * 1024 * 1024;
const TELEGRAM_LIMIT_DEFAULTS = ['bot' => 50 * 1024 * 1024, 'account' => 2000 * 1024 * 1024];
const TELEGRAM_USER_PYTHON = '/usr/bin/python3';
const TELEGRAM_USER_TIMEOUT = 300;
const TGU_VIDEO_MAX_BYTES = 150 * 1024 * 1024;   // большое видео поста Telegram, которое скачивает личный аккаунт пользователя
const TGU_VIDEO_TIMEOUT = 900;
const TELEGRAM_USER_PENDING_TTL = 900;

// ---- Тарифы, баланс и приглашения ----
// Деньги — рубли на внутреннем балансе, без «баллов». Хранятся в копейках. Вывести нельзя — только оплатить Posta.
const BILLING_ENFORCE = false;            // лимиты тарифов: false — только показываются (приёма платежей ещё нет), true — проверяются при публикации
const BILLING_PERIOD_DAYS = 30;
const BILLING_FREE_PLAN = 'free';
const BILLING_PLANS = [                   // цены в рублях за период; совпадают с ценниками на лендинге
    'free' => ['title' => 'Смотреть', 'tag' => 'TITTA', 'price' => 0, 'channels' => 1, 'posts_per_day' => 3, 'schedule' => false],
    // «Хот-дог»: неделя «Автопилота» за 49 ₽ — один раз и только до первой покупки любого тарифа
    'trial' => ['title' => 'Хот-дог', 'tag' => 'KORV', 'price' => 49, 'channels' => 1, 'posts_per_day' => 10, 'schedule' => true, 'days' => 7, 'once' => true],
    'autopilot' => ['title' => 'Автопилот', 'tag' => 'AUTÖPILÖT', 'price' => 390, 'channels' => 1, 'posts_per_day' => 10, 'schedule' => true],
    'network' => ['title' => 'Сетка', 'tag' => 'SJÄTKÅ', 'price' => 990, 'channels' => 5, 'posts_per_day' => 50, 'schedule' => true],
];
// «Свой тариф»: цена «Автопилота» + за каждый канал сверх первого + за каждые 10 постов в день сверх 10. Готовые тарифы — со скидкой.
const BILLING_CUSTOM = ['base' => 390, 'channel' => 150, 'posts_step' => 10, 'posts_step_price' => 60, 'max_channels' => 10, 'max_posts_per_day' => 100];
const BILLING_HOUR_SHARE = 4;             // за час — не больше четверти дневного лимита (минимум 1 пост): канал не забанят за спам
const REFERRAL_PERCENT = 20;              // приглашённый оплатил тариф — пригласившему на баланс 20% от суммы
const REFERRAL_BIND_DAYS = 7;             // приглашение засчитывается новому профилю (первые 7 дней) до его первой покупки
