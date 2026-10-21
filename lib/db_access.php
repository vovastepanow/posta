<?php
// Функции доступа к данным: замена load_data/update_data для всех таблиц.

// ---- Пользователи и настройки ----

/** Загрузить настройки пользователя из БД (raw, без нормализации). */
function db_load_settings($uid = null): array {
    $uid = $uid ?: app_user_id();
    $settings = db_value('SELECT settings FROM users WHERE id = ?', [$uid]);
    return db_unjson($settings, default_settings());
}

/** Сохранить настройки пользователя в БД (raw). */
function db_save_settings(array $settings, $uid = null): void {
    $uid = $uid ?: app_user_id();
    db_tx(function () use ($uid, $settings) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
        db_exec('UPDATE users SET settings = ? WHERE id = ?', [db_json($settings), $uid]);
    });
}


/** Обновить настройки через мутатор: внутри db_tx. */
function update_settings(callable $fn, $uid = null): array {
    $uid = $uid ?: app_user_id();
    return db_tx(function () use ($uid, $fn) {
        $current = load_settings($uid);
        $updated = $fn($current);
        if ($updated !== null) {
            save_settings($updated, $uid);
            return $updated;
        }
        return $current;
    });
}


/** Список всех пользователей. */
function user_ids(): array {
    return array_column(db_all('SELECT id FROM users ORDER BY last_seen DESC'), 'id');
}

/** Обновить proposals_checked_at. */
function user_proposals_checked($uid = null): void {
    $uid = $uid ?: app_user_id();
    db_exec('UPDATE users SET proposals_checked_at = ? WHERE id = ?', [time(), $uid]);
}

/** Обновить threads_checked_at. */
function user_threads_checked($uid = null): void {
    $uid = $uid ?: app_user_id();
    db_exec('UPDATE users SET threads_checked_at = ? WHERE id = ?', [time(), $uid]);
}

// ---- Пул записей ----

/** Загрузить пул пользователя как массив записей (в порядке position). */
function pool_load($uid = null): array {
    $uid = $uid ?: app_user_id();
    $rows = db_all('SELECT data FROM pool_items WHERE user_id = ? ORDER BY position', [$uid]);
    return array_map(fn($r) => db_unjson($r['data'], []), $rows);
}

/** Заменить пул пользователя целиком (через db_tx). */
function pool_update(callable $fn, $uid = null): array {
    $uid = $uid ?: app_user_id();
    return db_tx(function () use ($uid, $fn) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
        $current = pool_load($uid);
        $updated = $fn($current);
        if ($updated === null) return $current;

        db_exec('DELETE FROM pool_items WHERE user_id = ?', [$uid]);
        foreach ($updated as $pos => $item) {
            $key = content_image_key($item);
            db_exec('INSERT INTO pool_items (user_id, position, image_key, data) VALUES (?, ?, ?, ?)',
                    [$uid, $pos, $key, db_json($item)]);
        }
        return $updated;
    });
}

// ---- История голосования ----

/** Загрузить историю в формате [collection => [...]]. */
function history_load($uid = null): array {
    $uid = $uid ?: app_user_id();
    $history = empty_history();

    foreach (HISTORY_COLLECTIONS as $collection) {
        $rows = db_all('SELECT data FROM history_items WHERE user_id = ? AND collection = ? ORDER BY position',
                      [$uid, $collection]);
        $history[$collection] = array_map(fn($r) => db_unjson($r['data'], []), $rows);
    }
    return $history;
}

/** Обновить историю целиком (через db_tx). */
function history_update(callable $fn, $uid = null): array {
    $uid = $uid ?: app_user_id();
    return db_tx(function () use ($uid, $fn) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
        $current = history_load($uid);
        $updated = $fn($current);
        if ($updated === null) return $current;

        db_exec('DELETE FROM history_items WHERE user_id = ?', [$uid]);
        foreach (HISTORY_COLLECTIONS as $collection) {
            foreach ($updated[$collection] ?? [] as $pos => $item) {
                $key = content_image_key($item);
                $hidden = (!empty($item['proposal_hidden']) ? 1 : 0);
                db_exec('INSERT INTO history_items (user_id, collection, position, image_key, proposal_hidden, data)
                        VALUES (?, ?, ?, ?, ?, ?)',
                       [$uid, $collection, $pos, $key, $hidden, db_json($item)]);
            }
        }
        return $updated;
    });
}

/** Добавить одну запись в историю (для голосования). */
function history_append($collection, array $record, $uid = null): void {
    $uid = $uid ?: app_user_id();
    db_tx(function () use ($uid, $collection, $record) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
        $pos = db_value('SELECT COALESCE(MAX(position), -1) + 1 FROM history_items WHERE user_id = ? AND collection = ?',
                       [$uid, $collection]) ?? 0;
        $key = content_image_key($record);
        $hidden = (!empty($record['proposal_hidden']) ? 1 : 0);
        db_exec('INSERT INTO history_items (user_id, collection, position, image_key, proposal_hidden, data)
                VALUES (?, ?, ?, ?, ?, ?)',
               [$uid, $collection, $pos, $key, $hidden, db_json($record)]);
    });
}

/** Для редактора баз и отбора: все записи пула и скрытые в истории по всем пользователям. */
function all_users_pool_items(): array {
    $items = [];

    // Пул
    $rows = db_all('SELECT user_id, data FROM pool_items ORDER BY user_id, position');
    foreach ($rows as $r) {
        $items[] = db_unjson($r['data'], []);
    }

    // История (предложки, скрытые)
    // Все коллекции истории: их медиа нужно для показа в таблице, а не только предложке. Иначе очистка удаляла файлы дизлайков и жалоб.
    $rows = db_all('SELECT data FROM history_items');
    foreach ($rows as $r) {
        $items[] = db_unjson($r['data'], []);
    }

    return $items;
}

// ---- Соединения (Telegram, VK) ----

/** Загрузить connections.json. */
function connections_load($uid = null): array {
    $uid = $uid ?: app_user_id();
    $data = db_value('SELECT connections FROM users WHERE id = ?', [$uid]);
    return db_unjson($data, []);
}

/** Обновить connections. */
function connections_update(callable $fn, $uid = null): array {
    $uid = $uid ?: app_user_id();
    return db_tx(function () use ($uid, $fn) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
        $current = connections_load($uid);
        $updated = $fn($current);
        if ($updated === null) return $current;
        db_exec('UPDATE users SET connections = ? WHERE id = ?', [db_json($updated), $uid]);
        return $updated;
    });
}

// ---- Исходящие посты (Telegram) ----

/** Загрузить очередь постов (outbox). */
function outbox_load($uid = null): array {
    $uid = $uid ?: app_user_id();
    $rows = db_all('SELECT data FROM outbox WHERE user_id = ? ORDER BY publish_at', [$uid]);
    return array_map(fn($r) => db_unjson($r['data'], []), $rows);
}

/** Обновить очередь постов. */
function outbox_update(callable $fn, $uid = null): array {
    $uid = $uid ?: app_user_id();
    return db_tx(function () use ($uid, $fn) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
        $current = outbox_load($uid);
        $updated = $fn($current);
        if ($updated === null) return $current;

        db_exec('DELETE FROM outbox WHERE user_id = ?', [$uid]);
        foreach ($updated as $item) {
            $id = $item['id'] ?? bin2hex(random_bytes(8));
            db_exec('INSERT INTO outbox (id, user_id, publish_at, status, data) VALUES (?, ?, ?, ?, ?)',
                    [$id, $uid, $item['publish_at'] ?? time(), $item['status'] ?? 'pending', db_json($item)]);
        }
        return $updated;
    });
}

// ---- Опубликованные посты ----

/** Загрузить журнал опубликованных. */
function published_load($uid = null, $limit = null): array {
    $uid = $uid ?: app_user_id();
    $sql = 'SELECT data FROM published WHERE user_id = ? ORDER BY id DESC';
    if ($limit) $sql .= ' LIMIT ' . (int)$limit;
    $rows = db_all($sql, [$uid]);
    return array_map(fn($r) => db_unjson($r['data'], []), $rows);
}

/** Добавить пост в журнал (с обрезкой). */
function published_add(array $entry, $uid = null): void {
    $uid = $uid ?: app_user_id();
    db_tx(function () use ($uid, $entry) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
        db_exec('INSERT INTO published (user_id, at, data) VALUES (?, ?, ?)',
                [$uid, $entry['at'] ?? time(), db_json($entry)]);

        $count = db_value('SELECT COUNT(*) FROM published WHERE user_id = ?', [$uid]);
        if ($count > TELEGRAM_PUBLISHED_LOG_LIMIT) {
            $excess = $count - TELEGRAM_PUBLISHED_LOG_LIMIT;
            db_exec('DELETE FROM published WHERE user_id = ? AND id IN (SELECT id FROM published WHERE user_id = ? ORDER BY id ASC LIMIT ?)',
                    [$uid, $uid, $excess]);
        }
    });
}

// ---- Ключ-значение хранилище ----

/** Получить значение (или по умолчанию). */
function kv_get($key, $default = null) {
    $row = db_one('SELECT value FROM kv WHERE key = ?', [$key]);
    if (!$row) return $default;
    $data = db_unjson($row['value'], null);
    return is_array($data) && isset($data['_kv_value']) ? $data['_kv_value'] : $default;
}

/** Обновить значение через мутатор. */
function kv_update($key, callable $fn, $default = null) {
    return db_tx(function () use ($key, $fn, $default) {
        $row = db_one('SELECT value FROM kv WHERE key = ?', [$key]);
        $data = $row ? db_unjson($row['value'], null) : null;
        $current = is_array($data) && isset($data['_kv_value']) ? $data['_kv_value'] : $default;
        $updated = $fn($current);
        if ($updated === null) return $current;

        db_exec('INSERT OR REPLACE INTO kv (key, value) VALUES (?, ?)', [$key, db_json(['_kv_value' => $updated])]);
        return $updated;
    });
}

// ---- Каталог источников и темы ----

const CATALOG_KV_KEYS = ['dvach_topic_boards', 'discovery'];

/** Загрузить каталог источников, темы и голоса. */
function catalog_load(): array {
    $catalog = ['sources' => [], 'votes' => [], 'suggestions' => [], 'topics' => [], 'kv' => []];

    $rows = db_all('SELECT kind, source_id, position, data FROM catalog_sources ORDER BY kind, position');
    foreach ($rows as $r) {
        if (!isset($catalog['sources'][$r['kind']])) $catalog['sources'][$r['kind']] = [];
        // Доска Двача — строка, а db_unjson() отдаёт только массивы: имя доски берётся из source_id
        $catalog['sources'][$r['kind']][$r['source_id']] = $r['kind'] === '2ch_boards' ? (string)$r['source_id'] : db_unjson($r['data'], []);
    }

    // В колонке data — JSON записи: его нужно раскрыть, иначе при следующей записи каталога голоса, предложения и темы пропадут
    $decode = fn(array $rows) => array_values(array_filter(array_map(fn($r) => db_unjson($r['data'], null), $rows), 'is_array'));
    $catalog['votes'] = $decode(db_all('SELECT data FROM source_votes ORDER BY position'));
    $catalog['suggestions'] = $decode(db_all('SELECT data FROM source_suggestions ORDER BY position'));
    $catalog['topics'] = array_column($decode(db_all('SELECT data FROM topics ORDER BY position')), null, 'id');

    $rows = db_all('SELECT key, value FROM kv WHERE key IN (' . implode(', ', array_fill(0, count(CATALOG_KV_KEYS), '?')) . ')', CATALOG_KV_KEYS);
    foreach ($rows as $r) $catalog['kv'][$r['key']] = db_unjson($r['value'], []);

    return $catalog;
}

/** Обновить каталог целиком. */
function catalog_update(callable $fn): void {
    $catalog = catalog_load();
    $updated = $fn($catalog);
    if ($updated === null) return;

    db_tx(function () use ($updated) {
        db_exec('DELETE FROM catalog_sources');
        foreach ($updated['sources'] as $kind => $sources) {
            foreach (array_values($sources) as $pos => $source) {
                $source_id = is_array($source) ? (string)($source['id'] ?? $pos) : (string)$source;   // доска Двача — просто имя
                db_exec('INSERT INTO catalog_sources (kind, source_id, position, data) VALUES (?, ?, ?, ?)',
                        [$kind, $source_id, $pos, db_json($source)]);
            }
        }

        db_exec('DELETE FROM source_votes');
        foreach ($updated['votes'] as $pos => $vote) {
            db_exec('INSERT INTO source_votes (source_id, position, data) VALUES (?, ?, ?)',
                    [$vote['source_id'] ?? '', $pos, db_json($vote)]);
        }

        db_exec('DELETE FROM source_suggestions');
        foreach ($updated['suggestions'] as $pos => $sugg) {
            db_exec('INSERT INTO source_suggestions (source_id, position, data) VALUES (?, ?, ?)',
                    [$sugg['source_id'] ?? '', $pos, db_json($sugg)]);
        }

        db_exec('DELETE FROM topics');
        $pos = 0;
        foreach ($updated['topics'] as $id => $topic) {
            $topic['id'] = $id;
            db_exec('INSERT INTO topics (id, position, data) VALUES (?, ?, ?)',
                    [$id, $pos++, db_json($topic)]);
        }

        // В kv лежат и чужие ключи (лимиты Telegram, отметки и т. п.) — меняются только ключи каталога
        foreach (CATALOG_KV_KEYS as $key) {
            if (array_key_exists($key, $updated['kv'])) db_exec('INSERT OR REPLACE INTO kv (key, value) VALUES (?, ?)', [$key, db_json($updated['kv'][$key])]);
        }
    });
}

// ---- Состояние парсера ----

/** Получить состояние парсера пользователя. */
function parser_state_load($uid = null): array {
    $uid = $uid ?: app_user_id();
    $row = db_one('SELECT pid, started_at, tab, tab_seen, tab_opened_at FROM parser_runtime WHERE user_id = ?', [$uid]);
    return $row ?: ['pid' => 0, 'started_at' => 0, 'tab' => '', 'tab_seen' => 0, 'tab_opened_at' => 0];
}

/** Обновить состояние парсера. */
function parser_state_update(array $state, $uid = null): void {
    $uid = $uid ?: app_user_id();
    db_exec('INSERT OR REPLACE INTO parser_runtime (user_id, pid, started_at, tab, tab_seen, tab_opened_at) VALUES (?, ?, ?, ?, ?, ?)',
            [$uid, $state['pid'] ?? 0, $state['started_at'] ?? 0, $state['tab'] ?? '', $state['tab_seen'] ?? 0, $state['tab_opened_at'] ?? 0]);
}

// ---- Кэш отскриненных записей ----

/** Загрузить кэш отскриненных записей со скорингом. */
function screened_cache_load(): array {
    return db_all('SELECT url, topic, source, score, phash, at FROM screened_cache ORDER BY at DESC');
}
