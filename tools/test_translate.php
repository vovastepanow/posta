<?php
// Проверка автоперевода без сети: php tools/test_translate.php. Сетевой вызов подменён ($GLOBALS['translate_transport']).
// Тестовые записи кэша и настроек удаляются при выходе.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

class TestApiExit extends Exception { public function __construct(public $data, public int $status) { parent::__construct('api'); } }
if (!function_exists('json_response')) {
    function json_response($d, $s = 200): never { throw new TestApiExit($d, $s); }
    function json_error($m, $s = 400): never { json_response(['status' => 'error', 'message' => $m], $s); }
    function ok(array $e = []): never { json_response(['status' => 'success'] + $e); }
}
require_once __DIR__ . '/../handlers/content.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

$calls = 0;
$GLOBALS['translate_transport'] = function (string $url) use (&$calls) {
    $calls++;
    parse_str((string)parse_url($url, PHP_URL_QUERY), $q);
    $text = $q['q'] ?? '';
    // Имитация сервиса: переводим на «ру-…», сохраняем плейсхолдеры как есть
    $body = ['responseStatus' => 200, 'quotaFinished' => false, 'responseData' => ['translatedText' => 'Перевод: ' . $text, 'detectedLanguage' => 'en']];
    return ['status' => 200, 'body' => json_encode($body, JSON_UNESCAPED_UNICODE)];
};
$reset = function () { db_exec("DELETE FROM kv WHERE key LIKE 'translate:%' OR key = 'translate_quota_until'"); };
$reset();

// 1. Определение языка без сети
$check('русский → ru', text_language_guess('Это обычный русский текст для проверки языка') === 'ru');
$check('украинский с «ї» → uk', text_language_guess('Це українська мова, дуже гарна їжа і їжак') === 'uk');
$check('английский → ? (определит сервис)', text_language_guess('This is a short English sentence for testing') === '?');
$check('«😂😂 https://x.com/a» → null (букв мало)', text_language_guess('😂😂 https://x.com/a') === null);

// 2. Русский текст не уходит в сеть
$before = $calls;
$check('русский текст — null и без сети', translate_to_russian('Это обычный русский текст для проверки языка') === null && $calls === $before);

// 3. Нарезка: 1300 символов, три абзаца
$para = str_repeat('Hello world and more words here. ', 12);   // ~400 символов
$long = trim($para) . "\n\n" . trim($para) . "\n\n" . trim($para);
$chunks = translate_chunks($long);
$check('куски ≤ 450 символов', $chunks && max(array_map(fn($c) => mb_strlen($c[0]), $chunks)) <= 450);
$rejoin = implode('', array_map(fn($c) => $c[1] . $c[0], $chunks));
$check('ни одно слово не разрезано: последовательность слов та же', preg_split('/\s+/u', trim($rejoin)) === preg_split('/\s+/u', trim($long)));
$check('абзацы склеиваются обратно с переносами', substr_count($rejoin, "\n\n") === 2 && str_replace(["\n\n", ' '], '', $rejoin) === str_replace(["\n\n", ' '], '', $long));

// 4. Ссылки, @ник, #тег остаются как были
$reset();
$calls = 0;
$msg = 'Look at this new video from the channel @newsbot #tech https://example.com/post/42 it is really interesting for everyone';
$r = translate_to_russian($msg);
$check('перевод есть', $r !== null && $r['lang'] === 'en');
$check('ссылка, @ник и #тег на месте', $r && str_contains($r['text'], 'https://example.com/post/42') && str_contains($r['text'], '@newsbot') && str_contains($r['text'], '#tech'));
$check('плейсхолдеров в результате не осталось', $r && !str_contains($r['text'], 'ZXQ'));

// 5. Второй вызов того же текста — из кэша
$n = $calls;
$again = translate_to_russian($msg);
$check('второй вызов — из кэша, сеть не дергается', $again !== null && $calls === $n && $again['text'] === $r['text']);

// 6. Квота: quotaFinished → null и до конца суток сеть не вызывается
$reset();
$GLOBALS['translate_transport'] = function (string $url) use (&$calls) {
    $calls++;
    return ['status' => 200, 'body' => json_encode(['responseStatus' => 200, 'quotaFinished' => true, 'responseData' => ['translatedText' => '']])];
};
$calls = 0;
$q1 = translate_to_russian('Quota test sentence that is long enough to translate here');
$q2 = translate_to_russian('Another sentence that is also long enough to be translated');
$check('квота исчерпана → null', $q1 === null);
$check('до конца суток сеть не вызывается', $q2 === null && $calls === 1);
$reset();

// 7. Превышение бюджета → null
$GLOBALS['translate_transport'] = function (string $url) use (&$calls) { $calls++; return ['status' => 200, 'body' => json_encode(['responseStatus' => 200, 'responseData' => ['translatedText' => 'x', 'detectedLanguage' => 'en']])]; };
$calls = 0;
$check('бюджет исчерпан → null', translate_to_russian('Budget test sentence that is long enough to translate', -1.0) === null);

// 8. Сохранение фильтров: один auto_translate не перезапускает парсер
$uid = bin2hex(random_bytes(16));
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
app_user_bind($uid);
$check('пользователь без ключа auto_translate → true', load_settings()['auto_translate'] === true);
$s = db_load_settings($uid); unset($s['auto_translate']); db_save_settings($s, $uid);
$check('старые настройки без ключа → true', load_settings($uid)['auto_translate'] === true);
try { handle_save_filters(['auto_translate' => 'false']); $res = null; } catch (TestApiExit $e) { $res = $e->data; }
$check('сохранение только auto_translate: parser = unchanged', is_array($res) && ($res['parser'] ?? '') === 'unchanged' && ($res['filters']['auto_translate'] ?? true) === false);

// Уборка
db_exec('DELETE FROM users WHERE id = ?', [$uid]);
db_exec('DELETE FROM kv WHERE key LIKE ?', ['%' . $uid . '%']);
$reset();

echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
