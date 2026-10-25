<?php
// Публикация = лайк (POSTED_IS_LIKED_PROMPT.md): отметка записи, «Предложка», отмена. Без настоящего Telegram.
// Запускать на копии проекта с копией базы и отдельной папкой медиа: см. POSTA_MEDIA_CACHE_DIR, как в test_schedule.php.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/content.php';   // proposal_items — выдача «Предложки»

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
    app_user_bind($uid);   // дальше все функции работают от имени этого пользователя
    return $uid;
};
$record = fn($tag, $extra = []) => ['image' => "https://cdn.test/{$tag}.jpg", 'text' => "Запись {$tag}", 'topic' => current_topic(), 'source' => 'test', 'source_key' => 'source:test',
    'source_url' => "https://t.me/test/{$tag}", 'posted_at' => time() - 3600, 'relevance' => 90, 'description' => 'd', 'tags' => []] + $extra;
$in_collection = function (array $history, string $collection, string $key) {
    foreach ($history[$collection] ?? [] as $r) if (is_array($r) && content_item_key($r) === $key) return $r;
    return null;
};
$where = function (string $key, $uid) use ($in_collection) {
    $h = history_load($uid);
    foreach (['liked', 'disliked', 'undecided', 'complaints'] as $c) if ($in_collection($h, $c, $key)) return $c;
    return null;
};
$pool_has = fn(string $key, $uid) => (bool)pool_find_item(pool_load($uid), $key);
$media_file = function (string $tag) {
    $path = MEDIA_CACHE_DIR . "/posted-{$tag}.jpg";
    @mkdir(dirname($path), 0775, true);
    file_put_contents($path, 'test');
    return $path;
};

$make_user();   // пользователь для настроек темы (current_topic) и выдачи «Предложки»
echo "A. Ключ записи:\n";
$r1 = $record('k1');
$check('ключ записи с картинкой — адрес картинки', content_record_key(['image' => $r1['image'], 'text' => 'x']) === $r1['image']);
$check('ключ текстового поста — по адресу поста (как в пуле)', content_record_key(['image' => '', 'source_url' => 'https://t.me/test/zz', 'text' => 'x', 'text_post' => true]) === 'post:https://t.me/test/zz');
$check('пустые поля — пустой ключ', content_record_key(null) === '' && content_record_key([]) === '');

echo "Б. Из пула («Подбор») — в «liked»:\n";
$uid = $make_user();
$key = $r1['image'];
$cached = $media_file('k1');
pool_update(fn($pool) => [...$pool, $r1], $uid);
$check('отметка из пула вернула liked', content_mark_posted($key, $uid) === 'liked');
$check('запись ушла из пула', !$pool_has($key, $uid));
$check('запись в liked', $where($key, $uid) === 'liked');
$h = history_load($uid);
$row = $in_collection($h, 'liked', $key);
$check('скрыта из «Предложки» (proposal_hidden)', !empty($row['proposal_hidden']));
$check('время публикации — published_at; posted_at (время поста источника) не тронут', !empty($row['published_at']) && (int)$row['posted_at'] === (int)$r1['posted_at']);
$check('файл в кэше не удалён', is_file($cached));

echo "В. Из «не знаю»:\n";
$uid2 = $make_user();
$r2 = $record('k2');
history_append('undecided', $r2, $uid2);
$check('из «не знаю» → liked', content_mark_posted($r2['image'], $uid2) === 'liked' && $where($r2['image'], $uid2) === 'liked');

echo "Г. Повтор ничего не меняет:\n";
$before = json_encode(history_load($uid));
$check('повторная отметка — already', content_mark_posted($key, $uid) === 'already');
$check('история после повтора не изменилась', json_encode(history_load($uid)) === $before);

echo "Д. Из «не понравилось» и «жалоб» — в «liked»:\n";
$r3 = $record('k3');
history_append('disliked', $r3, $uid2);
$r4 = $record('k4');
history_append('complaints', $r4, $uid2);
$check('из disliked → liked', content_mark_posted($r3['image'], $uid2) === 'liked' && $where($r3['image'], $uid2) === 'liked');
$check('из complaints → liked', content_mark_posted($r4['image'], $uid2) === 'liked' && $where($r4['image'], $uid2) === 'liked');

echo "Е. Неизвестный ключ:\n";
$check('неизвестный ключ — null, без ошибки', content_mark_posted('https://cdn.test/nope.jpg', $uid) === null);
$check('пустой ключ — null', content_mark_posted('', $uid) === null);

echo "Ж. «Предложка» скрывает отмеченное:\n";
$shown = fn($uid) => array_column(array_filter(proposal_items(history_load($uid)), fn($i) => true), 'image');
$check('отмеченная запись не показана в «Предложке»', !in_array($key, $shown($uid), true));
$check('отмеченная из «не знаю» не показана в «Предложке»', !in_array($r2['image'], $shown($uid2), true));

echo "З. Отмена (часть 2.4): возврат в «Предложку», остаётся в «liked»:\n";
$check('отмена вернула отметку', content_unmark_posted($key, $uid) === true);
$row = $in_collection(history_load($uid), 'liked', $key);
$check('в liked, без published_at, proposal_hidden снят', $row && empty($row['published_at']) && empty($row['proposal_hidden']));
$check('снова видна в «Предложке»', in_array($key, $shown($uid), true));
$check('отмена без отметки — false', content_unmark_posted($key, $uid) === false);

echo "И. Повторная отметка после отмены:\n";
$check('после отмены снова можно отметить', content_mark_posted($key, $uid) === 'liked');

echo "К. Очередь: ключ записи в посте и снятие при отмене:\n";
$work = MEDIA_CACHE_DIR . "/work-" . bin2hex(random_bytes(4));
@mkdir($work, 0775, true);
$queued = outbox_queue_post($uid, ['id' => 'chan-test', 'chat_title' => 'Тест'], 'Текст', [], time() + 3600, null, $work, null, null, null, $key);
$post = array_values(array_filter(outbox_load($uid), fn($e) => ($e['id'] ?? '') === $queued['id']))[0] ?? [];
$check('в посте очереди сохранён ключ записи', ($post['record'] ?? '') === $key);
outbox_remove_post($queued['id'], $uid);
$check('пост из очереди удалён', !array_filter(outbox_load($uid), fn($e) => ($e['id'] ?? '') === $queued['id']));
remove_dir($work);

foreach ($made as $u) {
    db_exec('DELETE FROM history_items WHERE user_id = ?', [$u]);
    db_exec('DELETE FROM pool_items WHERE user_id = ?', [$u]);
    db_exec('DELETE FROM users WHERE id = ?', [$u]);
}
@unlink(MEDIA_CACHE_DIR . '/posted-k1.jpg');
echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли.\n";
exit($failures ? 1 : 0);
