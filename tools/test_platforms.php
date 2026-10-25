<?php
// Проверки площадок без сети и модели: php tools/test_platforms.php
// Строка итога по площадкам, честный баланс находок автоподбора, короткие запросы TikTok, нейтральная аудитория.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function (string $name, bool $ok) use (&$failures) { echo ($ok ? 'OK   ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

// Итог по площадкам: пустые — с причиной, у непустых — постов и источников
$rep = platform_report_init();
$rep['telegram'] = ['sources' => 18, 'with' => 18, 'posts' => 312];
$rep['dvach'] = ['sources' => 4, 'with' => 4, 'posts' => 501];
$rep['fourchan'] = ['sources' => 2, 'with' => 2, 'posts' => 138];
$rep['youtube'] = ['sources' => 0, 'with' => 0, 'posts' => 0];
$rep['vk'] = ['sources' => 0, 'with' => 0, 'posts' => 0];
$rep['tiktok'] = ['sources' => 3, 'with' => 0, 'posts' => 0];
$rep['ok'] = ['sources' => 0, 'with' => 0, 'posts' => 0];
$rep['rss'] = ['sources' => 0, 'with' => 0, 'posts' => 0];
$line = platform_summary_line($rep, false);
$check('итог начинается с «Площадки:»', str_starts_with($line, 'Площадки: '));
$check('Telegram с числом постов и каналов', str_contains($line, 'Telegram 312 постов (18 каналов)'));
$check('Двач и 4chan в журнале', str_contains($line, 'Двач 501 (4 доски)') && str_contains($line, '4chan 138'));
$check('YouTube без источников — причина', str_contains($line, 'YouTube 0 — нет источников по теме'));
$check('VK без ключа — «нет ключа»', str_contains($line, 'VK 0 — нет ключа'));
$check('TikTok с источниками, но без постов — отдельная причина', str_contains($line, 'TikTok 0 — источники есть, постов нет'));
$check('RSS без лент — «нет лент по теме»', str_contains($line, 'RSS 0 — нет лент по теме'));
$check('с ключом VK без источников — не «нет ключа»', str_contains(platform_summary_line($rep, true), 'VK 0 — нет источников по теме'));

// Баланс находок: по две с каждой площадки, потом по кругу; внутри площадки — лучшие первыми
$tg = [['id' => 't1'], ['id' => 't2'], ['id' => 't3'], ['id' => 't4']];
$vk = [['id' => 'v1'], ['id' => 'v2']];
$yt = [['id' => 'y1'], ['id' => 'y2'], ['id' => 'y3']];
$picked = array_column(discovery_balance([$tg, $vk, $yt], 12, 2), 'id');
$check('баланс: первые две с каждой площадки', array_slice($picked, 0, 6) === ['t1', 't2', 'v1', 'v2', 'y1', 'y2']);
$check('баланс: остаток — по кругу, Telegram не забивает', $picked === ['t1', 't2', 'v1', 'v2', 'y1', 'y2', 't3', 'y3', 't4']);
$check('баланс: лимит соблюдён', count(discovery_balance([$tg, $vk, $yt], 4, 2)) === 4);

// Запросы TikTok: короткие слова темы, без «видео» и без длинных фраз
$queries = discovery_tiktok_queries(['парадоксальные видео', 'смешные коты и собаки в доме', 'мемы'], 'тест-котики');
$check('TikTok: не больше двух запросов', count($queries) <= 2);
$check('TikTok: фразы из трёх слов и «видео» отброшены', !in_array('парадоксальные видео', $queries, true) && !in_array('смешные коты и собаки в доме', $queries, true));

// Нейтральная аудитория: неизвестные подписчики и просмотры не роняют оценку
$check('тема 98% без данных об аудитории — 98%', source_quality_score(98, 0, null)['score'] === 98);
$check('тема 98% и известные просмотры — не ниже 90%', source_quality_score(98, 0, 0.8)['score'] >= 90);
$check('тема 98%, подписчики известны, просмотры нет — не ниже 85%', source_quality_score(98, 50000, null)['score'] >= 85);

echo $failures ? "\nПровалов: $failures\n" : "\nВсе проверки прошли.\n";
exit($failures ? 1 : 0);
