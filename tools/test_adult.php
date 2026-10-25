<?php
// Проверка фильтра 18+ и «Жесть» без браузера и моделей. Запускать на копии проекта: путь к базе — от папки проекта.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/content.php';

if (MEDIA_CACHE_DIR === '/var/lib/posta/media') exit("Тест запущен с настоящей папкой медиа: задайте POSTA_MEDIA_CACHE_DIR\n");

$failures = 0;
$check = function ($name, $ok) use (&$failures) {
    echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n";
    if (!$ok) $failures++;
};

// Тестовый пользователь: новый id, в конце удаляется вместе с записями
$uid = bin2hex(random_bytes(16));
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
app_user_bind($uid);
$set_flag = function ($on) use ($uid) {
    db_exec('UPDATE users SET adult_allowed = ? WHERE id = ?', [$on ? 1 : 0, $uid]);
    user_adult_allowed($uid, true);   // сброс кэша флага
};

// 1. content_is_adult: пять случаев из части B
$adult_cases = [
    [['tags' => ['adult.porn']], true],
    [['topic' => 'Порно'], true],
    [['board' => 'hc'], true],
    [['tags' => ['humor.jokes'], 'topic' => 'Мемы'], false],
    [[], false],
];
echo "\n1. content_is_adult:\n";
foreach ($adult_cases as $n => [$r, $want]) $check('случай ' . ($n + 1), content_is_adult($r) === $want);

// 2–3. Выбор темы
echo "\n2. Выбор темы без флага и с флагом:\n";
$set_flag(false);
$check('«Порно» запрещено', topic_save_denied('Порно') !== null);
$check('синоним «порнуха» запрещён', topic_save_denied('порнуха') !== null);
$check('«Мемы» разрешено', topic_save_denied('Мемы') === null);
$set_flag(true);
$check('с флагом «Порно» разрешено', topic_save_denied('Порно') === null);

// 4. Список тем
echo "\n4. Список тем:\n";
$set_flag(false);
$visible = topics_public_list();
$check('без флага нет веток 18+ и «Жесть»', !array_filter($visible, fn($t) => $t['nsfw'] || in_array($t['key'], ['adult', 'shock'], true)));
$check('без флага обычные темы на месте', (bool)array_filter($visible, fn($t) => $t['title'] === 'Мемы'));

// 5. Пул: 18+ скрыта без флага, но из базы не удаляется
echo "\n5. Пул и показ:\n";
$normal = ['image' => 'https://example.com/normal.jpg', 'topic' => 'Мемы', 'tags' => ['humor.jokes'], 'source' => 'Тест', 'posted_at' => time()];
$adult_local = MEDIA_CACHE_URL . '/' . str_repeat('a', 32) . '.jpg';
$adult = ['image' => 'https://example.com/adult.jpg', 'image_local' => $adult_local, 'topic' => 'Мемы', 'tags' => ['adult.porn'], 'source' => 'Тест', 'posted_at' => time()];
save_settings(['topic' => 'Мемы']);
pool_update(fn() => [$normal, $adult]);
$set_flag(false);
$check('без флага видна одна запись', count(pool_topic_positions(pool_load())) === 1);
pool_for_current_topic();
$check('без флага 18+ остаётся в базе', count(pool_load()) === 2);
$set_flag(true);
$check('с флагом видны обе записи', count(pool_topic_positions(pool_load())) === 2);

// 6. Публикация
echo "\n6. Публикация:\n";
$set_flag(false);
$check('18+ из пула запрещена', publish_media_denied([['src' => $adult_local]]) !== null);
$check('обычная запись разрешена', publish_media_denied([['src' => 'https://example.com/normal.jpg']]) === null);
$check('свой файл не проверяется', publish_media_denied([['upload' => '0', 'src' => '']]) === null);

// 7. Уборка
db_exec('DELETE FROM users WHERE id = ?', [$uid]);
user_adult_allowed($uid, true);
echo "\nПровалов: $failures\n";
exit($failures ? 1 : 0);
