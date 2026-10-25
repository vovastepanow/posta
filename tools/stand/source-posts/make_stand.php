<?php
// Стенд ленты постов кандидата: php tools/stand/source-posts/make_stand.php
// index.html — копия страницы приложения с путями на уровень выше и встроенными фикстурами (tools/fixtures/source_preview_*.json).
// Картинки площадок заменяются встроенными SVG-заглушками (стенд работает без сети); видео без файла — со ссылкой на пост.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
$dir = __DIR__;
$fixtures = __DIR__ . '/../../fixtures';

/** Картинка-заглушка с подписью, data-адрес. */
function stand_picture(string $label, string $bg, int $w = 800, int $h = 600): string {
    $svg = "<svg xmlns='http://www.w3.org/2000/svg' width='$w' height='$h'><rect width='100%' height='100%' fill='$bg'/>"
        . "<text x='50%' y='50%' dominant-baseline='middle' text-anchor='middle' font-family='sans-serif' font-size='" . (int)($h / 9) . "' fill='#111'>" . htmlspecialchars($label) . "</text></svg>";
    return 'data:image/svg+xml;base64,' . base64_encode($svg);
}

$palette = ['#FFDA1A', '#7CDAEA', '#FF7A00', '#E2D8BC', '#E1001A', '#1546C8'];
$cases = [];
foreach (['memes' => 'source_preview_tg_memes.json', 'text' => 'source_preview_tg_text.json', 'rss' => 'source_preview_rss.json'] as $kase => $file) {
    $d = json_decode((string)file_get_contents("$fixtures/$file"), true)['discovered'];
    $n = 0;
    foreach ($d['posts'] as &$post) {
        foreach ($post['media'] as &$m) {
            $n++;
            $label = ($m['kind'] === 'video' ? 'Видео ' : 'Фото ') . $n;
            if ($m['src'] !== '') $m['src'] = stand_picture($label, $palette[$n % count($palette)]);
            if ($m['poster'] !== '') $m['poster'] = stand_picture($label, $palette[$n % count($palette)]);
            if ($m['kind'] === 'video') $m['src'] = '';   // файла для стенда нет: плашка «Видео — откройте пост»
        }
        unset($m);
    }
    unset($post);
    $cases[$kase] = $d;
}
$json = json_encode(['cases' => $cases], JSON_UNESCAPED_UNICODE | JSON_UNESCAPED_SLASHES | JSON_HEX_TAG | JSON_HEX_AMP);

$html = (string)file_get_contents(__DIR__ . '/../../../index.html');
$html = str_replace(['href="css/', 'src="js/', 'href="fonts/', 'href="img/', 'src="img/'], ['href="../../../css/', 'src="../../../js/', 'href="../../../fonts/', 'href="../../../img/', 'src="../../../img/'], $html);
$stub = "<script>window.__STAND__ = $json;\n(function () {\n"
    . "    const params = new URLSearchParams(location.search);\n"
    . "    const kase = params.get('case') || 'memes';\n"
    . "    document.documentElement.dataset.theme = params.get('theme') || 'day';\n"
    . "    if (params.get('tab')) setTimeout(() => document.querySelector('[data-tab=' + params.get('tab') + ']')?.click(), 1500);\n"
    . "    if (params.get('measure')) setTimeout(() => { document.querySelector('[data-tab=source-tab]')?.click(); setTimeout(() => {\n"
    . "        const cards = [...document.querySelectorAll('#source-posts .sp-card')];\n"
    . "        const count = () => cards.filter(c => { const r = c.getBoundingClientRect(); return r.top >= 0 && r.bottom <= innerHeight && r.left >= 0 && r.right <= innerWidth; }).length;\n"
    . "        const fully = count();\n"
    . "        document.getElementById('source-card-wrapper')?.scrollIntoView({ block: 'start' });\n"
    . "        setTimeout(() => { document.title = 'DENSITY ' + fully + ' of ' + cards.length + ' ' + innerWidth + 'x' + innerHeight + ' card-top ' + count(); }, 300);\n"
    . "    }, 2500); }, 500);\n"
    . "    const real = window.fetch.bind(window), F = window.__STAND__;\n"
    . "    window.fetch = async (input, init) => {\n"
    . "        const url = new URL(String(input), location.href);\n"
    . "        if (!url.pathname.endsWith('index.php')) return real(input, init);\n"
    . "        const action = url.searchParams.get('action');\n"
    . "        let data = {};\n"
    . "        if (action === 'suggest_sources') data = { discovered: F.cases[kase] };\n"
    . "        if (action === 'tab_open' || action === 'tab_heartbeat') data = { owner: true, parser: 'idle' };\n"
    . "        if (action === 'telegram_queue') data = { connected: true, native_account: false, channels: [], items: [] };\n"
    . "        if (action === 'get_current_item') data = { status: 'empty', message: 'Нет записи' };\n"
    . "        return new Response(JSON.stringify(data), { status: 200, headers: { 'Content-Type': 'application/json; charset=utf-8' } });\n"
    . "    };\n})();</script>";
$html = str_replace('</head>', $stub . "\n</head>", $html);
file_put_contents("$dir/index.html", $html);
file_put_contents("$dir/phone.html", '<!doctype html><html lang="ru"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Лента на телефоне</title></head><body style="margin:0;background:#888"><iframe id="ph" src="index.html?case=memes&tab=source-tab" width="390" height="844" style="border:0"></iframe></body></html>');
echo "стенд: $dir/index.html (кейсы: " . implode(', ', array_keys($cases)) . ")\n";
