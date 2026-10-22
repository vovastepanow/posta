<?php
// Вход через Telegram: сессии, бот для входа, логин-запросы.

// ---- Бот входа ----

/** ['token' => …, 'username' => …] или null — бот входа не задан. */
function telegram_login_bot() {
    $data = kv_get('telegram_login_bot');
    if (!is_array($data)) return null;
    if (isset($data['token_enc'])) $data['token'] = secret_decrypt($data['token_enc']);
    unset($data['token_enc']);
    return !empty($data['token']) ? $data : null;
}

function telegram_login_configured() { return telegram_login_bot() !== null; }

// ---- Сессии ----

/** Новая сессия: возвращает токен для куки (64 hex). В базе — только sha256 от него. */
function session_create($uid) {
    if (!is_valid_user_id($uid)) $uid = bin2hex(random_bytes(16));
    $token = bin2hex(random_bytes(32));
    $token_hash = hash('sha256', $token);
    $now = time();
    db_tx(function () use ($uid, $token_hash, $now) {
        db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, $now]);
        db_exec('INSERT INTO sessions (token_hash, user_id, created_at, last_seen) VALUES (?, ?, ?, ?)', [$token_hash, $uid, $now, $now]);
    });
    return $token;
}

/** id пользователя по токену из куки или '' (нет такой сессии). Не чаще SESSION_TOUCH_SECONDS обновляет last_seen. */
function session_user($token) {
    if (!is_string($token) || !preg_match('/^[a-f0-9]{64}$/', $token)) return '';
    $token_hash = hash('sha256', $token);
    $row = db_one('SELECT user_id, last_seen FROM sessions WHERE token_hash = ?', [$token_hash]);
    if (!$row) return '';
    $now = time();
    if ($now - (int)$row['last_seen'] >= SESSION_TOUCH_SECONDS) {
        db_exec('UPDATE sessions SET last_seen = ? WHERE token_hash = ?', [$now, $token_hash]);
    }
    return $row['user_id'];
}

function session_delete($token) {
    if (!is_string($token) || !preg_match('/^[a-f0-9]{64}$/', $token)) return;
    $token_hash = hash('sha256', $token);
    db_exec('DELETE FROM sessions WHERE token_hash = ?', [$token_hash]);
}

function session_delete_all($uid) {
    if (!is_valid_user_id($uid)) return;
    db_exec('DELETE FROM sessions WHERE user_id = ?', [$uid]);
}

// ---- Выбор пользователя из кук ----

/**
 * Кто делает запрос. $sid — кука SESSION_COOKIE, $legacy — старая кука USER_COOKIE (id пользователя).
 * Возвращает ['uid' => id, 'sid' => токен для куки или null (кука не меняется), 'drop_legacy' => bool].
 */
function session_resolve($sid, $legacy) {
    // 1. $sid — действующая сессия
    if (is_string($sid) && preg_match('/^[a-f0-9]{64}$/', $sid)) {
        $uid = session_user($sid);
        if ($uid !== '') {
            return ['uid' => $uid, 'sid' => null, 'drop_legacy' => $legacy !== ''];
        }
    }

    // 2. $legacy — корректный id анонимного пользователя
    if (is_valid_user_id($legacy)) {
        $user = db_one('SELECT id, telegram_id FROM users WHERE id = ?', [$legacy]);
        if ($user && $user['telegram_id'] === null) {
            $new_sid = session_create($legacy);
            return ['uid' => $legacy, 'sid' => $new_sid, 'drop_legacy' => true];
        }
    }

    // 3. Иначе — новый анонимный пользователь
    $uid = bin2hex(random_bytes(16));
    $new_sid = session_create($uid);
    return ['uid' => $uid, 'sid' => $new_sid, 'drop_legacy' => $legacy !== ''];
}

// ---- Установка куки сессии ----

function session_cookie_set($token) {
    if (PHP_SAPI === 'cli') return;
    $https = (!empty($_SERVER['HTTPS']) && $_SERVER['HTTPS'] !== 'off') || strtolower($_SERVER['HTTP_X_FORWARDED_PROTO'] ?? '') === 'https';
    if (!headers_sent()) {
        setcookie(SESSION_COOKIE, $token, [
            'expires' => time() + USER_COOKIE_DAYS * DAY_SECONDS,
            'secure' => $https,
            'path' => rtrim(dirname($_SERVER['SCRIPT_NAME'] ?? '/'), '/\\') . '/',
            'httponly' => true,
            'samesite' => 'Lax'
        ]);
    }
}

// ---- Вход через Telegram (бот по ссылке, без домена и вебхука) ----

/** ['url' => 'https://t.me/<бот>?start=login_<токен>', 'token' => <токен>]. Токен — bin2hex(random_bytes(16)). */
function login_start($uid) {
    if (!telegram_login_configured()) {
        throw new RuntimeException('Вход через Telegram не настроен на сервере.');
    }

    $now = time();
    $count = db_value(
        'SELECT COUNT(*) FROM login_requests WHERE user_id = ? AND created_at > ? AND status IN (?, ?)',
        [$uid, $now - LOGIN_REQUEST_TTL, 'pending', 'asked']
    );
    if ($count >= 5) {
        throw new DomainException('Слишком много попыток входа, подождите несколько минут');
    }

    $token = bin2hex(random_bytes(16));
    $token_hash = hash('sha256', $token);
    $bot = telegram_login_bot();
    $bot_username = $bot['username'];

    db_exec(
        'INSERT INTO login_requests (token_hash, user_id, created_at, status) VALUES (?, ?, ?, ?)',
        [$token_hash, $uid, $now, 'pending']
    );

    return [
        'url' => "https://t.me/{$bot_username}?start=login_{$token}",
        'token' => $token
    ];
}

/** Разбирает апдейт бота. Возвращает список вызовов API бота: [[метод, поля], …] — их выполнит login_poll_updates. */
function login_handle_update(array $update): array {
    $calls = [];

    // Обработка message
    if (isset($update['message'])) {
        $msg = $update['message'];
        $chat = $msg['chat'] ?? [];
        $from = $msg['from'] ?? [];
        $text = $msg['text'] ?? '';

        // Только личные чаты от не-ботов
        if ($chat['type'] === 'private' && empty($from['is_bot'])) {
            // Проверка /start login_<32 hex>
            if (preg_match('/^\/start login_([a-f0-9]{32})$/', $text, $m)) {
                $token = $m[1];
                $token_hash = hash('sha256', $token);
                $now = time();

                $req = db_one(
                    'SELECT token_hash FROM login_requests WHERE token_hash = ? AND status = ? AND created_at > ?',
                    [$token_hash, 'pending', $now - LOGIN_REQUEST_TTL]
                );

                if ($req) {
                    // Найден запрос
                    $tg_name = trim(($from['first_name'] ?? '') . ' ' . ($from['last_name'] ?? ''));
                    db_exec(
                        'UPDATE login_requests SET status = ?, chat_id = ?, telegram_id = ?, tg_username = ?, tg_name = ? WHERE token_hash = ?',
                        ['asked', $msg['chat']['id'], $from['id'], $from['username'] ?? '', $tg_name, $token_hash]
                    );

                    $calls[] = [
                        'sendMessage',
                        [
                            'chat_id' => $msg['chat']['id'],
                            'text' => "Войти на сайт Posta как {$tg_name}? Если вы сами не нажимали «Войти» на сайте — не подтверждайте.",
                            'reply_markup' => json_encode([
                                'inline_keyboard' => [[
                                    ['text' => 'Подтвердить вход', 'callback_data' => "login:{$token}"]
                                ]]
                            ])
                        ]
                    ];
                } else {
                    // Ссылка устарела или не найдена
                    $calls[] = [
                        'sendMessage',
                        [
                            'chat_id' => $msg['chat']['id'],
                            'text' => 'Ссылка для входа устарела. Нажмите «Войти через Telegram» на сайте ещё раз.'
                        ]
                    ];
                }
            } else {
                // Другой текст в личном чате
                $calls[] = [
                    'sendMessage',
                    [
                        'chat_id' => $msg['chat']['id'],
                        'text' => 'Я бот для входа на сайт Posta. Нажмите на сайте «Войти через Telegram».'
                    ]
                ];
            }
        }
    }

    // Обработка callback_query
    if (isset($update['callback_query'])) {
        $cq = $update['callback_query'];
        $data = $cq['data'] ?? '';
        $from = $cq['from'] ?? [];

        if (preg_match('/^login:([a-f0-9]{32})$/', $data, $m)) {
            $token = $m[1];
            $token_hash = hash('sha256', $token);
            $now = time();

            $req = db_one(
                'SELECT token_hash, telegram_id, chat_id FROM login_requests WHERE token_hash = ? AND status = ? AND created_at > ?',
                [$token_hash, 'asked', $now - LOGIN_REQUEST_TTL]
            );

            if ($req && (int)$req['telegram_id'] === (int)($from['id'] ?? 0)) {
                // Совпало
                db_exec('UPDATE login_requests SET status = ? WHERE token_hash = ?', ['confirmed', $token_hash]);

                $calls[] = ['answerCallbackQuery', ['callback_query_id' => $cq['id'], 'text' => 'Готово']];
                $calls[] = [
                    'editMessageText',
                    [
                        'chat_id' => $cq['message']['chat']['id'],
                        'message_id' => $cq['message']['message_id'],
                        'text' => 'Вход подтверждён. Вернитесь на сайт — страница обновится сама.'
                    ]
                ];
            } else {
                // Ссылка устарела или идентификатор не совпадает
                $calls[] = [
                    'answerCallbackQuery',
                    [
                        'callback_query_id' => $cq['id'],
                        'text' => 'Ссылка устарела, начните вход на сайте заново',
                        'show_alert' => true
                    ]
                ];
            }
        }
    }

    return $calls;
}

/** Забирает апдейты бота входа и отвечает на них. Неблокирующая блокировка: второй одновременный вызов просто выходит. */
function login_poll_updates() {
    $bot = telegram_login_bot();
    if (!$bot) return;

    $lock = try_lock(USERS_DIR . '/.login-bot.lock');
    if (!$lock) return;

    try {
        $offset = (int)kv_get('login_bot_offset', 0);
        try {
            $updates = telegram_bot_request($bot['token'], 'getUpdates', [
                'offset' => $offset,
                'timeout' => 0,
                'allowed_updates' => json_encode(['message', 'callback_query'])
            ], [], 10);
        } catch (RuntimeException $e) {
            $msg = $e->getMessage();
            if (str_contains($msg, '409')) {
                throw new RuntimeException($msg . ' — у бота включён вебхук — выключите его: deleteWebhook');
            }
            throw $e;
        }

        if (!is_array($updates)) return;

        foreach ($updates as $update) {
            $calls = login_handle_update($update);
            foreach ($calls as $call) {
                try {
                    telegram_bot_request($bot['token'], $call[0], $call[1] ?? []);
                } catch (RuntimeException $e) {
                    error_log('login_poll_updates: ' . $e->getMessage());
                }
            }
            kv_update('login_bot_offset', fn() => $update['update_id'] + 1);
        }
    } finally {
        flock($lock, LOCK_UN);
        fclose($lock);
    }
}

/** Для опроса страницы: 'pending' | 'done' | 'expired'. При 'done' пользователь уже переключён, кука сессии выставлена. */
function login_status($uid, $sid, $token) {
    login_poll_updates();

    if (!is_string($token) || !preg_match('/^[a-f0-9]{32}$/', $token)) {
        return 'expired';
    }

    $token_hash = hash('sha256', $token);
    $now = time();

    $req = db_one(
        'SELECT * FROM login_requests WHERE token_hash = ? AND user_id = ? AND created_at > ?',
        [$token_hash, $uid, $now - LOGIN_REQUEST_TTL]
    );

    if (!$req) return 'expired';

    if ($req['status'] === 'pending' || $req['status'] === 'asked') {
        return 'pending';
    }

    if ($req['status'] === 'confirmed') {
        $result = 'done';
        db_tx(function () use ($token_hash, $uid, $sid, $req) {
            db_exec('UPDATE login_requests SET status = ? WHERE token_hash = ?', ['used', $token_hash]);
            login_complete($uid, $sid, $req);
        });
        return $result;
    }

    return 'expired';
}

/** Привязывает Telegram к профилю или переключает на уже существующий. Возвращает id пользователя после входа. */
function login_complete($uid, $sid, array $request) {
    $tg_id = (int)$request['telegram_id'];
    $owner = db_value('SELECT id FROM users WHERE telegram_id = ?', [$tg_id]);

    if (!$owner) {
        // Привязать к текущему
        stop_content_parser();
        db_exec(
            'UPDATE users SET telegram_id = ?, tg_username = ?, tg_name = ? WHERE id = ?',
            [$tg_id, $request['tg_username'], $request['tg_name'], $uid]
        );
        session_delete($sid);
        session_cookie_set(session_create($uid));
        return $uid;
    }

    if ($owner === $uid) {
        // Обновить данные
        stop_content_parser();
        db_exec(
            'UPDATE users SET tg_username = ?, tg_name = ? WHERE id = ?',
            [$request['tg_username'], $request['tg_name'], $uid]
        );
        session_delete($sid);
        session_cookie_set(session_create($uid));
        return $uid;
    }

    // Вход в существующий аккаунт
    stop_content_parser();
    if (!user_is_empty($uid)) {
        user_merge($uid, $owner);
    } else {
        // Пустой пользователь: удалить его
        db_exec('DELETE FROM parser_runtime WHERE user_id = ?', [$uid]);
        db_exec('DELETE FROM sessions WHERE user_id = ?', [$uid]);
        db_exec('DELETE FROM users WHERE id = ?', [$uid]);
    }
    app_user_bind($owner);
    db_exec(
        'UPDATE users SET tg_username = ?, tg_name = ? WHERE id = ?',
        [$request['tg_username'], $request['tg_name'], $owner]
    );
    session_delete($sid);
    session_cookie_set(session_create($owner));
    return $owner;
}

// ---- Слияние профилей ----

/** Переносит в $into всё ценное из $from и удаляет $from. Всё в одной транзакции db_tx. */
function user_merge($from, $into) {
    if (!is_valid_user_id($from) || !is_valid_user_id($into)) return;
    if ($from === $into) return;

    db_tx(function () use ($from, $into) {
        // История: для каждой коллекции добавить записи из $from, которых нет в $into
        foreach (HISTORY_COLLECTIONS as $coll) {
            $from_history = history_load($from);
            $into_history = history_load($into);

            // Get image keys for comparison
            $into_images = array_map(fn($i) => content_image_key($i), $into_history[$coll] ?? []);

            $new_items = [];
            foreach ($from_history[$coll] ?? [] as $item) {
                $key = content_image_key($item);
                if ($key !== '' && !in_array($key, $into_images, true)) {
                    $new_items[] = $item;
                }
            }
            if ($new_items) {
                // history_update ждёт историю целиком по коллекциям: голый список стёр бы всю историю $into
                history_update(fn($h) => [$coll => [...($h[$coll] ?? []), ...$new_items]] + $h, $into);
            }
        }

        // Пул: остаётся пул $into; если он пустой — взять пул $from
        $into_pool = pool_load($into);
        if (empty($into_pool)) {
            $from_pool = pool_load($from);
            pool_update(fn() => $from_pool, $into);
        }

        // Каналы: старые каналы обоих профилей переносятся в таблицу; канал, чей chat_id у $into уже есть, у $from удаляем; новые — в конец списка $into
        channels_load($from);
        channels_load($into);
        db_exec('DELETE FROM channels WHERE user_id = ? AND chat_id IN (SELECT chat_id FROM channels WHERE user_id = ?)', [$from, $into]);
        $next = (int)db_value('SELECT COALESCE(MAX(position), -1) + 1 FROM channels WHERE user_id = ?', [$into]);
        foreach (db_all('SELECT id FROM channels WHERE user_id = ? ORDER BY position, connected_at', [$from]) as $ch) {
            db_exec('UPDATE channels SET user_id = ?, position = ? WHERE id = ?', [$into, $next++, $ch['id']]);
        }

        // Соцсети: ключи $into не трогать, недостающие ключи взять из $from
        $from_conn = connections_load($from);
        $into_conn = connections_load($into);
        $merged_conn = $into_conn;
        foreach ($from_conn as $key => $value) {
            if (!isset($merged_conn[$key])) {
                $merged_conn[$key] = $value;
            }
        }
        connections_update(fn() => $merged_conn, $into);

        // Деньги и учёт
        db_exec('UPDATE ledger SET user_id = ? WHERE user_id = ?', [$into, $from]);
        db_exec('UPDATE post_usage SET user_id = ? WHERE user_id = ?', [$into, $from]);
        db_exec('UPDATE outbox SET user_id = ? WHERE user_id = ?', [$into, $from]);
        db_exec('UPDATE published SET user_id = ? WHERE user_id = ?', [$into, $from]);
        db_exec('UPDATE ledger SET related_user = ? WHERE related_user = ?', [$into, $from]);

        // Тариф
        $from_sub = db_one('SELECT until FROM subscriptions WHERE user_id = ?', [$from]);
        $into_sub = db_one('SELECT until FROM subscriptions WHERE user_id = ?', [$into]);
        $from_until = (int)($from_sub['until'] ?? 0);
        $into_until = (int)($into_sub['until'] ?? 0);
        if ($from_until > $into_until) {
            db_exec(
                'INSERT OR REPLACE INTO subscriptions (user_id, plan, channels, posts_per_day, price, started_at, until)
                 SELECT ?, plan, channels, posts_per_day, price, started_at, until FROM subscriptions WHERE user_id = ?',
                [$into, $from]
            );
        }

        // Приглашения
        db_exec('UPDATE users SET referred_by = ? WHERE referred_by = ? AND id <> ?', [$into, $from, $into]);
        if (!db_value('SELECT referred_by FROM users WHERE id = ?', [$into])) {
            $from_referrer = db_value('SELECT referred_by FROM users WHERE id = ?', [$from]);
            if ($from_referrer && $from_referrer !== $into) {
                db_exec('UPDATE users SET referred_by = ? WHERE id = ?', [$from_referrer, $into]);
            }
        }

        // Удалить
        db_exec('DELETE FROM users WHERE id = ?', [$from]);
        if (is_valid_user_id($from)) remove_dir(USERS_DIR . '/' . $from);
    });
}
