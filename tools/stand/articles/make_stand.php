<?php
// Стенд карточек статей: php tools/stand/articles/make_stand.php
// Пишет рядом index.html — копию страницы приложения с путями к css/ и js/ на уровень выше и встроенными фикстурами.
// Фикстуры статей — из tools/fixtures/vc_*.json (разбор без сети). Картинки — встроенные SVG-заглушки: стенд работает без сети.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../../../bootstrap.php';

$dir = __DIR__;
$fixtures = __DIR__ . '/../../fixtures';

/** Картинка-заглушка: цветной прямоугольник с подписью, data-адрес (без сети). */
function stand_picture(string $label, string $bg, int $w = 1200, int $h = 675): string {
    $svg = "<svg xmlns='http://www.w3.org/2000/svg' width='$w' height='$h'><rect width='100%' height='100%' fill='$bg'/>"
        . "<text x='50%' y='50%' dominant-baseline='middle' text-anchor='middle' font-family='sans-serif' font-size='" . (int)($h / 14) . "' fill='#111'>" . htmlspecialchars($label) . "</text></svg>";
    return 'data:image/svg+xml;base64,' . base64_encode($svg);
}

$now = time();
$vc = osnova_from_api((string)file_get_contents("$fixtures/vc_article.json"), 'https://vc.ru/services/3209892-cloudflare-zafiksirovala-ataki-na-sayty-iz-za-uiazvimosti-php');
$vc['cover']['src'] = stand_picture('Обложка статьи', '#7CDAEA');
$long = osnova_from_api((string)file_get_contents("$fixtures/vc_longread.json"), 'https://vc.ru/x/3211013-den-1703');
$long['cover']['src'] = stand_picture('Дайджест', '#FF7A00');
$urlVc = 'https://vc.ru/services/3209892-cloudflare-zafiksirovala-ataki-na-sayty-iz-za-uiazvimosti-php';
$urlLong = 'https://vc.ru/x/3211013-den-1703';
$urlPlate = 'https://vc.ru/legal/3210108-gosduma-zakonoproekt-o-podderzhke-semejnyh-mikropredpriyatiy';

/** Статья в записи: объект как после article_apply_osnova() (без блоков). */
$articleOf = fn(array $a, string $url, ?string $cover) => ['title' => $a['title'], 'lead' => $a['lead'], 'cover' => $cover ? ['src' => $cover, 'width' => 1200, 'height' => 675] : null,
    'rubric' => $a['rubric'], 'author' => $a['author'], 'minutes' => $a['minutes'], 'words' => $a['words'], 'site' => parse_url($url, PHP_URL_HOST)];
$base = fn(string $text, string $source, string $url, array $extra = []) => [
    'text' => $text, 'source' => $source, 'source_key' => 'rss:' . parse_url($url, PHP_URL_HOST), 'source_url' => $url, 'url' => $url,
    'posted_at' => $now - 5400, 'relevance' => 87, 'rating' => 72, 'why_relevant' => 'Прямая связь с темой подбора: бизнес и продажи.',
    'description' => 'Описание ИИ: запись о событии в теме, без лишних деталей.', 'tags_info' => [['title' => 'Бизнес', 'path' => 'money.business']],
    'engagement' => null, 'post_check' => ['status' => 'ok', 'problems' => [], 'warnings' => [], 'bad' => []], 'preload' => [], 'images' => [], 'gallery' => [],
] + $extra;

$mediaImg = stand_picture('Фото поста', '#FFDA1A', 1200, 800);
$items = [
    'media' => $base('Пост с картинкой: короткий текст под фото о запуске нового курса для предпринимателей.', 'Телеграм-канал «Бизнес-ноты»', 'https://t.me/biznesnoty/123',
        ['image' => $mediaImg, 'display_image' => $mediaImg, 'images' => [$mediaImg]]),
    'text' => $base(str_repeat('Длинный текст поста без картинки: разбор того, как небольшой магазин пересчитал цену доставки и почему оборот вырос на треть. ', 4), 'Блог предпринимателя', 'https://example.org/blog/post-1',
        ['image' => '', 'display_image' => '', 'images' => [], 'text_post' => true]),
    'article' => $base($vc['title'] . "\n" . $vc['lead'], 'vc.ru', $urlVc, ['image' => $vc['cover']['src'], 'display_image' => $vc['cover']['src'], 'images' => [$vc['cover']['src']],
        'article' => $articleOf($vc, $urlVc, $vc['cover']['src']), 'engagement' => ['source' => 'osnova'] + $vc['counters']]),
    'longread' => $base($long['title'] . "\n" . $long['lead'], 'vc.ru', $urlLong, ['image' => $long['cover']['src'], 'display_image' => $long['cover']['src'], 'images' => [$long['cover']['src']],
        'article' => $articleOf($long, $urlLong, $long['cover']['src']), 'engagement' => ['source' => 'osnova'] + $long['counters']]),
    'plate' => $base('Госдума поддержала в первом чтении законопроект о семейных микропредприятиях.' . "\n" . 'Авторы считают, что инициатива поможет развитию семейного предпринимательства.', 'vc.ru', $urlPlate,
        ['image' => '', 'display_image' => '', 'images' => [], 'article' => ['title' => 'Госдума поддержала в первом чтении законопроект об отнесении семейных микропредприятий к социальным', 'lead' => 'Авторы считают, что инициатива поможет развитию семейного предпринимательства, укреплению преемственности и вовлечению молодёжи.', 'cover' => null, 'rubric' => 'Право', 'author' => 'Таня Боброва', 'minutes' => 1, 'words' => 120, 'site' => 'vc.ru'],
            'engagement' => ['source' => 'osnova', 'views' => 0, 'comments' => 60, 'likes' => 26]]),
];
$items['plate']['text'] = "Госдума поддержала в первом чтении законопроект об отнесении семейных микропредприятий к социальным\nАвторы считают, что инициатива поможет развитию семейного предпринимательства.";

foreach ($long['blocks'] as $i => $b) if ($b['type'] === 'image') $long['blocks'][$i]['src'] = stand_picture('Картинка ' . ($i + 1), '#E2D8BC', 1200, 700);
$reads = [
    $urlVc => ['url' => $urlVc, 'article' => ['title' => $vc['title'], 'lead' => $vc['lead'], 'cover' => $vc['cover'], 'rubric' => $vc['rubric'], 'author' => $vc['author'], 'minutes' => $vc['minutes'], 'words' => $vc['words'], 'blocks' => $vc['blocks']]],
    $urlLong => ['url' => $urlLong, 'article' => ['title' => $long['title'], 'lead' => $long['lead'], 'cover' => $long['cover'], 'rubric' => $long['rubric'], 'author' => $long['author'], 'minutes' => $long['minutes'], 'words' => $long['words'], 'blocks' => $long['blocks']]],
];

$calendarPosts = [['id' => 'cal1', 'channel' => 'c1', 'at' => $now + 3 * 3600, 'status' => 'queued', 'text' => $items['article']['text'], 'thumb' => false, 'article' => $items['article']['article'] + ['minutes' => 2, 'rubric' => 'Сервисы', 'site' => 'vc.ru'], 'channel_title' => 'Бизнес-ноты', 'media' => [], 'full_text' => $items['article']['text'], 'forecast' => null, 'duplicate' => false, 'error' => ''],
    ['id' => 'cal2', 'channel' => 'c1', 'at' => $now + 26 * 3600, 'status' => 'queued', 'text' => 'Обычный пост без статьи: короткий текст для календаря.', 'thumb' => false, 'channel_title' => 'Бизнес-ноты', 'media' => [], 'full_text' => '', 'forecast' => null, 'duplicate' => false, 'error' => '']];
$stand = ['calendarPosts' => $calendarPosts, 'items' => $items, 'reads' => $reads, 'proposals' => [$items['media'] + ['image' => $mediaImg, 'vote' => 'liked', 'source_url' => ''], $items['article'] + ['vote' => 'liked'], $items['plate'] + ['vote' => 'undecided']]];
$json = json_encode($stand, JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_HEX_TAG | JSON_HEX_AMP);

$html = (string)file_get_contents(__DIR__ . '/../../../index.html');
$html = str_replace(['href="css/', 'src="js/', 'href="fonts/', 'href="img/', 'src="img/'], ['href="../../../css/', 'src="../../../js/', 'href="../../../fonts/', 'href="../../../img/', 'src="../../../img/'], $html);
$stub = "<script>window.__STAND__ = $json;\n(function () {\n"
    . "    const params = new URLSearchParams(location.search);\n"
    . "    const kase = params.get('case') || 'article';\n"
    . "    document.documentElement.dataset.theme = params.get('theme') || 'day';\n"
    . "    if (params.get('tab')) setTimeout(() => document.querySelector('[data-tab=' + params.get('tab') + ']')?.click(), 1500);\n"
    . "    const real = window.fetch.bind(window), F = window.__STAND__;\n"
    . "    window.fetch = async (input, init) => {\n"
    . "        const url = new URL(String(input), location.href);\n"
    . "        if (!url.pathname.endsWith('index.php')) return real(input, init);\n"
    . "        const action = url.searchParams.get('action');\n"
    . "        let data = {};\n"
    . "        if (action === 'get_current_item') data = F.items[kase] ?? { status: 'empty', message: 'Нет записи' };\n"
    . "        if (action === 'proposals') data = { items: F.proposals, status: 'ok' };\n"
    . "        if (action === 'article_read') data = F.reads[url.searchParams.get('url')] ?? { error: 'нет статьи' };\n"
    . "        if (action === 'tab_open' || action === 'tab_heartbeat') data = { owner: true, parser: 'idle' };\n"
    . "        if (action === 'calendar') data = { connected: true, hints_seen: true, channels: [{ id: 'c1', title: 'Бизнес-ноты', username: 'biznesnoty', color: '#FFDA1A', schedule: null, reach: null }], posts: F.calendarPosts };\n"
    . "        if (action === 'telegram_queue') data = { connected: true, native_account: false, channels: [{ id: 'c1', title: 'Бизнес-ноты', username: 'biznesnoty' }], items: [] };\n"
    . "        return new Response(JSON.stringify(data), { status: 200, headers: { 'Content-Type': 'application/json; charset=utf-8' } });\n"
    . "    };\n})();</script>";
$html = str_replace('</head>', $stub . "\n</head>", $html);
file_put_contents("$dir/index.html", $html);

// Обёртка для телефона: окно 390×844 с той же страницей
file_put_contents("$dir/phone-calendar.html", '<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Календарь на телефоне</title></head><body style="margin:0;background:#888"><iframe id="ph" src="index.html?case=article&tab=calendar-tab" width="390" height="844" style="border:0"></iframe></body></html>');
file_put_contents("$dir/phone.html", '<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Телефон</title></head>'
    . '<body style="margin:0;background:#888"><iframe id="ph" src="index.html?case=article&theme=day" width="390" height="844" style="border:0"></iframe></body></html>');
echo "стенд: $dir/index.html, $dir/phone.html (записей: " . count($items) . ", статей в читалке: " . count($reads) . ")\n";
