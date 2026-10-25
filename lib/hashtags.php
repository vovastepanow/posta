<?php
// Словарь хэштегов (TAXONOMY_PROMPT.md, часть C, упрощённо): канонические теги из data/taxonomy/hashtags/*.tsv,
// синонимы и нормализация. Без моделей и без базы данных: словарь читается из файлов и кэшируется на запрос.

/** Нормализация тега: без «#», строчные, ё → е, пробелы и дефисы → «_». Одна функция на всё: словарь, синонимы, ответы модели. */
function hashtag_norm(string $tag): string {
    $tag = mb_strtolower(trim($tag));
    $tag = str_replace(['ё', '#'], ['е', ''], $tag);
    return trim(preg_replace('/[\s\-]+/u', '_', $tag) ?? '', '_');
}

/** Словарь: ['tags' => [канонический => запись], 'aliases' => [нормализованный синоним => канонический]]. */
function hashtag_dictionary(): array {
    static $dict = null;
    if ($dict !== null) return $dict;
    $dict = ['tags' => [], 'aliases' => []];
    foreach (glob(ROOT . '/data/taxonomy/hashtags/*.tsv') ?: [] as $file) {
        foreach (file($file, FILE_IGNORE_NEW_LINES | FILE_SKIP_EMPTY_LINES) as $line) {
            if (str_starts_with($line, '#')) continue;
            [$tag, $topics, $kind, $synonyms] = array_pad(explode("\t", $line), 4, '');
            $canon = hashtag_norm($tag);
            if ($canon === '') continue;
            $dict['tags'][$canon] = ['tag' => $canon, 'topics' => array_values(array_filter(array_map('trim', explode(',', $topics)))), 'kind' => trim($kind)];
            foreach (array_filter(array_map('trim', explode('|', $synonyms))) as $syn) $dict['aliases'][hashtag_norm($syn)] = $canon;
        }
    }
    return $dict;
}

/** Хэштег по тексту (с «#», синонимом или каноническим): запись словаря или null. */
function hashtag_lookup(string $text): ?array {
    $norm = hashtag_norm($text);
    $dict = hashtag_dictionary();
    $canon = isset($dict['tags'][$norm]) ? $norm : ($dict['aliases'][$norm] ?? null);
    return $canon === null ? null : $dict['tags'][$canon];
}

/** Канонические теги частной темы по её ключу (обратный индекс словаря, на запрос). У тем без ключа и у общих веток — пусто. */
function hashtags_for_topic(string $key): array {
    static $index = null;
    if ($index === null) {
        $index = [];
        foreach (hashtag_dictionary()['tags'] as $tag) foreach ($tag['topics'] as $topic) if ($topic !== '*') $index[$topic][] = $tag['tag'];
    }
    return $key === '' ? [] : ($index[$key] ?? []);
}

// ---- Хэштеги поста: рубрика → имя → формат (не больше трёх, только канонические) ----

const HASHTAG_MAX_LEN = 16;
const CHANNEL_HASHTAG_MODES = ['off', 'rubric', 'full'];   // хэштеги в постах канала: нет, только рубрика, рубрика + имя + формат
const HASHTAG_CANDIDATES_MAX = 500;   // неизвестных имён копим для пополнения словаря

/** Теги словаря одного вида (rubric, entity, format) по порядку файлов. */
function hashtags_of_kind(string $kind): array {
    return array_values(array_map(fn($t) => $t['tag'], array_filter(hashtag_dictionary()['tags'], fn($t) => $t['kind'] === $kind)));
}

/** Рубрика темы: своя рубрика пользователя (настройки), иначе из словаря. Ветка (ключ без точки) — рубрика её «прочего». null — нет. */
function hashtag_rubric(string $topic_key): ?string {
    if ($topic_key === '') return null;
    if (!str_contains($topic_key, '.')) $topic_key .= '.other';
    if (app_user_bind() !== '' && ($own = hashtag_rubric_override($topic_key)) !== null) return $own;
    foreach (hashtags_for_topic($topic_key) as $tag) if ((hashtag_dictionary()['tags'][$tag]['kind'] ?? '') === 'rubric') return $tag;
    return null;
}

/** Рубрика темы, заданная этим пользователем; null — нет. */
function hashtag_rubric_override(string $topic_key): ?string {
    $own = (string)(db_load_settings()['hashtag_rubrics'][$topic_key] ?? '');
    return $own !== '' ? $own : null;
}

/** Годится ли слово в тег: одно слово из букв и цифр (допустим «_»), 2–16 символов, не одни цифры. */
function hashtag_valid(string $tag): bool {
    return preg_match('/^[\p{L}\p{N}_]{2,' . HASHTAG_MAX_LEN . '}$/u', $tag) === 1 && !preg_match('/^\d+$/', $tag);
}

/** Поле entity от модели: без «#» и лишнего, до 40 символов. */
function content_entity_clean($entity): string {
    return mb_substr(trim(str_replace('#', '', strip_cjk((string)$entity)), " \t\n\r«»\"'.,"), 0, 40);
}

/** Правило поля entity для промптов анализа (неизменная часть — кэш префикса). */
function content_entity_rule(): string {
    return "entity: одно главное имя собственное из записи — человек, игра, бренд, фильм, мем-персонаж — как его пишут в хэштеге,"
        . " по-русски, если так принято (дота, скибиди, маск, wildberries). Нет явного имени — пустая строка. Не тема и не описание.\n";
}

/** Имя записи → канонический тег: словарь (вид entity), иначе годное новое имя (копится в кандидаты). null — нет имени. */
function hashtag_entity(string $entity, bool $note = true): ?string {
    if (($norm = hashtag_norm($entity)) === '') return null;
    if (($found = hashtag_lookup($norm)) !== null) return $found['kind'] === 'entity' ? $found['tag'] : null;   // синоним рубрики или формата — не имя
    $tag = str_replace('_', '', $norm);   // составные имена — слитно, как в словаре
    if (!hashtag_valid($tag)) return null;
    if ($note) hashtag_candidate_note($tag, $entity);
    return $tag;
}

/** Неизвестное имя — в kv hashtag_candidates: {тег: {count, last_seen, sample}}, самые частые и свежие остаются. */
function hashtag_candidate_note(string $tag, string $sample): void {
    kv_update('hashtag_candidates', function ($all) use ($tag, $sample) {
        $all = is_array($all) ? $all : [];
        $all[$tag] = ['count' => (int)($all[$tag]['count'] ?? 0) + 1, 'last_seen' => time(), 'sample' => mb_substr($sample, 0, 60)];
        if (count($all) > HASHTAG_CANDIDATES_MAX) {
            uasort($all, fn($a, $b) => [$b['count'], $b['last_seen']] <=> [$a['count'], $a['last_seen']]);
            $all = array_slice($all, 0, HASHTAG_CANDIDATES_MAX, true);
        }
        return $all;
    }, []);
}

/** Формат записи по её виду: видео, статья, опрос, подборка (от 3 картинок), переписка (текст-скрин). null — без формата. */
function hashtag_format(array $record): ?string {
    $video = post_item_has_video($record) || (($record['video_via'] ?? '') !== '' && empty($record['video_unavailable']));
    return match (true) {
        $video => 'видео',
        !empty($record['article']) => 'статья',
        !empty($record['poll']) => 'опрос',
        count(content_gallery($record)) >= 3 || count((array)($record['images'] ?? [])) >= 3 => 'подборка',
        ($record['format'] ?? '') === 'text' && ($record['image'] ?? '') !== '' => 'переписка',
        default => null,
    };
}

/**
 * Хэштеги поста: рубрика (из первой метки, иначе из темы) → имя (поле entity) → формат. Только канонические теги, с «#».
 * $note — копить неизвестные имена в кандидаты (при показе карточки не нужно).
 */
function post_hashtags(array $record, bool $note = false): array {
    $key = (string)(($record['tags'] ?? [])[0] ?? '') ?: (string)(topic_lookup((string)($record['topic'] ?? ''))['key'] ?? '');
    $tags = [hashtag_rubric($key), hashtag_entity((string)($record['entity'] ?? ''), $note), hashtag_format($record)];
    return array_values(array_unique(array_map(fn($t) => "#$t", array_filter($tags))));
}

/** Хэштеги из текста поста-источника → канонические (до 8). Неизвестные пропускаются. */
function post_source_hashtags(array $item): array {
    preg_match_all('~(?<![\p{L}\p{N}_])#([\p{L}\p{N}_]{2,40})~u', (string)($item['text'] ?? ''), $m);
    $out = [];
    foreach ($m[1] as $t) if (($found = hashtag_lookup($t)) !== null) $out[$found['tag']] = true;
    return array_slice(array_keys($out), 0, 8);
}

/** Подсказки тегов по началу слова: канонические, синоним ведёт к своему тегу. [['tag', 'kind']], до $limit. */
function hashtag_suggest(string $q, int $limit = 8): array {
    if (($q = hashtag_norm($q)) === '') return [];
    $dict = hashtag_dictionary();
    $hits = [];
    foreach ($dict['tags'] as $tag => $t) if (str_starts_with($tag, $q)) $hits[$tag] = $t['kind'];
    foreach ($dict['aliases'] as $alias => $tag) if (count($hits) < $limit && str_starts_with($alias, $q)) $hits[$tag] ??= $dict['tags'][$tag]['kind'];
    return array_map(fn($tag, $kind) => ['tag' => $tag, 'kind' => $kind], array_keys(array_slice($hits, 0, $limit, true)), array_slice($hits, 0, $limit, true));
}

/** Хэштеги из редактора: до трёх годных тегов с «#», без повторов. */
function publish_hashtags_input($raw): array {
    $list = is_string($raw) ? json_decode($raw, true) : $raw;
    $out = [];
    foreach (is_array($list) ? $list : [] as $t) if (hashtag_valid($tag = str_replace('_', '', hashtag_norm((string)$t)))) $out["#$tag"] = true;
    return array_slice(array_keys($out), 0, 3);
}

/**
 * Теги, которые разрешает канал: off — никаких, rubric — только рубрики (словарные или своя рубрика пользователя), full — все.
 * Неизвестные имена при публикации копятся в кандидаты словаря.
 */
function channel_hashtags(array $channel, array $tags): array {
    $mode = $channel['hashtags'] ?? 'off';
    if ($mode === 'off') return [];
    $own = app_user_bind() !== '' ? array_values(db_load_settings()['hashtag_rubrics'] ?? []) : [];
    $kind = fn($t) => in_array(substr($t, 1), $own, true) ? 'rubric' : (hashtag_lookup($t)['kind'] ?? null);
    foreach ($tags as $t) if ($kind($t) === null) hashtag_candidate_note(substr($t, 1), substr($t, 1));
    return $mode === 'rubric' ? array_values(array_filter($tags, fn($t) => $kind($t) === 'rubric')) : $tags;
}

/**
 * Хэштеги дописываются последней строкой текста, без повторов уже стоящих в тексте. Не влезает в $limit символов —
 * теги снимаются с конца (формат, потом имя, рубрика — последней).
 */
function text_with_hashtags(string $text, array $tags, int $limit): string {
    $text = rtrim($text);
    $tags = array_values(array_filter(array_unique($tags), fn($t) => !preg_match('~(?<![\p{L}\p{N}_])' . preg_quote($t, '~') . '(?![\p{L}\p{N}_])~u', $text)));
    while ($tags) {
        $out = ($text === '' ? '' : "$text\n\n") . implode(' ', $tags);
        if (mb_strlen($out) <= $limit) return $out;
        array_pop($tags);
    }
    return $text;
}
