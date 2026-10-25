<?php
// Проверка фильтра украинского контента без браузера, сети и моделей: php tools/test_ua_filter.php.
// Запускать на копии базы (POSTA_DB_FILE) и с временной папкой медиа (POSTA_MEDIA_CACHE_DIR), как tools/test_adult.php.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

if (MEDIA_CACHE_DIR === '/var/lib/posta/media') exit("Тест запущен с настоящей папкой медиа: задайте POSTA_MEDIA_CACHE_DIR\n");

$failures = 0;
$check = function ($name, $ok) use (&$failures) {
    echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n";
    if (!$ok) $failures++;
};

// Переключатель в начале — включён; в конце возвращаем как был
$was_on = ua_filter_on();
ua_filter_set(true);

// 1. text_is_ukrainian
$check('украинский текст с «і» и словами → да', text_is_ukrainian('Що відбувається у Києві сьогодні') === true);
$check('русский текст → нет', text_is_ukrainian('Что происходит в Киеве сегодня') === false);
$check('белорусский текст с «і» → нет', text_is_ukrainian('Дзе ты жывеш? І што робіш') === false);
$check('«Слава Україні» → да (ї)', text_is_ukrainian('Слава Україні') === true);
$check('пустая строка → нет', text_is_ukrainian('') === false);
$check('ссылки и хэштеги не мешают', text_is_ukrainian('https://example.com/що #київ @канал') === false);

// 2. ua_url_blocked
$check('pravda.com.ua → блок', ua_url_blocked('https://www.pravda.com.ua/news/1') === true);
$check('news.unian.net → блок (поддомен)', ua_url_blocked('https://news.unian.net/x') === true);
$check('ria.ru → не блок', ua_url_blocked('https://ria.ru/x') === false);
$check('site.укр → блок (зона)', ua_url_blocked('https://site.укр/') === true);
$check('punycode зоны .укр → блок', ua_url_blocked('https://site.xn--j1amh/') === true);

// 3. ua_item_blocked
$check('4chan с флагом UA → блок', ua_item_blocked(['source_key' => '4chan:pol:1', 'country' => 'UA', 'text' => 'Привет']) !== null);
$check('русская подпись про Украину с российского источника → не блок', ua_item_blocked([
    'source_key' => 'telegram:ria_novosti_test', 'source' => 'Telegram | РИА', 'text' => 'Новости: в Украине прошли выборы, как сообщают в Москве.',
    'url' => 'https://t.me/ria_novosti_test/1']) === null);
$check('RSS-пост с сайта .ua → блок', ua_item_blocked(['source_key' => 'rss:https://news.site.ua/feed', 'url' => 'https://news.site.ua/1', 'text' => 'Новость']) !== null);
$check('украинская подпись → блок', ua_item_blocked(['source_key' => 'telegram:abc', 'text' => 'Що відбувається у Києві сьогодні', 'captions' => []]) !== null);

// 4. ua_source_blocked: VK-сообщество из Украины (country.id = 2) и название с прямым признаком
$check('VK-сообщество, страна Украина → блок', ua_source_blocked('vk:testcommunity', ['country' => ['id' => 2, 'title' => 'Украина']]) !== null);
$check('VK-сообщество без страны Украина → не блок', ua_source_blocked('vk:testcommunity', ['country' => ['id' => 1]]) === null);
$check('название «Новости Украины наш канал» → блок', ua_source_blocked('telegram:testchan', ['title' => 'Новости Украины: наш канал']) !== null);

// 5. Выключенный фильтр: все вердикты пустые; включённый — снова есть
ua_filter_set(false);
$check('фильтр выключен: URL не блокируется', ua_url_blocked('https://www.pravda.com.ua/news/1') === false);
$check('фильтр выключен: пост не блокируется', ua_item_blocked(['country' => 'UA']) === null);
$check('фильтр выключен: источник не блокируется', ua_source_blocked('vk:x', ['country' => ['id' => 2]]) === null);
ua_filter_set(true);
$check('фильтр снова включён: URL блокируется', ua_url_blocked('https://www.pravda.com.ua/news/1') === true);

// 6. build_candidate_queue: украинские посты не в очереди, счётчик посчитан
$by = [PLATFORM_4CHAN => [
    ['source_key' => '4chan:pol:9', 'source' => '4chan /pol/', 'posted_at' => time(), 'image' => 'https://i.example/1.jpg', 'images' => ['https://i.example/1.jpg'],
        'text' => 'пост', 'country' => 'UA', 'url' => 'https://boards.4chan.org/pol/thread/9'],
    ['source_key' => '4chan:pol:10', 'source' => '4chan /pol/', 'posted_at' => time(), 'image' => 'https://i.example/2.jpg', 'images' => ['https://i.example/2.jpg'],
        'text' => 'обычный русский пост с картинкой и достаточным текстом', 'country' => '', 'url' => 'https://boards.4chan.org/pol/thread/10'],
]];
$skipped = [];
$queue = build_candidate_queue($by, [], [], [], $skipped);
$check('build_candidate_queue: украинский пост отсеян, счётчик посчитан', ($skipped['ukraine'] ?? 0) === 1 && ($skipped['ukraine_sources'] ?? 0) === 1);
$check('build_candidate_queue: в очереди остался русский пост', count($queue) === 1 && ($queue[0]['source_key'] ?? '') === '4chan:pol:10');

// 7. Ручное добавление украинского источника: проверка без сети — тот же вердикт, что делает source_add_manual
$check('ручное добавление: украинский источник отвечает причиной (проверка без сети)', ua_source_blocked('telegram:uachan', ['title' => 'Українські новини, наш канал']) !== null);

ua_filter_set($was_on);
echo $failures ? "Провалено: $failures\n" : "Все проверки пройдены.\n";
exit($failures ? 1 : 0);
