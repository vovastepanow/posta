<?php
// Радар лидов: настройки, запуск, блокировка параллельных проходов

require_once __DIR__ . '/ollama_classifier.php';
require_once __DIR__ . '/telegram.php';

/**
 * Загрузить настройки радара пользователя из kv
 */
function lead_radar_settings($uid = null) {
    $uid = $uid ?: app_user_id();
    return kv_get("lead_radar:$uid", lead_radar_settings_defaults());
}

function lead_radar_settings_defaults(): array {
    return ['enabled' => false, 'chats' => [], 'last_ids' => [], 'last_run' => null, 'last_seen' => 0, 'last_error' => null, 'resolved' => [],
        'queries' => [], 'chat_info' => []];
}

/**
 * Сохранить настройки радара
 */
function lead_radar_save_settings(array $settings, $uid = null) {
    $uid = $uid ?: app_user_id();
    kv_update("lead_radar:$uid", fn() => $settings);
}

/**
 * Валидировать и нормализировать список чатов
 */
function lead_radar_validate_chats($input) {
    $lines = array_filter(array_map('trim', explode("\n", (string)$input)), 'strlen');
    $chats = [];
    foreach (array_slice($lines, 0, 50) as $line) {
        $line = str_replace(['https://t.me/', 't.me/'], '@', $line);
        if (preg_match('/^@[A-Za-z0-9_]{4,32}$/', $line) || preg_match('/^-100\d+$/', $line)) {
            if (!in_array($line, $chats, true)) {
                $chats[] = $line;
            }
        }
    }
    return $chats;
}

/**
 * Получить информацию о статусе радара (connected, enabled, chats, last_run, last_seen, last_error)
 */
function lead_radar_status($uid = null) {
    $uid = $uid ?: app_user_id();
    $settings = lead_radar_settings($uid);
    $last_run = $settings['last_run'] ? date('c', $settings['last_run']) : null;

    return [
        'connected' => telegram_user_connection($uid) !== null,
        'enabled' => (bool)$settings['enabled'],
        'chats' => count($settings['chats'] ?? []),
        'last_run' => $last_run,
        'last_seen' => (int)($settings['last_seen'] ?? 0),
        'last_error' => $settings['last_error']
    ];
}

/** Файлы прохода: блокировка и журнал в users/ (наружу закрыт; общие для cron и админки, в отличие от приватной /tmp Apache). */
function lead_radar_lock_path($uid) { return USERS_DIR . '/.radar-' . $uid . '.lock'; }
function lead_radar_log_path($uid) { return USERS_DIR . '/.radar-' . $uid . '.log'; }

/** Идёт ли сейчас проход: неблокирующая попытка взять ту же блокировку. */
function lead_radar_is_running($uid): bool {
    users_ensure_root();
    $lock = fopen(lead_radar_lock_path($uid), 'c');
    $free = flock($lock, LOCK_EX | LOCK_NB);
    if ($free) flock($lock, LOCK_UN);
    fclose($lock);
    return !$free;
}

/** Сохранить итоги прохода поверх свежих настроек (владелец мог изменить чаты, пока шёл проход). */
function lead_radar_save_run(string $uid, array $changes) {
    kv_update("lead_radar:$uid", fn($s) => array_replace((array)$s + lead_radar_settings_defaults(), $changes), lead_radar_settings_defaults());
}

/**
 * Основной проход радара: читает чаты, добавляет в очередь, классифицирует. $log получает по строке на шаг.
 * Возвращает ['seen' => n, 'queued' => n, 'classified' => n, 'leads' => n, 'errors' => [...]] или ['skipped' => '...'] / ['error' => '...']
 */
function lead_radar_run($uid, ?callable $log = null) {
    $say = fn($line) => $log ? $log($line) : null;
    users_ensure_root();
    $lock = fopen(lead_radar_lock_path($uid), 'c');

    if (!flock($lock, LOCK_EX | LOCK_NB)) {
        fclose($lock);
        return ['skipped' => 'уже идёт'];
    }

    try {
        $settings = lead_radar_settings($uid);
        $pdo = db();

        // Проверки
        $stop = !$settings['enabled'] ? 'Радар выключен' : (empty($settings['chats']) ? 'Нет чатов в списке' : (!telegram_user_connection($uid) ? 'Аккаунт Telegram не подключён' : null));
        if ($stop) {
            lead_radar_save_run($uid, ['last_error' => $stop]);
            $say("Остановлено: $stop");
            return ['error' => $stop];
        }

        // last_run пишем до чтения: автозапуск не стартует второй проход
        lead_radar_save_run($uid, ['last_run' => time()]);

        $result = ['seen' => 0, 'queued' => 0, 'classified' => 0, 'leads' => 0, 'errors' => []];

        // Чтение чатов
        $say('Читаю ' . count($settings['chats']) . ' ' . lead_radar_plural(count($settings['chats']), ['чат', 'чата', 'чатов']) . '…');
        $chats = array_map(fn($ref) => ['ref' => $ref, 'min_id' => $settings['last_ids'][$ref] ?? 0], $settings['chats']);

        try {
            $tg_result = telegram_user_read_chats($chats, $uid);
        } catch (Exception $e) {
            lead_radar_save_run($uid, ['last_error' => $e->getMessage()]);
            $say('Ошибка Telegram: ' . $e->getMessage());
            return ['error' => $e->getMessage()];
        }

        // Обновляем last_ids, resolved и то, что запомнили о чатах
        $new_last_ids = $settings['last_ids'] ?? [];
        $resolved = $settings['resolved'] ?? [];
        $chat_info = $settings['chat_info'] ?? [];

        foreach ((array)($tg_result['chats'] ?? []) as $chat_data) {
            $ref = $chat_data['ref'];
            $count = count($chat_data['messages'] ?? []);
            $chat_info[$ref] = ['title' => (string)($chat_data['title'] ?? ''), 'error' => $chat_data['error'] ?: null] + ($chat_info[$ref] ?? []);
            if (!empty($chat_data['error'])) {
                $result['errors'][] = "$ref: {$chat_data['error']}";
                $say("$ref: ошибка — {$chat_data['error']}");
            } else {
                $say("$ref: $count " . lead_radar_plural($count, ['новое сообщение', 'новых сообщения', 'новых сообщений']));
            }

            if (!empty($chat_data['messages'])) {
                $result['seen'] += $count;
                $new_last_ids[$ref] = max(array_column($chat_data['messages'], 'id'));
            }

            if (isset($chat_data['chat_id'])) {
                $resolved[$ref] = $chat_data['chat_id'];
            }
        }

        // Обработка сообщений: дешёвый отсев и добавление в очередь
        $skipped = 0;
        foreach ((array)($tg_result['chats'] ?? []) as $chat_data) {
            foreach ((array)($chat_data['messages'] ?? []) as $msg) {
                if (!lead_prefilter($msg['text'])) { $skipped++; continue; }

                $result['queued']++;
                $pdo->prepare("
                    INSERT OR IGNORE INTO telegram_lead_queue
                    (user_id, chat_id, message, message_id, status, chat_ref, chat_title,
                     chat_username, sender_id, sender_username, sender_name, message_date)
                    VALUES (?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?, ?, ?)
                ")->execute([
                    $uid,
                    $chat_data['chat_id'] ?? 0,
                    $msg['text'],
                    $msg['id'],
                    $chat_data['ref'],
                    $chat_data['title'] ?? '',
                    $chat_data['username'] ?? '',
                    $msg['sender_id'] ?? 0,
                    $msg['sender_username'] ?? '',
                    $msg['sender_name'] ?? '',
                    date('c', $msg['date'] ?? time())
                ]);
            }
        }
        $say("Отсеяно без модели: $skipped, в очередь: {$result['queued']}");

        // Уборка очереди: старые processing, failed < 3 attempts
        $pdo->prepare("
            UPDATE telegram_lead_queue
            SET status = 'pending'
            WHERE status = 'processing' AND datetime(created_at) < datetime('now', '-15 minutes')
                AND user_id = ?
        ")->execute([$uid]);

        $pdo->prepare("
            UPDATE telegram_lead_queue
            SET status = 'pending'
            WHERE status = 'failed' AND attempts < 3 AND user_id = ?
        ")->execute([$uid]);

        // Классификация до LEAD_BATCH сообщений (LEAD_BATCH_OVERRIDE в окружении уменьшает пачку — для проверок)
        $batch = min(LEAD_BATCH, max(1, (int)(getenv('LEAD_BATCH_OVERRIDE') ?: LEAD_BATCH)));
        $pending = $pdo->prepare("
            SELECT id, message, chat_id, message_id, chat_ref, chat_title, chat_username,
                   sender_id, sender_username, sender_name, message_date
            FROM telegram_lead_queue
            WHERE status = 'pending' AND user_id = ?
            LIMIT ?
        ");
        $pending->execute([$uid, $batch]);
        $queue_items = $pending->fetchAll(PDO::FETCH_ASSOC);

        foreach ($queue_items as $item) {
            $preview = mb_substr(preg_replace('/\s+/u', ' ', $item['message']), 0, 80);
            try {
                $pdo->prepare("UPDATE telegram_lead_queue SET status = 'processing' WHERE id = ?")->execute([$item['id']]);

                if (ua_filter_on() && text_is_ukrainian((string)$item['message'])) {   // украинский текст — до модели не отдаём (UA_FILTER_PROMPT.md, B9)
                    $pdo->prepare("UPDATE telegram_lead_queue SET status = 'processed', ollama_result = ?, ollama_reason = ?, processed_at = CURRENT_TIMESTAMP WHERE id = ?")
                        ->execute([0, 'украинский текст: фильтр Украины', $item['id']]);
                    $say("Пропуск: украинский текст — «{$preview}»");
                    continue;
                }

                $started = microtime(true);
                $clf = lead_classify($item['message']);
                $seconds = (int)round(microtime(true) - $started);
                $result['classified']++;
                $say("Модель ($seconds с): " . ($clf['lead'] === 1 ? 'лид' : 'не лид') . ' — ' . $clf['reason'] . " · «{$preview}»");

                if ($clf['lead'] === 1) {
                    $result['leads']++;
                    $msg_url = null;
                    if ($item['chat_username']) {
                        $msg_url = "https://t.me/{$item['chat_username']}/{$item['message_id']}";
                    }

                    $pdo->prepare("
                        INSERT OR IGNORE INTO telegram_leads
                        (queue_id, user_id, chat_id, message, message_id, lead_status,
                         chat_title, sender_id, sender_name, telegram_username, message_url, reason)
                        VALUES (?, ?, ?, ?, ?, 'new', ?, ?, ?, ?, ?, ?)
                    ")->execute([
                        $item['id'],
                        $uid,
                        $item['chat_id'],
                        $item['message'],
                        $item['message_id'],
                        $item['chat_title'],
                        $item['sender_id'],
                        $item['sender_name'],
                        $item['sender_username'],
                        $msg_url,
                        $clf['reason']
                    ]);
                }

                $pdo->prepare("UPDATE telegram_lead_queue SET status = 'processed', ollama_result = ?, ollama_reason = ?, processed_at = CURRENT_TIMESTAMP WHERE id = ?")
                    ->execute([$clf['lead'], $clf['reason'], $item['id']]);
            } catch (Exception $e) {
                $pdo->prepare("UPDATE telegram_lead_queue SET status = 'failed', attempts = attempts + 1, ollama_error = ? WHERE id = ?")
                    ->execute([mb_substr($e->getMessage(), 0, 300), $item['id']]);
                $result['errors'][] = 'Классификация: ' . $e->getMessage();
                $say('Модель: ошибка — ' . $e->getMessage() . " · «{$preview}»");
            }
        }

        // Итоги прохода — поверх свежих настроек
        lead_radar_save_run($uid, ['last_ids' => $new_last_ids, 'resolved' => $resolved, 'chat_info' => $chat_info, 'last_seen' => $result['seen'],
            'last_run' => time(), 'last_error' => empty($result['errors']) ? null : implode('; ', $result['errors'])]);

        $say("Готово: просмотрено {$result['seen']}, лидов {$result['leads']}");
        return $result;
    } finally {
        flock($lock, LOCK_UN);
        fclose($lock);
    }
}

/** Русское склонение: plural(5, ['чат', 'чата', 'чатов']). */
function lead_radar_plural($n, array $forms): string {
    $n = abs($n) % 100;
    $d = $n % 10;
    return $forms[$n > 10 && $n < 20 ? 2 : ($d === 1 ? 0 : ($d >= 2 && $d <= 4 ? 1 : 2))];
}
