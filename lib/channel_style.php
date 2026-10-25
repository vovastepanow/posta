<?php
// Стиль канала и подпись от ИИ: стиль описывается один раз по свежим постам канала и хранится в channels.style (JSON),
// подпись пишется одним коротким запросом к модели с этим описанием, без повторного чтения канала.

const CHANNEL_STYLE_POSTS = 20;          // сколько последних постов читаем со страницы t.me/s/<канал>
const CHANNEL_STYLE_MIN_POST = 40;       // короче — не пример стиля (репосты, «🔥», голые ссылки)
const CHANNEL_STYLE_SAMPLE_CHARS = 700;  // обрезка одного поста в запросе к модели
const CHANNEL_STYLE_MAX_SUMMARY = 1200;  // описание стиля, символов
const CHANNEL_STYLE_STALE_DAYS = 30;     // старше — предлагаем изучить заново
const CAPTION_PER_HOUR = 20;             // подписей на пользователя в час: модель работает на компьютере владельца
const CAPTION_MAX_CHARS = 1024;          // лимит подписи Telegram к медиа

/** Описание стиля канала для интерфейса: ['summary', 'source' => auto|manual|'', 'updated_at', 'stale', 'can_learn', 'busy', 'error']. */
function channel_style_public(array $channel): array {
    $s = is_array($channel['style'] ?? null) ? $channel['style'] : [];
    $job = (array)kv_get('channel_style_job:' . $channel['id'], []);
    $busy = ($job['status'] ?? '') === 'running' && time() - (int)($job['at'] ?? 0) < 600;
    $updated = (int)($s['updated_at'] ?? 0);
    return ['summary' => (string)($s['summary'] ?? ''), 'source' => (string)($s['source'] ?? ''), 'updated_at' => $updated,
        'stale' => $updated > 0 && ($s['source'] ?? '') === 'auto' && time() - $updated > CHANNEL_STYLE_STALE_DAYS * DAY_SECONDS,
        'can_learn' => ($channel['chat_username'] ?? '') !== '', 'busy' => $busy, 'error' => !$busy && ($job['status'] ?? '') === 'error' ? (string)($job['message'] ?? '') : ''];
}

/** Тексты последних постов канала (новые первыми): публичная страница t.me/s/<канал> и свои прошлые публикации через приложение. */
function channel_style_samples(array $channel, $uid = null): array {
    $username = (string)($channel['chat_username'] ?? '');
    $texts = [];
    $add = function ($t) use (&$texts) {
        $t = trim((string)$t);
        if ($t !== EMPTY_POST_TEXT && mb_strlen($t) >= CHANNEL_STYLE_MIN_POST && !in_array($t, $texts, true)) $texts[] = $t;
    };
    if ($username !== '' && ($html = fetch_telegram_channel_page($username))) {
        foreach (telegram_posts_from_html($html, ['id' => $username], CHANNEL_STYLE_POSTS) as $p) $add($p['text'] ?? '');
    }
    if ($username !== '') foreach (published_load($uid, 50) as $e) if (str_contains((string)($e['url'] ?? ''), "t.me/$username/")) $add($e['text'] ?? '');
    return array_slice($texts, 0, CHANNEL_STYLE_POSTS);
}

/** Запрос к модели: описать манеру письма канала по примерам. */
function channel_style_prompt(array $samples): string {
    $list = '';
    foreach ($samples as $i => $t) $list .= '[' . ($i + 1) . '] ' . mb_substr($t, 0, CHANNEL_STYLE_SAMPLE_CHARS) . "\n\n";
    return "Ниже последние посты одного Telegram-канала. Опиши, КАК автор пишет, чтобы другой человек мог писать так же.\n\n{$list}"
        . "Опиши по пунктам, коротко и конкретно (всего до 700 символов, каждый пункт одной-двумя фразами, без вступлений и без Markdown-разметки): язык и тон; обращение к читателю («ты»/«вы»/без); длина постов и абзацев; "
        . "эмодзи (есть ли, сколько, какие, где стоят); оформление (жирный, списки, заглавные, многоточия); хештеги и подписи в конце; шутки и приёмы; что автор никогда не делает. "
        . 'Не пересказывай темы постов и не цитируй их целиком.';
}

/** Описание не длиннее лимита: режем по концу абзаца или предложения, а не посреди слова. */
function channel_style_trim(string $text): string {
    $text = trim($text);
    if (mb_strlen($text) <= CHANNEL_STYLE_MAX_SUMMARY) return $text;
    $cut = mb_substr($text, 0, CHANNEL_STYLE_MAX_SUMMARY);
    $end = max((int)mb_strrpos($cut, "\n"), (int)mb_strrpos($cut, '. '), (int)mb_strrpos($cut, '.)'));
    return rtrim($end > CHANNEL_STYLE_MAX_SUMMARY / 2 ? mb_substr($cut, 0, $end + 1) : $cut);
}

/** Изучить стиль канала по постам и сохранить. Исключение — понятная причина (канал закрыт, модель не ответила). */
function channel_style_learn(string $channel_id, $uid = null): array {
    $uid = $uid ?: app_user_id();
    $channel = channel_get($channel_id, $uid) ?? throw new DomainException('Такого канала нет.');
    if (($channel['chat_username'] ?? '') === '') throw new RuntimeException('У закрытого канала нет публичной страницы: опишите стиль вручную.');
    $samples = channel_style_samples($channel, $uid);
    if (count($samples) < 3) throw new RuntimeException('В канале мало постов с текстом (нашлось ' . count($samples) . ', нужно хотя бы 3): опишите стиль вручную.');
    $summary = trim(strip_cjk(ai_generate_local(smart_model(), channel_style_prompt($samples), [], null, ['temperature' => 0.3, 'num_predict' => 400], false)));
    $summary = channel_style_trim(str_replace('**', '', $summary));
    if (mb_strlen($summary) < 40) throw new RuntimeException('Модель не смогла описать стиль: попробуйте ещё раз.');
    // Два коротких поста целиком — образец для подражания (модель точнее копирует манеру по живому тексту)
    $examples = array_map(fn($t) => mb_substr($t, 0, 300), array_slice(array_filter($samples, fn($t) => mb_strlen($t) <= 500), 0, 2));
    $style = ['summary' => $summary, 'examples' => array_values($examples), 'source' => 'auto', 'updated_at' => time()];
    db_exec('UPDATE channels SET style = ? WHERE id = ? AND user_id = ?', [db_json($style), $channel_id, $uid]);
    return $style;
}

/** Описание стиля, написанное владельцем; пустое — стиль стирается. Образцы постов остаются, если они были. */
function channel_style_save_manual(string $channel_id, string $summary, $uid = null): array {
    $uid = $uid ?: app_user_id();
    $channel = channel_get($channel_id, $uid) ?? throw new DomainException('Такого канала нет.');
    $summary = trim(preg_replace('/\R{3,}/u', "\n\n", $summary));
    if (mb_strlen($summary) > CHANNEL_STYLE_MAX_SUMMARY) throw new InvalidArgumentException('Описание стиля — не длиннее ' . CHANNEL_STYLE_MAX_SUMMARY . ' символов.');
    $style = $summary === '' ? [] : ['summary' => $summary, 'examples' => (array)($channel['style']['examples'] ?? []), 'source' => 'manual', 'updated_at' => time()];
    db_exec('UPDATE channels SET style = ? WHERE id = ? AND user_id = ?', [db_json($style), $channel_id, $uid]);
    return $style;
}

/** Не больше CAPTION_PER_HOUR подписей в час на пользователя. */
function caption_quota_take($uid): void {
    $key = "caption:$uid:" . date('YmdH');
    if (kv_update($key, fn($n) => (int)$n + 1, 0) > CAPTION_PER_HOUR) throw new RuntimeException('Слишком много подписей за час (до ' . CAPTION_PER_HOUR . '): попробуйте позже.');
}

/** Запрос к модели: написать подпись к посту в стиле канала. */
function caption_prompt(array $channel, string $about, string $original, int $limit, string $current = ''): string {
    $style = is_array($channel['style'] ?? null) ? $channel['style'] : [];
    $p = 'Ты автор Telegram-канала' . (($channel['chat_title'] ?? '') !== '' ? ' «' . $channel['chat_title'] . '»' : '') . ". Напиши подпись к посту с картинкой или видео.\n\n";
    if (($style['summary'] ?? '') !== '') $p .= "Так пишет автор канала (стиль строго соблюдай):\n{$style['summary']}\n\n";
    else $p .= "Стиль канала неизвестен: пиши коротко (одно-два предложения, до 150 символов), живо и по-человечески, с лёгкой иронией, не больше одного эмодзи.\n\n";
    foreach ((array)($style['examples'] ?? []) as $i => $e) $p .= 'Пример поста канала ' . ($i + 1) . ":\n{$e}\n\n";
    if ($about !== '') $p .= "Что на картинке или видео:\n{$about}\n\n";
    if ($original !== '') $p .= "Исходный текст поста у другого источника (перепиши своими словами, не копируй, чужие названия каналов и ссылки не переноси):\n" . mb_substr($original, 0, 800) . "\n\n";
    if ($current !== '') $p .= "Черновик подписи, который нужно улучшить, сохранив смысл:\n" . mb_substr($current, 0, 800) . "\n\n";
    return $p . "Требования: пиши на языке канала, не длиннее {$limit} символов; не выдумывай факты, имена и цифры, которых нет выше; без ссылок и без @упоминаний; "
        . 'не используй штампы и пафос («пусть это принесёт улыбку», «идеальная картина», «это тот случай»); не описывай картинку дословно, а подай её с характером; '
        . 'ответь только текстом подписи, без кавычек, без слов «Вот подпись» и пояснений.';
}

/** Подпись от ИИ в стиле канала → текст. $context: ['description' => что на медиа, 'original' => исходный текст, 'current' => черновик]. */
function channel_caption_generate(string $channel_id, array $context, $uid = null): string {
    $uid = $uid ?: app_user_id();
    $channel = channel_get($channel_id, $uid) ?? throw new DomainException('Такого канала нет.');
    $clip = fn($k) => trim(mb_substr((string)($context[$k] ?? ''), 0, 1500));
    [$about, $original, $current] = [$clip('description'), $clip('original'), $clip('current')];
    if ($about === '' && $original === '' && $current === '') throw new RuntimeException('Не о чем писать: у записи нет ни описания, ни текста. Напишите пару слов в поле текста, и ИИ их разовьёт.');
    caption_quota_take($uid);
    $text = trim(strip_cjk(ai_generate_local(smart_model(), caption_prompt($channel, $about, $original, CAPTION_MAX_CHARS - 100, $current), [], null,
        ['temperature' => 0.8, 'num_predict' => 420], false)));
    $text = trim($text, " \t\n\r\"«»");
    if ($text === '') throw new RuntimeException('Модель не написала подпись: попробуйте ещё раз.');
    return mb_substr($text, 0, CAPTION_MAX_CHARS);
}

/** Запустить изучение стиля в фоне (минуты на CPU); ход виден в channel_style_public: busy / error. true — запущено. */
function channel_style_learn_async(string $channel_id): bool {
    $uid = app_user_id();
    channel_get($channel_id, $uid) ?? throw new DomainException('Такого канала нет.');
    $started = false;
    kv_update('channel_style_job:' . $channel_id, function ($job) use (&$started) {
        $job = (array)$job;
        if (($job['status'] ?? '') === 'running' && time() - (int)($job['at'] ?? 0) < 600) return null;
        $started = true;
        return ['status' => 'running', 'at' => time()];
    }, []);
    if ($started) exec('/usr/bin/php ' . escapeshellarg(ROOT . '/tools/cli.php') . ' channel-style ' . escapeshellarg($uid) . ' ' . escapeshellarg($channel_id) . ' >/dev/null 2>&1 &');
    return $started;
}
