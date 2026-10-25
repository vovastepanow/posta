<?php
// Проверка карты тем (taxonomy-check): без моделей и без записи в базу. Ключи, которые уже стоят в записях, терять нельзя.

const TAXONOMY_KEY_RE = '/^[a-z_]+\.[a-z0-9_]+$/';     // ключ частной темы: ветка.тема
const TAXONOMY_ABOUT_MAX = 140;                         // описание — одна короткая фраза
const TAXONOMY_CHILDREN_MIN = 2;
const TAXONOMY_CHILDREN_MAX = 80;

/** Ключи частных тем, которые уже встречаются в записях (истории и пула): их удалять нельзя. */
function taxonomy_used_keys(): array {
    $used = [];
    foreach (db_all('SELECT data FROM history_items UNION ALL SELECT data FROM pool_items') as $row) {
        $data = db_unjson($row['data'], []);
        foreach ((array)($data['tags'] ?? []) as $tag) if (is_string($tag) && $tag !== '') $used[$tag] = true;
    }
    return array_keys($used);
}

const TAXONOMY_HASHTAG_STOP = ['контент', 'пост', 'видео', 'фото', 'картинка', 'интересное', 'топ', 'лучшее', 'прикол'];

/** Ошибки словаря хэштегов: повтор, несуществующая тема, мусор (короче 2 символов, только цифры, стоп-слова). */
function taxonomy_hashtag_errors(array $topic_keys): array {
    $errors = [];
    $seen = [];
    $rubrics = [];   // тема => её рубрики
    foreach (hashtag_dictionary()['tags'] as $tag) {
        $name = $tag['tag'];
        if (mb_strlen($name) < 2 || preg_match('/^\d+$/', $name)) $errors[] = "хэштег «{$name}»: короче 2 символов или только цифры";
        if ($tag['kind'] !== 'format' && in_array($name, TAXONOMY_HASHTAG_STOP, true)) $errors[] = "хэштег «{$name}»: стоп-слово";
        if (isset($seen[$name])) $errors[] = "повтор хэштега «{$name}»";
        $seen[$name] = true;
        if ($tag['topics'] === []) $errors[] = "хэштег «{$name}»: нет темы";
        if ($tag['topics'] === ['*']) { if ($tag['kind'] !== 'format') $errors[] = "хэштег «{$name}»: «*» (любая тема) — только у формата"; continue; }
        foreach ($tag['topics'] as $topic) if (!isset($topic_keys[$topic])) $errors[] = "хэштег «{$name}»: тема «{$topic}» не существует";
        if ($tag['kind'] === 'rubric') foreach ($tag['topics'] as $topic) $rubrics[$topic][] = $name;
    }
    // Рубрика — ровно одна на частную тему (ветки без точки в ключе рубрики не имеют)
    foreach (array_keys($topic_keys) as $key) {
        if (!str_contains($key, '.')) continue;
        $n = count($rubrics[$key] ?? []);
        if ($n !== 1) $errors[] = "тема «{$key}»: рубрик {$n}, нужна ровно одна" . ($n ? ' (' . implode(', ', $rubrics[$key]) . ')' : '');
    }
    return $errors;
}

/** Ошибки и сводка: ['errors' => [...], 'summary' => [...]]. Пустой errors — карта в порядке. */
function taxonomy_check(): array {
    $errors = [];
    $tree = topic_tree();
    $keys = [];
    $names = [];
    $summary = ['веток' => count($tree), 'частных тем' => 0, 'по веткам' => []];
    foreach ($tree as $branch) {
        $bkey = $branch['key'];
        if (!preg_match('/^[a-z_]+$/', $bkey)) $errors[] = "ветка «{$bkey}»: ключ не по шаблону";
        if (isset($keys[$bkey])) $errors[] = "повтор ключа «{$bkey}»";
        $keys[$bkey] = true;
        $children = $branch['children'];
        $summary['частных тем'] += count($children);
        $summary['по веткам'][$bkey] = count($children);
        if (count($children) < TAXONOMY_CHILDREN_MIN || count($children) > TAXONOMY_CHILDREN_MAX) $errors[] = "ветка «{$bkey}»: частных тем " . count($children) . ' (нужно ' . TAXONOMY_CHILDREN_MIN . '–' . TAXONOMY_CHILDREN_MAX . ')';
        if (!in_array($bkey . '.other', array_column($children, 'key'), true)) $errors[] = "ветка «{$bkey}»: нет «{$bkey}.other»";
        foreach ([$branch, ...$children] as $e) {
            $key = (string)($e['key'] ?? '');
            if ($e !== $branch && !preg_match(TAXONOMY_KEY_RE, $key)) $errors[] = "ключ «{$key}» не по шаблону ветка.тема";
            if (isset($keys[$key]) && $e !== $branch) $errors[] = "повтор ключа «{$key}»";
            $keys[$key] = true;
            $about = trim((string)($e['about'] ?? ''));
            if ($about === '') $errors[] = "«{$key}»: пустой about";
            elseif (mb_strlen($about) > TAXONOMY_ABOUT_MAX) $errors[] = "«{$key}»: about длиннее " . TAXONOMY_ABOUT_MAX . ' символов';
            foreach (array_merge([$e['title']], (array)($e['aliases'] ?? [])) as $name) {
                $norm = topic_norm((string)$name);
                if ($norm === '') continue;
                if (isset($names[$norm]) && $names[$norm] !== $key) $errors[] = "название/синоним «{$name}» у «{$key}» совпадает с «{$names[$norm]}»";
                $names[$norm] = $names[$norm] ?? $key;
            }
        }
    }
    $errors = array_merge($errors, taxonomy_hashtag_errors($keys));
    $present = array_fill_keys(array_keys($keys), true);
    foreach (taxonomy_used_keys() as $used) {
        if (!isset($present[$used])) $errors[] = "пропал ключ, который есть в записях: «{$used}»";
    }
    return ['errors' => $errors, 'summary' => $summary];
}
