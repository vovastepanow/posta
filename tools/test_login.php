<?php
// Проверка входа через Telegram без реального Telegram
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

if (MEDIA_CACHE_DIR === '/var/lib/posta/media') exit("Тест запущен с настоящей папкой медиа: задайте POSTA_MEDIA_CACHE_DIR\n");

$failures = 0;
function ok($msg) { echo "OK $msg\n"; }
function fail($msg) { global $failures; $failures++; echo "FAIL $msg\n"; }

echo "Тестирование входа через Telegram\n";

// 1. Новый вход: /start → sendMessage → callback_query → confirmed
{
    echo "\n1. Новый вход:\n";
    $a = bin2hex(random_bytes(16));
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$a, time()]);

    $token = bin2hex(random_bytes(16));
    $token_hash = hash('sha256', $token);
    $now = time();
    db_exec(
        'INSERT INTO login_requests (token_hash, user_id, created_at, status) VALUES (?, ?, ?, ?)',
        [$token_hash, $a, $now, 'pending']
    );

    // Имитируем /start от пользователя
    $update = ['message' => [
        'chat' => ['type' => 'private', 'id' => 111],
        'from' => ['id' => 111, 'is_bot' => false, 'first_name' => 'Test', 'last_name' => 'User', 'username' => 'testuser'],
        'text' => "/start login_{$token}"
    ]];

    $calls = login_handle_update($update);
    count($calls) === 1 && $calls[0][0] === 'sendMessage' ? ok('ответ sendMessage') : fail('неверный ответ на /start');

    // Проверяем, что статус изменился на asked
    $status = db_value('SELECT status FROM login_requests WHERE token_hash = ?', [$token_hash]);
    $status === 'asked' ? ok('статус changed to asked') : fail("статус $status вместо asked");

    // Callback от того же пользователя
    $chat_id = db_value('SELECT chat_id FROM login_requests WHERE token_hash = ?', [$token_hash]);
    $update2 = ['callback_query' => [
        'id' => 'cq1',
        'from' => ['id' => 111],
        'data' => "login:{$token}",
        'message' => ['chat' => ['id' => $chat_id], 'message_id' => 1]
    ]];

    $calls2 = login_handle_update($update2);
    count($calls2) === 2 && $calls2[0][0] === 'answerCallbackQuery' ? ok('callback ответ') : fail('неверный ответ на callback');

    $status = db_value('SELECT status FROM login_requests WHERE token_hash = ?', [$token_hash]);
    $status === 'confirmed' ? ok('статус confirmed') : fail("статус $status вместо confirmed");

    // Завершаем вход
    $req = db_one('SELECT * FROM login_requests WHERE token_hash = ?', [$token_hash]);
    app_user_bind($a);
    login_complete($a, '', $req);

    $user = db_one('SELECT telegram_id, tg_username FROM users WHERE id = ?', [$a]);
    $user['telegram_id'] == 111 ? ok('telegram_id привязан') : fail('telegram_id не привязан');

    db_exec('DELETE FROM login_requests WHERE user_id = ?', [$a]);
    db_exec('DELETE FROM sessions WHERE user_id = ?', [$a]);
    db_exec('DELETE FROM users WHERE id = ?', [$a]);
}

// 2. Чужая кнопка: callback от другого пользователя отклоняется
{
    echo "\n2. Чужая кнопка:\n";
    $b = bin2hex(random_bytes(16));
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$b, time()]);

    $token = bin2hex(random_bytes(16));
    $token_hash = hash('sha256', $token);
    db_exec(
        'INSERT INTO login_requests (token_hash, user_id, created_at, status, chat_id, telegram_id) VALUES (?, ?, ?, ?, ?, ?)',
        [$token_hash, $b, time(), 'asked', 222, 222]
    );

    // Callback от другого пользователя
    $update = ['callback_query' => [
        'id' => 'cq2',
        'from' => ['id' => 333],  // ДРУГОЙ ID
        'data' => "login:{$token}",
        'message' => ['chat' => ['id' => 222], 'message_id' => 1]
    ]];

    $calls = login_handle_update($update);
    count($calls) === 1 && $calls[0][0] === 'answerCallbackQuery' && !empty($calls[0][1]['show_alert'])
        ? ok('чужой callback отклонён') : fail('чужой callback не отклонён');

    $status = db_value('SELECT status FROM login_requests WHERE token_hash = ?', [$token_hash]);
    $status !== 'confirmed' ? ok('статус не changed') : fail('статус неправильно changed');

    db_exec('DELETE FROM login_requests WHERE user_id = ?', [$b]);
    db_exec('DELETE FROM sessions WHERE user_id = ?', [$b]);
    db_exec('DELETE FROM users WHERE id = ?', [$b]);
}

// 3. Просроченный токен
{
    echo "\n3. Просроченный токен:\n";
    $c = bin2hex(random_bytes(16));
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$c, time()]);

    $old_token = bin2hex(random_bytes(16));
    $old_token_hash = hash('sha256', $old_token);
    db_exec(
        'INSERT INTO login_requests (token_hash, user_id, created_at, status) VALUES (?, ?, ?, ?)',
        [$old_token_hash, $c, time() - 700, 'pending']  // 700 секунд назад
    );

    $update = ['message' => [
        'chat' => ['type' => 'private', 'id' => 444],
        'from' => ['id' => 444, 'is_bot' => false, 'first_name' => 'Test3'],
        'text' => "/start login_{$old_token}"
    ]];

    $calls = login_handle_update($update);
    count($calls) === 1 && str_contains($calls[0][1]['text'], 'устарела')
        ? ok('просроченный токен отклонён') : fail('просроченный токен не отклонён');

    db_exec('DELETE FROM login_requests WHERE user_id = ?', [$c]);
    db_exec('DELETE FROM sessions WHERE user_id = ?', [$c]);
    db_exec('DELETE FROM users WHERE id = ?', [$c]);
}

// 4. Чужой токен в login_status
{
    echo "\n4. Чужой токен:\n";
    $d = bin2hex(random_bytes(16));
    $e = bin2hex(random_bytes(16));
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$d, time()]);
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$e, time()]);

    $token = bin2hex(random_bytes(16));
    $token_hash = hash('sha256', $token);
    db_exec(
        'INSERT INTO login_requests (token_hash, user_id, created_at, status) VALUES (?, ?, ?, ?)',
        [$token_hash, $d, time(), 'pending']
    );

    // e пытается использовать токен от d
    $r = login_status($e, '', $token);
    $r === 'expired' ? ok('чужой токен expired') : fail("статус $r вместо expired");

    db_exec('DELETE FROM login_requests WHERE user_id IN (?, ?)', [$d, $e]);
    db_exec('DELETE FROM sessions WHERE user_id IN (?, ?)', [$d, $e]);
    db_exec('DELETE FROM users WHERE id IN (?, ?)', [$d, $e]);
}

// 5. Вход в существующий с пустого
{
    echo "\n5. Вход в существующий с пустого:\n";
    $existing = bin2hex(random_bytes(16));
    $empty = bin2hex(random_bytes(16));
    db_exec('INSERT INTO users (id, created_at, telegram_id) VALUES (?, ?, ?)', [$existing, time(), 555]);
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$empty, time()]);

    $token = bin2hex(random_bytes(16));
    $token_hash = hash('sha256', $token);
    db_exec(
        'INSERT INTO login_requests (token_hash, user_id, created_at, status, chat_id, telegram_id, tg_username, tg_name) VALUES (?, ?, ?, ?, ?, ?, ?, ?)',
        [$token_hash, $empty, time(), 'confirmed', 555, 555, 'existing', 'Existing User']
    );

    $req = db_one('SELECT * FROM login_requests WHERE token_hash = ?', [$token_hash]);
    app_user_bind($empty);
    $result = login_complete($empty, '', $req);

    $result === $existing ? ok('вошли в existing') : fail("вошли в $result вместо $existing");
    !db_value('SELECT 1 FROM users WHERE id = ?', [$empty]) ? ok('empty удалён') : fail('empty не удалён');

    db_exec('DELETE FROM login_requests WHERE user_id = ?', [$existing]);
    db_exec('DELETE FROM sessions WHERE user_id = ?', [$existing]);
    db_exec('DELETE FROM users WHERE id = ?', [$existing]);
}

// 6. Слияние с оценками
{
    echo "\n6. Слияние с оценками:\n";
    $merging = bin2hex(random_bytes(16));
    $owner = bin2hex(random_bytes(16));

    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$merging, time()]);
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$owner, time()]);

    // merging: 2 оценки, одна совпадает с owner
    db_exec('INSERT INTO history_items (user_id, collection, position, image_key, data) VALUES (?, ?, ?, ?, ?)',
        [$merging, 'liked', 1, 'key1', '{"image":"key1"}']);
    db_exec('INSERT INTO history_items (user_id, collection, position, image_key, data) VALUES (?, ?, ?, ?, ?)',
        [$merging, 'liked', 2, 'key_unique', '{"image":"key_unique"}']);

    // owner: 1 оценка (совпадает с первой от merging)
    db_exec('INSERT INTO history_items (user_id, collection, position, image_key, data) VALUES (?, ?, ?, ?, ?)',
        [$owner, 'liked', 1, 'key1', '{"image":"key1"}']);

    $owner_before = (int)db_value('SELECT COUNT(*) FROM history_items WHERE user_id = ?', [$owner]);

    app_user_bind($merging);
    user_merge($merging, $owner);

    !db_value('SELECT 1 FROM users WHERE id = ?', [$merging]) ? ok('merging удалён') : fail('merging не удалён');

    $owner_after = (int)db_value('SELECT COUNT(*) FROM history_items WHERE user_id = ?', [$owner]);
    $owner_after === $owner_before + 1 ? ok('одна новая оценка') : fail("оценок $owner_after вместо " . ($owner_before + 1));

    db_exec('DELETE FROM history_items WHERE user_id = ?', [$owner]);
    db_exec('DELETE FROM users WHERE id = ?', [$owner]);
}

// 7. Старая кука вошедшего игнорируется
{
    echo "\n7. Старая кука вошедшего:\n";
    $logged_in = bin2hex(random_bytes(16));
    db_exec('INSERT INTO users (id, created_at, telegram_id) VALUES (?, ?, ?)', [$logged_in, time(), 777]);

    $r = session_resolve('', $logged_in);  // Попытаемся использовать старую куку вошедшего
    $r['uid'] !== $logged_in ? ok('старая кука вошедшего → новый') : fail('старая кука вошедшего не отклонена');

    db_exec('DELETE FROM sessions WHERE user_id IN (?, ?)', [$logged_in, $r['uid']]);
    db_exec('DELETE FROM users WHERE id IN (?, ?)', [$logged_in, $r['uid']]);
}

// 8. Сообщение в группе игнорируется
{
    echo "\n8. Сообщение в группе:\n";
    $token = bin2hex(random_bytes(16));
    $update = ['message' => [
        'chat' => ['type' => 'group', 'id' => 999],
        'from' => ['id' => 999, 'is_bot' => false],
        'text' => "/start login_{$token}"
    ]];

    $calls = login_handle_update($update);
    count($calls) === 0 ? ok('групповое сообщение игнорировано') : fail('групповое сообщение обработано');
}

// 9. users-merge command
{
    echo "\n9. users-merge:\n";
    $from = bin2hex(random_bytes(16));
    $to = bin2hex(random_bytes(16));

    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$from, time()]);
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$to, time()]);

    // from: 2 оценки
    for ($i = 1; $i <= 2; $i++) {
        db_exec('INSERT INTO history_items (user_id, collection, position, image_key, data) VALUES (?, ?, ?, ?, ?)',
            [$from, 'liked', $i, "key{$i}", "{\"image\":\"key{$i}\"}"]);
    }

    // to: 2 оценки, одна совпадает
    db_exec('INSERT INTO history_items (user_id, collection, position, image_key, data) VALUES (?, ?, ?, ?, ?)',
        [$to, 'liked', 1, 'key1', '{"image":"key1"}']);
    db_exec('INSERT INTO history_items (user_id, collection, position, image_key, data) VALUES (?, ?, ?, ?, ?)',
        [$to, 'disliked', 1, 'key_other', '{"image":"key_other"}']);

    $to_before = (int)db_value('SELECT COUNT(*) FROM history_items WHERE user_id = ?', [$to]);

    app_user_bind($from);
    user_merge($from, $to);

    !db_value('SELECT 1 FROM users WHERE id = ?', [$from]) ? ok('from удалён') : fail('from не удалён');

    $to_after = (int)db_value('SELECT COUNT(*) FROM history_items WHERE user_id = ?', [$to]);
    $to_after === $to_before + 1 ? ok('неповторяющиеся оценки добавлены') : fail("оценок $to_after вместо " . ($to_before + 1));

    db_exec('DELETE FROM history_items WHERE user_id = ?', [$to]);
    db_exec('DELETE FROM users WHERE id = ?', [$to]);
}

echo "\n" . ($failures === 0 ? "✓ Все тесты пройдены\n" : "✗ Провалов: $failures\n");
exit($failures > 0 ? 1 : 0);
