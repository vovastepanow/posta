<?php
// Файлы поста очереди (CALENDAR_MEDIA_FIX_PROMPT.md): копирование из кэша, копия поста, заглушка в подписи, миниатюры, ремонт.
// Без настоящего Telegram. Запускать на копии проекта с копией базы и отдельной папкой медиа: см. POSTA_MEDIA_CACHE_DIR.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

if (MEDIA_CACHE_DIR === '/var/lib/posta/media') exit("Тест запущен с настоящей папкой медиа: задайте POSTA_MEDIA_CACHE_DIR\n");
if (!function_exists('imagecreatetruecolor')) exit("Нужен PHP GD для теста.\n");

$failures = 0;
$check = function ($name, $ok) use (&$failures) {
    echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n";
    if (!$ok) $failures++;
};

$uid = bin2hex(random_bytes(16));
$other = bin2hex(random_bytes(16));
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$other, time()]);
app_user_bind($uid);

// Картинка в кэше сайта: адрес в кэше, как у записей Предложки
$url = MEDIA_CACHE_URL . '/' . md5('outbox-media-test') . '.jpg';
$cached = media_cache_path($url);
@mkdir(dirname($cached), 0775, true);
$img = imagecreatetruecolor(800, 600);
imagejpeg($img, $cached, 90);
$work = telegram_work_dir();
$queued_ids = [];
$thumbs = [];
$channel = ['id' => 'chan-test', 'chat_title' => 'Тест'];

echo "A. Постановка с картинкой из кэша:\n";
$prepared = telegram_prepare_media([['kind' => 'photo', 'src' => $url, 'name' => 'тест']], [], $work);
$check('JPEG без пережатия — путь указывает на кэш (причина пропажи файлов)', $prepared[0]['path'] === $cached);
$queued = outbox_queue_post($uid, $channel, 'Пост без текста', $prepared, time() + 3600, null, $work);
$queued_ids[] = $queued['id'];
$post = array_values(array_filter(outbox_load($uid), fn($e) => ($e['id'] ?? '') === $queued['id']))[0] ?? [];
$dir = outbox_media_dir($queued['id']);
$check('файл скопирован в папку поста', is_file($dir . '/' . basename($cached)));
$check('путь поста указывает в папку поста', str_starts_with((string)($post['media'][0]['path'] ?? ''), $dir . '/'));
$check('файл в кэше сайта не тронут', is_file($cached));
$check('заглушка «Пост без текста» в очереди очищена', ($post['text'] ?? 'x') === '');
$check('миниатюра создана и записана в пост', ($post['thumb_id'] ?? '') !== '' && is_file(thumb_file($post['thumb_id'])));
$thumbs[] = $post['thumb_id'] ?? '';
$check('миниатюра своя: владелец видит', thumb_owned($post['thumb_id'] ?? '', $uid));
$check('миниатюра чужая: другой пользователь не видит', !thumb_owned($post['thumb_id'] ?? '', $other));
$check('неверный id миниатюры — не отдаётся', !thumb_owned('../../etc', $uid));
$check('миниатюра меньше 160 px', ($size = @getimagesize(thumb_file($post['thumb_id'] ?? ''))) && max($size[0], $size[1]) <= THUMB_SIDE);

echo "Б. Копия поста:\n";
$copy = outbox_duplicate_post($queued['id'], time() + 7200, $uid);
$check('копия создана', $copy !== null);
$queued_ids[] = $copy['id'] ?? '';
$check('файл копии есть в папке копии', $copy && is_file(outbox_media_dir($copy['id']) . '/' . basename($cached)));
$check('копия ссылается на ту же миниатюру', ($copy['thumb_id'] ?? '') === ($post['thumb_id'] ?? '!'));

echo "В. Файл пропал до постановки — ошибка сразу:\n";
$bad = [['kind' => 'photo', 'path' => '/nonexistent/none.jpg', 'name' => 'x', 'src' => '']];
$failed_at_queue = false;
try { outbox_queue_post($uid, $channel, 'текст', $bad, time() + 3600, null, $work); }
catch (RuntimeException $e) { $failed_at_queue = str_contains($e->getMessage(), 'Не удалось сохранить файлы поста'); }
$check('постановка без файла отклонена с понятной ошибкой', $failed_at_queue);

echo "Г. Ремонт: файл пропал, заглушка в подписи:\n";
$repair_id = $queued['id'];
unlink($dir . '/' . basename($cached));
outbox_update(fn($q) => array_map(fn($e) => ($e['id'] ?? '') === $repair_id ? array_merge($e, ['text' => 'Пост без текста']) : $e, $q), $uid);
$dry = implode("\n", outbox_repair(false, $uid));
$check('dry-run: файл найден в кэше, но не скопирован', !is_file($dir . '/' . basename($cached)) && str_contains($dry, 'будет скопирован'));
$check('dry-run: заглушка отмечена', str_contains($dry, 'заглушка в подписи очищена'));
$check('dry-run: текст не изменён', (string)(array_values(array_filter(outbox_load($uid), fn($e) => ($e['id'] ?? '') === $repair_id))[0]['text'] ?? '') === 'Пост без текста');
outbox_repair(true, $uid);
$fixed = array_values(array_filter(outbox_load($uid), fn($e) => ($e['id'] ?? '') === $repair_id))[0] ?? [];
$check('ремонт: файл поста восстановлен из кэша', is_file($dir . '/' . basename($cached)));
$check('ремонт: заглушка очищена', ($fixed['text'] ?? 'x') === '');

echo "Д. Пост «Файлы поста не найдены» с будущим временем — снова в очередь:\n";
outbox_update(fn($q) => array_map(fn($e) => ($e['id'] ?? '') === $repair_id ? array_merge($e, ['status' => 'failed', 'error' => 'Файлы поста не найдены.']) : $e, $q), $uid);
unlink($dir . '/' . basename($cached));
outbox_repair(true, $uid);
$again = array_values(array_filter(outbox_load($uid), fn($e) => ($e['id'] ?? '') === $repair_id))[0] ?? [];
$check('вернулся в очередь', ($again['status'] ?? '') === 'queued' && ($again['error'] ?? 'x') === '');

echo "Е. Прошедшее время — остаётся «не вышел»:\n";
outbox_update(fn($q) => array_map(fn($e) => ($e['id'] ?? '') === $repair_id ? array_merge($e, ['status' => 'failed', 'error' => 'Файлы поста не найдены.', 'publish_at' => time() - 600]) : $e, $q), $uid);
$late = implode("\n", outbox_repair(true, $uid));
$still = array_values(array_filter(outbox_load($uid), fn($e) => ($e['id'] ?? '') === $repair_id))[0] ?? [];
$check('прошедшее время — не переводится в очередь', ($still['status'] ?? '') === 'failed' && str_contains($late, 'остаётся «не вышел»'));

echo "Ж. Видео: миниатюра — кадр:\n";
if (ffmpeg_binary() === '') {
    echo "SKIP без ffmpeg\n";
} else {
    $video_dir = sys_get_temp_dir() . '/posta-om-' . bin2hex(random_bytes(4));   // отдельно: $work уже убрана при постановке
    @mkdir($video_dir, 0775, true);
    $video = $video_dir . '/clip.mp4';
    $made = ffmpeg_run('-f lavfi -i testsrc=duration=2:size=320x240:rate=10 -c:v libx264 -pix_fmt yuv420p', $video, 60);
    $check('тестовое видео собрано', $made);
    $vid = post_thumb_make([['kind' => 'video', 'path' => $video]]);
    $thumbs[] = $vid;
    $check('миниатюра видео — кадр', $vid !== '' && is_file(thumb_file($vid)));
}

echo "З. Чистка миниатюр: свежие не трогаются:\n";
$check('свежие миниатюры не удаляются', thumb_gc() === 0 && is_file(thumb_file($thumbs[0])));

// Уборка: посты, миниатюры, файлы, пользователи
foreach ($queued_ids as $id) if ($id !== '') remove_dir(outbox_media_dir($id));
foreach ($thumbs as $t) if ($t !== '') @unlink(thumb_file($t));
if (isset($video_dir)) remove_dir($video_dir);
@unlink($cached);
remove_dir($work);
foreach ([$uid, $other] as $u) {
    db_exec('DELETE FROM outbox WHERE user_id = ?', [$u]);
    db_exec('DELETE FROM users WHERE id = ?', [$u]);
}
echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли.\n";
exit($failures ? 1 : 0);
