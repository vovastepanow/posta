<?php
// Проверка расписания и своей очереди публикаций без настоящего Telegram. Запускать на копии проекта
// с отдельной папкой медиа (POSTA_MEDIA_CACHE_DIR): см. SCHEDULE_PROMPT.md, часть F.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

if (MEDIA_CACHE_DIR === '/var/lib/posta/media') exit("Тест запущен с настоящей папкой медиа: задайте POSTA_MEDIA_CACHE_DIR\n");

$failures = 0;
$check = function ($name, $ok) use (&$failures) {
    echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n";
    if (!$ok) $failures++;
};

$made = [];
$make_user = function () use (&$made) {
    $uid = bin2hex(random_bytes(16));
    db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
    $made[] = $uid;
    return $uid;
};
$new_channel = fn($chat_id) => ['token' => '123:fake', 'bot_id' => 1, 'bot_username' => 'bot', 'chat_id' => $chat_id, 'chat_title' => "Канал {$chat_id}", 'chat_type' => 'channel', 'connected_at' => time()];

// Маленькая картинка в папке, как после telegram_prepare_media
$make_media = function () {
    $dir = sys_get_temp_dir() . '/test-schedule-' . bin2hex(random_bytes(6));
    mkdir($dir, 0700);
    $img = imagecreatetruecolor(10, 10);
    imagepng($img, "$dir/photo-1.png");
    return [$dir, [['kind' => 'photo', 'path' => "$dir/photo-1.png", 'name' => 'photo', 'width' => 10, 'height' => 10, 'duration' => 0]]];
};
$queue_post = function ($uid, $channel, $publish_at, $slot_at = null) use ($make_media) {
    [$dir, $media] = $make_media();
    return outbox_queue_post($uid, $channel, 'тест', $media, $publish_at, $slot_at, $dir)['id'];
};
$post_row = fn($id) => db_one('SELECT status, data FROM outbox WHERE id = ?', [$id]);
$post_data = fn($id) => db_unjson(db_value('SELECT data FROM outbox WHERE id = ?', [$id]), []);

// Ближайший понедельник 09:00 по Москве (за месяц вперёд, чтобы не упереться в «минимум минута»)
$msk = new DateTimeZone('Europe/Moscow');
$mon09 = (new DateTimeImmutable('now', $msk))->modify('+30 days')->setTime(9, 0);
while ($mon09->format('N') !== '1') $mon09 = $mon09->modify('+1 day');
$ts = fn($dt) => $dt->getTimestamp();

// 1. Слоты
echo "\n1. Слоты:\n";
$uid = $make_user();
$ch = channel_save($new_channel(-1001), $uid);
channel_schedule_save($ch, ['tz' => 'Europe/Moscow', 'jitter' => 0, 'slots' => ['1' => ['10:00', '19:00'], '2' => ['10:00', '19:00'], '3' => ['10:00', '19:00'], '4' => ['10:00', '19:00'], '5' => ['10:00', '19:00']]], $uid);
$channel = channel_get($ch, $uid);
$mon10 = $mon09->setTime(10, 0);
$mon19 = $mon09->setTime(19, 0);
$slot = channel_next_slot($channel, $ts($mon09), []);
$check('от пн 09:00 — пн 10:00', $slot['slot_at'] === $ts($mon10));
$queue_post($uid, $channel, $ts($mon10), $ts($mon10));
$slot = channel_next_slot($channel, $ts($mon09), outbox_load($uid));
$check('пн 10:00 занят — пн 19:00', $slot['slot_at'] === $ts($mon19));
$fri20 = $mon09->modify('-3 days')->setTime(20, 0);
$check('от пт 20:00 без постов — пн 10:00', channel_next_slot($channel, $ts($fri20), [])['slot_at'] === $ts($mon10));
$check('от пт 20:00 при занятом пн 10:00 — пн 19:00', channel_next_slot($channel, $ts($fri20), outbox_load($uid))['slot_at'] === $ts($mon19));

// 2. Сдвиг
echo "\n2. Сдвиг:\n";
$uid2 = $make_user();
$ch2 = channel_save($new_channel(-1002), $uid2);
channel_schedule_save($ch2, ['tz' => 'Europe/Moscow', 'jitter' => 10, 'slots' => ['1' => ['10:00']]], $uid2);
$channel2 = channel_get($ch2, $uid2);
$slot = channel_next_slot($channel2, $ts($mon09), []);
$check('сдвиг в пределах ±600 от слота', abs($slot['publish_at'] - $slot['slot_at']) <= 600 && $slot['slot_at'] === $ts($mon10));
$queue_post($uid2, $channel2, $slot['slot_at'] + 500, $slot['slot_at']);
$next = channel_next_slot($channel2, $ts($mon09), outbox_load($uid2));
$check('занятость по slot_at, а не по сдвинутому времени', $next['slot_at'] === $ts($mon10->modify('+7 days')));

// 3. Пояс
echo "\n3. Пояс:\n";
$uid3 = $make_user();
$ch3 = channel_save($new_channel(-1003), $uid3);
$ch4 = channel_save($new_channel(-1004), $uid3);
channel_schedule_save($ch3, ['tz' => 'Europe/Moscow', 'jitter' => 0, 'slots' => ['1' => ['10:00']]], $uid3);
channel_schedule_save($ch4, ['tz' => 'Asia/Yekaterinburg', 'jitter' => 0, 'slots' => ['1' => ['10:00']]], $uid3);
$before_monday = $mon09->modify('-1 day')->setTimezone(new DateTimeZone('UTC'))->setTime(0, 0);
$msk_slot = channel_next_slot(channel_get($ch3, $uid3), $ts($before_monday), [])['slot_at'];
$yek_slot = channel_next_slot(channel_get($ch4, $uid3), $ts($before_monday), [])['slot_at'];
$check('Екатеринбург на 2 часа раньше Москвы по UTC', $msk_slot - $yek_slot === 7200);

// 4. Неверное расписание
echo "\n4. Проверка расписания:\n";
$bad = function (array $schedule) use ($ch, $uid) {
    try { channel_schedule_save($ch, $schedule, $uid); return false; } catch (InvalidArgumentException $e) { return true; }
};
$check('25:00 — ошибка', $bad(['slots' => ['1' => ['25:00']]]));
$check('10:7 — ошибка', $bad(['slots' => ['1' => ['10:7']]]));
$check('25 слотов в день — ошибка', $bad(['slots' => ['1' => array_map(fn($i) => sprintf('%02d:%02d', intdiv($i, 2), ($i % 2) * 30), range(0, 24))]]));
$saved = channel_schedule_save($ch, ['tz' => 'Нет/Такого', 'jitter' => 0, 'slots' => ['1' => ['10:00']]], $uid);
$check('неизвестный пояс — пояс сервера, не ошибка', $saved['tz'] === date_default_timezone_get());

// 5. Отправка
echo "\n5. Отправка:\n";
$uid5 = $make_user();
$ch5 = channel_save($new_channel(-1005), $uid5);
$channel5 = channel_get($ch5, $uid5);
$calls = 0;
$send_ok = function (array $post, array $channel) use (&$calls) { $calls++; return ['url' => 'https://t.me/x/1']; };
$id5 = $queue_post($uid5, $channel5, time() - 10);
$dir5 = outbox_media_dir($id5);
$r = outbox_process_due(time(), $send_ok);
$check('созревший пост отправлен один раз', $calls === 1 && $r['sent'] === 1);
$check('пост удалён из очереди', $post_row($id5) === null);
$check('запись в журнале с url', (bool)array_filter(published_load($uid5), fn($p) => ($p['url'] ?? '') === 'https://t.me/x/1'));
$check('папки поста нет', !is_dir($dir5));

// 6. Будущий пост
echo "\n6. Будущий пост:\n";
$calls = 0;
$id6 = $queue_post($uid5, $channel5, time() + 3600);
outbox_process_due(time(), $send_ok);
$check('будущий пост не отправлен', $calls === 0 && $post_row($id6)['status'] === 'queued');
outbox_remove_post($id6, $uid5);

// 7. Повторы
echo "\n7. Повторы:\n";
$uid7 = $make_user();
$ch7 = channel_save($new_channel(-1007), $uid7);
$channel7 = channel_get($ch7, $uid7);
$timeout = function () { throw new RuntimeException('Telegram недоступен: Timeout'); };
$calls7 = 0;
$send7 = function (array $post, array $channel) use (&$calls7, $timeout) { $calls7++; $timeout(); };
$now = time();
$id7 = $queue_post($uid7, $channel7, $now - 10);
outbox_process_due($now, $send7);
$d = $post_data($id7);
$check('после первой неудачи — queued, attempts 1, повтор через 60 с', $post_row($id7)['status'] === 'queued' && $d['attempts'] === 1 && $d['next_try_at'] === $now + 60);
outbox_process_due($now + 5, $send7);
$check('до next_try_at не отправляет', $calls7 === 1);
outbox_process_due($now + 61, $send7);
outbox_process_due($now + 61 + 301, $send7);
$check('после трёх неудач — failed', $post_row($id7)['status'] === 'failed' && $calls7 === 3);

// 8. Неисправимая ошибка
echo "\n8. Неисправимая ошибка:\n";
$calls8 = 0;
$kicked = function (array $post, array $channel) use (&$calls8) { $calls8++; throw new RuntimeException('Forbidden: bot was kicked from the channel'); };
$id8 = $queue_post($uid7, $channel7, time() - 10);
outbox_process_due(time(), $kicked);
$check('сразу failed после первой попытки', $calls8 === 1 && $post_row($id8)['status'] === 'failed');
outbox_remove_post($id8, $uid7);

// 9. Двойной запуск
echo "\n9. Двойной запуск:\n";
$calls9 = 0;
$send9 = function (array $post, array $channel) use (&$calls9) { $calls9++; return ['url' => '']; };
$id9 = $queue_post($uid7, $channel7, time() - 10);
outbox_process_due(time(), $send9);
outbox_process_due(time(), $send9);
$check('два вызова подряд — одна отправка', $calls9 === 1);
$id9b = $queue_post($uid7, $channel7, time() - 10);
$first = outbox_claim($id9b, time());
$second = outbox_claim($id9b, time());
$check('второй захват того же поста отказан', $first !== null && $second === null);
outbox_remove_post($id9b, $uid7);

// 10. Зависший sending
echo "\n10. Зависший sending:\n";
$calls10 = 0;
$send10 = function (array $post, array $channel) use (&$calls10) { $calls10++; return ['url' => '']; };
$id10 = $queue_post($uid7, $channel7, time() - 10);
$stuck = $post_data($id10);
$stuck['status'] = 'sending';
$stuck['sending_at'] = time() - 1200;
outbox_row_save($id10, $stuck, 'sending');
outbox_process_due(time(), $send10);
$check('зависший sending → failed без отправки', $calls10 === 0 && $post_row($id10)['status'] === 'failed');
outbox_remove_post($id10, $uid7);

// 11. Отмена
echo "\n11. Отмена:\n";
$id11 = $queue_post($uid7, $channel7, time() + 3600);
$dir11 = outbox_media_dir($id11);
$stuck = $post_data($id11);
$stuck['status'] = 'sending';
outbox_row_save($id11, $stuck, 'sending');
$check('отмена sending — отказ', outbox_remove_post($id11, $uid7) === false && $post_row($id11) !== null);
outbox_row_save($id11, $post_data($id11) + ['status' => 'queued'], 'queued');
$check('отмена queued — пост и папка удалены', outbox_remove_post($id11, $uid7) === true && $post_row($id11) === null && !is_dir($dir11));

// 12. Канал отключён
echo "\n12. Канал отключён:\n";
$uid12 = $make_user();
$ch12 = channel_save($new_channel(-1012), $uid12);
$channel12 = channel_get($ch12, $uid12);
$id12 = $queue_post($uid12, $channel12, time() - 10);
channel_delete($ch12, $uid12);
$calls12 = 0;
$send12 = function (array $post, array $channel) use (&$calls12) { $calls12++; return ['url' => '']; };
outbox_process_due(time(), $send12);
$check('канал отключён — failed «Канал отключён», без отправки', $calls12 === 0 && $post_row($id12)['status'] === 'failed' && str_contains($post_data($id12)['error'], 'Канал отключён'));
outbox_remove_post($id12, $uid12);

// 13. Уборка
echo "\n13. Уборка:\n";
foreach ($made as $u) {
    foreach (db_all('SELECT id FROM outbox WHERE user_id = ?', [$u]) as $row) remove_dir(outbox_media_dir($row['id']));
    db_exec('DELETE FROM users WHERE id = ?', [$u]);
    db_exec('DELETE FROM kv WHERE key LIKE ?', ['%' . $u]);
}
$check('тестовые пользователи удалены', (int)db_value('SELECT COUNT(*) FROM users WHERE id IN (' . implode(',', array_fill(0, count($made), '?')) . ')', $made) === 0);

echo "\nПровалов: $failures\n";
exit($failures ? 1 : 0);
