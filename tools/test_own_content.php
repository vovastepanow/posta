<?php
// Проверка «Своего контента» и API: php tools/test_own_content.php (нужна сеть для CSV с настоящими картинками). Всё созданное удаляется.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/content.php';
require_once __DIR__ . '/../handlers/sources.php';
require_once __DIR__ . '/../handlers/account.php';

// Заглушки ответов index.php: результат обработчика приходит исключением
class TestApiExit extends Exception { public function __construct(public $data, public int $status) { parent::__construct('api'); } }
function json_response($d, $s = 200): never { throw new TestApiExit($d, $s); }
function json_error($m, $s = 400): never { json_response(['status' => 'error', 'message' => $m], $s); }
function ok(array $e = []): never { json_response(['status' => 'success'] + $e); }

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

// Тестовый пользователь с темой
$uid = bin2hex(random_bytes(16));
$topic = 'тест-свой-контент';
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
app_user_bind($uid);
db_save_settings(['topic' => $topic] + db_load_settings($uid), $uid);
$tmp = [];
$cleanup = function () use ($uid, &$tmp) {
    foreach (pool_load($uid) as $item) foreach (media_cache_item_urls($item) as $u) if (($p = media_cache_path($u)) !== null) @unlink($p);
    foreach (history_load($uid) as $items) foreach ((array)$items as $item) foreach (media_cache_item_urls((array)$item) as $u) if (($p = media_cache_path($u)) !== null) @unlink($p);
    foreach (['pool_items', 'history_items', 'settings_items'] as $t) { try { db_exec("DELETE FROM $t WHERE user_id = ?", [$uid]); } catch (Throwable $e) { /* таблицы может не быть */ } }
    db_exec('DELETE FROM kv WHERE key LIKE ?', ["%$uid%"]);
    db_exec('DELETE FROM users WHERE id = ?', [$uid]);
    foreach ($tmp as $f) @unlink($f);
    update_source_db(function ($db) {
        foreach (array_keys($db['topics']) as $id) if (($db['topics'][$id]['title'] ?? '') === 'Тест-свой-контент') unset($db['topics'][$id]);
        return $db;
    });
};
register_shutdown_function($cleanup);

/** PNG заданного цвета во временном файле → запись для own_upload. */
function test_png($rgb, $name) {
    global $tmp;
    $im = imagecreatetruecolor(64, 48);
    imagefill($im, 0, 0, imagecolorallocate($im, ...$rgb));
    $path = tempnam(sys_get_temp_dir(), 'own');
    imagepng($im, $path);
    $tmp[] = $path;
    return ['name' => $name, 'tmp_name' => $path, 'error' => UPLOAD_ERR_OK, 'trusted' => true];
}
$mine = fn() => array_values(array_filter(pool_load($uid), fn($i) => !empty($i['own'])));

echo "D. Картинки:\n";
$r = own_upload([test_png([200, 30, 30], 'red.png'), test_png([30, 200, 30], 'green.png')], 'подпись', false);
$check('2 файла → 2 поста', $r['added'] === 2 && !$r['errors']);
$pool = $mine();
$check('в пуле 2 своих записи', count($pool) === 2);
$check('у записей есть image_local и файл на диске', !array_filter($pool, fn($i) => ($i['image_local'] ?? '') === '' || !is_file(media_cache_path($i['image_local']))));
$check('источник «Свой контент · имя файла», без ссылки на пост', ($pool[0]['source'] ?? '') !== '' && str_starts_with($pool[0]['source'], 'Свой контент · ') && source_post_url($pool[0]) === '');
$check('тема записи — тема пользователя', ($pool[0]['topic'] ?? '') === current_topic());
$r = own_upload([test_png([200, 30, 30], 'red.png')], '', false);
$check('тот же файл повторно не дублируется', $r['added'] === 0 && count($mine()) === 2);
$r = own_upload([test_png([30, 30, 200], 'a.png'), test_png([200, 200, 30], 'b.png')], 'альбом', true);
$album = $mine()[0] ?? [];
$check('альбом: один пост с галереей из 2 файлов', $r['added'] === 1 && count($album['gallery'] ?? []) === 2 && !empty($album['own']));
$video = ['name' => 'clip.mp4', 'tmp_name' => test_png([1, 2, 3], 'x')['tmp_name'], 'error' => UPLOAD_ERR_OK, 'trusted' => true];
$r = own_upload([$video, ['name' => 'big.png', 'tmp_name' => '', 'error' => UPLOAD_ERR_INI_SIZE]], '', false);
$check('видео отклоняется с понятным текстом', str_contains(implode(' ', $r['errors']), 'через редактор поста'));
$check('файл больше лимита сервера — «2 МБ»', str_contains(implode(' ', $r['errors']), '2 МБ'));

echo "D. CSV:\n";
$check('Windows-1251 и разделитель ,', own_csv_rows(mb_convert_encoding("текст,картинка\nпривет,https://a.ru/1.jpg\n", 'Windows-1251', 'UTF-8'))[0]['text'] === 'привет');
$check('BOM и ; (заголовки text;image)', own_csv_rows("\xEF\xBB\xBFtext;image\nhi;https://a.ru/1.jpg\n")[0]['image'] === 'https://a.ru/1.jpg');
$good1 = 'https://imgs.xkcd.com/comics/vulnerability.png';
$good2 = 'https://imgs.xkcd.com/comics/fusion_device.png';
$csv = "text;image\nпервый;$good1\nсломанный;https://127.0.0.1/none.png\nтретий;$good2\n";
$r = own_import_csv($csv);
$check('CSV: добавлено 2 (одна строка с битой ссылкой)', $r['added'] === 2);
$check('ошибка называет номер строки (3)', str_starts_with($r['errors'][0] ?? '', 'Строка 3:'));
$check('в пуле 2 + 2 + альбом = 5 своих записей', count($mine()) === 5);

echo "E. Рейтинги источников:\n";
$stats_before = source_feedback_stats(current_topic());
$own = $mine()[0];
pool_update(fn($pool) => apply_vote($pool, content_image_key($own), 'like', null, fn($r, $c) => history_append($c, $r)));
$liked = history_load($uid)['liked'] ?? [];
$check('«нравится» своей записи попало в историю', count(array_filter($liked, fn($r) => str_starts_with((string)($r['source_key'] ?? ''), 'own:'))) === 1);
$stats_after = source_feedback_stats(current_topic());
$check('source_feedback_stats не изменился и не знает ключа own:…', $stats_after == $stats_before && !array_filter(array_keys($stats_after), fn($k) => str_starts_with($k, 'own:')));
history_append('liked', ['image' => 'https://example.com/x.jpg', 'source_key' => 'telegram:test_own_probe', 'topic' => current_topic(), 'text' => '']);
$check('обычная запись по-прежнему считается (контроль)', isset(source_feedback_stats(current_topic())['telegram:test_own_probe']));

echo "F. API:\n";
$users_before = (int)db_value('SELECT COUNT(*) FROM users');
$call = function ($token, $body) {
    $_SERVER['HTTP_AUTHORIZATION'] = $token === null ? '' : 'Bearer ' . $token;
    try { handle_api_add_post(is_string($body) ? $body : json_encode($body)); } catch (TestApiExit $e) { return [$e->status, $e->data]; } catch (RuntimeException $e) { return [400, ['message' => $e->getMessage()]]; }   // как глобальный обработчик index.php
    return [0, null];
};
$token = api_token_create($uid);
$stored = kv_get('api_token:' . $uid);
$check('токен: 48 hex-символов, в базе только sha256', preg_match('/^[a-f0-9]{48}$/', $token) === 1 && $stored === hash('sha256', $token) && !str_contains((string)db_value('SELECT value FROM kv WHERE key = ?', ['api_token:' . $uid]), $token));
$check('токен находит пользователя', api_token_user($token) === $uid && api_token_user(str_repeat('0', 48)) === null);
[$st, $data] = $call(str_repeat('a', 48), ['text' => 'x']);
$check('неверный токен → 401 без подробностей', $st === 401 && $data['message'] === 'Неверный токен.');
[$st] = $call(null, ['text' => 'x']);
$check('без токена → 401', $st === 401);
$before = count($mine());
[$st, $data] = $call($token, ['text' => 'Пост из API', 'image_url' => 'https://imgs.xkcd.com/comics/momentum_demonstration.png']);
if ($st !== 200) echo '   ответ: ', json_encode([$st, $data], JSON_UNESCAPED_UNICODE), "\n";
$check('верный токен: пост добавлен', $st === 200 && ($data['added'] ?? 0) === 1 && count($mine()) === $before + 1);
$api_post = $mine()[0] ?? [];
$check('источник «Свой контент · API», текст сохранён', ($api_post['source'] ?? '') === 'Свой контент · API' && ($api_post['text'] ?? '') === 'Пост из API');
[$st, $data] = $call($token, ['text' => 'без картинки']);
$check('без картинки → понятная ошибка 400', $st === 400 && str_contains($data['message'], 'image_url'));
[$st, $data] = $call($token, ['image_url' => 'https://127.0.0.1/x.png']);
$check('внутренний адрес не скачивается', $st === 400);
$last = 0;
for ($i = 0; $i < 70 && $last !== 429; $i++) { [$last] = $call($token, '{}'); }
$check('сверх 60 запросов в час → 429', $last === 429 && (int)kv_get("api_rate:$uid:" . date('YmdH'), 0) === API_RATE_PER_HOUR + 1);
api_token_revoke($uid);
[$st] = $call($token, ['text' => 'x', 'image_url' => $good1]);
$check('после отзыва токен не работает (401)', $st === 401 && !api_token_exists($uid));
$check('API не создал пользователей', (int)db_value('SELECT COUNT(*) FROM users') === $users_before);

if (in_array('--only-own', $argv, true)) { echo $failures ? "Провалено: $failures\n" : "Всё прошло.\n"; exit($failures ? 1 : 0); }
echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
