<?php
// Персонажи темы «Нижний интернет» (LOWER_INTERNET_PROMPT.md, часть B): короткий список, имена в промпте, засев дерева, ключевые слова.
// Запуск только на копии базы: POSTA_DB_FILE=<копия> php tools/test_topic_entities.php. Модель не вызывается.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
if (!getenv('POSTA_DB_FILE') || DB_FILE === ROOT . '/data/posta.sqlite') exit("Тест запускается на копии базы: задайте POSTA_DB_FILE=<копия>\n");

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

echo "A. Короткий список:\n";
$check('повторы и пустые убраны', topic_short_list(['Вжлинк', ' вжлинк ', '', 'Пахом'], 5) === ['Вжлинк', 'Пахом']);
$check('лимит соблюдён', topic_short_list(['раз', 'два', 'три'], 2) === ['раз', 'два']);
$check('не строки — пусто', topic_short_list(null, 5) === []);

echo "B. Контекст промпта:\n";
topic_tree_seed();
$check('имена персонажей попадают в контекст', str_contains(topic_prompt_context('Нижний интернет'), 'Вжлинк'));

echo "C. Засев сбрасывает профиль только при смене персонажей, описания или запросов:\n";
$fake = ['criteria' => 'старое', 'exclude' => '', 'question' => 'Это старое?', 'queries' => [], 'telegram' => [], 'v' => TOPIC_PROFILE_VERSION,
    'smart' => true, 'at' => time(), 'complaints' => 0, 'keywords' => ['старое']];
$old_state = function () use ($fake) {
    update_source_db(function ($db) use ($fake) {
        foreach ($db['topics'] as &$t) if (($t['key'] ?? '') === 'internet.lower') { $t['entities'] = []; $t['queries'] = []; $t['about'] = 'Старое описание.'; $t['profile'] = $fake; }
        unset($t);
        return $db;
    });
    topic_registry(true);
    db_exec('INSERT OR REPLACE INTO kv (key, value) VALUES (?, ?)', ['discovery_names:нижний интернет', '{"_kv_value":["старое"]}']);
};
$lower = fn() => array_values(array_filter(topic_registry(), fn($e) => ($e['key'] ?? '') === 'internet.lower'))[0] ?? null;

$old_state();
topic_tree_seed();
$e = $lower();
$check('после засева с изменениями профиль пустой', $e && $e['profile'] === []);
$check('после засева записи персонажей вернулись из дерева', $e && in_array('Вжлинк', $e['entities'], true));
$check('kv с названиями от модели удалён', db_value('SELECT COUNT(*) FROM kv WHERE key = ?', ['discovery_names:нижний интернет']) == 0);

update_source_db(function ($db) use ($fake) {
    foreach ($db['topics'] as &$t) if (($t['key'] ?? '') === 'internet.lower') $t['profile'] = $fake;
    unset($t);
    return $db;
});
topic_registry(true);
topic_tree_seed();
$check('повторный засев профиль не трогает', ($lower()['profile']['criteria'] ?? '') === 'старое');

echo "D. Ключевые слова из имён:\n";
$words = topic_profile_keywords($lower()['entities'], ['пиксельпот']);
$check('фамилия «литвинков» есть', in_array('литвинков', $words, true));
$check('«монгол», «vjlink» есть', in_array('монгол', $words, true) && in_array('vjlink', $words, true));
$check('«никита» и «влад» нет', !in_array('никита', $words, true) && !in_array('влад', $words, true));
$check('латиница: «litvinkov», «mongol», «gobzavr» есть, «nikita» и «oleg» нет', !array_diff(['litvinkov', 'mongol', 'gobzavr'], $words) && !in_array('nikita', $words, true) && !in_array('oleg', $words, true));
$check('латинские имена не вытесняют русские из промпта', !str_contains(topic_prompt_context('Нижний интернет'), 'Litvinkov'));
$check('короткие слова (до 4 букв) нет', !in_array('funn', $words, true) && !array_filter($words, fn($w) => mb_strlen($w) < 4 && $w !== 'пиксельпот'));
$check('слова модели идут в конце', end($words) === 'пиксельпот');
$check('без повторов', count($words) === count(array_unique($words)));
$check('лимит keywords в профиле 40', count(topic_profile_clean(['criteria' => 'x', 'question' => 'Это?', 'keywords' => array_map(fn($i) => "слово{$i}", range(1, 50))])['keywords']) === 40);

echo "E. Хэштеги имён в поиске постов:\n";
$check('слова имён без общих и коротких', in_array('литвинков', topic_entity_words(['Никита Литвинков', 'Влад Савельев']), true) && !in_array('никита', topic_entity_words(['Никита Литвинков']), true));
$cands = post_search_candidates('Нижний интернет');   // за проход берутся POST_SEARCH_QUERIES из списка, поэтому проверяем сам список
$check('в списке запросов есть #литвинков и #вжлинк', in_array('#литвинков', $cands, true) && in_array('#вжлинк', $cands, true));
$check('пост с #литвинков проходит локальный фильтр', post_search_matches('Нижний интернет', ['text' => 'Новый клип #литвинков']));
$check('пост только с #мем не проходит', !post_search_matches('Нижний интернет', ['text' => 'Смешной кот #мем']));

topic_tree_seed();   // на копии оставляем дерево в актуальном виде
echo $failures ? "\nНе пройдено проверок: {$failures}.\n" : "\nВсе проверки пройдены.\n";
exit($failures ? 1 : 0);
