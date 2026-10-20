<?php
// Настройки. Секреты сервера — в config.local.php (не в git), шаблон: config.local.example.php.
$local = is_file(__DIR__ . '/config.local.php') ? (require __DIR__ . '/config.local.php') : [];
foreach (['VK_CLIENT_ID', 'VK_REDIRECT_URI', 'VK_SERVICE_TOKEN', 'VK_OAUTH_SCOPE'] as $name) {
    if ((string)getenv($name) === '') putenv($name . '=' . (is_array($local) ? ($local[$name] ?? '') : ''));
}

const APP_NAME = 'Postmaker';
const ROOT = __DIR__;

// ---- Данные ----
const SOURCES_FILE = ROOT . '/sources_db.json';           // общий каталог источников и реестр тем
const USERS_DIR = ROOT . '/users';                        // users/<id>/: пул, история, настройки, журнал парсера
const USER_COOKIE = 'ct_uid';
const USER_COOKIE_DAYS = 365;
const USER_RETENTION_DAYS = 60;                           // профили без заходов дольше удаляются
const MAX_PARALLEL_PARSERS = 4;  // Увеличено для лучшей GPU загрузки (от 6% до 70%+)
const DB_FILE = ROOT . '/data/postmaker.sqlite';             // вся база: пользователи, пулы, история, каталог, кэши
const DB_BUSY_TIMEOUT_MS = 15000;
const ANALYSIS_CACHE_FILE = ROOT . '/analysis_cache.json'; // полный анализ, общий для всех пользователей темы
const ANALYSIS_CACHE_LIMIT = 2000;
const ANALYSIS_CACHE_DAYS = 14;
const SCREENED_CACHE_FILE = ROOT . '/screened_cache.json'; // быстрые оценки картинок по темам
const SCREENED_CACHE_LIMIT = 5000;
const SCREENED_CACHE_DAYS = 14;
const PARSER_SCRIPT = ROOT . '/cron_parser.php';
const PARSER_RUNTIME_VERSION = 'v3';                      // входит в имена журнала и состояния парсера
// Медиа: по умолчанию в папке проекта; на сервере — вне веб-корня, путь задаётся в config.local.php (MEDIA_CACHE_DIR, MEDIA_CACHE_URL)
define('MEDIA_CACHE_DIR', $local['MEDIA_CACHE_DIR'] ?? ROOT . '/media_cache');
define('MEDIA_CACHE_URL', $local['MEDIA_CACHE_URL'] ?? 'media_cache');
const MEDIA_CACHE_GRACE_SECONDS = 600;                    // свежие файлы кэша не трогаем: парсер мог ещё не записать запись
const DEFAULT_TOPIC = 'мемы и юмор';
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
const PERSONAL_TASTE_ENABLED = false;                     // прогноз вкуса по прошлым голосам (выключен: AUC 0.60)
const TASTE_EXAMPLES = 10;
const TASTE_EXAMPLE_CHARS = 140;
const ANALYSIS_IMAGE_MAX_SIDE = 768;
const FIXED_COST_IMAGE_MAX_SIDE = 1024;                   // малые qwen3-vl берут ~1040 токенов за картинку любого размера

// ---- Парсер ----
const CONTENT_POOL_TARGET = 50;                        // записей в пуле на тему
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
const RELEVANCE_THRESHOLD = 61;                           // порог «по теме» для попадания в пул
const PRESCREEN_MAX_SIDE = 768;
const PRESCREEN_THRESHOLD = RELEVANCE_THRESHOLD;           // тот же порог, что и для пула: записи 40–60 не тратят полный анализ зря
const PRESCREEN_BATCH = 10;
const ANALYZE_BLOCK = 8;                                  // прошедшие быструю оценку копятся до стольких и уходят на полный анализ разом: меньше смен модели
const PRESCREEN_SOURCE_MISS_LIMIT = 6;                    // промахов подряд — источник пропускается до конца запуска
const PRESCREEN_RELATIVE_PICK = 0;                        // 0 — выключено: ниже порога на полный анализ не идёт никто
const PRESCREEN_RELATIVE_FLOOR = 10;

// ---- Автоподбор источников ----
const SOURCE_DISCOVERY_ENABLED = true;
const SOURCE_DISCOVERY_INTERVAL = 12 * 3600;
const SOURCE_DISCOVERY_MAX_SECONDS = 900;
const SOURCE_DISCOVERY_MAX_CHECKS = 40;
const SOURCE_AUTO_ADD_SCORE = 101;                        // >100: автодобавление выключено, всё решает пользователь
const SOURCE_SUGGEST_SCORE = 55;
const SOURCE_SAMPLE_IMAGES = 6;
const SOURCE_EARLY_REJECT_AFTER = 3;
const SOURCE_EARLY_REJECT_SCORE = 45;
const SOURCE_TEXT_WEIGHT = 0.15;
const DVACH_BOARDS_PER_TOPIC = 4;
const DVACH_BOARDS_PER_RUN = 20;
const DISCOVERY_CHECKED_LIMIT = 1000;
const DISCOVERY_SNOWBALL_SEEDS = 8;
const DISCOVERY_TEXT_REJECT_SCORE = 20;
const DISCOVERY_MIN_SUBSCRIBERS = 30;
const DISCOVERY_MIN_OWN_SOURCES = 3;
const DISCOVERY_SHORTAGE_INTERVAL = 1800;

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
const SOURCE_UNKNOWN_AUDIENCE_SCORE = 30;
const SOURCE_UNKNOWN_VIEW_RATE_SCORE = 40;
const SOURCE_DEAD_VIEW_RATE = 0.015;
const SOURCE_MIN_SUBSCRIBERS_FOR_RATE = 300;
const SOURCE_FEEDBACK_MIN_VOTES = 3;
const SOURCE_FEEDBACK_BAN_VOTES = 8;
const SOURCE_FEEDBACK_BAN_SCORE = 0.2;
const DVACH_THREAD_MIN_POSTS = 5;
const POOL_LIVENESS_INTERVAL = 7200;
const POOL_LIVENESS_MAX_THREADS = 40;
const DVACH_THREADS_PER_BOARD = 8;
const DVACH_THREAD_TITLE_MAX = 36;
const DVACH_POSTS_PER_THREAD = 60;
const TELEGRAM_POSTS_PER_CHANNEL = 40;
const VK_POSTS_PER_PUBLIC = 60;

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
const TELEGRAM_LIMITS_FILE = ROOT . '/telegram_limits.json';
const TELEGRAM_APP_KEYS_FILE = ROOT . '/telegram_app.json';
const TELEGRAM_USER_PYTHON = '/usr/bin/python3';
const TELEGRAM_USER_TIMEOUT = 300;
const TELEGRAM_USER_PENDING_TTL = 900;
