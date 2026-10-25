<?php
// Проверка новостных сайтов и 4chan: php tools/test_news_4chan.php [--only-offline]. Без сети: фикстуры из tools/fixtures/.
// Строки с пометкой [сеть] ходят во внешние сайты. Тестовые записи (тема «тест-») удаляются.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$throws = function (callable $fn, string $part) { try { $fn(); return false; } catch (RuntimeException $e) { return str_contains($e->getMessage(), $part); } };
$fixture = fn($name) => file_get_contents(__DIR__ . '/fixtures/' . $name);

echo "A. Статьи новостных сайтов (фикстуры):\n";
$lenta = article_parse($fixture('article_lenta.html'));
$check('Лента: картинка https из og:image', str_starts_with($lenta['image'], 'https://icdn.lenta.ru/'));
$check('Лента: основной текст статьи, не меню (длина ' . mb_strlen($lenta['article_text']) . ')', mb_strlen($lenta['article_text']) > 300 && str_starts_with($lenta['article_text'], 'Появились новые подробности'));
$ria = article_parse($fixture('article_ria.html'));
$check('РИА: картинка и описание', str_starts_with($ria['image'], 'https://') && $ria['description'] !== '');
$check('РИА: время из article:published_time', $ria['published'] === strtotime('2026-10-23T02:26:00'));
$check('РИА: основной текст из блоков article__text (длина ' . mb_strlen($ria['article_text']) . ')', mb_strlen($ria['article_text']) > 300 && str_contains($ria['article_text'], 'Новый банк развития'));
$check('текст статьи не длиннее предела', mb_strlen($ria['article_text']) <= ARTICLE_TEXT_MAX);

echo "A. Дубли и свежесть:\n";
$a = news_story_words('ЦБ поднял ключевую ставку до 16% впервые');
$b = news_story_words('ЦБ поднял ключевую ставку до 16% впервые в истории');
$c = news_story_words('Футболисты сборной вышли в плей-офф чемпионата');
$check('одна новость с двух сайтов — дубль', news_similar(array_slice($a, 0, 6), array_slice($b, 0, 6)));
$check('разные новости — не дубли', !news_similar(array_slice($a, 0, 6), array_slice($c, 0, 6)));
$check('ключ новости детерминирован', news_story_key('Ставка ЦБ 16%') === news_story_key('ставка цб, 16!'));
$site = ['id' => 'https://vc.ru/rss', 'news' => true, 'max_age_hours' => 48];
$check('новость старше 48 ч — устарела', news_post_outdated(['posted_at' => time() - 3 * 86400], $site));
$check('новость за сутки — свежая', !news_post_outdated(['posted_at' => time() - 86400], $site));
$check('обычный RSS — срок CONTENT_MAX_AGE_DAYS', !news_post_outdated(['posted_at' => time() - 86400], ['id' => 'https://x.test/rss']));

echo "A. Подпись новости и ссылка на источник:\n";
$caption = news_caption(['text' => "Ставка выросла\nЦБ повысил ключевую ставку до 16%. Решение вступит в силу в понедельник. Банки уже пересматривают ипотеку. Четвёртое предложение не нужно.",
    'url' => 'https://lenta.ru/news/2026/10/23/x/', 'news_site' => 'lenta.ru']);
$check('подпись: заголовок, не больше трёх предложений выжимки, строка «Источник» со ссылкой', str_starts_with($caption, '**Ставка выросла**') && str_contains($caption, 'Источник: lenta.ru — https://lenta.ru/news/2026/10/23/x/') && !str_contains($caption, 'Четвёртое'));
$check('ссылка на источник распознаётся в тексте', news_has_source_link($caption, 'https://lenta.ru/news/2026/10/23/x/'));

echo "A. Лента без картинки: флаг оставляет записи, без флага — нет:\n";
$xml = '<?xml version="1.0"?><rss version="2.0"><channel><title>T</title>'
    . '<item><title>Без картинки</title><link>https://news.test/a</link><description>Анонс</description><pubDate>Mon, 20 Oct 2026 10:00:00 +0000</pubDate></item>'
    . '<item><title>С картинкой</title><link>https://news.test/b</link><enclosure url="https://news.test/b.jpg" type="image/jpeg"/></item></channel></rss>';
$check('без флага — только запись с картинкой', count(rss_parse($xml, 10, 'Т', 'https://news.test/rss')) === 1);
$kept = rss_parse($xml, 10, 'Т', 'https://news.test/rss', true);
$check('с флагом — обе записи, у первой пустая картинка', count($kept) === 2 && array_values(array_filter($kept, fn($p) => $p['image'] === ''))[0]['text'] !== '');

echo "A. Дубли в отборе кандидатов:\n";
$known = record_known_keys([['news_words' => array_slice($a, 0, 6), 'pooled_at' => time(), 'source_url' => 'https://a.test/1', 'image' => 'https://a.test/1.jpg']]);
$dup = ['news_words' => array_slice($b, 0, 6), 'image' => 'https://b.test/9.jpg', 'url' => 'https://b.test/9'];
$other = ['news_words' => array_slice($c, 0, 6), 'image' => 'https://b.test/10.jpg', 'url' => 'https://b.test/10'];
$check('дубль с другого сайта отсеивается', candidate_is_known($dup, $known));
$check('другая новость проходит', !candidate_is_known($other, $known));

echo "C. 4chan (фикстура треда, без сети):\n";
$thread = json_decode($fixture('fourchan_thread.json'), true);
$op = $thread['posts'][0];
$media = fourchan_post_media('wsg', $op);
$check('пост с webm/mp4: ролик и превью (s.jpg) на i.4cdn.org', count($media) === 1 && str_ends_with($media[0]['video'], '.mp4') && str_ends_with($media[0]['image'], 's.jpg') && str_starts_with($media[0]['image'], FOURCHAN_IMG . '/wsg/'));
$check('пост без файла — без медиа', fourchan_post_media('wsg', ['no' => 1, 'com' => 'текст']) === []);
$check('текст поста: без HTML, «>>123» убраны, сущности декодированы', fourchan_post_text(['com' => '<a href="#p1">&gt;&gt;1</a><br>Hi &amp; bye<br><span class="quote">&gt;greentext</span>', 'sub' => 'Тема']) === "Тема\nHi & bye\ngreentext");
$check('ссылка на пост — тред с якорем', fourchan_post_url('wsg', 6250192, 6250495) === 'https://boards.4chan.org/wsg/thread/6250192#p6250495');
$check('тред закрыт — не годится', fourchan_thread_dead_reason(['closed' => 1, 'replies' => 50, 'last_modified' => time()]) === 'тред закрыт');
$check('медиа 4chan: заголовки браузера и Referer доски', (function () { $h = fourchan_media_headers(FOURCHAN_IMG . '/wsg/1.jpg'); return in_array('Referer: https://boards.4chan.org/wsg/', $h, true) && fourchan_media_headers('https://example.com/a.jpg') === []; })());
$check('доска «b» берётся под любую тему', FOURCHAN_ALWAYS_BOARDS === ['b'] && !defined('FOURCHAN_NEVER') && !defined('FOURCHAN_TEXT_BLOCK_RE'));
$check('доска «k» в темах не участвует', !array_key_exists('k', FOURCHAN_BOARD_TAGS));

echo "D. Лимиты (без сети):\n";
$GLOBALS['fourchan_stats'] = ['api' => FOURCHAN_API_PER_RUN, 'media' => 0, 'api_cached' => 0];
$check('лимит запросов к API за проход: сверх — исключение без запроса', $throws(fn() => fourchan_get_json('boards.json'), 'лимит запросов к 4chan'));
fourchan_begin_pass();
$GLOBALS['news_enrich_left'] = 0;
$no_img = [['url' => 'https://news.test/x', 'image' => '', 'text' => 'Короткий анонс', 'posted_at' => time()]];
$check('бюджет страниц статей исчерпан — запись без картинки отбрасывается', news_enrich_posts($no_img, ['id' => 'https://news.test/rss', 'news' => true]) === []);
$GLOBALS['news_enrich_left'] = NEWS_ENRICH_PER_PASS;

echo "E. Текстовые посты блогов и VK (фикстуры, без сети):\n";
$long = str_repeat('Длинный текст блога о доходах и налогах. ', 4);
$blog = '<?xml version="1.0"?><rss version="2.0"><channel><title>Блог</title><item><title>Без картинки</title><link>https://blog.test/p</link><description>' . $long . '</description><pubDate>Mon, 20 Oct 2026 10:00:00 +0000</pubDate></item></channel></rss>';
$check('RSS без флага текстовых постов — запись без картинки отбрасывается', rss_parse($blog, 10, 'Б', 'https://blog.test/rss') === []);
$tp = rss_parse($blog, 10, 'Б', 'https://blog.test/rss', false, true);
$check('RSS с флагом — текстовый пост с пометкой text_post и без картинки', count($tp) === 1 && $tp[0]['text_post'] === true && $tp[0]['image'] === '');
$vk_post = ['owner_id' => -1, 'id' => 5, 'date' => time(), 'text' => $long, 'attachments' => []];
$check('VK без флага — пост без картинки не берётся', vk_wall_post($vk_post, 'Паблик', 'team') === null);
$vk_text = vk_wall_post($vk_post, 'Паблик', 'team', true);
$check('VK с флагом — текстовый пост паблика с пометкой text_post', is_array($vk_text) && $vk_text['text_post'] === true && $vk_text['image'] === '');

echo "A. Выжимка по запросу:\n";
$check('выжимка без https-ссылки отказывает до запроса к модели', $throws(fn() => news_excerpt('http://lenta.ru/x', 'тест-пользователь'), 'Нужна ссылка'));

if (in_array('--only-offline', $argv, true)) { echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n"; exit($failures ? 1 : 0); }

echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
