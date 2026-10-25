<?php
// Уточнение к теме («Города» + «тюмень»): ключ поиска, совпадение по основе слова, схема и каталог (TOPIC_QUERY_PROMPT.md).
// Запуск: php tools/test_topic_query.php. Без модели и без записи в базу: только чистые функции.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

echo "A. Нормализация запроса:\n";
$check('пробелы и регистр', normalize_query('  Тюмень  ') === 'Тюмень');
$check('кавычки и плюс убраны', normalize_query('«тюмень» + "Зенит"') === 'тюмень Зенит');
$check('длина до TOPIC_QUERY_MAX_LEN', mb_strlen(normalize_query(str_repeat('я', 100))) === TOPIC_QUERY_MAX_LEN);
$check('пустой запрос — пустая строка', normalize_query('   ') === '');

echo "B. Ключ поиска и части темы:\n";
$check('хвост « ? запрос» не попадает в части', topic_parts('Города ? тюмень') === ['Города']);
$check('тема + вторая + запрос: части без запроса', topic_parts('Города + Природа ? тюмень') === ['Города', 'Природа']);
$check('база темы без запроса', topic_base('Города + Природа ? тюмень') === 'Города + Природа');
$check('уточнение из ключа', topic_query_of('Города + Природа ? тюмень') === 'тюмень');
$check('без уточнения — пусто', topic_query_of('Города + Природа') === '');

echo "C. Совпадение уточнения с текстом:\n";
$check('«В Тюмени открыли…» — да', query_text_hit('В Тюмени открыли новый парк', 'тюмень'));
$check('«тюменский» — да', query_text_hit('Тюменский университет набирает', 'тюмень'));
$check('«Tyumen» — да', query_text_hit('Weekend in Tyumen', 'тюмень'));
$check('«Тюлень» — нет', !query_text_hit('Тюлень на льдине', 'тюмень'));
$check('без уточнения в тексте — нет', !query_text_hit('Фото кота', 'тюмень'));
$check('два слова: оба нужны', query_text_hit('Зенит выиграл в Санкт-Петербурге', 'зенит петербург') && !query_text_hit('Зенит выиграл', 'зенит петербург'));

echo "D. Схема анализа:\n";
$plain = content_analysis_schema();
$with = content_analysis_schema('тюмень');
$check('без уточнения нет query_match', !isset($plain['properties']['query_match']) && !in_array('query_match', $plain['required'], true));
$check('без уточнения поля те же, что раньше', array_keys($plain['properties']) === ['relevance', 'tags', 'description', 'format', 'entity', 'evidence']);
$check('с уточнением есть query_match', isset($with['properties']['query_match']) && in_array('query_match', $with['required'], true));
$check('с уточнением relevance и query_match идут первыми', array_slice(array_keys($with['properties']), 0, 2) === ['relevance', 'query_match']);
$check('текстовая пачка с уточнением тоже', in_array('query_match', text_batch_schema('тюмень')['properties']['items']['items']['required'], true));

echo "E. Каталог: источник под уточнение:\n";
$own = ['id' => '1', 'topics' => ['Города'], 'query' => 'тюмень'];
$general = ['id' => '2', 'topics' => ['Города']];
$check('источник с уточнением — для той же пары', catalog_entry_matches_topic($own, 'Города ? тюмень'));
$check('источник с уточнением — не для другого запроса', !catalog_entry_matches_topic($own, 'Города ? омск'));
$check('источник с уточнением — не для темы без запроса', !catalog_entry_matches_topic($own, 'Города'));
$check('общий источник темы — и для пары', catalog_entry_matches_topic($general, 'Города ? тюмень'));

echo "\n" . ($failures ? "Провалено: $failures\n" : "Все проверки прошли.\n");
exit($failures ? 1 : 0);
