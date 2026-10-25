<?php
// Статьи с сайтов: php tools/test_articles.php. Без сети и без записи в базу: фикстуры vc.ru из tools/fixtures/.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };
$fixture = fn($name) => file_get_contents(__DIR__ . '/fixtures/' . $name);
$url = 'https://vc.ru/services/3209892-cloudflare-zafiksirovala-ataki-na-sayty-iz-za-uiazvimosti-php';
$og = 'Атаки на сайты из-за уязвимости PHP, Cloudflare сообщает о миллионах попыток, ограничения от Битрикс24 и Beget, киберугрозы в Японии и Нидерландах';

echo "A. Данные vc.ru (фикстура API):\n";
$a = osnova_from_api($fixture('vc_article.json'), $url);
$check('заголовок непустой и без тегов', $a && $a['title'] !== '' && !str_contains($a['title'], '<'));
$check('анонс без SEO-списка (og:description)', $a && $a['lead'] !== '' && !str_contains($a['lead'], 'Атаки на сайты'));
$check('анонс не длиннее ' . OSNOVA_LEAD_MAX . ' символов', $a && mb_strlen($a['lead']) <= OSNOVA_LEAD_MAX);
$check('обложка с leonardo.osnova.io, не сгенерированная', $a && str_starts_with($a['cover']['src'] ?? '', 'https://leonardo.osnova.io/') && !article_image_is_stub($a['cover']['src']));
$check('рубрика, автор, минуты не меньше 1', $a && $a['rubric'] === 'Сервисы' && $a['author'] !== '' && $a['minutes'] >= 1);
$check('блоки без HTML и без хэштегов', $a && !array_filter($a['blocks'], fn($b) => str_contains(json_encode($b, JSON_UNESCAPED_UNICODE), '<') || str_contains(json_encode($b, JSON_UNESCAPED_UNICODE), '#новости')));
$check('редирект vc.ru раскодирован в настоящий адрес', $a && !str_contains(json_encode($a['blocks'], JSON_UNESCAPED_SLASHES), 'redirect?to'));
$check('встроенная статья — мини-карточка без адреса', $a && count(array_filter($a['blocks'], fn($b) => $b['type'] === 'embed')) === 1);

$l = osnova_from_api($fixture('vc_longread.json'), 'https://vc.ru/x/3211013-den');
$check('ссылки абзацев — список [текст, https-адрес] (в дайджесте)', (function () use ($l) {
    foreach ($l['blocks'] as $b) foreach (($b['links'] ?? []) as [$t, $h]) if ($t !== '' && str_starts_with($h, 'https://')) return true;
    return false;
})());
$check('дайджест: блоки по типам (абзацы, заголовки, список, картинка)', $l && count(array_filter($l['blocks'], fn($b) => $b['type'] === 'h')) >= 3 && count(array_filter($l['blocks'], fn($b) => $b['type'] === 'list')) === 1);
$check('дайджест: минуты считаются по словам (≥1)', $l && $l['minutes'] === max(1, (int)round($l['words'] / OSNOVA_WORDS_PER_MINUTE)));
$dtf = osnova_from_api($fixture('dtf_longread.json'), 'https://dtf.ru/games/5364328-metro-2033-redux');
$check('dtf.ru: галерея — все картинки статьи в блоках, рубрика и обложка', $dtf && count(array_filter($dtf['blocks'], fn($b) => $b['type'] === 'image')) === 14 && $dtf['rubric'] === 'Игры' && str_starts_with($dtf['cover']['src'] ?? '', 'https://leonardo.osnova.io/'));
$check('ответ без статьи — null', osnova_from_api('{"message":"ok","result":null}', $url) === null);
$check('чужой сайт — не Osnova', osnova_target('https://lenta.ru/news/2026/10/23/x/') === null && osnova_target('https://vc.ru/u/') === null);

echo "B. Заглушки и списки ключевых слов:\n";
$check('обложка api.vc.ru/…/cover/fb/ — заглушка', article_image_is_stub('https://api.vc.ru/v2.9/cover/fb/c/3209892/abc/cover.jpg'));
$check('обложка api.dtf.ru/…/cover/ — заглушка', article_image_is_stub('https://api.dtf.ru/v2.9/cover/x/cover.jpg'));
$check('настоящая картинка leonardo — не заглушка', !article_image_is_stub('https://leonardo.osnova.io/4ce902bc/-/resize/1300/'));
$check('og:description vc.ru — список ключевых слов', article_is_keyword_list($og));
$check('обычный анонс с точкой — не список', !article_is_keyword_list('ЦБ повысил ключевую ставку до 16%. Решение вступит в силу в понедельник.'));
$check('короткое предложение с запятой — не список', !article_is_keyword_list('Банк снизил ставку, и это касается ипотеки.'));

echo "C. Разбор страницы: JSON-LD и заглушка в og:image:\n";
$html = '<html><head><meta property="og:image" content="https://api.vc.ru/v2.9/cover/fb/c/1/x/cover.jpg">'
    . '<meta property="og:description" content="' . htmlspecialchars($og) . '"></head><body>'
    . '<script type="application/ld+json">{"@context":"https://schema.org","@type":"NewsArticle","headline":"Заголовок","articleBody":"Первый абзац статьи.\n\nВторой абзац статьи, подлиннее.","image":["https://leonardo.osnova.io/4ce902bc-5e19-50b9-99e4-b017a0fc2786/-/resize/1300/"]}</script></body></html>';
$p = article_parse($html);
$check('og:image-заглушка не становится картинкой', $p['image'] === 'https://leonardo.osnova.io/4ce902bc-5e19-50b9-99e4-b017a0fc2786/-/resize/1300/');
$check('текст из JSON-LD (articleBody)', str_contains($p['article_text'], 'Второй абзац'));
$arr = article_jsonld('<script type="application/ld+json">{"@type":"NewsArticle","headline":["Заголовок"],"articleBody":["Абзац один.","Абзац два."]}</script>');
$check('JSON-LD: поля списком строк не ломают разбор', $arr['body'] === "Абзац один.\nАбзац два." && $arr['headline'] === 'Заголовок');
$check('описание-список не попадает как анонс', article_is_keyword_list($p['description']));

echo "D. Применение к записи:\n";
$post = ['url' => $url, 'image' => 'https://api.vc.ru/v2.9/cover/fb/c/1/x/cover.jpg', 'images' => ['https://api.vc.ru/v2.9/cover/fb/c/1/x/cover.jpg'], 'text' => "Старый заголовок\n" . $og, 'posted_at' => 0];
$applied = article_apply_osnova($post, $a, $url);
$check('заглушка заменена настоящей обложкой', $applied['image'] === $a['cover']['src'] && $applied['images'] === [$a['cover']['src']]);
$check('текст записи = заголовок + анонс, без SEO-списка', $applied['text'] === $a['title'] . "\n" . $a['lead'] && !str_contains($applied['text'], 'Атаки на сайты'));
$check('в записи только объект статьи, без блоков', isset($applied['article']) && !array_key_exists('blocks', $applied['article']) && $applied['article']['site'] === 'vc.ru');
$check('счётчики — в engagement с источником osnova', ($applied['engagement']['source'] ?? '') === 'osnova' && array_key_exists('comments', $applied['engagement']));
$check('выжимка берёт текст абзацев, а не заголовки блоков картинок', str_contains($applied['article_text'], 'Федеральные') === false || str_contains($applied['article_text'], 'Минпромторг'));
$nocover = article_apply_osnova(['url' => $url, 'image' => 'https://api.vc.ru/v2.9/cover/fb/c/1/x/cover.jpg', 'images' => [], 'text' => 'x', 'posted_at' => 0], array_merge($a, ['cover' => null]), $url);
$check('нет настоящей обложки — заглушка снята, картинки нет', $nocover['image'] === '' && $nocover['images'] === []);
$check('статья без картинки — объект статьи сохранён (текстовая статья)', isset($nocover['article']));

echo "E. Читалка для обычного сайта:\n";
$plain = article_plain_blocks("Первый абзац.\n\nВторой абзац.");
$check('абзацы без ссылок и без HTML', count($plain) === 2 && $plain[0] === ['type' => 'p', 'text' => 'Первый абзац.', 'links' => []]);

echo $failures ? "\nПровалено: $failures\n" : "\nВсё прошло.\n";
exit($failures ? 1 : 0);
