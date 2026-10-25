<?php
// Доводка частных тем юмора (SUBTOPIC_PROMPT.md, части E и G): описания из дерева в таблицу тем и сброс оценок по
// «Абсурдному юмору» и «Чёрному юмору» в screened_cache. Запуск: php tools/subtopic_finish.php. Не работает, пока идёт парсер.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

// Парсер пишет в ту же базу: доводку не запускаем при его работе (шаблон закреплён на начале строки, чтобы не найти сам себя)
if (trim((string)shell_exec("pgrep -f '^/usr/bin/php .*cron_parser\\.php' | wc -l")) !== '0') exit("Парсер работает — запустите доводку после его окончания.\n");

$c = topic_tree_seed();
echo "Дерево тем: создано {$c['created']}, обновлено {$c['updated']}, перенесено {$c['moved']}, пропущено {$c['skipped']}\n";

$names = [];
foreach (SUBTOPIC_CACHE_TOPICS as $t) { $names[] = $t; $names[] = mb_convert_case(mb_substr($t, 0, 1), MB_CASE_UPPER) . mb_substr($t, 1); }
$placeholders = implode(',', array_fill(0, count($names), '?'));
$removed = db_exec("DELETE FROM screened_cache WHERE topic IN ({$placeholders})", $names);
echo "Быстрые оценки по частным темам юмора сброшены: {$removed}\n";
