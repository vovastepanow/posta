<?php
// Проверка сбора картинок Telegram и дедупликации на сохранённых страницах (без сети). Запуск: php tools/test_media_dedupe.php
require __DIR__ . '/../config.php';
require __DIR__ . '/../lib/core.php';
require __DIR__ . '/../lib/sources.php';

$failures = 0;
$check = function (string $name, bool $ok) use (&$failures) { echo ($ok ? 'OK   ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

// Адреса дедупликации: один файл в нескольких размерах и на разных CDN — один ключ, побеждает крупный
$urls = ['https://cdn4.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt_s.jpg', 'https://cdn4.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt_x.jpg', 'https://cdn5.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt.jpg'];
$check('дедупликация: три варианта одного файла — остаётся оригинал без суффикса (крупнее _s и _x)', media_dedupe_urls($urls) === ['https://cdn5.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt.jpg']);
$check('дедупликация: два суффикса — побеждает крупнее (_x над _s)', media_dedupe_urls(['https://cdn4.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt_s.jpg', 'https://cdn4.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt_x.jpg']) === ['https://cdn4.telesco.pe/file/AbCdEfGhIjKlMnOpQrSt_x.jpg']);
$check('дедупликация: параметр width побеждает меньший', media_dedupe_urls(['https://a.test/p/pic.jpg?width=320', 'https://a.test/p/pic.jpg?width=1280']) === ['https://a.test/p/pic.jpg?width=1280']);
$check('дедупликация: разные файлы не склеиваются', count(media_dedupe_urls(['https://a.test/x/one.jpg', 'https://a.test/x/two.jpg'])) === 2);
$check('дедупликация: миниатюра того же файла не побеждает оригинал', media_dedupe_urls(['https://a.test/p/photo_small.jpg', 'https://a.test/p/photo.jpg']) === ['https://a.test/p/photo.jpg']);
$check('дедупликация: разные файлы с похожими именами не склеиваются', count(media_dedupe_urls(['https://a.test/p/thumb_small.jpg', 'https://a.test/p/photo.jpg'])) === 2);
$check('дедупликация: порядок первого вхождения сохраняется', media_dedupe_urls(['https://a.test/b.jpg', 'https://a.test/a.jpg', 'https://a.test/b_s.jpg']) === ['https://a.test/b.jpg', 'https://a.test/a.jpg']);

// Страницы каналов: в images только медиа самого поста, превью ссылок — в link_image, повторов файлов нет
foreach (glob(__DIR__ . '/fixtures/telegram_channel_*.html') as $file) {
    $name = basename($file);
    $html = file_get_contents($file);
    $page_link_previews = [];
    preg_match_all('~tgme_widget_message_link_preview.*?background-image:\s*url\([\'"]?(https://[^\'")\s]+)~si', $html, $lp);
    foreach ($lp[1] as $u) $page_link_previews[] = media_file_key($u);
    $posts = telegram_posts_from_html($html, ['id' => 'test', 'name' => 'Test'], 20);
    $check("$name: постов с картинками — " . count($posts), count($posts) > 0);
    $leaked = $dups = $bad_link = 0;
    foreach ($posts as $p) {
        $keys = array_map('media_file_key', $p['images']);
        if (count($keys) !== count(array_unique($keys))) $dups++;
        foreach ($keys as $k) if (in_array($k, $page_link_previews, true) && !in_array($k, array_map('media_file_key', array_column($p['gallery'] ?: [['image' => $p['image']]], 'image')), true)) $leaked++;
        if ($p['link_image'] !== '' && in_array(media_file_key($p['link_image']), $keys, true)) $bad_link++;
        if (count($p['images']) > 10) $dups++;
    }
    $check("$name: нет превью ссылок в images", $leaked === 0 && $bad_link === 0);
    $check("$name: нет повторов файла внутри поста", $dups === 0);
}

echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли\n";
exit($failures ? 1 : 0);
