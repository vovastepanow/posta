<?php
// Тестирование восстановления медиа и защиты кэша (часть E из MEDIA_RECOVERY_PROMPT.md)
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/content.php';

if (MEDIA_CACHE_DIR === '/var/lib/posta/media') exit("Тест запущен с настоящей папкой медиа: задайте POSTA_MEDIA_CACHE_DIR\n");

$failures = 0;
$check = function ($name, $ok) use (&$failures) {
    echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n";
    if (!$ok) $failures++;
};

// Создать тестовый каталог медиа и помощники
function create_test_file($name, $content = 'test') {
    $path = MEDIA_CACHE_DIR . '/' . $name;
    file_put_contents($path, $content);
    return $path;
}

function touch_old_file($name) {
    $path = MEDIA_CACHE_DIR . '/' . $name;
    if (is_file($path)) touch($path, time() - 3600);
}

// Тестовые пользователи
$uid1 = bin2hex(random_bytes(16));
$uid2 = bin2hex(random_bytes(16));
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid1, time()]);
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid2, time()]);

echo "\n1. Защита от массового удаления:\n";

// 1.1 Пустая база + 10 файлов → ничего не удалено
create_test_file('00000000000000000000000000000001.jpg', 'img');
for ($i = 2; $i <= 10; $i++) create_test_file(sprintf('%032d.jpg', $i), 'img');
$removed = media_cache_cleanup([], false);
$check("Пустая база не удаляет файлы", $removed === 0 && count(scandir(MEDIA_CACHE_DIR)) >= 11);

// 1.2 С force=true, но файлы молодые — не удаляются
$removed = media_cache_cleanup([], false, true);
$check("Молодые файлы не удаляются при force", $removed === 0);

// 1.3 Сделать файлы старыми и удалить с force=true
for ($i = 1; $i <= 10; $i++) touch_old_file(sprintf('%032d.jpg', $i));
$removed = media_cache_cleanup([], true, true);
$check("Старые файлы удаляются при force=true и include_fresh=true", $removed === 10);

echo "\n2. Доля удалений:\n";

// 10 файлов, записи ссылаются на 5 → 50% > 30% → ничего не удалено
for ($i = 1; $i <= 10; $i++) create_test_file(sprintf('%032d.jpg', $i), 'img');
$pool = [];
for ($i = 1; $i <= 5; $i++) {
    $pool[] = ['image' => 'kept' . $i, 'image_local' => MEDIA_CACHE_URL . '/' . sprintf('%032d.jpg', $i)];
}
$removed = media_cache_cleanup($pool, false);
$check("Удаление >30% блокируется", $removed === 0);

// Записи ссылаются на 8 → удалены 2
$pool = [];
for ($i = 1; $i <= 8; $i++) {
    $pool[] = ['image' => 'kept' . $i, 'image_local' => MEDIA_CACHE_URL . '/' . sprintf('%032d.jpg', $i)];
}
for ($i = 1; $i <= 10; $i++) touch_old_file(sprintf('%032d.jpg', $i));   // свежие файлы очистка не трогает по замыслу
$removed = media_cache_cleanup($pool, false);
$check("Удаление <30% работает", $removed === 2);
// Очистить для следующих тестов
@array_walk(scandir(MEDIA_CACHE_DIR), function($f) { if (preg_match('/^[a-f0-9]{32}\.jpg$/', $f)) @unlink(MEDIA_CACHE_DIR . '/' . $f); });

echo "\n3. Общий файл двух пользователей:\n";

$image_url = MEDIA_CACHE_URL . '/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1.jpg';
create_test_file('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1.jpg', 'shared');

// Добавить одиночную запись у первого пользователя в пул
pool_update(function() use ($image_url) {
    return [['image' => $image_url, 'image_local' => $image_url]];
}, $uid1);

// Добавить одиночную запись у второго пользователя в историю
history_update(function($h) use ($image_url) {
    $h['liked'] = [['image' => $image_url, 'image_local' => $image_url]];
    return $h;
}, $uid2);

// Попытаться удалить файл через пользователя 1
app_user_bind($uid1);
media_cache_remove($image_url);
$check("media_cache_remove сохраняет файл, если он нужен другому пользователю", is_file(MEDIA_CACHE_DIR . '/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa1.jpg'));

echo "\n4. Аудит и ремонт:\n";

$f = fn($c) => str_repeat($c, 32) . '.jpg';   // имя файла кэша — ровно 32 hex
history_update(fn($h) => ['liked' => [['image' => 'https://example.com/b.jpg', 'image_local' => MEDIA_CACHE_URL . '/' . $f('b'),
        'source_url' => 'https://t.me/test/123', 'media_cached' => true, 'media_checked' => true, 'timestamp' => time()]],
    'disliked' => [['image' => 'https://example.com/d.jpg', 'image_local' => MEDIA_CACHE_URL . '/' . $f('d'), 'source_url' => 'https://t.me/test/124', 'media_cached' => true]]] + $h, $uid1);
app_user_bind($uid1);
$check("До ремонта запись с пропавшим файлом не ждёт загрузки", count(proposal_pending()) === 0);
$dry = media_repair_user($uid1, true);
$check("--dry-run считает, но не меняет", $dry['marked'] === 1 && count(proposal_pending()) === 0);
$st = media_repair_user($uid1);
$check("Ремонт: помечена 1, очищено 2", $st['marked'] === 1 && $st['cleared'] === 2);
$pending = array_values(proposal_pending());
$check("Запись «Предложки» ждёт загрузки", count($pending) === 1 && ($pending[0]['image_local'] ?? 'x') === '' && empty($pending[0]['media_cached']));
$dis = history_load($uid1)['disliked'][0] ?? [];
$check("Дизлайк: ссылка очищена, на загрузку не помечен", ($dis['image_local'] ?? 'x') === '' && !empty($dis['media_cached']));
$check("Повторный ремонт ничего не меняет", array_sum(media_repair_user($uid1)) === 0);

echo "\n5. Старый путь media_cache/...:\n";

create_test_file($f('c'), 'old');
history_update(fn($h) => ['undecided' => [['image' => 'https://example.com/c.jpg', 'image_local' => 'media_cache/' . $f('c'),
    'source_url' => 'https://t.me/test/456', 'media_cached' => true]]] + $h, $uid1);
$st = media_repair_user($uid1);
$fixed = history_load($uid1)['undecided'][0] ?? [];
$check("Старый путь заменён на новый, файл не качается заново", ($fixed['image_local'] ?? '') === MEDIA_CACHE_URL . '/' . $f('c') && $st['fixed'] === 1 && $st['marked'] === 0);

echo "\n6. Флаг media_gone:\n";

history_update(function ($h) use ($f) {
    $h['liked'][] = ['image' => 'https://example.com/g.jpg', 'image_local' => MEDIA_CACHE_URL . '/' . $f('e'), 'source_url' => 'https://t.me/test/789', 'media_gone' => true, 'media_cached' => true];
    return $h;
}, $uid1);
$before = count(proposal_pending());
media_repair_user($uid1);
$check("media_gone: ссылка очищена, но запись не уходит на загрузку", count(proposal_pending()) === $before);

echo "\n7. Финал:\n";

// Удалить тестовых пользователей
db_exec('DELETE FROM users WHERE id IN (?, ?)', [$uid1, $uid2]);
db_exec('DELETE FROM pool_items WHERE user_id IN (?, ?)', [$uid1, $uid2]);
db_exec('DELETE FROM history_items WHERE user_id IN (?, ?)', [$uid1, $uid2]);

$check("Тестовые пользователи удалены", !db_value('SELECT COUNT(*) FROM users WHERE id = ?', [$uid1]));

// Очистить каталог медиа
@array_walk(scandir(MEDIA_CACHE_DIR), function($f) { if (is_file(MEDIA_CACHE_DIR . '/' . $f)) @unlink(MEDIA_CACHE_DIR . '/' . $f); });

echo "\n";
if ($failures) echo "Не пройдено: $failures\n";
exit($failures > 0 ? 1 : 0);
