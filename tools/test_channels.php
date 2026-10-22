<?php
// Проверка нескольких Telegram-каналов без настоящего Telegram. Запускать на копии проекта: путь к базе — от папки проекта.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

if (MEDIA_CACHE_DIR === '/var/lib/posta/media') exit("Тест запущен с настоящей папкой медиа: задайте POSTA_MEDIA_CACHE_DIR\n");

$failures = 0;
$check = function ($name, $ok) use (&$failures) {
    echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n";
    if (!$ok) $failures++;
};

// Тестовые пользователи создаются здесь и удаляются в конце
$made = [];
$make_user = function (array $connections = []) use (&$made) {
    $uid = bin2hex(random_bytes(16));
    db_exec('INSERT INTO users (id, created_at, connections) VALUES (?, ?, ?)', [$uid, time(), db_json($connections)]);
    $made[] = $uid;
    return $uid;
};
// Старый формат: один канал в users.connections.telegram
$legacy = fn($chat_id, $title) => ['telegram' => ['token_enc' => secret_encrypt('123:fake'), 'bot_id' => 1, 'bot_username' => 'bot', 'chat_id' => $chat_id,
    'chat_title' => $title, 'chat_username' => '', 'chat_type' => 'channel', 'connected_at' => 1000]];
// Новый канал, токен открытый (channel_save шифрует сам)
$new = fn($chat_id, $title) => ['token' => '123:fake', 'bot_id' => 1, 'bot_username' => 'bot', 'chat_id' => $chat_id, 'chat_title' => $title, 'chat_type' => 'channel', 'connected_at' => time()];

// 1. Перенос старого канала
echo "\n1. Перенос:\n";
$a = $make_user($legacy(-1001, 'Первый'));
$check('один канал после переноса', count(channels_load($a)) === 1);
$check('токен расшифрован', (channels_load($a)[0]['token'] ?? '') === '123:fake');
$check('повторный вызов не создаёт второй', count(channels_load($a)) === 1);
$check('connections.telegram на месте', isset(connections_load($a)['telegram']));

// 2. Совместимость telegram_connection()
echo "\n2. Совместимость:\n";
$t = telegram_connection($a);
$check('канал по умолчанию — тот же chat_id', ($t['chat_id'] ?? 0) === -1001);
$check('есть token и id', !empty($t['token']) && !empty($t['id']));

// 3. Второй канал и смена канала по умолчанию
echo "\n3. Несколько каналов:\n";
$b_id = channel_save($new(-1002, 'Второй'), $a);
$check('два канала', count(channels_load($a)) === 2);
$check('первый остаётся по умолчанию', telegram_connection($a)['chat_id'] === -1001);
channel_make_default($b_id, $a);
$check('сделать основным: второй по умолчанию', telegram_connection($a)['chat_id'] === -1002);
$c_id = channel_save($new(-1003, 'Третий'), $a);
channel_make_default($c_id, $a);
$check('порядок остальных сохранён', array_column(channels_load($a), 'chat_id') === [-1003, -1002, -1001]);

// 4. Повторное подключение того же канала
echo "\n4. Повтор:\n";
$check('тот же chat_id обновляет строку', channel_save($new(-1002, 'Второй переименован'), $a) === $b_id && count(channels_load($a)) === 3);

// 5. Чужой канал
echo "\n5. Чужой канал:\n";
$other = $make_user();
$foreign_id = channel_save($new(-2001, 'Чужой'), $other);
$check('channel_get чужого → null', channel_get($foreign_id, $a) === null);
$check('публикация в чужой — ошибка 404', (publish_channel_resolve($foreign_id, $a, false)['status'] ?? 0) === 404);

// 6. Лимит тарифа
echo "\n6. Лимит тарифа:\n";
$solo = $make_user($legacy(-3001, 'Соло'));
db_exec('INSERT INTO subscriptions (user_id, plan, channels, posts_per_day, price, started_at, until) VALUES (?, ?, ?, ?, ?, ?, ?)',
    [$solo, 'test', 1, 10, 0, time(), time() + DAY_SECONDS * 30]);
$check('сверх тарифа — отказ при enforce', is_string(channel_connect_denied(-3002, $solo, true)));
$check('без enforce — разрешено', channel_connect_denied(-3002, $solo, false) === null);
$check('без enforce — over_limit', channel_over_limit(-3002, $solo));
$check('уже подключённый канал — не сверх тарифа', !channel_over_limit(-3001, $solo) && channel_connect_denied(-3001, $solo, true) === null);
$second = channel_save($new(-3002, 'Два'), $solo);
$check('публикация во второй канал при enforce — 402', (publish_channel_resolve($second, $solo, true)['status'] ?? 0) === 402);
$check('публикация во второй канал без enforce — можно', isset(publish_channel_resolve($second, $solo, false)['channel']));

// 7. Удаление
echo "\n7. Удаление:\n";
$del = $make_user();
$d1 = channel_save($new(-4001, 'Удаляемый'), $del);
$d2 = channel_save($new(-4002, 'С отложкой'), $del);
outbox_update(fn($q) => [...$q, ['id' => 'abcdef012345', 'status' => 'telegram', 'channel' => $d2, 'publish_at' => time() + 3600, 'text' => 'пост', 'media' => [],
    'tg_ids' => [1], 'attempts' => 0, 'error' => '', 'created_at' => time()]], $del);
$check('канал с отложенным постом не удаляется', channel_delete_denied($d2, $del) !== null);
$check('канал без постов можно удалить', channel_delete_denied($d1, $del) === null);
$check('удалённый канал пропал', channel_delete($d1, $del) && channel_get($d1, $del) === null);
$gone = $make_user($legacy(-4003, 'Старый'));
channel_delete(channels_load($gone)[0]['id'], $gone);
$check('удалённый старый канал не возвращается', channels_load($gone) === []);

// 8. Слияние профилей
echo "\n8. Слияние:\n";
$from = $make_user();
$into = $make_user();
channel_save($new(-5001, 'X'), $from);
channel_save($new(-5002, 'Y'), $from);
channel_save($new(-5002, 'Y'), $into);
channel_save($new(-5003, 'Z'), $into);
user_merge($from, $into);
$chats = array_column(channels_load($into), 'chat_id');
sort($chats);
$check('у $into ровно X, Y, Z', $chats === [-5003, -5002, -5001]);

// 9. Уборка
foreach ($made as $uid) {
    db_exec('DELETE FROM kv WHERE key = ?', ['channels_legacy_done:' . $uid]);
    db_exec('DELETE FROM users WHERE id = ?', [$uid]);
}
echo "\nПровалов: $failures\n";
exit($failures ? 1 : 0);
