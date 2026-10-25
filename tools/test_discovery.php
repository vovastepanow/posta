<?php
// Проверка автоподбора источников: php tools/test_discovery.php — только проверки без сети и модели.
// Живой 15-минутный прогон с моделью и сетью — php tools/test_discovery.php --live [--limit=N] (только когда парсер не идёт; --force — обойти проверку).
// Тема — «тест-котики»; её записи в каталоге удаляются при любом завершении, в том числе по SIGINT/SIGTERM.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/sources.php';

// Заглушки ответов index.php: результат обработчика приходит исключением
class TestApiExit extends Exception { public function __construct(public $data, public int $status) { parent::__construct('api'); } }
function json_response($d, $s = 200): never { throw new TestApiExit($d, $s); }
function json_error($m, $s = 400): never { json_response(['status' => 'error', 'message' => $m], $s); }
function ok(array $e = []): never { json_response(['status' => 'success'] + $e); }

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$limit = 10;
foreach ($argv as $arg) if (preg_match('~^--limit=(\d+)$~', $arg, $m)) $limit = (int)$m[1];
$topic = 'тест-котики';

$uid = bin2hex(random_bytes(16));
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
app_user_bind($uid);
db_save_settings(['topic' => $topic] + db_load_settings($uid), $uid);

/** Всё, что тест записал в каталог, предложения, голоса и отметки проверок — удаляется. */
$cleanup = function () use ($uid, $topic) {
    $prefix = topic_norm($topic) . '|';
    update_source_db(function ($db) use ($topic, $prefix) {
        foreach (array_merge(CATALOG_KEYS, []) as $key) $db[$key] = array_values(array_filter($db[$key], fn($e) => !in_array($topic, (array)($e['topics'] ?? []), true)));
        $db['source_suggestions'] = array_values(array_filter($db['source_suggestions'], fn($s) => !topics_are_equivalent((string)($s['topic'] ?? ''), $topic)));
        $db['source_votes'] = array_values(array_filter($db['source_votes'], fn($v) => ($v['topic'] ?? '') === '' || !topic_same((string)$v['topic'], $topic)));
        $db['discovery']['checked_at'] = array_filter((array)($db['discovery']['checked_at'] ?? []), fn($k) => !str_starts_with((string)$k, $prefix), ARRAY_FILTER_USE_KEY);
        unset($db['discovery']['runs'][mb_strtolower(trim($topic))]);
        return $db;
    });
    db_exec('DELETE FROM kv WHERE key LIKE ?', ["%$uid%"]);
    db_exec('DELETE FROM users WHERE id = ?', [$uid]);
};
register_shutdown_function($cleanup);
// Прерывание (Ctrl+C, kill) — выход через shutdown, уборка выполняется
if (function_exists('pcntl_signal')) { pcntl_async_signals(true); foreach ([SIGINT, SIGTERM] as $sig) pcntl_signal($sig, fn() => exit(130)); }

/** Парсер владельца сейчас работает (процесс cron_parser.php). */
function test_parser_running(): bool { exec('pgrep -f ' . escapeshellarg('^/usr/bin/php .*cron_parser\.php'), $pids, $code); return $code === 0; }

/** Ответ обработчика подбора для фильтра площадки. */
$suggest = function (string $platform) {
    try { handle_suggest_sources(['platform' => $platform, 'skip' => []]); } catch (TestApiExit $e) { return $e->data; }
    return [];
};

echo "A. Фильтр и честные сообщения:\n";
$youtube = $suggest('youtube');
$check('YouTube: без находок — «Пока ничего нового» и действие «discover»', ($youtube['action'] ?? '') === 'discover' && str_contains($youtube['message'] ?? '', 'Пока ничего нового'));
$shorts = $suggest('shorts');
$check('YouTube Shorts: фильтр принимается, тоже «discover»', ($shorts['action'] ?? '') === 'discover');
$pinterest = $suggest('pinterest');
$check('Pinterest: сами не находятся — действие «add_link»', ($pinterest['action'] ?? '') === 'add_link' && str_contains($pinterest['message'] ?? '', 'Добавить источник'));
$tumblr = $suggest('tumblr');
$check('Tumblr: действие «add_link»', ($tumblr['action'] ?? '') === 'add_link');
$rss = $suggest('rss');
$check('RSS и сайты: только обычные ленты, без поиска — «add_link»', ($rss['action'] ?? '') === 'add_link');
$vk = $suggest('vk');
$check('VK не подключён — «connect_vk» с понятным текстом', ($vk['action'] ?? '') === 'connect_vk' && str_contains($vk['message'] ?? '', 'Подключите VK'));
$check('TikTok: поиск есть — не тупик «add_link» (без сети: решение по площадке)', source_auto_searchable(PLATFORM_TIKTOK));
$any = $suggest('any');
$check('фильтр «Все источники» отвечает находкой или пояснением', array_key_exists('discovered', $any));
$unknown = $suggest('nosuch');
$check('неизвестная площадка — понятная ошибка', str_contains($unknown['message'] ?? '', 'Неизвестная платформа'));

if (in_array('--only-a', $argv, true)) { echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n"; exit($failures ? 1 : 0); }

echo "B. Порядок проверок и ссылки (без сети):\n";
$balanced = discovery_balance([['a1', 'a2', 'a3', 'a4', 'a5', 'a6', 'a7', 'a8'], ['b1', 'b2'], ['c1', 'c2', 'c3', 'c4', 'c5']], 10, DISCOVERY_MIN_CHECKS_PER_PLATFORM);
$check('баланс: каждой площадке — минимум 4 проверки, всего 10', count($balanced) === 10 && count(array_filter($balanced, fn($x) => $x[0] === 'a')) === 4 && count(array_filter($balanced, fn($x) => $x[0] === 'c')) === 4);
$check('баланс: площадка с двумя кандидатами получает оба', in_array('b1', $balanced, true) && in_array('b2', $balanced, true));
$check('баланс: пусто — пусто', discovery_balance([], 10, 4) === []);
$links = source_linked_sources('t.me/durov t.me/memesdaily vk.com/team tiktok.com/@kote_kitty ok.ru/group/53038939046008 youtube.com/@YouTube pinterest.com/pinterest/ x.tumblr.com');
$platforms = array_column($links, 'platform');
$check('ссылки: t.me (служебные пути мимо), VK, TikTok, ОК, YouTube, Pinterest, Tumblr', in_array(PLATFORM_TELEGRAM, $platforms, true) && in_array(PLATFORM_VK, $platforms, true)
    && in_array(PLATFORM_TIKTOK, $platforms, true) && in_array(PLATFORM_OK, $platforms, true) && count($links) === 7);
$check('ссылки: служебный путь t.me/durov не попадает', !in_array('durov', array_column($links, 'id'), true));

echo "C. Очередь по кругу площадок (без сети):\n";
$q = discovery_queue_new();
foreach (range(1, 40) as $i) discovery_queue_add($q, 'telegram', ['platform' => PLATFORM_TELEGRAM, 'id' => "t$i", 'origin' => 'поиск', 'depth' => 0]);
foreach (['UCy1', 'UCy2'] as $cid) discovery_queue_add($q, 'youtube', ['platform' => PLATFORM_RSS, 'id' => "https://www.youtube.com/feeds/videos.xml?channel_id=$cid", 'origin' => 'поиск', 'depth' => 0]);
foreach (range(1, 13) as $i) discovery_queue_add($q, 'ok', ['platform' => PLATFORM_OK, 'id' => "o$i", 'origin' => 'лента', 'depth' => 0]);
$head = [discovery_queue_next($q)];   // первая принятая — снежный ком ниже
for ($i = 0; $i < 10; $i++) discovery_queue_add($q, 'telegram', ['platform' => PLATFORM_TELEGRAM, 'id' => "sn$i", 'origin' => 'похож', 'depth' => 1]);
for ($i = 0; $i < 15; $i++) $head[] = discovery_queue_next($q);
$groups = array_unique(array_map(fn($c) => discovery_group_of($c['platform'], (string)$c['id']), $head));
$check('в первых 16 проверках есть все три площадки', count($groups) === 3);
$check('ОК не отстаёт: в первых 16 не меньше ' . DISCOVERY_MIN_CHECKS_PER_PLATFORM . ' проверок', count(array_filter($head, fn($c) => $c['platform'] === PLATFORM_OK)) >= DISCOVERY_MIN_CHECKS_PER_PLATFORM);
$check('снежный ком из Telegram не попадает в первые 16 раньше ОК', !in_array('sn0', array_column($head, 'id'), true));
$check('группы площадок — по порядку круга, ссылки на YouTube в свою группу', discovery_group_of(PLATFORM_RSS, 'https://www.youtube.com/feeds/videos.xml?channel_id=UCy1') === 'youtube' && discovery_group_of(PLATFORM_RSS, 'https://pinterest.com/x/') === 'links');
$lines = discovery_summary_lines(['telegram' => ['found' => 48, 'checked' => 12, 'accepted' => 4, 'rejected' => 6, 'failed' => 2]], false);
$check('сводка: строка по площадке и «не подключён» для VK', str_starts_with($lines[0], 'Telegram: найдено 48, проверено 12, принято 4, отказ 6, не загрузилось 2, известных пропущено ') && in_array('ВКонтакте: не подключён', $lines, true));

echo "D. Отложенные кандидаты t.me (kv, тестовая тема):\n";
$pq = discovery_queue_new();
discovery_queue_add($pq, 'telegram', ['platform' => PLATFORM_TELEGRAM, 'id' => 'plain', 'origin' => 'поиск', 'depth' => 0]);
discovery_queue_add($pq, 'telegram', ['platform' => PLATFORM_TELEGRAM, 'id' => 'deferred', 'origin' => 'тест', 'depth' => 1, 'deferred' => true], true);
$check('отложенный кандидат идёт первым в своей группе', discovery_queue_next($pq)['id'] === 'deferred');
$dc = ['platform' => PLATFORM_TELEGRAM, 'id' => 'testdefer', 'origin' => 'тест', 'depth' => 0];
discovery_defer($topic, $dc);
discovery_defer($topic, $dc);
$taken = discovery_take_deferred($topic);
$check('отложенный кандидат записан один раз, без дублей', count($taken) === 1 && $taken[0]['id'] === 'testdefer');
$check('после взятия список темы пуст', discovery_take_deferred($topic) === []);

echo "E. Снежный ком не уходит от темы (без модели):\n";
$check('«MALUNA | Свечи и косметика» (глубина ≥1) не проходит по названию и описанию', discovery_title_hit('Бизнес', 'MALUNA | Свечи и косметика', 'Свечи и косметика') === false);
$check('«Девичник XSIZE» не проходит по названию', discovery_title_hit('Бизнес', 'Девичник XSIZE', '') === false);
$check('канал со словом темы в названии проходит', discovery_title_hit('Бизнес', 'Бизнес-клуб', '') === true);
$check('на глубине 2 порог выше на DISCOVERY_DEEP_EXTRA_SCORE', DISCOVERY_DEEP_EXTRA_SCORE === 10);

if (in_array('--live', $argv, true)) {
    if (test_parser_running() && !in_array('--force', $argv, true)) { echo "Парсер сейчас работает — живой прогон пропущен (--force — запустить всё равно).\n"; $cleanup(); exit(2); }
    echo "B. Прогон на «{$topic}» (лимит проверок: {$limit}); журнал — выше и в файле при запуске через tee:\n";
    $before = [];
    run_source_discovery_locked($topic, $limit);
    $after = load_source_db();
    foreach (CATALOG_KEYS as $platform => $key) {
        $n = count(array_filter($after[$key], fn($e) => in_array($topic, (array)($e['topics'] ?? []), true)));
        echo "  каталог «{$key}» по теме: {$n}\n";
    }
    echo '  предложения по теме: ' . count(array_filter($after['source_suggestions'], fn($x) => topics_are_equivalent((string)($x['topic'] ?? ''), $topic))) . "\n";
}

$cleanup();
echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
