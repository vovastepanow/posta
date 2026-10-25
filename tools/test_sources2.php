<?php
// Проверка второй волны источников: php tools/test_sources2.php. Строки с пометкой [сеть] ходят во внешние сайты. Всё созданное удаляется.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/content.php';
require_once __DIR__ . '/../handlers/sources.php';

// Заглушки ответов index.php: результат обработчика приходит исключением
class TestApiExit extends Exception { public function __construct(public $data, public int $status) { parent::__construct('api'); } }
function json_response($d, $s = 200): never { throw new TestApiExit($d, $s); }
function json_error($m, $s = 400): never { json_response(['status' => 'error', 'message' => $m], $s); }
function ok(array $e = []): never { json_response(['status' => 'success'] + $e); }

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
/** Функция бросает RuntimeException, в тексте которого есть $part. */
$throws = function (callable $fn, string $part) { try { $fn(); return false; } catch (RuntimeException $e) { return str_contains($e->getMessage(), $part); } };

// Тестовый пользователь с темой: источники добавляются в каталог под этой темой и удаляются после проверки
$topic = 'тест-источники';
$uid = bin2hex(random_bytes(16));
db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
app_user_bind($uid);
db_save_settings(['topic' => $topic] + db_load_settings($uid), $uid);
$cleanup = function () use ($uid, $topic) {
    foreach (pool_load($uid) as $item) foreach (media_cache_item_urls($item) as $u) if (($p = media_cache_path($u)) !== null) @unlink($p);
    foreach (['pool_items', 'history_items', 'settings_items', 'mirrors'] as $t) { try { db_exec("DELETE FROM $t WHERE user_id = ?", [$uid]); } catch (Throwable $e) { /* таблицы может не быть */ } }
    update_source_db(function ($db) use ($topic) {
        foreach (['telegram_channels', 'vk_publics', 'rss_feeds', 'ok_groups'] as $key) $db[$key] = array_values(array_filter($db[$key], fn($e) => !in_array($topic, (array)($e['topics'] ?? []), true)));
        $db['source_votes'] = array_values(array_filter($db['source_votes'], fn($v) => ($v['topic'] ?? '') !== $topic));
        return $db;
    });
    db_exec('DELETE FROM kv WHERE key LIKE ?', ["%$uid%"]);
    db_exec('DELETE FROM users WHERE id = ?', [$uid]);
};
register_shutdown_function($cleanup);

echo "A. Ссылки и ленты YouTube, Pinterest, Tumblr:\n";
$cases = [
    ['https://www.tumblr.com/Staff', 'https://staff.tumblr.com/rss', 'tumblr.com/имя'],
    ['https://ru.pinterest.com/user/board/', 'https://www.pinterest.com/user/board.rss', 'pinterest доска'],
    ['https://www.pinterest.com/user/', 'https://www.pinterest.com/user/feed.rss', 'pinterest профиль'],
    ['https://www.youtube.com/channel/UCBR8-60-B28hp2BmDPdntcQ', 'https://www.youtube.com/feeds/videos.xml?channel_id=UCBR8-60-B28hp2BmDPdntcQ', 'youtube /channel/'],
];
foreach ($cases as [$link, $feed, $label]) $check("$label → лента", rss_known_feed($link) === $feed);
$check('обычный сайт не распознаётся как особый', rss_known_feed('https://habr.com/ru/') === null);
$check('ссылка на пин Pinterest — понятная ошибка', $throws(fn() => rss_known_feed('https://www.pinterest.com/pin/12345/'), 'Нужна ссылка на профиль или доску Pinterest'));
$check('поиск Pinterest — понятная ошибка', $throws(fn() => rss_known_feed('https://www.pinterest.com/search/pins/?q=cats'), 'Нужна ссылка на профиль или доску Pinterest'));
foreach (['https://youtu.be/dQw4w9WgXcQ', 'https://www.youtube.com/watch?v=dQw4w9WgXcQ', 'https://www.youtube.com/shorts/6yX9tINzYf0'] as $video) {
    $check("видео YouTube $video → ошибка", $throws(fn() => source_parse_link($video), 'Нужна ссылка на канал YouTube'));
}
$check('Tumblr с плохим именем — ошибка', $throws(fn() => rss_known_feed('https://tumblr.com/'), 'Нужна ссылка на блог Tumblr'));
$check('метка площадки: YouTube', rss_feed_kind('https://www.youtube.com/feeds/videos.xml?channel_id=UCBR8-60-B28hp2BmDPdntcQ') === 'YouTube');
$check('метка площадки: Pinterest', rss_feed_kind('https://www.pinterest.com/user/board.rss') === 'Pinterest');
$check('метка площадки: Tumblr', rss_feed_kind('https://staff.tumblr.com/rss') === 'Tumblr');
$check('метка площадки: обычный RSS', rss_feed_kind('https://xkcd.com/atom.xml') === 'RSS');
$check('Pinterest: 236x → 736x', rss_upgrade_image_url('https://i.pinimg.com/236x/aa/bb/cc/dd.jpg') === 'https://i.pinimg.com/736x/aa/bb/cc/dd.jpg');
$check('не-Pinterest картинка не меняется', rss_upgrade_image_url('https://example.com/236x/a.jpg') === 'https://example.com/236x/a.jpg');

// Разбор лент без сети: превью YouTube лежит в media:group, ссылка на ролик — в конце текста
$yt_feed = 'https://www.youtube.com/feeds/videos.xml?channel_id=UCBR8-60-B28hp2BmDPdntcQ';
$yt = '<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns:yt="http://www.youtube.com/xml/schemas/2015" xmlns:media="http://search.yahoo.com/mrss/" xmlns="http://www.w3.org/2005/Atom">
<title>Тест</title>
<entry>
<yt:videoId>abcDEF12345</yt:videoId>
<title>Видео про котов</title>
<link rel="alternate" href="https://www.youtube.com/watch?v=abcDEF12345"/>
<published>2026-10-20T10:00:00+00:00</published>
<media:group>
<media:content url="https://www.youtube.com/v/abcDEF12345?version=3" type="application/x-shockwave-flash" width="640" height="390"/>
<media:thumbnail url="https://i2.ytimg.com/vi/abcDEF12345/hqdefault.jpg" width="480" height="360"/>
<media:description>Описание ролика</media:description>
</media:group>
</entry>
</feed>';
$yt_posts = rss_parse($yt, 5, 'YouTube | тест', $yt_feed);
$check('YouTube: превью из media:group', count($yt_posts) === 1 && $yt_posts[0]['image'] === 'https://i2.ytimg.com/vi/abcDEF12345/hqdefault.jpg');
$check('YouTube: в конце текста ссылка на ролик', str_ends_with($yt_posts[0]['text'] ?? '', "\nhttps://www.youtube.com/watch?v=abcDEF12345"));
$check('YouTube: видео не скачивается', ($yt_posts[0]['video'] ?? 'x') === '');

// Pinterest: миниатюра 236x в описании заменяется на 736x
$pin = '<?xml version="1.0"?><rss version="2.0"><channel><title>Доска</title><item><title>Пин</title><link>https://www.pinterest.com/pin/111/</link>'
    . '<pubDate>Mon, 20 Oct 2026 10:00:00 +0000</pubDate><description><![CDATA[<img src="https://i.pinimg.com/236x/aa/bb/cc/dd.jpg" />Текст]]></description></item></channel></rss>';
$pin_posts = rss_parse($pin, 5, 'Pinterest | доска', 'https://www.pinterest.com/user/board.rss');
$check('Pinterest: картинка 736x', count($pin_posts) === 1 && $pin_posts[0]['image'] === 'https://i.pinimg.com/736x/aa/bb/cc/dd.jpg');

// Внешние сущности не раскрываются: в тексте не появляется содержимое /etc/passwd
$xxe = '<?xml version="1.0"?><!DOCTYPE rss [<!ENTITY e SYSTEM "file:///etc/passwd">]><rss version="2.0"><channel><title>x</title>'
    . '<item><title>&e;</title><link>https://example.com/a</link><enclosure url="https://example.com/a.jpg" type="image/jpeg"/></item></channel></rss>';
$xxe_posts = rss_parse($xxe, 5, '', 'https://example.com/feed');
$check('XXE: /etc/passwd не читается', !str_contains(json_encode($xxe_posts), 'root:'));

echo "B. Одноклассники без сети:\n";
$check('ссылка на группу ОК → id группы', ok_group_id('https://ok.ru/group/53038939046008') === '53038939046008');
$check('мобильная ссылка ОК → id группы', ok_group_id('https://m.ok.ru/dk?st.cmd=altGroupMain&st.groupId=53038939046008') === '53038939046008');
$check('профиль ОК — ошибка «нужна группа»', $throws(fn() => ok_group_id('https://ok.ru/profile/1'), 'Нужна ссылка на группу Одноклассников'));
$check('видео ОК — ошибка «нужна группа»', $throws(fn() => ok_group_id('https://ok.ru/video/123'), 'Нужна ссылка на группу Одноклассников'));
$check('ссылка на группу ОК не уходит в rss_discover (источник ок)', source_parse_link('https://ok.ru/group/53038939046008') === ['platform' => PLATFORM_OK, 'id' => '53038939046008']);
$check('ключ источника ОК: ok:<id>', source_id_for(PLATFORM_OK, '53038939046008') === 'ok:53038939046008');
$check('id группы ОК: только 10–20 цифр', is_valid_source_id(PLATFORM_OK, '53038939046008') && !is_valid_source_id(PLATFORM_OK, '123') && !is_valid_source_id(PLATFORM_OK, 'club1'));
$check('сайт ОК — доверенный хост для ссылки «Открыть пост»', is_trusted_source_url('https://ok.ru/group/53038939046008/topic/1'));

// Даты: относительные от фиксированного «сейчас» (UTC — часовой пояс приложения по умолчанию)
$now = strtotime('2026-10-23 12:00:00 UTC');
$check('дата «вчера в 14:05»', ok_parse_date('вчера в 14:05', $now) === strtotime('2026-10-22 14:05:00 UTC'));
$check('дата «сегодня 14:05»', ok_parse_date('сегодня 14:05', $now) === strtotime('2026-10-23 14:05:00 UTC'));
$check('дата «12 окт» — текущий год', ok_parse_date('12 окт', $now) === strtotime('2026-10-12 00:00:00 UTC'));
$check('дата «12 окт 2025 в 09:30»', ok_parse_date('12 окт 2025 в 09:30', $now) === strtotime('2025-10-12 09:30:00 UTC'));
$check('дата «18 мар» в марте 2026 — тот же год', ok_parse_date('18 мар', strtotime('2026-03-20 00:00:00 UTC')) === strtotime('2026-03-18 00:00:00 UTC'));
$check('дата «18 мар» в январе — прошлый год', ok_parse_date('18 мар', strtotime('2026-01-10 00:00:00 UTC')) === strtotime('2025-03-18 00:00:00 UTC'));
$check('непонятная дата → 0', ok_parse_date('ерунда', $now) === 0);

// Записи мобильной страницы: без картинки и чужие картинки пропускаются, счётчики и ссылка — на месте
$ok_html = '<html><head><title>Тест группы | OK.RU</title></head><body>'
    . '<li class="item it feed-card js-call" data-log-click="{&quot;topicId&quot;:&quot;111222333&quot;}">'
    . '<div class="tstmp feed_descr">12 окт 2025</div><div class="topic-text_content __new-line"><h2>Заголовок</h2> текст поста</div>'
    . '<a href="/x" class="topic-more">Показать ещё</a><div class="widget-list_actions">'
    . '<span class="widget-list_i __comment"><span class="widget_action_count">6</span></span>'
    . '<span class="widget-list_i __reshare"><span class="widget_action_count">4</span></span>'
    . '<span class="widget-list_i __like"><span class="widget_action_count">10</span></span></div>'
    . '<img class="image_inner_img outlink_poster_img" src="https://i.okcdn.ru/i?r=abc&amp;x=1"></li>'
    . '<li class="item it feed-card js-call" data-log-click="{&quot;topicId&quot;:&quot;444&quot;}"><div class="tstmp feed_descr">1 мар 2025</div>'
    . '<div class="topic-text_content">без картинки</div><div class="widget-list_actions"></div></li>'
    . '<li class="item it feed-card js-call" data-log-click="{&quot;topicId&quot;:&quot;555&quot;}"><div class="tstmp feed_descr">2 мар 2025</div>'
    . '<div class="topic-text_content">чужая картинка</div><img class="image_inner_img" src="https://evil.example.com/a.jpg"></li></body></html>';
$ok_posts = ok_parse_feed($ok_html, '53038939046008', 10, '', $now);
$check('ОК: одна запись с картинкой (без картинки и чужой хост — мимо)', count($ok_posts) === 1);
$p = $ok_posts[0] ?? [];
$check('ОК: ссылка на запись и id', ($p['url'] ?? '') === 'https://ok.ru/group/53038939046008/topic/111222333' && ($p['post_id'] ?? '') === '111222333');
$check('ОК: счётчики — класс, комментарии, репосты', ($p['likes'] ?? null) === 10 && ($p['comments'] ?? null) === 6 && ($p['reshares'] ?? null) === 4);
$check('ОК: дата и текст', ($p['posted_at'] ?? 0) === strtotime('2025-10-12 00:00:00 UTC') && str_contains($p['text'] ?? '', 'Заголовок') && str_contains($p['text'] ?? '', 'текст поста'));
$check('ОК: картинка https i.okcdn.ru', ($p['image'] ?? '') === 'https://i.okcdn.ru/i?r=abc&x=1');
$check('ОК: название из og:title без «| OK.RU»', ok_title_from_html($ok_html, '1') === 'Тест группы');

// Каталог ОК не теряет записи при записи каталога (catalog_update удаляет всё, чего нет в списке видов)
update_source_db(function ($db) use ($topic) { $db['ok_groups'][] = ['id' => '5000000000', 'name' => 'Тест', 'topics' => [$topic]]; return $db; });
$before = count(load_source_db()['ok_groups']);
update_source_db(fn($db) => $db);
$check('каталог: ok_groups переживает запись каталога', $before >= 1 && count(load_source_db()['ok_groups']) === $before);
update_source_db(fn($db) => [...$db, 'ok_groups' => array_values(array_filter($db['ok_groups'], fn($e) => ($e['id'] ?? '') !== '5000000000'))]);

echo "A. Сеть (живые ленты):\n";
try {
    $src = source_parse_link('https://www.youtube.com/@YouTube');
    $posts = rss_fetch_posts($src['id'], 3);
    // Обычное видео получает ссылку на ролик в тексте; у шортсов ссылка — отдельным полем (правило шортсов менялось отдельно)
    $check("[сеть] @YouTube → лента YouTube, посты с картинками (" . count($posts) . ')', $src['platform'] === PLATFORM_RSS && count($posts) > 0 && $posts[0]['image'] !== ''
        && (str_contains($posts[0]['text'], 'youtube.com/watch') || str_contains((string)($posts[0]['video_page'] ?? ''), 'youtube.com/')));
} catch (Throwable $e) { $check('[сеть] @YouTube: ' . $e->getMessage(), false); }
try {
    $src = source_parse_link('https://staff.tumblr.com/');
    $posts = rss_fetch_posts($src['id'], 3);
    $check('[сеть] Tumblr staff → посты с картинками (' . count($posts) . ')', $src['platform'] === PLATFORM_RSS && count($posts) > 0);
} catch (Throwable $e) { $check('[сеть] Tumblr: ' . $e->getMessage(), false); }
try {
    $src = source_parse_link('https://www.pinterest.com/pinterest/official-news/');
    $posts = rss_fetch_posts($src['id'], 3);
    $check('[сеть] Pinterest доска → посты, картинка 736x (' . count($posts) . ')', count($posts) > 0 && str_contains($posts[0]['image'], 'i.pinimg.com/736x/'));
} catch (Throwable $e) { $check('[сеть] Pinterest: ' . $e->getMessage(), false); }

echo "A. Добавление в каталог с тестовой темой:\n";
try {
    $added = source_add_manual('https://www.youtube.com/@YouTube', $topic);
    $db = load_source_db();
    $entry = array_values(array_filter($db['rss_feeds'], fn($e) => in_array($topic, (array)($e['topics'] ?? []), true)))[0] ?? null;
    $check('[сеть] YouTube попал в rss_feeds с темой', $added['platform'] === PLATFORM_RSS && $entry !== null);
    if ($entry) {
        [$posts] = collect_catalog_source(PLATFORM_RSS, $entry);
        $check('[сеть] сборщик берёт посты RSS для темы (' . count($posts) . ')', count($posts) > 0);
    }
} catch (Throwable $e) { $check('[сеть] добавление: ' . $e->getMessage(), false); }

echo "B. Сеть (живая группа ОК):\n";
try {
    $posts = ok_fetch_posts('53038939046008', OK_POSTS_PER_GROUP);
    $dated = count(array_filter($posts, fn($p) => $p['posted_at'] > 0));
    $check('[сеть] группа ОК → посты с картинками и датами (' . count($posts) . ', с датой ' . $dated . ')', count($posts) > 0 && $dated === count($posts));
} catch (Throwable $e) { $check('[сеть] группа ОК: ' . $e->getMessage(), false); }
// Группа без постов за CONTENT_MAX_AGE_DAYS не добавляется: эта ветка проверяется живой группой с давними постами
try {
    source_add_manual('https://m.ok.ru/dk?st.cmd=altGroupMain&st.groupId=53038939046008', $topic);
    $check('[сеть] группа без свежих постов не добавляется', false);
} catch (RuntimeException $e) { $check('[сеть] группа без свежих постов — понятная ошибка', str_contains($e->getMessage(), 'нет свежих постов с картинками')); }
$check('[сеть] после отказа каталог ОК не изменился', count(array_filter(load_source_db()['ok_groups'], fn($e) => in_array($topic, (array)($e['topics'] ?? []), true))) === 0);

echo "C. Лучшие посты (без сети):\n";
$v = best_posts_virality([['likes' => 10, 'reposts' => 0, 'comments' => 0], ['likes' => 10, 'reposts' => 0, 'comments' => 0], ['likes' => 100, 'reposts' => 5, 'comments' => 3]]);
$check('виральность ВК/ОК: первые два ≈ 1, третий заметно больше', round($v[0]['viral'], 1) === 1.0 && round($v[1]['viral'], 1) === 1.0 && $v[2]['viral'] > 2);
$t = best_posts_virality([['kind' => 'telegram', 'views' => 100, 'likes' => 5], ['kind' => 'telegram', 'views' => 100, 'likes' => 5], ['kind' => 'telegram', 'views' => 1000, 'likes' => 100]]);
$check('виральность Telegram: просмотры и доля реакций поровну', $t[0]['viral'] === 1.0 && $t[2]['viral'] > 3);
$check('виральность: пустой список', best_posts_virality([]) === []);
$sample = [
    ['post_id' => '1', 'posted_at' => 300, 'text' => 'Котик на диване', 'images' => ['https://a/1.jpg'], 'video' => '', 'likes' => 5, 'views' => null, 'reposts' => null, 'comments' => null, 'viral' => 1.0],
    ['post_id' => '2', 'posted_at' => 200, 'text' => 'Альбом про котов', 'images' => ['https://a/2.jpg', 'https://a/3.jpg'], 'video' => '', 'likes' => 50, 'views' => null, 'reposts' => null, 'comments' => null, 'viral' => 2.0],
    ['post_id' => '3', 'posted_at' => 100, 'text' => 'Ролик', 'images' => ['https://a/4.jpg'], 'video' => 'https://v/5.mp4', 'likes' => 20, 'views' => null, 'reposts' => null, 'comments' => null, 'viral' => 0.5],
];
$ids = fn($list) => array_column($list, 'post_id');
$check('фильтр: фото — одна картинка без видео', $ids(best_posts_matching($sample, 'photo', '')) === ['1']);
$check('фильтр: альбом — несколько картинок', $ids(best_posts_matching($sample, 'album', '')) === ['2']);
$check('фильтр: видео', $ids(best_posts_matching($sample, 'video', '')) === ['3']);
$check('фильтр по фразе без учёта регистра', $ids(best_posts_matching($sample, 'any', 'КОТ')) === ['1', '2']);
$check('сортировка по лайкам', $ids(best_posts_sort($sample, 'likes')) === ['2', '3', '1']);
$check('сортировка по виральности', $ids(best_posts_sort($sample, 'viral')) === ['2', '1', '3']);
$check('сортировка по дате — свежие первыми', $ids(best_posts_sort($sample, 'date')) === ['1', '2', '3']);
$tok = best_post_token(['source_key' => 'telegram:x', 'post_id' => '1', 'image' => 'https://a/1.jpg']);
$check('токен поста: стабилен и привязан к картинке', $tok === best_post_token(['source_key' => 'telegram:x', 'post_id' => '1', 'image' => 'https://a/1.jpg']) && $tok !== best_post_token(['source_key' => 'telegram:x', 'post_id' => '1', 'image' => 'https://a/9.jpg']));
$check('Telegram: сортировка views есть, у RSS только дата', in_array('views', best_posts_sorts_for(PLATFORM_TELEGRAM), true) && best_posts_sorts_for(PLATFORM_RSS) === ['date']);
$check('период не из списка — ошибка', $throws(fn() => best_posts('https://t.me/durov', 3, 'date'), 'Период'));
$check('VK без подключения — понятная ошибка', $throws(fn() => best_posts('https://vk.com/team', 7, 'date'), 'Сначала подключите VK') || vk_is_connected());

echo "C. Сеть (Telegram, подборка):\n";
if (!vk_is_connected()) echo "VK не подключён, пропущено: проверка паблика VK\n";
try {
    $r = best_posts_search('https://t.me/durov', 30, 'viral');
    $check('[сеть] Telegram за месяц: посты с картинками и виральностью (' . count($r['posts']) . ')', count($r['posts']) > 0 && isset($r['posts'][0]['viral']) && $r['posts'][0]['image'] !== '');
    $viral = array_column($r['posts'], 'viral');
    $viral_desc = $viral;
    rsort($viral_desc);
    $check('[сеть] сортировка viral: по убыванию', $viral === $viral_desc);
    $again = best_posts_search('https://t.me/durov', 30, 'viral');
    $check('[сеть] повтор в течение 15 минут — из кэша (тот же ключ)', $again['key'] === $r['key']);
    $pick = array_slice(array_column($r['posts'], 'token'), 0, 2);
    $added = best_posts_take($r['key'], $pick);
    $pool = array_values(array_filter(pool_load($uid), fn($i) => ($i['source_key'] ?? '') === 'telegram:durov'));
    $check('[сеть] выбранные посты — в подборке (' . $added . ')', $added >= 1 && count($pool) === $added);
    $check('[сеть] у записей картинка в кэше медиа и источник — Telegram', $pool && !array_filter($pool, fn($i) => !is_file(media_cache_path($i['image_local'] ?? '') ?? '/nonexistent')));
    // Картинка могла не скачаться в первый раз — тогда повтор добавит именно её; дублей быть не должно
    $repeat = best_posts_take($r['key'], $pick);
    $urls = array_column(array_filter(pool_load($uid), fn($i) => ($i['source_key'] ?? '') === 'telegram:durov'), 'source_url');
    $check('[сеть] повторный take не дублирует посты (добавлено ' . $repeat . ')', count($urls) === count(array_unique($urls)) && $repeat <= count($pick) - $added);
    $check('[сеть] подделанный токен — ошибка', $throws(fn() => best_posts_take($r['key'], [str_repeat('a', 64)]), 'Пост не найден'));
    $check('[сеть] чужой ключ кэша — ошибка', $throws(fn() => best_posts_take('best:someone-else:x', $pick), 'не найдены'));
    // Ответы обработчиков, какие видит страница: успех — status и список постов без полного текста
    $_GET = ['link' => 'https://t.me/durov', 'days' => '30', 'sort' => 'viral', 'type' => 'any', 'phrase' => ''];
    try { handle_best_posts(); } catch (TestApiExit $e) { $resp = $e->data; }
    $check('[сеть] обработчик «Лучших постов»: ответ с ключом, сортировками и подписями', ($resp['status'] ?? '') === 'success' && isset($resp['key'], $resp['sorts']) && isset($resp['posts'][0]['token']) && mb_strlen($resp['posts'][0]['text']) <= BEST_POSTS_TEXT);
    $_GET = ['link' => 'https://t.me/durov', 'days' => '3', 'sort' => 'date'];
    try { handle_best_posts(); $period_error = ''; } catch (RuntimeException $e) { $period_error = $e->getMessage(); }   // index.php превращает в ответ 400
    $check('[сеть] обработчик: период не из списка — понятная ошибка', str_contains($period_error, 'Период'));
    try { handle_best_posts_take(['key' => $resp['key'] ?? '', 'tokens' => []]); $take_error = ''; } catch (RuntimeException $e) { $take_error = $e->getMessage(); }
    $check('[сеть] обработчик take: пустой выбор — понятная ошибка', str_contains($take_error, 'от 1 до'));
} catch (Throwable $e) { $check('[сеть] Telegram: ' . $e->getMessage(), false); }

echo "D. Зеркало (правила и сохранение, без сети):\n";
$check('правила: слова «только с» — нет нужного слова → отсев', mirror_rules_apply('Просто текст', ['include' => ['котик']]) === null);
$check('правила: слово «только с» есть → пост проходит', mirror_rules_apply('Котик спит', ['include' => ['котик']]) === 'Котик спит');
$check('правила: «без» — запрещённое слово отсеивает', mirror_rules_apply('Реклама здесь', ['exclude' => ['реклама']]) === null);
$check('правила: обычная замена', mirror_rules_apply('кот и кот', ['replace' => [['from' => 'кот', 'to' => 'пёс', 'regex' => false]]]) === 'пёс и пёс');
$check('правила: регулярная замена', mirror_rules_apply('цена 120 руб', ['replace' => [['from' => '\d+', 'to' => 'N', 'regex' => true]]]) === 'цена N руб');
$check('правила: плохой regex не роняет и не меняет текст', mirror_rules_apply('текст', ['replace' => [['from' => '(', 'to' => 'x', 'regex' => true]]]) === 'текст');
$check('правила: тяжёлый regex не зависает и не роняет (лимит возвратов)', mirror_rules_apply(str_repeat('a', 40) . '!', ['replace' => [['from' => '(a+)+$', 'to' => 'b', 'regex' => true]]]) !== null);
$check('правила: ссылки убираются', mirror_rules_apply('Смотри https://example.com/x сейчас', ['strip_links' => true]) === 'Смотри сейчас');
$check('правила: @упоминания убираются, почта — нет', mirror_rules_apply('Пишите @admin_x и a@b.ru', ['strip_mentions' => true]) === 'Пишите и a@b.ru');
$check('правила: #хэштеги убираются', mirror_rules_apply('Утро #кофе #cat', ['strip_hashtags' => true]) === 'Утро');
$check('правила: текст в конец', mirror_rules_apply('Пост', ['append' => 'Подпишись']) === "Пост\nПодпишись");
$check('сохранение: плохой regex — понятная ошибка', $throws(fn() => mirror_rules_clean(['replace' => [['from' => '(', 'regex' => true, 'to' => '']]]), 'Регулярное выражение не работает'));
$check('сохранение: regex длиннее 200 знаков — ошибка', $throws(fn() => mirror_rules_clean(['replace' => [['from' => str_repeat('a', 201), 'regex' => true, 'to' => '']]]), 'длиннее 200'));
$check('сохранение: правила чистятся и обрезаются (append ≤ 500)', mb_strlen(mirror_rules_clean(['append' => str_repeat('я', 700)])['append']) === 500);

// Зеркало: сохранение (очередь не трогаем — она пишет в очередь отправки), предел и дубликаты
$m = mirror_save($uid, ['link' => 'https://t.me/durov', 'mode' => 'pool', 'rules' => ['include' => ['пост'], 'strip_links' => true]]);
$check('зеркало сохранено в режиме подборки', ($m['mode'] ?? '') === 'pool' && ($m['platform'] ?? '') === PLATFORM_TELEGRAM && $m['rules']['strip_links'] === true);
$check('зеркало: тот же источник и режим — дубликат', $throws(fn() => mirror_save($uid, ['link' => 'https://t.me/durov', 'mode' => 'pool']), 'уже есть'));
$check('зеркало: очередь без своего канала — ошибка', $throws(fn() => mirror_save($uid, ['link' => 'https://t.me/durov', 'mode' => 'queue', 'channel' => 'чужой-канал']), 'Выберите свой канал'));
$check('зеркало: неизвестный режим — ошибка', $throws(fn() => mirror_save($uid, ['link' => 'https://t.me/durov', 'mode' => 'slot']), 'Неизвестный режим'));
for ($i = 0; $i < 9; $i++) db_exec('INSERT INTO mirrors (id, user_id, platform, source_id, name, channel_id, mode, rules, last_post_key, last_checked_at, created_at) VALUES (?, ?, ?, ?, ?, NULL, ?, ?, ?, 0, ?)', [bin2hex(random_bytes(6)), $uid, PLATFORM_TELEGRAM, "filler$i", 'x', 'pool', '{}', '[]', time()]);
$check('зеркал не больше 10 на пользователя', $throws(fn() => mirror_save($uid, ['link' => 'https://t.me/memechi', 'mode' => 'pool']), 'не больше 10'));
db_exec("DELETE FROM mirrors WHERE user_id = ? AND source_id LIKE 'filler%'", [$uid]);
$check('список зеркал: правила — массив, канала нет', (mirror_list($uid)[0]['rules']['strip_links'] ?? null) === true && mirror_list($uid)[0]['channel_title'] === '');

echo "D. Сеть (зеркало, подборка):\n";
try {
    $before = count(pool_load($uid));
    $first = mirror_collect_user($uid);
    $added = count(pool_load($uid)) - $before;
    $check('[сеть] первый проход: не больше 3 постов (' . $added . ')', $added <= MIRROR_FIRST_RUN && $first['pooled'] === $added);
    $second = mirror_collect_user($uid);
    $check('[сеть] повтор в течение 15 минут — зеркало не проверяется', $second['mirrors'] === 0 && $second['pooled'] === 0);
    $check('[сеть] после проверки запомнены ключи постов', json_decode(db_value('SELECT last_post_key FROM mirrors WHERE user_id = ?', [$uid]), true) !== []
        || (int)db_value('SELECT last_checked_at FROM mirrors WHERE user_id = ?', [$uid]) > 0);
    mirror_delete($uid, $m['id']);
    $check('зеркало удалено', count(mirror_rows($uid)) === 0);
} catch (Throwable $e) { $check('[сеть] зеркало: ' . $e->getMessage(), false); }

echo "E. Папка и Яндекс Диск:\n";
own_quota_take($uid, OWN_DAY_FILES);
$check('суточный лимит: до 1000 файлов в сутки', $throws(fn() => own_quota_take($uid, 1), 'не больше 1000'));
$check('ссылка Яндекс Диска: короткая и с параметрами — нормализуется', yadisk_public_url('https://yadi.sk/d/abcd1234?w=1') === 'https://yadi.sk/d/abcd1234' && yadisk_public_url('https://disk.yandex.ru/d/AbCdEf123/') === 'https://disk.yandex.ru/d/AbCdEf123');
$check('ссылка Яндекс Диска: чужой сайт и файл — ошибка', $throws(fn() => yadisk_public_url('https://example.com/d/abcd1234'), 'Нужна публичная ссылка') && $throws(fn() => yadisk_public_url('https://disk.yandex.ru/i/abcd'), 'Нужна публичная ссылка'));
echo "E. Сеть (Яндекс Диск):\n";
try {
    own_import_yadisk('https://disk.yandex.ru/d/invalidkey1234');
    $check('[сеть] чужой ключ — ошибка «папка не найдена»', false);
} catch (RuntimeException $e) { $check('[сеть] чужой ключ — ошибка «папка не найдена»', str_contains($e->getMessage(), 'Папка не найдена')); }

$cleanup();
echo "Замороженные источники и настройка «Только актуальные»:\n";
$check('настройка включена — замороженный источник пропускается', source_frozen_skip(true, null));
$check('настройка выключена — замороженный источник собирается', !source_frozen_skip(false, null));
$check('удалённый источник пропускается и при выключенной настройке', source_frozen_skip(false, 'удалён'));

echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
