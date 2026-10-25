<?php
// Функции доступа к данным SQLite: пользователи, пул, история, очередь, каталог, kv.

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

/** Когда последний раз сверялись с источником: proposals — «Предложка», threads — треды Двача в пуле. 0 — не сверялись. */
function user_checked_at($what, $uid = null): int {
    return (int)db_value('SELECT ' . ($what === 'threads' ? 'threads_checked_at' : 'proposals_checked_at') . ' FROM users WHERE id = ?', [$uid ?: app_user_id()]);
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
        // Картинка уже есть в истории (повторный голос, гонка двух вкладок): остаётся только последняя оценка
        if ($key !== '') db_exec('DELETE FROM history_items WHERE user_id = ? AND image_key = ?', [$uid, $key]);
        $hidden = (!empty($record['proposal_hidden']) ? 1 : 0);
        db_exec('INSERT INTO history_items (user_id, collection, position, image_key, proposal_hidden, data)
                VALUES (?, ?, ?, ?, ?, ?)',
               [$uid, $collection, $pos, $key, $hidden, db_json($record)]);
    });
}

/** Убрать из истории повторы одной картинки (по image_key): остаётся запись с самой поздней оценкой (timestamp, затем id). Возвращает число удалённых. */
function history_dedupe($uid = null, bool $dry_run = false): int {
    $uid = $uid ?: app_user_id();
    $best = [];
    $drop = [];
    foreach (db_all("SELECT id, image_key, COALESCE(json_extract(data, '$.timestamp'), 0) AS ts FROM history_items WHERE user_id = ? AND image_key <> ''", [$uid]) as $r) {
        $keep = $best[$r['image_key']] ?? null;
        if ($keep === null) { $best[$r['image_key']] = $r; continue; }
        $newer = [(int)$r['ts'], (int)$r['id']] > [(int)$keep['ts'], (int)$keep['id']];
        $drop[] = ($newer ? $keep : $r)['id'];
        if ($newer) $best[$r['image_key']] = $r;
    }
    if (!$dry_run) foreach (array_chunk($drop, 200) as $chunk) db_exec('DELETE FROM history_items WHERE id IN (' . implode(',', array_map('intval', $chunk)) . ')');
    return count($drop);
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

/** Подключения пользователя (Telegram, VK) из users.connections. */
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

// ---- Каналы (несколько Telegram-каналов на пользователя) ----

/**
 * Секреты ресурса: расшифрованный JSON из secrets_enc; у строк до миграции 16 — токен из token_enc.
 * Битые или пустые секреты — пустой массив.
 */
function channel_secrets_from_row(array $row): array {
    $aad = fn($field) => channel_secret_aad($row['user_id'] ?? '', $row['id'] ?? '', $row['platform'] ?? 'telegram', $field);
    if (($row['secrets_enc'] ?? '') !== '' && is_string($plain = secret_decrypt($row['secrets_enc'], $aad('secrets'))) && is_array($json = json_decode($plain, true))) return $json;
    $token = secret_decrypt($row['token_enc'] ?? '', $aad('token'));
    return $token !== null && $token !== '' ? ['token' => $token] : [];
}

/** Миграция 16: токен Telegram из token_enc — в secrets_enc (зашифрованный JSON). Вызывается из db_migrate внутри её транзакции. */
function channels_backfill_secrets(PDO $pdo): void {
    foreach ($pdo->query("SELECT id, token_enc FROM channels WHERE secrets_enc = '' AND token_enc <> ''")->fetchAll(PDO::FETCH_ASSOC) as $row) {
        $token = secret_decrypt($row['token_enc']);
        if ($token === null || $token === '') continue;
        $pdo->prepare('UPDATE channels SET secrets_enc = ? WHERE id = ?')->execute([secret_encrypt(json_encode(['token' => $token])), $row['id']]);
    }
}

/**
 * Перешифровать секреты ресурсов в формат с привязкой (AAD) — для существующих строк, расшифровавшихся по-старому.
 * Сухой прогон ($apply = false) только считает. Возвращает число перешифрованных строк. Живой базе — только по решению владельца.
 */
function channels_reseal_secrets(bool $apply = false): int {
    $n = 0;
    foreach (db_all("SELECT id, user_id, platform, secrets_enc, token_enc FROM channels") as $row) {
        $set = [];
        foreach (['secrets_enc' => 'secrets', 'token_enc' => 'token'] as $col => $field) {
            if ($row[$col] === '' || str_starts_with($row[$col], '2.') || ($plain = secret_decrypt($row[$col])) === null) continue;
            $set[$col] = secret_encrypt($plain, channel_secret_aad($row['user_id'], $row['id'], $row['platform'], $field));
        }
        if (!$set) continue;
        $n++;
        if ($apply) db_exec('UPDATE channels SET ' . implode(', ', array_map(fn($k) => "$k = ?", array_keys($set))) . ' WHERE id = ?', [...array_values($set), $row['id']]);
    }
    return $n;
}

/** Передать ресурс другому пользователю (слияние профилей): секреты перешифровываются под нового владельца. */
function channel_move_to_user(string $channel_id, string $from, string $into, int $position): void {
    $row = db_one('SELECT id, user_id, platform, secrets_enc, token_enc FROM channels WHERE id = ? AND user_id = ?', [$channel_id, $from]);
    if (!$row) return;
    $set = ['user_id' => $into, 'position' => $position];
    foreach (['secrets_enc' => 'secrets', 'token_enc' => 'token'] as $col => $field) {
        if ($row[$col] === '') continue;
        $plain = secret_decrypt($row[$col], channel_secret_aad($from, $channel_id, $row['platform'], $field));
        if ($plain !== null) $set[$col] = secret_encrypt($plain, channel_secret_aad($into, $channel_id, $row['platform'], $field));
    }
    db_exec('UPDATE channels SET ' . implode(', ', array_map(fn($k) => "$k = ?", array_keys($set))) . ' WHERE id = ?', [...array_values($set), $channel_id]);
}

/** Строка таблицы channels → канал: токен расшифрован в поле 'token'; secrets — все секреты ресурса (не отдавать наружу). */
function channel_from_row(array $row): array {
    $c = ['id' => $row['id'], 'user_id' => $row['user_id'] ?? '', 'platform' => $row['platform'], 'ref' => (string)($row['ref'] ?? ''), 'meta' => db_unjson($row['meta'] ?? '', []), 'position' => (int)$row['position'], 'bot_id' => (int)$row['bot_id'], 'bot_username' => $row['bot_username'],
        'chat_id' => (int)$row['chat_id'], 'chat_title' => $row['chat_title'], 'chat_username' => $row['chat_username'], 'chat_type' => $row['chat_type'], 'connected_at' => (int)$row['connected_at'],
        'schedule' => db_unjson($row['schedule'] ?? '', []), 'style' => db_unjson($row['style'] ?? '', []),
        'hashtags' => in_array($row['hashtags'] ?? '', CHANNEL_HASHTAG_MODES, true) ? $row['hashtags'] : 'off'];
    $c['secrets'] = channel_secrets_from_row($row);
    if ($c['platform'] === 'telegram' && ($c['secrets']['token'] ?? '') !== '') $c['token'] = $c['secrets']['token'];
    return $c;
}

/** Каналы пользователя по position. Пока таблица пуста, переносит старый канал из connections.telegram. */
function channels_load($uid = null): array {
    $uid = $uid ?: app_user_id();
    if (!db_value('SELECT 1 FROM channels WHERE user_id = ? LIMIT 1', [$uid])) channels_import_legacy($uid);
    return array_map('channel_from_row', db_all('SELECT * FROM channels WHERE user_id = ? ORDER BY position, connected_at', [$uid]));
}

/** Только каналы Telegram по position: остальные соцсети на эти функции не смотрят. */
function telegram_channels_load($uid = null): array {
    return array_values(array_filter(channels_load($uid), fn($c) => $c['platform'] === 'telegram'));
}

/** Канал пользователя по id или null (чужой id — тоже null). */
function channel_get($channel_id, $uid = null): ?array {
    $uid = $uid ?: app_user_id();
    $row = db_one('SELECT * FROM channels WHERE id = ? AND user_id = ?', [(string)$channel_id, $uid]);
    return $row ? channel_from_row($row) : null;
}

/**
 * Добавить или обновить (по chat_id) канал; возвращает его id. Новый канал встаёт в конец списка,
 * если не задан position (0 — первым). Токен передаётся открытым и хранится зашифрованным.
 */
function channel_save(array $channel, $uid = null): string {
    $uid = $uid ?: app_user_id();
    return db_tx(function () use ($channel, $uid) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
        $platform = (string)($channel['platform'] ?? 'telegram');
        $ref = $platform === 'telegram' ? (string)(int)($channel['chat_id'] ?? 0) : (string)($channel['ref'] ?? '');
        $secrets = $channel['secrets'] ?? (isset($channel['token']) ? ['token' => (string)$channel['token']] : []);
        // id нужен до вставки: секреты привязаны к записи (AAD)
        $existing = db_value('SELECT id FROM channels WHERE user_id = ? AND platform = ? AND ref = ?', [$uid, $platform, $ref]);
        $id = $existing ?: 'c' . bin2hex(random_bytes(5));
        $fields = ['platform' => $platform, 'ref' => $ref, 'secrets_enc' => secret_encrypt(json_encode($secrets, JSON_UNESCAPED_UNICODE), channel_secret_aad($uid, $id, $platform, 'secrets')),
            'meta' => json_encode($channel['meta'] ?? [], JSON_UNESCAPED_UNICODE),
            'token_enc' => secret_encrypt((string)($channel['token'] ?? ''), channel_secret_aad($uid, $id, $platform, 'token')), 'bot_id' => (int)($channel['bot_id'] ?? 0), 'bot_username' => (string)($channel['bot_username'] ?? ''),
            'chat_id' => (int)($channel['chat_id'] ?? 0), 'chat_title' => (string)($channel['chat_title'] ?? ''), 'chat_username' => (string)($channel['chat_username'] ?? ''), 'chat_type' => (string)($channel['chat_type'] ?? ''),
            'connected_at' => (int)($channel['connected_at'] ?? time())];
        if ($existing) {
            db_exec('UPDATE channels SET ' . implode(', ', array_map(fn($k) => "$k = ?", array_keys($fields))) . ' WHERE id = ?', [...array_values($fields), $id]);
            return $id;
        }
        $position = isset($channel['position']) ? (int)$channel['position'] : (int)db_value('SELECT COALESCE(MAX(position), -1) + 1 FROM channels WHERE user_id = ?', [$uid]);
        db_exec('INSERT INTO channels (id, user_id, position, ' . implode(', ', array_keys($fields)) . ') VALUES (?, ?, ?, ' . implode(', ', array_fill(0, count($fields), '?')) . ')',
            [$id, $uid, $position, ...array_values($fields)]);
        return $id;
    });
}

/** Удалить канал пользователя. Старый канал из connections не возвращается: см. channels_import_legacy. */
function channel_delete($channel_id, $uid = null): bool {
    $uid = $uid ?: app_user_id();
    kv_update('channels_legacy_done:' . $uid, fn() => 1);
    return db_exec('DELETE FROM channels WHERE id = ? AND user_id = ?', [(string)$channel_id, $uid]) > 0;
}

/** Сделать канал первым (каналом по умолчанию): остальные сохраняют порядок. */
function channel_make_default($channel_id, $uid = null): void {
    $uid = $uid ?: app_user_id();
    db_tx(function () use ($channel_id, $uid) {
        $ids = array_column(db_all('SELECT id FROM channels WHERE user_id = ? ORDER BY position, connected_at', [$uid]), 'id');
        if (!in_array((string)$channel_id, $ids, true)) throw new InvalidArgumentException('Такого канала нет.');
        foreach ([(string)$channel_id, ...array_values(array_diff($ids, [(string)$channel_id]))] as $i => $id) {
            db_exec('UPDATE channels SET position = ? WHERE id = ?', [$i, $id]);
        }
    });
}

/**
 * Старый канал из connections.telegram — в channels первым (position 0), остальные сдвигаются. Повторно не переносит:
 * после переноса или удаления отметка в kv. true — перенесли сейчас.
 */
function channels_import_legacy($uid = null): bool {
    $uid = $uid ?: app_user_id();
    $t = connections_load($uid)['telegram'] ?? null;
    if (!is_array($t) || empty($t['chat_id']) || empty($t['token_enc']) || kv_get('channels_legacy_done:' . $uid)) return false;
    if (($token = secret_decrypt($t['token_enc'])) === null || $token === '') return false;
    return db_tx(function () use ($uid, $t, $token) {
        if (db_value('SELECT 1 FROM channels WHERE user_id = ? AND chat_id = ?', [$uid, (int)$t['chat_id']])) return false;
        db_exec('UPDATE channels SET position = position + 1 WHERE user_id = ?', [$uid]);
        channel_save(['token' => $token, 'position' => 0, 'bot_id' => $t['bot_id'] ?? 0, 'bot_username' => $t['bot_username'] ?? '', 'chat_id' => $t['chat_id'],
            'chat_title' => $t['chat_title'] ?? '', 'chat_username' => $t['chat_username'] ?? '', 'chat_type' => $t['chat_type'] ?? '',
            'connected_at' => $t['connected_at'] ?? time()], $uid);
        kv_update('channels_legacy_done:' . $uid, fn() => 1);
        return true;
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
    return db_all('SELECT url, topic, source, score, phash, at FROM screened_cache WHERE ver = ? ORDER BY at DESC', [SCREENED_CACHE_VERSION]);
}
