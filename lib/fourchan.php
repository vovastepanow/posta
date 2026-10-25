<?php
// 4chan: открытый JSON API (доски → треды → посты с файлами). Устроено как Двач: доски по теме, треды, посты с медиа.
// Правила API 4chan: не чаще одного запроса в секунду, If-Modified-Since для каталогов и тредов. Файлы — только с заголовками
// браузера, Referer и паузой 2 с (иначе 429). Тексты — английские: переводятся в подписи при генерации.

const FOURCHAN_API = 'https://a.4cdn.org';
const FOURCHAN_IMG = 'https://i.4cdn.org';
const FOURCHAN_BOARDS_PER_RUN = 4;
const FOURCHAN_THREADS_PER_BOARD = 6;
const FOURCHAN_POSTS_PER_THREAD = 20;
const FOURCHAN_API_PER_RUN = 30;
const FOURCHAN_MEDIA_GAP = 2.0;           // секунд между файлами
const FOURCHAN_API_GAP = 1.0;             // секунд между запросами к API
const FOURCHAN_BOARDS_CACHE = 86400;
const FOURCHAN_ALWAYS_BOARDS = ['b'];   // берутся под любую тему: контент отовсюду, отбирает проверка по теме
const FOURCHAN_UA = 'Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36';
const FOURCHAN_BOARD_TAGS = [
    'wsg' => ['гифки', 'юмор', 'мемы'], 'a' => ['аниме', 'японская культура', 'манга'], 'v' => ['видеоигры', 'игры'], 'vg' => ['видеоигры', 'игры'],
    'vr' => ['виртуальная реальность', 'видеоигры'], 'ck' => ['еда', 'кулинария', 'готовка', 'рецепты'], 'an' => ['животные', 'питомцы', 'кошки', 'собаки'],
    'fit' => ['фитнес', 'спорт', 'тренировки', 'здоровье'], 'g' => ['технологии', 'программирование', 'компьютеры', 'гаджеты'], 'sci' => ['наука', 'космос', 'научпоп'],
    'his' => ['история'], 'tv' => ['кино', 'сериалы', 'фильмы'], 'sp' => ['спорт', 'футбол'], 'o' => ['автомобили', 'авто', 'транспорт'],
    'out' => ['природа', 'активный отдых', 'туризм', 'походы'], 'trv' => ['путешествия', 'туризм', 'отдых'], 'diy' => ['своими руками', 'рукоделие', 'хобби', 'мастерская'],
    'wg' => ['обои', 'природа', 'красивые картинки'], 'p' => ['фотография', 'фото'], 'x' => ['паранормальное', 'мистика', 'крипота', 'ужасы'],
    'biz' => ['бизнес', 'деньги', 'финансы', 'криптовалюта'], 'co' => ['комиксы', 'мультфильмы'], 'mu' => ['музыка'], 'toy' => ['игрушки', 'хобби'],
    'n' => ['транспорт', 'поезда', 'самолёты'], 'pol' => ['политика', 'новости'],
];   // «k» (оружие) в темах не используется

/** Счётчики прохода: запросы к API и медиа. Сбрасываются в начале прохода парсера. */
function fourchan_begin_pass(): void { $GLOBALS['fourchan_stats'] = ['api' => 0, 'media' => 0, 'api_cached' => 0]; }

function fourchan_stat(string $key, int $add = 1): void {
    $GLOBALS['fourchan_stats'][$key] = ($GLOBALS['fourchan_stats'][$key] ?? 0) + $add;
}

/** JSON API 4chan с If-Modified-Since: 304 — прошлый ответ из kv. Лимит запросов за проход и один запрос в секунду. */
function fourchan_get_json(string $path) {
    $key = 'fourchan:' . sha1($path);
    $cached = kv_get($key);
    // Свежая копия (моложе BOARD_POLL_MIN) — без похода в сеть; старше — условный запрос с 304 (FRESH_ORDER_POLITE_FETCH_PROMPT.md, часть B.4)
    if (is_array($cached) && isset($cached['at']) && time() - (int)$cached['at'] < BOARD_POLL_MIN && is_array(json_decode((string)$cached['body'], true))) {
        fourchan_stat('api_cached');
        return json_decode((string)$cached['body'], true);
    }
    if (($GLOBALS['fourchan_stats']['api'] ?? 0) >= FOURCHAN_API_PER_RUN) throw new RuntimeException('лимит запросов к 4chan за проход исчерпан');
    fourchan_stat('api');
    $headers = ['Accept: application/json'];
    if (is_array($cached) && $cached['modified'] !== '') $headers[] = 'If-Modified-Since: ' . $cached['modified'];
    host_throttle('a.4cdn.org', FOURCHAN_API_GAP);
    $r = fetch_public(FOURCHAN_API . '/' . ltrim($path, '/'), 4 * 1024 * 1024, null, 10, FOURCHAN_UA, $headers);
    if ($r['status'] === 304 && is_array($cached)) { fourchan_stat('api_cached'); kv_update($key, fn($c) => ['at' => time()] + (array)$c); return json_decode((string)$cached['body'], true); }
    if (!$r['ok']) throw new RuntimeException('4chan не отдал ' . $path . ($r['status'] ? " (HTTP {$r['status']})" : ': ' . $r['error']));
    $data = json_decode((string)$r['bytes'], true);
    if ($data === null) throw new RuntimeException('4chan отдал не JSON: ' . $path);
    kv_update($key, fn() => ['modified' => (string)$r['last_modified'], 'body' => (string)$r['bytes'], 'at' => time()]);
    return $data;
}

/** Доски: boards.json, кэш сутки: [['board', 'title', 'ws']]. */
function fourchan_boards(): array {
    $cached = kv_get('fourchan:boards');
    if (is_array($cached) && time() - (int)$cached['at'] < FOURCHAN_BOARDS_CACHE) return $cached['list'];
    $data = fourchan_get_json('boards.json');
    $list = [];
    foreach ((array)($data['boards'] ?? []) as $b) {
        if (!preg_match('~^[a-z0-9]{1,6}$~', (string)($b['board'] ?? ''))) continue;
        $list[] = ['board' => $b['board'], 'title' => (string)($b['title'] ?? ''), 'ws' => (int)($b['ws_board'] ?? 0) === 1];
    }
    kv_update('fourchan:boards', fn() => ['at' => time(), 'list' => $list]);
    return $list;
}

/** Доски, которые разрешено открывать: рабочие (worksafe) — всем, остальные — тем, кто включил «Показывать чувствительный и 18+ контент». */
function fourchan_allowed_boards(string $topic): array {
    $adult = user_adult_allowed();
    return array_values(array_filter(fourchan_boards(), fn($b) => $b['ws'] || $adult));
}

/** Доски по теме: теги досок совпадают с темой (как у Двача). Доска «k» (оружие) тегов не имеет и в темах не участвует. */
function fourchan_boards_for_topic(string $topic): array {
    $words = dvach_tag_words($topic);
    $allowed = array_column(fourchan_allowed_boards($topic), 'board');
    $out = [];
    foreach (FOURCHAN_BOARD_TAGS as $board => $tags) {
        if (!in_array($board, $allowed, true)) continue;
        foreach ($tags as $tag) if (dvach_tag_matches($tag, $words)) { $out[] = $board; break; }
    }
    if (!$out) fourchan_model_boards($topic);   // тегов нет — выбор моделью, как у Двача
    return array_values(array_unique([...$out, ...array_intersect([...FOURCHAN_ALWAYS_BOARDS, ...fourchan_approved_boards($topic)], $allowed)]));
}

/**
 * Доски без совпавших тегов: модель выбирает до трёх по названиям безопасных досок — один раз на тему, результат в одобренные.
 * Ошибка модели — тема не отмечается, попробуем в следующий проход. Модель не вызывается, если доски уже одобрены.
 */
function fourchan_model_boards(string $topic): void {
    $key = mb_strtolower(trim($topic));
    $asked = (array)kv_get('fourchan_model_asked', []);
    if (fourchan_approved_boards($topic) || isset($asked[$key])) return;
    $allowed = fourchan_allowed_boards($topic);
    $lines = array_map(fn($b) => "{$b['board']} — {$b['title']}", $allowed);
    $prompt = "Тема контента: " . topic_prompt_context($topic) . ". Ниже доски 4chan: адрес — название.\n" . implode("\n", $lines)
        . "\n\nВыбери до 3 досок, где чаще всего публикуют картинки и видео именно по этой теме. Выбирай только доски, которые прямо о ней."
        . " Общие доски (юмор, аниме, обои, игры) для узкой темы не выбирай. Если подходящих нет, верни пустой список."
        . " Ответь только JSON: {\"boards\": [\"адрес\", ...]}";
    try {
        $answer = decode_model_json(ai_generate_local(smart_model(), $prompt, [], 'json', ['temperature' => 0, 'num_predict' => 80], false)) ?? [];   // основная модель: маленькая путает темы
    } catch (Throwable $e) {
        cli_echo('-> Нейросеть не выбрала доски 4chan: ' . $e->getMessage());
        return;
    }
    kv_update('fourchan_model_asked', fn($m) => [...(array)$m, $key => time()], []);
    $valid = array_column($allowed, 'board');
    foreach (array_slice((array)($answer['boards'] ?? []), 0, 3) as $b) {
        $b = strtolower(trim((string)$b, " /\t"));
        if (in_array($b, $valid, true)) fourchan_approve_board($topic, $b);
    }
    cli_echo('-> Доски 4chan для темы «' . $topic . '» выбрала модель: ' . (fourchan_approved_boards($topic) ? '/' . implode('/, /', fourchan_approved_boards($topic)) . '/' : 'подходящих нет') . '.');
}

/** Доски этого прохода: не больше FOURCHAN_BOARDS_PER_RUN, выбор случайный. */
function fourchan_boards_this_run(array $boards): array {
    shuffle($boards);
    return array_slice($boards, 0, FOURCHAN_BOARDS_PER_RUN);
}

/** Живые треды доски из каталога (без закреплённых и закрытых), свежие сначала. */
function fourchan_threads(string $board): array {
    $pages = fourchan_get_json("$board/catalog.json");
    $threads = [];
    foreach ((array)$pages as $page) foreach ((array)($page['threads'] ?? []) as $t) {
        if (empty($t['sticky']) && empty($t['closed']) && isset($t['no'])) $threads[] = $t;
    }
    usort($threads, fn($a, $b) => (int)($b['last_modified'] ?? 0) <=> (int)($a['last_modified'] ?? 0));
    return $threads;
}

/** Посты треда (первые FOURCHAN_POSTS_PER_THREAD). */
function fourchan_thread_posts(string $board, int $thread): array {
    $data = fourchan_get_json("$board/thread/$thread.json");
    return array_slice(array_values((array)($data['posts'] ?? [])), 0, FOURCHAN_POSTS_PER_THREAD);
}

/** Файл поста: [['image' (картинка или превью), 'video' (ролик или '')]]. Адреса — i.4cdn.org/<доска>/<tim><ext>. */
function fourchan_post_media(string $board, array $post): array {
    if (empty($post['tim']) || empty($post['ext'])) return [];
    $ext = strtolower((string)$post['ext']);
    $file = FOURCHAN_IMG . "/$board/" . (int)$post['tim'] . $ext;
    $thumb = FOURCHAN_IMG . "/$board/" . (int)$post['tim'] . 's.jpg';
    if (in_array($ext, ['.webm', '.mp4'], true)) return [['image' => $thumb, 'video' => $file]];
    if (in_array($ext, ['.jpg', '.jpeg', '.png', '.gif'], true)) return [['image' => $file, 'video' => '']];
    return [];
}

/** Текст поста: HTML → текст; «>>123» убираем, «>зелёный текст» остаётся без знака «>». */
function fourchan_post_text(array $post): string {
    $html = (string)($post['com'] ?? '');
    $text = html_entity_decode(strip_tags(preg_replace('~<br\s*/?>~i', "\n", $html) ?? $html), ENT_QUOTES | ENT_HTML5, 'UTF-8');
    $lines = [];
    foreach (explode("\n", $text) as $line) {
        $line = trim(preg_replace('~>>\d+~', '', $line) ?? $line);
        if ($line !== '') $lines[] = ltrim($line, '>');
    }
    $subject = trim(html_entity_decode((string)($post['sub'] ?? ''), ENT_QUOTES | ENT_HTML5, 'UTF-8'));
    return trim(implode("\n", array_filter([$subject, implode("\n", $lines)], fn($x) => $x !== '')));
}

/** Ссылка на пост. */
function fourchan_post_url(string $board, int $thread, int $post): string {
    return "https://boards.4chan.org/$board/thread/$thread#p$post";
}

/** Причина, по которой тред не годится (закрыт, мало постов, молчит): или null. */
function fourchan_thread_dead_reason(array $t): ?string {
    if (!empty($t['closed'])) return 'тред закрыт';
    if ((int)($t['replies'] ?? 0) < DVACH_THREAD_MIN_POSTS) return 'в треде всего ' . (int)($t['replies'] ?? 0) . ' ответов';
    $days = (int)floor((time() - (int)($t['last_modified'] ?? 0)) / DAY_SECONDS);
    return $days > SOURCE_STALE_DAYS ? "в тред не писали {$days} дн" : null;
}

/** Медиа для загрузки: заголовки браузера и Referer доски — иначе 429. */
function fourchan_media_headers(string $url): array {
    if (!str_starts_with($url, FOURCHAN_IMG . '/')) return [];
    $board = explode('/', substr($url, strlen(FOURCHAN_IMG) + 1))[0];
    return ['Referer: https://boards.4chan.org/' . $board . '/', 'Accept: image/*,video/*;q=0.8,*/*;q=0.5'];
}

/** Файл 4chan по адресу: пауза FOURCHAN_MEDIA_GAP с хостом, заголовки браузера и Referer. Не получилось — ''. */
function fourchan_fetch_media(string $url, string $ext_kind = 'image'): string {
    host_throttle('i.4cdn.org', FOURCHAN_MEDIA_GAP);
    fourchan_stat('media');
    $r = fetch_public($url, $ext_kind === 'video' ? VIDEO_MAX_BYTES : MAX_IMAGE_BYTES, null, 25, FOURCHAN_UA, fourchan_media_headers($url));
    return $r['ok'] ? (string)$r['bytes'] : '';
}

/** Медиа-посты одного треда: поля записи пула, как у Двача. Модерация текста — до модели. $extra — общие поля треда (отбор по теме). */
function fourchan_thread_media(string $board, array $t, int $cap = 200, array $extra = []): array {
    $thread = (int)$t['no'];
    $out = [];
    try { $posts = fourchan_thread_posts($board, $thread); } catch (Throwable $e) { cli_echo("-> 4chan /$board/{$thread}: " . $e->getMessage()); return []; }
    $op = $posts[0] ?? $t;   // ОП-пост треда: название и шапка; в предпросмотре каталога у треда нет темы — берём из ОП-поста
    $title = mb_substr(clean_html_text($t['sub'] ?? '') ?: clean_html_text($op['sub'] ?? ''), 0, THREAD_TITLE_MAX);
    $op_text = mb_substr(fourchan_post_text($op), 0, THREAD_OP_MAX);
    foreach ($posts as $post) {
        if (!($files = fourchan_post_media($board, $post))) continue;
        $text = fourchan_post_text($post);
        $id = (string)$post['no'];
        $flag = strtoupper((string)($post['country'] ?? '')) === 'UA' || strtoupper((string)($op['country'] ?? '')) === 'UA' ? 'UA' : '';   // флаг у поста или у ОП (тогда весь тред) — для фильтра Украины
        $out[] = ['source_key' => "4chan:$board:$thread", 'posted_at' => (int)($post['time'] ?? 0), 'board' => $board, 'thread_id' => (string)$thread, 'post_id' => $id, 'country' => $flag,
            'source' => PLATFORM_4CHAN . " /$board/ · " . mb_substr($title ?: "#$thread", 0, 60),
            'source_url' => fourchan_post_url($board, $thread, (int)$id), 'image' => $files[0]['image'], 'images' => [$files[0]['image']],
            'video' => $files[0]['video'], 'captions' => [$text], 'text' => $text, 'engagement' => ['source' => '4chan', 'replies' => (int)($t['replies'] ?? 0), 'images' => (int)($t['images'] ?? 0)], 'url' => fourchan_post_url($board, $thread, (int)$id),
            'thread_title' => $title, 'thread_op' => (int)$id === $thread ? '' : $op_text, 'is_op' => (int)$id === $thread] + $extra;
        if (count($out) >= $cap) break;
    }
    return $out;
}

/** Посты 4chan доски для сбора: живые треды, не больше FOURCHAN_THREADS_PER_BOARD. С темой — треды по ней отбираются (IMAGEBOARD_CONTEXT_HASHTAGS_PROMPT.md, часть A). */
function parse_fourchan(string $board, int $thread_limit = FOURCHAN_THREADS_PER_BOARD, ?string $topic = null): array {
    $out = [];
    try { $threads = array_slice(fourchan_threads($board), 0, $thread_limit); } catch (Throwable $e) { cli_echo("-> 4chan /$board/: " . $e->getMessage()); return []; }
    $alive = array_values(array_filter($threads, fn($t) => fourchan_thread_dead_reason($t) === null));
    $gates = $topic === null ? [] : imageboard_thread_scores($topic, $board, array_map(fn($t) => ['id' => (string)$t['no'], 'title' => clean_html_text($t['sub'] ?? ''), 'op' => fourchan_post_text(['com' => $t['com'] ?? ''])], $alive));
    foreach ($alive as $t) {
        $gate = imageboard_thread_gate($gates[(string)$t['no']] ?? null);
        if ($gate === 'skip') { cli_echo("-> 4chan /$board/: тред «" . (clean_html_text($t['sub'] ?? '') ?: "#{$t['no']}") . "» не по теме (" . $gates[(string)$t['no']] . ') — пропуск.'); continue; }
        array_push($out, ...fourchan_thread_media($board, $t, 200 - count($out), ['thread_trust' => $gate === 'trust']));
        if (count($out) >= 200) break;
    }
    return $out;
}

/** Доски, которые пользователь одобрил для темы (сверх тегов). */
function fourchan_approved_boards(string $topic): array {
    return (array)(kv_get('fourchan_topic_boards', [])[mb_strtolower(trim($topic))] ?? []);
}

/** Одобрили доску для темы: попадает в её доски. */
function fourchan_approve_board(string $topic, string $board): void {
    if (!preg_match('~^[a-z0-9]{1,6}$~', $board)) return;
    kv_update('fourchan_topic_boards', function ($map) use ($topic, $board) {
        $map = (array)$map;
        $key = mb_strtolower(trim($topic));
        $map[$key] = array_values(array_unique([...(array)($map[$key] ?? []), $board]));
        return $map;
    });
}

/** Треды досок темы — кандидаты на вкладке «Источники» (как у Двача: доска → треды, без повторов и отказов). */
function fourchan_source_candidates(array $voted, string $topic): array {
    try { $boards = fourchan_boards_for_topic($topic); } catch (Throwable $e) { return []; }
    shuffle($boards);
    $out = [];
    foreach (array_slice($boards, 0, 3) as $board) {
        try { $threads = array_slice(fourchan_threads($board), 0, 10); } catch (Throwable $e) { continue; }
        foreach ($threads as $t) {
            $sid = "4chan:$board:" . (int)$t['no'];
            if (isset($voted[$sid]) || fourchan_thread_dead_reason($t) !== null) continue;
            $out[] = source_candidate($sid, PLATFORM_4CHAN, PLATFORM_4CHAN . " /$board/ · " . mb_substr(trim((string)($t['sub'] ?? '')) ?: '#' . $t['no'], 0, 80),
                'Тред 4chan с картинками по теме.', ['board' => $board, 'thread_id' => (string)$t['no'], 'thread_posts' => (int)($t['replies'] ?? 0), 'last_hit' => (int)($t['last_modified'] ?? 0)]);
        }
    }
    return $out;
}

/** Превью треда для карточки: медиа-посты треда и сводка. */
function fourchan_preview(array $c, int $count): array {
    $thread = ['no' => (int)$c['thread_id'], 'sub' => ''];
    return array_map(fn($p) => ['image' => $p['image'], 'images' => $p['images'], 'video' => $p['video'], 'text' => $p['text'] === '' ? EMPTY_POST_TEXT : $p['text'],
        'source' => $p['source'], 'post_id' => $p['post_id'], 'posted_at' => $p['posted_at'], 'url' => $p['source_url']],
        fourchan_thread_media($c['board'], $thread, $count));
}
