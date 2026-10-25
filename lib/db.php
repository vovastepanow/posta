<?php
// SQLite база: соединение, миграции, транзакции и вспомогательные функции для работы с данными.

/** Одно соединение PDO на процесс. */
function db(): PDO {
    static $pdo = null;
    if ($pdo !== null) return $pdo;

    $db_path = DB_FILE;
    $db_dir = dirname($db_path);
    if (!is_dir($db_dir)) {
        umask(0002);
        mkdir($db_dir, 0775, true);
    }

    $old_umask = umask(0002);   // база и её -wal/-shm наследуют права: без группового write CLI (v0v4) и Apache (www-data) не уживаются — «readonly database»
    $pdo = new PDO('sqlite:' . $db_path, null, null, [
        PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
        PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC,
    ]);

    $pdo->exec('PRAGMA journal_mode=WAL');
    $pdo->exec('PRAGMA busy_timeout=' . DB_BUSY_TIMEOUT_MS);
    $pdo->exec('PRAGMA foreign_keys=ON');
    $pdo->exec('PRAGMA synchronous=NORMAL');
    umask($old_umask);

    db_migrate($pdo);
    return $pdo;
}

/** Миграции: версия в PRAGMA user_version, применяются внутри транзакции. */
function db_migrate(PDO $pdo): void {
    $current = (int)$pdo->query('PRAGMA user_version')->fetchColumn();

    $migrations = [
        1 => "
CREATE TABLE IF NOT EXISTS users (
  id TEXT PRIMARY KEY,
  created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL DEFAULT 0,
  proposals_checked_at INTEGER NOT NULL DEFAULT 0,
  threads_checked_at INTEGER NOT NULL DEFAULT 0,
  settings TEXT NOT NULL DEFAULT '{}',
  connections TEXT NOT NULL DEFAULT '{}'
);

CREATE TABLE IF NOT EXISTS pool_items (
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  position INTEGER NOT NULL,
  image_key TEXT NOT NULL,
  data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS pool_items_user ON pool_items(user_id, position);
CREATE INDEX IF NOT EXISTS pool_items_key ON pool_items(image_key);

CREATE TABLE IF NOT EXISTS history_items (
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  collection TEXT NOT NULL,
  position INTEGER NOT NULL,
  image_key TEXT NOT NULL,
  proposal_hidden INTEGER NOT NULL DEFAULT 0,
  data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS history_items_user ON history_items(user_id, collection, position);
CREATE INDEX IF NOT EXISTS history_items_key ON history_items(image_key);

CREATE TABLE IF NOT EXISTS outbox (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  publish_at INTEGER NOT NULL,
  status TEXT NOT NULL,
  data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS outbox_user ON outbox(user_id, publish_at);

CREATE TABLE IF NOT EXISTS published (
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  at INTEGER NOT NULL,
  data TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS published_user ON published(user_id, id DESC);

CREATE TABLE IF NOT EXISTS parser_runtime (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  pid INTEGER NOT NULL DEFAULT 0,
  started_at INTEGER NOT NULL DEFAULT 0,
  tab TEXT NOT NULL DEFAULT '',
  tab_seen INTEGER NOT NULL DEFAULT 0,
  tab_opened_at INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS catalog_sources (
  kind TEXT NOT NULL,
  source_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  data TEXT NOT NULL,
  PRIMARY KEY (kind, source_id)
);

CREATE TABLE IF NOT EXISTS source_votes (
  id INTEGER PRIMARY KEY,
  source_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  data TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS source_suggestions (
  id INTEGER PRIMARY KEY,
  source_id TEXT NOT NULL,
  position INTEGER NOT NULL,
  data TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS topics (
  id TEXT PRIMARY KEY,
  position INTEGER NOT NULL,
  data TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS kv (
  key TEXT PRIMARY KEY,
  value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS analysis_cache (
  key TEXT PRIMARY KEY,
  analysis TEXT NOT NULL,
  at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS analysis_cache_at ON analysis_cache(at);

CREATE TABLE IF NOT EXISTS screened_cache (
  url TEXT PRIMARY KEY,
  topic TEXT NOT NULL,
  source TEXT NOT NULL DEFAULT '',
  score INTEGER NOT NULL,
  at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS screened_cache_at ON screened_cache(at);
CREATE INDEX IF NOT EXISTS screened_cache_topic ON screened_cache(topic);
        ",
        2 => "
ALTER TABLE screened_cache ADD COLUMN phash TEXT NOT NULL DEFAULT '';
CREATE INDEX IF NOT EXISTS screened_cache_phash ON screened_cache(phash);
        ",
        3 => "
ALTER TABLE users ADD COLUMN ref_code TEXT;
ALTER TABLE users ADD COLUMN referred_by TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS users_ref_code ON users(ref_code) WHERE ref_code IS NOT NULL;
CREATE INDEX IF NOT EXISTS users_referred_by ON users(referred_by);

CREATE TABLE IF NOT EXISTS ledger (
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  at INTEGER NOT NULL,
  amount INTEGER NOT NULL,
  kind TEXT NOT NULL,
  note TEXT NOT NULL DEFAULT '',
  related_user TEXT NOT NULL DEFAULT ''
);
CREATE INDEX IF NOT EXISTS ledger_user ON ledger(user_id, id);

CREATE TABLE IF NOT EXISTS subscriptions (
  user_id TEXT PRIMARY KEY REFERENCES users(id) ON DELETE CASCADE,
  plan TEXT NOT NULL,
  channels INTEGER NOT NULL,
  posts_per_day INTEGER NOT NULL,
  price INTEGER NOT NULL,
  started_at INTEGER NOT NULL,
  until INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS post_usage (
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS post_usage_user ON post_usage(user_id, at);
        ",
        5 => "
ALTER TABLE users ADD COLUMN telegram_id INTEGER;
ALTER TABLE users ADD COLUMN tg_username TEXT NOT NULL DEFAULT '';
ALTER TABLE users ADD COLUMN tg_name TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS users_telegram_id ON users(telegram_id) WHERE telegram_id IS NOT NULL;

CREATE TABLE IF NOT EXISTS sessions (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  last_seen INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS sessions_user ON sessions(user_id);

CREATE TABLE IF NOT EXISTS login_requests (
  token_hash TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'asked', 'confirmed', 'used')),
  chat_id INTEGER,
  telegram_id INTEGER,
  tg_username TEXT NOT NULL DEFAULT '',
  tg_name TEXT NOT NULL DEFAULT ''
);
        ",
        // Радар лидов: схема была написана под MySQL (migrations/009_*.sql) и в SQLite не создавалась
        6 => "
CREATE TABLE IF NOT EXISTS telegram_lead_queue (
  id INTEGER PRIMARY KEY,
  user_id TEXT NOT NULL,
  chat_id INTEGER NOT NULL,
  message TEXT NOT NULL,
  message_id INTEGER,
  status TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'processing', 'processed', 'failed')),
  ollama_result INTEGER,
  ollama_error TEXT,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  processed_at TEXT,
  attempts INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS telegram_lead_queue_status ON telegram_lead_queue(status);
CREATE INDEX IF NOT EXISTS telegram_lead_queue_chat ON telegram_lead_queue(chat_id, created_at);

CREATE TABLE IF NOT EXISTS telegram_leads (
  id INTEGER PRIMARY KEY,
  queue_id INTEGER NOT NULL UNIQUE REFERENCES telegram_lead_queue(id) ON DELETE CASCADE,
  user_id TEXT NOT NULL,
  chat_id INTEGER NOT NULL,
  message TEXT,
  message_id INTEGER,
  lead_status TEXT NOT NULL DEFAULT 'new' CHECK (lead_status IN ('new', 'replied', 'spam', 'interested', 'ignored')),
  telegram_username TEXT,
  telegram_profile_url TEXT,
  confidence_score REAL NOT NULL DEFAULT 1.0,
  assigned_to INTEGER,
  created_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP,
  replied_at TEXT,
  updated_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);
CREATE INDEX IF NOT EXISTS telegram_leads_status ON telegram_leads(lead_status, created_at);
CREATE INDEX IF NOT EXISTS telegram_leads_user ON telegram_leads(user_id);
        ",
        // 18+ и «Жесть»: флаг ставит администратор командой tools/cli.php adult-allow
        7 => "
ALTER TABLE users ADD COLUMN adult_allowed INTEGER NOT NULL DEFAULT 0;
        ",
        // Каналы Telegram пользователя: несколько ботов и каналов; position 0 — канал по умолчанию
        8 => "
CREATE TABLE IF NOT EXISTS channels (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform TEXT NOT NULL DEFAULT 'telegram',
  position INTEGER NOT NULL DEFAULT 0,
  token_enc TEXT NOT NULL,
  bot_id INTEGER NOT NULL,
  bot_username TEXT NOT NULL DEFAULT '',
  chat_id INTEGER NOT NULL,
  chat_title TEXT NOT NULL DEFAULT '',
  chat_username TEXT NOT NULL DEFAULT '',
  chat_type TEXT NOT NULL DEFAULT '',
  connected_at INTEGER NOT NULL,
  UNIQUE (user_id, chat_id)
);
CREATE INDEX IF NOT EXISTS channels_user ON channels(user_id, position);
        ",
        // Расписание канала: слоты по дням недели, часовой пояс, случайный сдвиг (JSON)
        9 => "
ALTER TABLE channels ADD COLUMN schedule TEXT NOT NULL DEFAULT '{}';
        ",
        10 => "
ALTER TABLE telegram_lead_queue ADD COLUMN chat_ref TEXT;
ALTER TABLE telegram_lead_queue ADD COLUMN chat_title TEXT;
ALTER TABLE telegram_lead_queue ADD COLUMN chat_username TEXT;
ALTER TABLE telegram_lead_queue ADD COLUMN sender_id INTEGER;
ALTER TABLE telegram_lead_queue ADD COLUMN sender_username TEXT;
ALTER TABLE telegram_lead_queue ADD COLUMN sender_name TEXT;
ALTER TABLE telegram_lead_queue ADD COLUMN message_date TEXT;
CREATE UNIQUE INDEX IF NOT EXISTS telegram_lead_queue_unique ON telegram_lead_queue(user_id, chat_id, message_id);

ALTER TABLE telegram_leads ADD COLUMN chat_title TEXT;
ALTER TABLE telegram_leads ADD COLUMN sender_id INTEGER;
ALTER TABLE telegram_leads ADD COLUMN sender_name TEXT;
ALTER TABLE telegram_leads ADD COLUMN message_url TEXT;
ALTER TABLE telegram_leads ADD COLUMN reason TEXT;
        ",
        // Радар лидов: почему модель отсеяла сообщение (для вкладки «Отсеяно моделью» в админке)
        11 => "
ALTER TABLE telegram_lead_queue ADD COLUMN ollama_reason TEXT;
        ",
        // Стиль канала для подписей от ИИ: описание манеры письма и образцы постов (JSON)
        12 => "
ALTER TABLE channels ADD COLUMN style TEXT NOT NULL DEFAULT '{}';
        ",
        // Зеркала источников: свой источник пользователя → подборка или очередь его канала без оценки моделью
        13 => "
CREATE TABLE IF NOT EXISTS mirrors (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform TEXT NOT NULL,
  source_id TEXT NOT NULL,
  name TEXT NOT NULL DEFAULT '',
  channel_id TEXT,
  mode TEXT NOT NULL DEFAULT 'pool',
  rules TEXT NOT NULL DEFAULT '{}',
  last_post_key TEXT NOT NULL DEFAULT '[]',
  last_checked_at INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS mirrors_user ON mirrors(user_id);
        ",
        14 => "
CREATE TABLE IF NOT EXISTS source_verdicts (
  topic TEXT NOT NULL,
  source TEXT NOT NULL,
  passed INTEGER NOT NULL DEFAULT 0,
  total INTEGER NOT NULL DEFAULT 0,
  at INTEGER NOT NULL,
  PRIMARY KEY (topic, source)
);
        ",
        // Хэштеги в постах канала: off | rubric | full (рубрика, имя, формат)
        15 => "
ALTER TABLE channels ADD COLUMN hashtags TEXT NOT NULL DEFAULT 'off';
        ",
        // Ресурсы соцсетей (AUTOPOST_NETWORKS_PROMPT.md, часть A): таблица пересобрана, т.к. UNIQUE (user_id, chat_id) не годится для других соцсетей.
        // Telegram: ref = chat_id; токен переезжает в secrets_enc (channels_backfill_secrets). Старые столбцы Telegram остаются.
        16 => "
CREATE TABLE channels_v16 (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  platform TEXT NOT NULL DEFAULT 'telegram',
  ref TEXT NOT NULL DEFAULT '',
  secrets_enc TEXT NOT NULL DEFAULT '',
  meta TEXT NOT NULL DEFAULT '{}',
  position INTEGER NOT NULL DEFAULT 0,
  token_enc TEXT NOT NULL DEFAULT '',
  bot_id INTEGER NOT NULL DEFAULT 0,
  bot_username TEXT NOT NULL DEFAULT '',
  chat_id INTEGER NOT NULL DEFAULT 0,
  chat_title TEXT NOT NULL DEFAULT '',
  chat_username TEXT NOT NULL DEFAULT '',
  chat_type TEXT NOT NULL DEFAULT '',
  connected_at INTEGER NOT NULL,
  schedule TEXT NOT NULL DEFAULT '{}',
  style TEXT NOT NULL DEFAULT '{}',
  hashtags TEXT NOT NULL DEFAULT 'off',
  UNIQUE (user_id, platform, ref)
);
INSERT INTO channels_v16 (id, user_id, platform, ref, position, token_enc, bot_id, bot_username, chat_id, chat_title, chat_username, chat_type, connected_at, schedule, style, hashtags)
    SELECT id, user_id, platform, CAST(chat_id AS TEXT), position, token_enc, bot_id, bot_username, chat_id, chat_title, chat_username, chat_type, connected_at, schedule, style, hashtags FROM channels;
DROP TABLE channels;
ALTER TABLE channels_v16 RENAME TO channels;
CREATE INDEX channels_user ON channels(user_id, position);
        ",
        // Быстрая оценка по «мусор ли» (PRESCREEN_SOFTEN_PROMPT.md): старые оценки по теме помечаются версией 1 и не читаются
        17 => "
ALTER TABLE screened_cache ADD COLUMN ver INTEGER NOT NULL DEFAULT 1;
        ",
        // Свои учётные данные (запасной вход, если Telegram недоступен): логин — почта, телефон или ник; пароль — password_hash
        18 => "
ALTER TABLE users ADD COLUMN login TEXT;
ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';
CREATE UNIQUE INDEX IF NOT EXISTS users_login ON users(login) WHERE login IS NOT NULL;

CREATE TABLE IF NOT EXISTS login_failures (
  key TEXT NOT NULL,
  at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS login_failures_key ON login_failures(key, at);
        "
    ];

    for ($v = $current + 1; $v <= max(array_keys($migrations)); $v++) {
        if (!isset($migrations[$v])) continue;
        $pdo->exec('BEGIN IMMEDIATE');
        try {
            $pdo->exec($migrations[$v]);
            if ($v === 16 && function_exists('channels_backfill_secrets')) channels_backfill_secrets($pdo);
            $pdo->exec('PRAGMA user_version = ' . $v);
            $pdo->exec('COMMIT');
        } catch (Exception $e) {
            $pdo->exec('ROLLBACK');
            throw $e;
        }
    }
}

/** Транзакция: BEGIN IMMEDIATE → fn() → COMMIT, при ошибке ROLLBACK. Поддерживает вложенность. */
function db_tx(callable $fn) {
    $pdo = db();
    $is_nested = $pdo->inTransaction();

    if (!$is_nested) $pdo->exec('BEGIN IMMEDIATE');
    try {
        $result = $fn();
        if (!$is_nested) $pdo->exec('COMMIT');
        return $result;
    } catch (Exception $e) {
        if (!$is_nested) $pdo->exec('ROLLBACK');
        throw $e;
    }
}

function db_all(string $sql, array $params = []): array {
    $stmt = db()->prepare($sql);
    $stmt->execute($params);
    return $stmt->fetchAll();
}

function db_one(string $sql, array $params = []): ?array {
    $stmt = db()->prepare($sql);
    $stmt->execute($params);
    return $stmt->fetch() ?: null;
}

function db_value(string $sql, array $params = []) {
    $stmt = db()->prepare($sql);
    $stmt->execute($params);
    return $stmt->fetchColumn();
}

function db_exec(string $sql, array $params = []): int {
    $stmt = db()->prepare($sql);
    $stmt->execute($params);
    return $stmt->rowCount();
}

function db_json($value): string {
    return json_encode($value, JSON_UNESCAPED_UNICODE | JSON_INVALID_UTF8_SUBSTITUTE);
}

function db_unjson(?string $json, $default = []) {
    if ($json === null || $json === '') return $default;
    $decoded = @json_decode($json, true);
    return is_array($decoded) ? $decoded : $default;
}
