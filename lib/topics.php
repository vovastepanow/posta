<?php
// Реестр тем (sources_db.json → topics): ['id', 'title', 'scope' (broad|narrow), 'parent', 'aliases', 'about', 'profile', …].
// Для темы годятся её источники (по названию и синонимам), у широкой — ещё источники её узких тем, у узкой без своих
// источников (меньше TOPIC_PARENT_FALLBACK_MIN) — источники широкой. Профиль темы (критерии, вопрос «да/нет», план поиска)
// пишет умная модель один раз и он идёт во все промпты темы.

const TOPIC_PARENT_FALLBACK_MIN = 3;
const TOPIC_TITLE_MAX = 40;
const TOPIC_SCOPES = ['broad', 'narrow'];
const TOPIC_EMBED_MODEL = 'bge-m3';       // «та же тема?» по эмбеддингам; нет модели — проверка строже
const TOPIC_SMART_TIMEOUT = 150;
const TOPIC_SAME_SIMILARITY = 0.85;       // «мемы и приколы» ↔ «Мемы и юмор» ≈ 0.91, «котики» ↔ «Кошки» ≈ 0.76
const TOPIC_PROFILE_VERSION = 1;
const TOPIC_PROFILE_FALLBACK_RETRY = 3600; // профиль от быстрой модели переписывается не раньше чем через час

/** Нижний регистр, ё → е, без знаков и лишних пробелов. */
function topic_norm($text) {
    $text = preg_replace('/[^\p{L}\p{N}\s-]+/u', ' ', str_replace('ё', 'е', mb_strtolower(trim((string)$text)))) ?? '';
    return trim(preg_replace('/\s+/u', ' ', $text) ?? '');
}

function topic_id_for($title) { return 't' . substr(sha1(topic_norm($title)), 0, 8); }

function topic_title_case($text) {
    $text = trim(preg_replace('/\s+/u', ' ', str_replace(['«', '»', '"', '“', '”'], '', (string)$text)) ?? '');
    return $text === '' ? '' : mb_strtoupper(mb_substr($text, 0, 1)) . mb_substr($text, 1);
}

function topic_normalize_entry($e) {
    if (!is_array($e) || trim((string)($e['title'] ?? '')) === '') return null;
    $title = mb_substr(topic_title_case($e['title']), 0, TOPIC_TITLE_MAX);
    $aliases = [];
    foreach ((array)($e['aliases'] ?? []) as $a) if (($a = trim((string)$a)) !== '' && topic_norm($a) !== topic_norm($title)) $aliases[topic_norm($a)] = mb_substr($a, 0, 80);
    $scope = in_array($e['scope'] ?? '', TOPIC_SCOPES, true) ? $e['scope'] : 'narrow';
    $valid_id = fn($id) => preg_match('/^t[0-9a-f]{8}$/', (string)$id) === 1;
    return [
        'id' => $valid_id($e['id'] ?? '') ? $e['id'] : topic_id_for($title),
        'title' => $title, 'scope' => $scope,
        'parent' => $scope === 'narrow' && $valid_id($e['parent'] ?? '') ? $e['parent'] : null,
        'aliases' => array_values(array_slice($aliases, 0, 30)),
        'about' => mb_substr(trim((string)($e['about'] ?? '')), 0, 200),
        'classified' => !empty($e['classified']),
        'smart' => !empty($e['smart']),       // описана умной моделью (иначе её переопишут)
        'profile' => topic_profile_clean($e['profile'] ?? []),
        'manual' => !empty($e['manual']),     // место темы поправил пользователь
        'created_at' => (int)($e['created_at'] ?? time()),
        'key' => preg_match('/^[a-z_]+(\.[a-z_]+)?$/', (string)($e['key'] ?? '')) ? $e['key'] : '',   // ключ из TOPIC_TREE
        'seed' => !empty($e['seed']),         // тема пришла из готового дерева
        'nsfw' => !empty($e['nsfw'])          // ветка 18+ или шок (частные наследуют)
    ];
}

/** Реестр из каталога: тема по умолчанию всегда есть, темы, которые встречаются у источников и подборов, регистрируются как есть. */
function topic_registry_from_db(array $db) {
    $registry = [];
    foreach ((array)$db['topics'] as $e) if ($e = topic_normalize_entry($e)) $registry[$e['id']] = $e + [];
    $known = [];
    foreach ($registry as $id => $e) foreach (array_merge([$e['title']], $e['aliases']) as $name) $known[topic_norm($name)] = $id;
    $add = function ($entry) use (&$registry, &$known) {
        if (!$entry || isset($known[topic_norm($entry['title'])])) return;
        $registry[$entry['id']] = $entry;
        $known[topic_norm($entry['title'])] = $entry['id'];
    };
    if (isset($known[topic_norm(DEFAULT_TOPIC)])) $registry[$known[topic_norm(DEFAULT_TOPIC)]]['smart'] = true;
    $add(topic_normalize_entry(['title' => DEFAULT_TOPIC, 'scope' => 'broad', 'classified' => true, 'smart' => true, 'created_at' => 0,
        'about' => 'Мемы, приколы, смешные картинки и видео на любые темы.']));
    $used = array_merge(array_keys((array)($db['dvach_topic_boards'] ?? [])), array_keys((array)($db['discovery']['runs'] ?? [])),
        array_column((array)($db['source_suggestions'] ?? []), 'topic'));
    foreach (array_merge((array)($db['telegram_channels'] ?? []), (array)($db['vk_publics'] ?? [])) as $e) array_push($used, ...(array)($e['topics'] ?? []));
    foreach ($used as $t) if (trim((string)$t) !== '') $add(topic_normalize_entry(['title' => (string)$t]));
    return array_values($registry);
}

/** [id => тема], кэш на запрос. */
function topic_registry($reload = false) {
    static $registry = null;
    if ($registry === null || $reload) {
        $registry = array_column(load_source_db()['topics'], null, 'id');
        topic_related_norms(null, true);
    }
    return $registry;
}

function topic_names(array $e) { return array_values(array_unique(array_map('topic_norm', array_merge([$e['title']], $e['aliases'])))); }

/** Тема по названию или синониму, иначе null. */
function topic_lookup($text) {
    if (($norm = topic_norm($text)) === '') return null;
    foreach (topic_registry() as $e) if (in_array($norm, topic_names($e), true)) return $e;
    return null;
}

function topic_save(array $entry) {
    $entry = topic_normalize_entry($entry) ?? throw new InvalidArgumentException('Пустое название темы.');
    update_source_db(function ($db) use ($entry) {
        $db['topics'] = [...array_values(array_filter($db['topics'], fn($old) => $old['id'] !== $entry['id'])), $entry];
        return $db;
    });
    topic_registry(true);
    return $entry;
}

/** Тема из реестра; нет — регистрируется как есть (уточнит модель при запуске парсера). */
function topic_ensure($text) {
    return topic_lookup($text) ?? (trim((string)$text) === '' ? null : topic_save(['title' => $text, 'scope' => 'narrow']));
}

function topic_add_alias(array $e, $alias) {
    if (in_array(topic_norm($alias), topic_names($e), true)) return $e;
    $e['aliases'][] = trim((string)$alias);
    return topic_save($e);
}

function topic_children(array $e) { return array_values(array_filter(topic_registry(), fn($c) => $c['parent'] === $e['id'])); }
function topic_parent(array $e) { return $e['parent'] !== null ? (topic_registry()[$e['parent']] ?? null) : null; }

/** Сколько каналов и пабликов привязано к самой теме. */
function topic_own_source_count(array $e, ?array $db = null) {
    $names = array_flip(topic_names($e));
    $count = 0;
    foreach (CATALOG_KEYS as $key) foreach (($db ?? load_source_db())[$key] as $s) {
        foreach ((array)($s['topics'] ?? []) as $t) if (isset($names[topic_norm($t)])) { $count++; break; }
    }
    return $count;
}

/** Нормальные формы тем, источники которых годятся для $topic. */
function topic_related_norms($topic, $reset = false) {
    static $cache = [];
    if ($reset) return $cache = [];
    $key = topic_norm($topic);
    if (isset($cache[$key])) return $cache[$key];
    if (($e = topic_lookup($topic)) === null) return $cache[$key] = [$key => true];
    $norms = array_flip(topic_names($e));
    if ($e['scope'] === 'broad') foreach (topic_children($e) as $c) $norms += array_flip(topic_names($c));
    elseif (($parent = topic_parent($e)) && topic_own_source_count($e) < TOPIC_PARENT_FALLBACK_MIN) $norms += array_flip(topic_names($parent));
    return $cache[$key] = $norms;
}

function topics_are_equivalent($entry_topic, $topic) { return isset(topic_related_norms($topic)[topic_norm($entry_topic)]); }

function topic_same($a, $b) {
    return topic_norm($a) === topic_norm($b) || (($e = topic_lookup($a)) !== null && in_array(topic_norm($b), topic_names($e), true));
}

/** Части составной темы «первая + вторая». */
function topic_parts($topic) { return array_values(array_filter(array_map('trim', explode(' + ', (string)$topic)), 'strlen')); }

/** Тема для промптов: название, что в неё входит, широкая тема, другие названия. */
function topic_prompt_context($topic) {
    if (count($parts = topic_parts($topic)) > 1) return implode(' и ', array_map('topic_prompt_context', $parts));
    if (($e = topic_lookup($topic)) === null) return "«{$topic}»";
    $notes = array_filter([rtrim($e['about'], '.'), ($p = topic_parent($e)) ? "часть темы «{$p['title']}»" : '',
        $e['aliases'] ? 'ещё называют: ' . implode(', ', array_slice($e['aliases'], 0, 4)) : '']);
    return "«{$e['title']}»" . ($notes ? ' (' . implode('; ', $notes) . ')' : '');
}

/** Для интерфейса. */
function topic_public(array $e, ?array $db = null) {
    $parent = topic_parent($e);
    $own = topic_own_source_count($e, $db);
    $p = $e['profile'];
    return ['id' => $e['id'], 'key' => $e['key'], 'nsfw' => $e['nsfw'], 'title' => $e['title'], 'scope' => $e['scope'], 'parent' => $parent ? ['id' => $parent['id'], 'title' => $parent['title']] : null,
        'path' => $parent ? "{$parent['title']} › {$e['title']}" : $e['title'], 'about' => $e['about'], 'aliases' => $e['aliases'], 'own_sources' => $own,
        'uses_parent_sources' => $parent !== null && $own < TOPIC_PARENT_FALLBACK_MIN, 'children' => array_column(topic_children($e), 'title'),
        'criteria' => $p['criteria'] ?? '', 'exclude' => $p['exclude'] ?? '', 'question' => $p['question'] ?? '',
        'profile' => $p ? ['smart' => $p['smart'], 'at' => $p['at']] : null];
}

/** Тема 18+ или шок: её флаг или флаг её общей темы. Пока никто не вызывает, понадобится для фильтров. */
function topic_is_nsfw($text) {
    if (($e = topic_lookup($text)) === null) return false;
    return $e['nsfw'] || (bool)(topic_parent($e)['nsfw'] ?? false);
}

// ---- Засев готового дерева (lib/topic_tree.php) ----

/** Тема реестра по названию (сначала) или синониму ветки; null — нет. */
function topic_tree_find(array $topics, array $names) {
    foreach ($names as $name) {
        $norm = topic_norm($name);
        foreach ($topics as $e) if (in_array($norm, array_map('topic_norm', array_merge([$e['title']], $e['aliases'])), true)) return $e;
    }
    return null;
}

/**
 * Засев TOPIC_TREE в реестр: существующие темы обновляются (id, пользовательские синонимы и профиль сохраняются),
 * новые создаются. Всё за один вызов каталога. Повторный запуск ничего не меняет.
 * Возвращает счётчики created / updated / moved / skipped.
 */
function topic_tree_seed(): array {
    $counts = ['created' => 0, 'updated' => 0, 'moved' => 0, 'skipped' => 0];
    update_source_db(function ($db) use (&$counts) {
        $topics = array_values($db['topics']);
        $resolved = [];   // ключ ветки → id темы
        $nodes = [];
        foreach (TOPIC_TREE as $branch) {
            $nodes[] = [$branch, null, $branch['nsfw']];
            foreach ($branch['children'] as $child) $nodes[] = [$child, $branch, $branch['nsfw']];
        }
        foreach ($nodes as [$node, $parent_node, $nsfw]) {
            $names = [$node['title'], ...$node['aliases']];
            $existing = topic_tree_find($topics, $names);
            $id = $existing['id'] ?? topic_id_for($node['title']);
            $resolved[$node['key']] = $id;
            $parent_id = $parent_node ? $resolved[$parent_node['key']] : null;
            $pos = $existing ? array_search($existing['id'], array_column($topics, 'id'), true) : false;
            $before = $existing ? json_encode($existing) : null;
            $e = $existing ?? ['id' => $id, 'title' => $node['title'], 'created_at' => time(), 'profile' => [], 'classified' => true, 'smart' => true];
            $e['key'] = $node['key'];
            $e['seed'] = true;
            $e['nsfw'] = $nsfw;
            $e['aliases'] = array_values(array_merge($e['aliases'] ?? [], $node['aliases']));
            if (!$existing || !$e['smart'] || trim((string)$e['about']) === '') $e['about'] = $node['about'];
            if (!empty($e['manual'])) {
                $counts['skipped']++;   // место поправил пользователь
            } else {
                $children = array_filter($topics, fn($c) => $c['parent'] === $id);
                $want_scope = $parent_node ? 'narrow' : 'broad';
                if ($want_scope === 'narrow' && $children) {
                    $counts['skipped']++;   // в тему входят другие темы — остаётся общей
                    cli_echo("«{$e['title']}» оставлена общей: в неё входят другие темы.");
                } else {
                    [$e['scope'], $e['parent']] = [$want_scope, $parent_id];
                }
            }
            $e = topic_normalize_entry($e);
            if (!$existing) {
                $topics[] = $e;
                $counts['created']++;
            } else {
                $topics[$pos] = $e;
                $moved = $e['scope'] !== $existing['scope'] || $e['parent'] !== $existing['parent'];
                if (json_encode($e) !== $before) $counts[$moved ? 'moved' : 'updated']++;
            }
        }
        // Пользовательские темы вне дерева: без ручного места и без дочерних сбрасываем классификацию — модель подберёт родителя среди общих
        $tree_ids = array_values($resolved);
        foreach ($topics as &$t) {
            if (!in_array($t['id'], $tree_ids, true) && !$t['manual'] && !array_filter($topics, fn($c) => $c['parent'] === $t['id'])) $t['classified'] = false;
        }
        unset($t);
        $db['topics'] = $topics;
        return $db;
    });
    topic_registry(true);
    return $counts;
}

// ---- Название и место новой темы ----

/** Модель (умная, при таймауте — быстрая) определяет: совпадает ли с существующей, название, широкая/узкая, общая тема. */
function topic_ai_classify($text, $allow_match = true, ?string $skip_id = null) {
    $lines = [];
    foreach (topic_registry() as $e) {
        if ($e['id'] === $skip_id) continue;
        $parent = topic_parent($e)['title'] ?? '';
        $lines[] = "- {$e['id']}: «{$e['title']}» — " . ($e['scope'] === 'broad' ? 'широкая' : 'узкая' . ($parent !== '' ? ", внутри «{$parent}»" : ''))
            . ($e['aliases'] ? '; ещё называли: ' . implode(', ', array_slice($e['aliases'], 0, 5)) : '');
    }
    $prompt = "Пользователь задаёт тему для подбора контента из соцсетей (картинки, видео, мемы). Приведи тему к аккуратному названию и найди ей место среди уже существующих тем.\n\n"
        . "Существующие темы:\n" . ($lines ? implode("\n", $lines) : '(пока нет)') . "\n\nТема пользователя: «{$text}»\n\nПравила:\n"
        . ($allow_match
            ? "1. match — id существующей темы, если тема пользователя означает ровно её же (синоним, другая формулировка, опечатка, другой язык). Если тема пользователя — частный случай существующей (например, «мемы про котов» при теме «Мемы и юмор»), это НЕ совпадение: match = \"\". Если похожей нет — match = \"\".\n"
            : "1. match всегда \"\": пользователь подтвердил, что это новая тема.\n")
        . "2. title — короткое название новой темы: 1–4 слова по-русски, с заглавной буквы, без кавычек и эмодзи. Существительные — во множественном числе, где это естественно («Кошки», «Ретро-автомобили», «Мемы про котов»). Смысл не меняй и не добавляй лишнего.\n"
        . "3. scope: \"broad\" — только большая общая область, в которой много разных предметов (как «Животные», «Мемы и юмор», «Автомобили», «Наука и космос», «Еда», «Спорт», «Игры»)."
        . " Всё более конкретное — \"narrow\": предмет, жанр, вид, персонаж, хобби внутри области (как «Кошки», «Мемы про котов», «Ретро-автомобили», «Футбол», «Выпечка»). Сомневаешься — narrow.\n"
        . "4. У узкой темы обязательно есть общая тема — область, частным случаем которой она является. parent — id подходящей широкой темы из списка;"
        . " если подходящей широкой темы в списке нет — parent = \"\", а parent_title — название такой общей темы (1–3 слова, как в примерах выше). У широкой темы parent = \"\" и parent_title = \"\".\n"
        . "5. about — одно короткое предложение: какие картинки и видео входят в тему (конкретно, чтобы по нему можно было отличить подходящую запись).\n"
        . "Примеры: «котики» → title «Кошки», narrow, общая «Животные»; «мемы про работу» → title «Мемы про работу», narrow, общая «Мемы и юмор»;"
        . " «тачки 90-х» → title «Автомобили 90-х», narrow, общая «Автомобили»; «космос» → title «Космос», broad.\nОтветь только JSON.";
    $schema = ['type' => 'object', 'properties' => array_fill_keys(['match', 'title', 'parent', 'parent_title', 'about'], ['type' => 'string'])
        + ['scope' => ['type' => 'string', 'enum' => TOPIC_SCOPES]], 'required' => ['match', 'title', 'scope', 'parent', 'parent_title', 'about']];
    $answer = null;
    foreach (array_unique([SMART_MODEL, prescreen_model()]) as $model) {
        $answer = ollama_quick_json($model, $prompt, $schema, 300, $model === SMART_MODEL ? TOPIC_SMART_TIMEOUT : 90);
        if (is_array($answer) && trim((string)($answer['title'] ?? '')) !== '') break;
        $answer = null;
    }
    if ($answer === null) return null;
    $title = topic_title_case(mb_substr((string)$answer['title'], 0, TOPIC_TITLE_MAX));
    $registry = topic_registry();
    $parent = (string)($answer['parent'] ?? '');
    $parent = ($registry[$parent]['scope'] ?? '') === 'broad' ? $parent : '';
    $parent_title = topic_title_case(mb_substr((string)($answer['parent_title'] ?? ''), 0, TOPIC_TITLE_MAX));
    // id одной темы, а название другой: верим названию
    if ($parent !== '' && $parent_title !== '' && !in_array(topic_norm($parent_title), topic_names($registry[$parent]), true)) $parent = topic_lookup($parent_title)['id'] ?? '';
    $scope = in_array($answer['scope'] ?? '', TOPIC_SCOPES, true) ? $answer['scope'] : 'narrow';
    if ($scope === 'narrow' && $parent === '' && $parent_title === '') $scope = 'broad';   // общую тему не назвала — сама общая
    return ['match' => $allow_match && isset($registry[(string)($answer['match'] ?? '')]) ? (string)$answer['match'] : '',
        'title' => $title !== '' && count(preg_split('/\s+/u', $title)) <= 5 ? $title : '', 'scope' => $scope, 'parent' => $parent,
        'parent_title' => $parent_title, 'about' => trim((string)($answer['about'] ?? '')), 'smart' => $model === SMART_MODEL];
}

/** Косинусная близость двух текстов по эмбеддингам; 0 — модель недоступна. */
function topic_similarity($a, $b) {
    // На процессоре: иначе bge-m3 занимает видеопамять и вытесняет модель оценки картинок, а для двух коротких строк CPU хватает
    $payload = ['model' => TOPIC_EMBED_MODEL, 'input' => [mb_strtolower($a), mb_strtolower($b)], 'options' => ['num_gpu' => 0], 'keep_alive' => '5m'];
    [$x, $y] = array_pad((array)(http_post_json(OLLAMA_URL . '/api/embed', $payload, 60)['data']['embeddings'] ?? []), 2, null);
    if (!is_array($x) || !is_array($y) || count($x) !== count($y)) return 0.0;
    $dot = $nx = $ny = 0.0;
    foreach ($x as $i => $v) { $dot += $v * $y[$i]; $nx += $v * $v; $ny += $y[$i] * $y[$i]; }
    return $nx > 0 && $ny > 0 ? $dot / sqrt($nx * $ny) : 0.0;
}

/** Широкая тема по названию (существующая или новая); узкую родителем не делаем. */
function topic_ensure_broad($title) {
    if ($found = topic_lookup($title)) return $found['scope'] === 'broad' ? $found : null;
    return topic_save(['title' => $title, 'scope' => 'broad', 'classified' => true]);
}

/** Родитель по ответу модели: id существующей широкой или новая широкая с parent_title. */
function topic_parent_from_answer(array $answer, $title) {
    if ($answer['scope'] !== 'narrow') return null;
    if ($answer['parent'] !== '') return $answer['parent'];
    return $answer['parent_title'] !== '' && topic_norm($answer['parent_title']) !== topic_norm($title) ? (topic_ensure_broad($answer['parent_title'])['id'] ?? null) : null;
}

function topic_create_from_answer($text, ?array $answer) {
    $title = ($answer['title'] ?? '') ?: topic_title_case(normalize_topic($text));
    if ($existing = topic_lookup($title)) return topic_add_alias($existing, $text);
    return topic_save(['title' => $title, 'scope' => $answer['scope'] ?? 'narrow', 'parent' => $answer ? topic_parent_from_answer($answer, $title) : null,
        'about' => $answer['about'] ?? '', 'aliases' => topic_norm($text) !== topic_norm($title) ? [$text] : [], 'classified' => $answer !== null, 'smart' => !empty($answer['smart'])]);
}

/**
 * Ввод → тема реестра. Маленькая модель почти на всё отвечает «совпадает с Мемы и юмор», поэтому её match принимается,
 * только если её название — имя той темы или смысл почти тот же по эмбеддингам. Возвращает topic, matched, created, ai.
 */
function topic_resolve($text, $force_new = false) {
    $text = normalize_topic($text);
    if (!$force_new && ($found = topic_lookup($text))) return ['topic' => $found, 'matched' => topic_norm($found['title']) !== topic_norm($text), 'created' => false, 'ai' => false];
    $answer = topic_ai_classify($text, !$force_new);
    if ($answer && $answer['match'] !== '') {
        $matched = topic_registry()[$answer['match']];
        $same = $answer['title'] !== '' && in_array(topic_norm($answer['title']), topic_names($matched), true);
        if ($same || topic_similarity($text, $matched['title']) >= TOPIC_SAME_SIMILARITY) return ['topic' => topic_add_alias($matched, $text), 'matched' => true, 'created' => false, 'ai' => true];
    }
    if ($force_new && ($existing = topic_lookup($text))) {   // «это другая тема»: ввод был синонимом — убираем его оттуда
        $existing['aliases'] = array_values(array_filter($existing['aliases'], fn($a) => topic_norm($a) !== topic_norm($text)));
        topic_save($existing);
        if (topic_norm($existing['title']) === topic_norm($text)) return ['topic' => $existing, 'matched' => false, 'created' => false, 'ai' => false];
    }
    if ($answer) $answer['match'] = '';
    return ['topic' => topic_create_from_answer($text, $answer), 'matched' => false, 'created' => true, 'ai' => $answer !== null];
}

/** Темы без описания от умной модели получают его (и место, если пользователь не правил). Название не меняется. */
function topic_classify_pending($limit = 1, $first = null) {
    $done = 0;
    $entries = topic_registry();
    if ($first !== null && ($preferred = topic_lookup($first))) $entries = [$preferred['id'] => $preferred] + $entries;
    foreach ($entries as $e) {
        if (($e['classified'] && $e['smart']) || $done >= $limit) continue;
        $done++;
        $answer = topic_ai_classify($e['title'], false, $e['id']);
        if ($answer === null || ($e['classified'] && !$answer['smart'])) continue;
        $e['about'] = $answer['about'] ?: $e['about'];
        if (!$e['manual']) {
            $e['scope'] = $answer['scope'];
            $e['parent'] = topic_parent_from_answer($answer, $e['title']);
            if (topic_children($e)) [$e['scope'], $e['parent']] = ['broad', null];
        }
        [$e['classified'], $e['smart']] = [true, $answer['smart']];
        topic_save($e);
        cli_echo("Тема «{$e['title']}»: " . ($e['scope'] === 'broad' ? 'общая' : 'частная' . ($e['parent'] ? ', внутри «' . (topic_registry()[$e['parent']]['title'] ?? '') . '»' : '')) . ". Описание: {$e['about']}");
    }
    return $done;
}

/** Пользователь поправил место темы: $parent — id, название новой общей темы или '' (тема сама общая). */
function topic_set_parent($id, $parent) {
    $e = topic_registry()[$id] ?? throw new InvalidArgumentException('Такой темы нет.');
    if (($parent = trim((string)$parent)) === '') {
        [$e['scope'], $e['parent']] = ['broad', null];
    } else {
        $target = topic_registry()[$parent] ?? topic_lookup($parent);
        if ($target && $target['id'] === $e['id']) throw new InvalidArgumentException('Тема не может входить сама в себя.');
        if ($target && $target['scope'] !== 'broad') throw new InvalidArgumentException("«{$target['title']}» — частная тема, общей она быть не может.");
        if (topic_children($e)) throw new InvalidArgumentException("В тему «{$e['title']}» входят другие темы — она должна остаться общей.");
        $target ??= topic_save(['title' => $parent, 'scope' => 'broad', 'classified' => true]);
        [$e['scope'], $e['parent']] = ['narrow', $target['id']];
    }
    [$e['classified'], $e['manual']] = [true, true];
    return topic_save($e);
}

// ---- Профиль темы ----

function topic_profile_clean($p) {
    if (!is_array($p) || empty($p['criteria']) || empty($p['question'])) return [];
    $text = fn($v, $n) => mb_substr(trim((string)$v), 0, $n);
    return ['criteria' => $text($p['criteria'], 300), 'exclude' => $text($p['exclude'] ?? '', 300), 'question' => $text($p['question'], 200),
        'queries' => array_slice(array_map(fn($q) => $text($q, 60), (array)($p['queries'] ?? [])), 0, 12),
        'telegram' => array_slice(array_values(array_filter(array_map('strval', (array)($p['telegram'] ?? [])), 'is_valid_telegram_channel')), 0, 20),
        'v' => (int)($p['v'] ?? 0), 'smart' => !empty($p['smart']), 'at' => (int)($p['at'] ?? 0)];
}

function topic_profile_build(array $e) {
    $prompt = "Тема подбора картинок и видео из соцсетей: " . topic_prompt_context($e['title']) . ".\nСоставь профиль для отбора записей по этой теме:\n"
        . "criteria — что должно быть видно на картинке или в видео, чтобы запись подходила под тему (1–2 предложения, конкретные признаки, не общие слова).\n"
        . "exclude — что похоже на тему, но не подходит (1 предложение).\n"
        . "question — один вопрос по-русски, на который отвечают «да» или «нет» по одной картинке; начинается со слов «Это …?» и проверяет критерии (например: «Это картинка с мотоциклом или мотоциклистом?»).\n"
        . "queries — 8 поисковых запросов по-русски, которыми люди ищут такие каналы и паблики: 1–3 слова, от прямого названия к синонимам, сленгу и подтемам. Все строго про тему, без слов «канал», «телеграм», «паблик», «группа», без опечаток.\n"
        . "queries_en — 3 запроса по-английски.\n"
        . "telegram — адреса (username, латиница, цифры и _) публичных Telegram-каналов по теме, которые ты точно знаешь. Не уверен — пустой список.\nОтветь только JSON.";
    $list = ['type' => 'array', 'items' => ['type' => 'string']];
    $schema = ['type' => 'object', 'properties' => ['criteria' => ['type' => 'string'], 'exclude' => ['type' => 'string'], 'question' => ['type' => 'string'],
        'queries' => $list, 'queries_en' => $list, 'telegram' => $list], 'required' => ['criteria', 'exclude', 'question', 'queries', 'queries_en', 'telegram']];
    foreach ([[SMART_MODEL, true], [prescreen_model(), false]] as [$model, $smart]) {
        try { $a = decode_model_json(ai_generate_local($model, $prompt, [], $schema, ['temperature' => 0.3, 'num_predict' => 700], false)); } catch (Throwable $err) { $a = null; }
        if (!is_array($a) || trim((string)($a['criteria'] ?? '')) === '') continue;
        $question = trim((string)($a['question'] ?? ''));
        $queries = [];
        foreach (array_merge((array)($a['queries'] ?? []), (array)($a['queries_en'] ?? [])) as $q) if (($q = discovery_clean_query($q)) !== '') $queries[topic_norm($q)] = $q;
        return topic_profile_clean(['criteria' => $a['criteria'], 'exclude' => $a['exclude'] ?? '', 'queries' => array_values($queries),
            'question' => str_contains($question, '?') ? $question : "Подходит ли эта картинка под тему «{$e['title']}»?",
            'telegram' => array_unique(array_map(fn($h) => preg_replace('~^(https?://)?t\.me/(s/)?~i', '', ltrim(trim((string)$h), '@')), (array)($a['telegram'] ?? []))),
            'v' => TOPIC_PROFILE_VERSION, 'smart' => $smart, 'at' => time()]);
    }
    return [];
}

/** Профиль темы; нет, устарел или от быстрой модели — пишется заново. Смена вопроса сбрасывает прежние быстрые оценки темы. */
function topic_profile_ensure($topic) {
    if (($e = topic_lookup($topic)) === null) return [];
    $p = $e['profile'];
    if ($p && $p['v'] === TOPIC_PROFILE_VERSION && ($p['smart'] || time() - $p['at'] < TOPIC_PROFILE_FALLBACK_RETRY)) return $p;
    if (!($built = topic_profile_build($e))) return $p ?: [];
    if ($p && $p['question'] !== $built['question']) {
        $cache = load_data(SCREENED_CACHE_FILE, []);
        $kept = array_filter($cache, fn($x) => !is_array($x) || !topic_same((string)($x['topic'] ?? ''), $e['title']));
        if (count($kept) !== count($cache)) save_data(SCREENED_CACHE_FILE, $kept);
    }
    topic_save(['profile' => $built] + $e);
    cli_echo("Профиль темы «{$e['title']}»" . ($built['smart'] ? '' : ' (быстрой моделью)') . ": {$built['criteria']}");
    return $built;
}

/** Профиль для промптов без обращения к модели (у составной темы — от первой). */
function topic_profile_for_prompts($topic) {
    $e = topic_lookup(topic_parts($topic)[0] ?? $topic);
    return $e && $e['profile'] && $e['profile']['v'] === TOPIC_PROFILE_VERSION ? $e['profile'] : [];
}

/**
 * Горячие темы сейчас: голоса за неделю (лайк 3, «не знаю» 1, не больше 15 с одного пользователя на тему)
 * и текущая тема активных за неделю пользователей (2). Тема до HOT_TOPICS_MIN_SCORE не попадает.
 * Возвращает не больше $limit тем, может вернуть меньше. Результат кэшируется на HOT_TOPICS_CACHE_SECONDS.
 */
function hot_topics($limit = HOT_TOPICS_LIMIT, $fresh = false) {
    $cache_file = ROOT . '/hot_topics.json';
    $cached = load_data($cache_file, []);
    if (!$fresh && isset($cached['at']) && time() - $cached['at'] < HOT_TOPICS_CACHE_SECONDS) return array_slice($cached['topics'], 0, $limit);

    $since = time() - HOT_TOPICS_WINDOW_DAYS * DAY_SECONDS;
    $weights = ['liked' => 3, 'undecided' => 1];
    $votes = [];   // id темы => uid => баллы
    foreach (db_all('SELECT user_id, collection, data FROM history_items WHERE collection IN (?, ?)', array_keys($weights)) as $r) {
        $d = db_unjson($r['data'], []);
        if ((int)($d['timestamp'] ?? 0) < $since || !($t = topic_lookup($d['topic'] ?? ''))) continue;
        $votes[$t['id']][$r['user_id']] = ($votes[$t['id']][$r['user_id']] ?? 0) + $weights[$r['collection']];
    }
    $scores = [];
    foreach ($votes as $id => $by_user) $scores[$id] = array_sum(array_map(fn($v) => min($v, 15), $by_user));

    $active = [];
    foreach (db_all('SELECT id FROM users WHERE last_seen >= ?', [$since]) as $u) $active[$u['id']] = true;
    foreach (user_ids() as $uid) {
        if (!isset($active[$uid]) || !($t = topic_lookup(load_settings($uid)['topic'] ?? ''))) continue;
        $scores[$t['id']] = ($scores[$t['id']] ?? 0) + 2;
    }

    $hot = [];
    foreach ($scores as $id => $score) if ($score >= HOT_TOPICS_MIN_SCORE && ($t = topic_registry()[$id] ?? null)) $hot[] = ['id' => $id, 'title' => $t['title'], 'score' => $score];
    usort($hot, fn($a, $b) => $b['score'] <=> $a['score']);
    $hot = array_slice($hot, 0, HOT_TOPICS_LIMIT);
    save_data($cache_file, ['at' => time(), 'topics' => $hot]);
    return array_slice($hot, 0, $limit);
}

function topic_is_hot($text) {
    $t = topic_lookup($text);
    return $t !== null && in_array($t['id'], array_column(hot_topics(), 'id'), true);
}

function topic_profile_hint($topic) {
    if (count($parts = topic_parts($topic)) > 1) return implode('', array_map('topic_profile_hint', $parts));
    $p = topic_profile_for_prompts($topic);
    return $p ? " Подходит: {$p['criteria']}" . ($p['exclude'] !== '' ? " Не подходит: {$p['exclude']}" : '') . '.' : '';
}
