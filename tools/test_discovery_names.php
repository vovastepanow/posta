<?php
// Проверка названий от модели, происхождения решений и порядка предложений: php tools/test_discovery_names.php
// Без сети, без модели и без записи в базу: только чистые функции.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

echo "A. Названия и адреса от модели:\n";
$check('префикс площадки срезается', discovery_plain_name('Telegram | SimpDungeon☠️') === 'SimpDungeon☠️');
$check('название без префикса не меняется', discovery_plain_name('Сюр') === 'Сюр');
$names = discovery_clean_names(['Сюр', 'сюр', 'Проклятые картинки', 'Telegram | Сюр', 'канал', 'мемы про котов'], ['Мемы про котов']);
$check('очистка названий: без дублей и без уже известных (в нижнем регистре, как запросы)', $names === ['сюр', 'проклятые картинки']);
$handles = discovery_clean_handles(['@memes_ru', 'memes_ru', 'bad name', 'x', 'nonsense_channel', 'abc-def', 'glupost']);
$check('адреса: правильные, без дублей, без @', $handles === ['memes_ru', 'nonsense_channel', 'glupost']);
$check('адресов не больше 10', count(discovery_clean_handles(array_map(fn($i) => 'channel_' . $i . 'xx', range(1, 15)))) === 10);

echo "A. Кэш названий:\n";
$now = time();
$check('нет кэша — пересоздать', discovery_names_stale(null, 3, $now) === true);
$check('свежий кэш, одобренных мало прибавилось — из кэша', discovery_names_stale(['approved' => 10, 'at' => $now], 12, $now) === false);
$check('одобренных на 5 больше — пересоздать', discovery_names_stale(['approved' => 10, 'at' => $now], 15, $now) === true);
$check('кэш старше 14 дней — пересоздать', discovery_names_stale(['approved' => 10, 'at' => $now - 14 * DAY_SECONDS], 10, $now) === true);

echo "B. Происхождение решения:\n";
$sug = ['source_id' => 'telegram:x', 'score' => 61, 'origin' => ['kind' => 'tg_name', 'detail' => 'название от модели «мемы»'], 'refs' => []];
$check('голос за находку копирует вид и оценку', source_vote_origin($sug) === ['origin_kind' => 'tg_name', 'origin_detail' => 'название от модели «мемы»', 'discovery_score' => 61]);
$check('решение не из предложений — каталог', source_vote_origin(null) === ['origin_kind' => 'catalog']);
$check('старое предложение без вида — unknown', source_vote_origin(['score' => 60])['origin_kind'] === 'unknown');

echo "C. Порядок предложений:\n";
$check('повторные находки поднимают ранг', suggestion_rank(['score' => 55, 'refs' => ['a', 'b']], 0.0) > suggestion_rank(['score' => 55, 'refs' => []], 0.0));
$check('повторных находок в ранге не больше 4', suggestion_rank(['score' => 0, 'refs' => range(1, 9)], 0.0) === (float)(DISCOVERY_REF_BONUS * 4));
$check('причина: без находок — без изменений', discovery_reason_with_refs('Найден', []) === 'Найден');
$check('причина: «ещё N раз найден через …»', str_contains(discovery_reason_with_refs('Найден', ['поиск VK «а»', 'похож на @b']), 'Ещё 2 раз найден через поиск VK «а», похож на @b'));

$votes = [];
for ($i = 0; $i < 10; $i++) $votes[] = ['topic' => 'Мемы', 'origin_kind' => 'tg_name', 'vote' => $i < 9 ? 'approve' : 'reject'];
$bonus = discovery_origin_bonus($votes, 'Мемы', 'tg_name');
$check('поправка по виду: 90% «да» → +10 (ограничена)', abs($bonus - 10.0) < 1e-9);
$check('поправка без данных — 0', discovery_origin_bonus([], 'Мемы', 'tg_name') === 0.0);
$check('мало решений того вида — 0', discovery_origin_bonus(array_slice($votes, 0, 3), 'Мемы', 'tg_name') === 0.0);
$bad = array_map(fn($v) => ['topic' => 'Мемы', 'origin_kind' => 'similar', 'vote' => 'reject'], range(1, 10));
$check('поправка отрицательная, но не ниже −10', discovery_origin_bonus($bad, 'Мемы', 'similar') === -9.0 || discovery_origin_bonus($bad, 'Мемы', 'similar') === -10.0);
$check('решения без вида (до учёта) в поправку не входят', discovery_origin_bonus(array_map(fn($v) => ['topic' => 'Мемы', 'vote' => 'approve'], range(1, 12)), 'Мемы', 'tg_name') === 0.0);

echo "D. Отчёт discovery-origins:\n";
$db = ['source_suggestions' => [['topic' => 'Мемы', 'origin' => ['kind' => 'vk_search']]],
    'source_votes' => [['topic' => 'Мемы', 'origin_kind' => 'vk_search', 'vote' => 'approve'], ['topic' => 'Мемы', 'origin_kind' => 'vk_search', 'vote' => 'reject'], ['topic' => 'Мемы', 'vote' => 'maybe']]];
$report = discovery_origin_report($db, 'Мемы');
$check('отчёт: найдено = предложения + решения вида', ($report['vk_search']['found'] ?? 0) === 3 && ($report['vk_search']['approve'] ?? 0) === 1);
$check('отчёт: решения без вида — «до учёта»', ($report['before']['maybe'] ?? 0) === 1);

echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли\n";
exit($failures ? 1 : 0);
