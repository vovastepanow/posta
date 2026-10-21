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

    $pdo = new PDO('sqlite:' . $db_path, null, null, [
        PDO::ATTR_ERRMODE => PDO::ERRMODE_EXCEPTION,
        PDO::ATTR_DEFAULT_FETCH_MODE => PDO::FETCH_ASSOC,
    ]);

    $pdo->exec('PRAGMA journal_mode=WAL');
    $pdo->exec('PRAGMA busy_timeout=' . DB_BUSY_TIMEOUT_MS);
    $pdo->exec('PRAGMA foreign_keys=ON');
    $pdo->exec('PRAGMA synchronous=NORMAL');

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
        "
    ];

    for ($v = $current + 1; $v <= max(array_keys($migrations)); $v++) {
        if (!isset($migrations[$v])) continue;
        $pdo->exec('BEGIN IMMEDIATE');
        try {
            $pdo->exec($migrations[$v]);
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
