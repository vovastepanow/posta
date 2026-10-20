<?php
// Утилиты: php tools/cli.php <команда> [аргументы]
//   nightly-discovery [N]          — подбор источников для N популярных тем (cron: 0 4 * * *)
//   check-threads [--dry-run]      — убрать из пулов записи удалённых тредов Двача
//   backfill-proposals [id] [--force] — достроить медиа записей «Предложки» (запускается сайтом сам)
//   backfill-engagement            — дособрать активность (просмотры, ответы) у записей пулов
//   engagement-report [файлы]      — угадывает ли активность голоса пользователей (AUC)
//   rescore-sources [--catalog] [--apply] — переоценить найденные каналы по текущим правилам
//   seed-topics                    — засеять готовое дерево тем (lib/topic_tree.php), повторный запуск ничего не меняет
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/content.php';
set_time_limit(0);
$command = $argv[1] ?? '';
$args = array_values(array_filter(array_slice($argv, 2), fn($a) => !str_starts_with($a, '--')));
$flag = fn($name) => in_array("--$name", $argv, true);
$short = fn($uid) => substr($uid, 0, 8);

switch ($command) {
case 'nightly-discovery':
    $topics = popular_topics(max(1, (int)($args[0] ?? 5)));
    echo date('c') . ' Ночной подбор источников. Темы: ' . ($topics ? implode(', ', $topics) : 'нет') . "\n";
    topic_classify_pending(10);
    foreach ($topics as $topic) {
        topic_ensure($topic);
        topic_profile_ensure($topic);
        source_discovery_due(load_source_db(), $topic) ? run_source_discovery($topic) : print("Тема «{$topic}»: подбор недавно уже был — пропускаю.\n");
    }
    echo date('c') . " Готово.\n";
    break;

case 'check-threads':
    foreach (user_ids() as $uid) {
        $r = prune_dead_pool_items($uid, $flag('dry-run'), true, false);
        printf("%s: проверено тредов %d, удалённых %d, записей %s: %d\n", $short($uid), $r['threads'], $r['gone'], $flag('dry-run') ? 'к удалению' : 'убрано', $r['removed']);
    }
    break;

case 'backfill-proposals':
    foreach ($args ? [$args[0]] : user_ids() as $uid) {
        app_user_bind($uid);
        for ($total = 0; ($n = proposal_backfill_media(10)) > 0; $total += $n);
        proposal_progress_write(['running' => false] + proposal_progress_read());
        if ($total) echo $short($uid) . ": обновлено записей: $total\n";
        if ($dead = proposal_prune_dead_threads($flag('force'))) echo $short($uid) . ": скрыто записей из мёртвых тредов: $dead\n";
    }
    break;

case 'backfill-engagement':
    $cache = [];
    foreach (user_ids() as $uid) {
        $snapshots = [];
        foreach (load_data(pool_file($uid), []) as $item) {
            if (empty($item['engagement']) && ($s = engagement_backfill_item($item, $cache))) $snapshots[content_image_key($item)] = $s + ['backfilled' => true];
        }
        $applied = 0;
        update_data(pool_file($uid), [], function ($pool) use ($snapshots, &$applied) {
            foreach ($pool as &$i) if (empty($i['engagement']) && isset($snapshots[$k = content_image_key($i)])) { $i['engagement'] = $snapshots[$k]; $applied++; }
            return $applied ? $pool : null;
        });
        printf("%s: дособрано %d\n", $short($uid), $applied);
    }
    break;

case 'engagement-report':
    engagement_report($args ?: array_map('history_file', user_ids()));
    break;

case 'rescore-sources':
    rescore_sources($flag('catalog'), $flag('apply'));
    break;

case 'migrate-media':
    // Без --apply только отчёт. Перед --apply: копия базы (sqlite3 data/postmaker.sqlite ".backup …")
    $r = media_migrate(ROOT . '/media_cache', $flag('apply'));
    echo ($r['applied'] ? 'Перенесено' : 'Будет перенесено') . ": файлов {$r['files']}" . ($r['applied'] ? ", перемещено {$r['moved']}" : '') . ", строк в базе {$r['rows']}\n";
    echo "Откуда: {$r['from']}\nКуда: {$r['to']}\n";
    if (!$r['applied']) echo "Для переноса запустите с --apply.\n";
    break;

case 'hot-topics':
    $hot = hot_topics(HOT_TOPICS_LIMIT, true);
    echo $hot ? "Горячие темы (" . count($hot) . " из " . HOT_TOPICS_LIMIT . "):\n" : "Горячих тем сейчас нет.\n";
    foreach ($hot as $t) echo "  {$t['title']} — {$t['score']}\n";
    break;

case 'seed-topics':
    $c = topic_tree_seed();
    echo "Дерево тем: создано {$c['created']}, обновлено {$c['updated']}, перенесено {$c['moved']}, пропущено {$c['skipped']}\n";
    foreach (TOPIC_TREE as $branch) {
        echo $branch['title'] . ($branch['nsfw'] ? ' [18+]' : '') . "\n";
        foreach ($branch['children'] as $child) echo "  " . $child['title'] . ($branch['nsfw'] ? ' [18+]' : '') . "\n";
    }
    break;

case 'migrate-json':
    echo "Миграция JSON → SQLite\n";
    if (is_file(SOURCES_FILE)) {
        $sources = load_data(SOURCES_FILE, []);
        update_source_db(fn() => $sources);
        echo "  ✓ sources_db.json\n";
    }
    foreach (user_ids() as $uid) {
        app_user_bind($uid);
        if (is_file(settings_file())) db_save_settings(load_data(settings_file(), []), $uid);
        if (is_file(pool_file())) pool_update(fn() => load_data(pool_file(), []), $uid);
        if (is_file(history_file())) history_update(fn() => load_data(history_file(), []), $uid);
        if (is_file(connections_file())) connections_update(fn() => load_data(connections_file(), []), $uid);
        echo "  ✓ users/" . substr($uid, 0, 8) . "\n";
    }
    echo "Готово.\n";
    break;

default:
    echo preg_replace('/^\/\/ ?/m', '', implode("\n", array_slice(file(__FILE__, FILE_IGNORE_NEW_LINES), 1, 7))) . "\n";
}

/** Снимок активности для записи, найденной до включения сбора (страница канала или тред заново). */
function engagement_backfill_item(array $item, array &$cache) {
    if (preg_match('~^https://t\.me/([A-Za-z0-9_]+)/(\d+)$~', source_post_url($item), $m)) {
        $key = "tg:{$m[1]}:" . intdiv($id = (int)$m[2], 8);
        $cache[$key] ??= ($html = http_get("https://t.me/s/{$m[1]}?before=" . ($id + 11), [CURLOPT_USERAGENT => BROWSER_UA, CURLOPT_TIMEOUT => 25])) ? telegram_page_engagement($html) : [];
        return isset($cache[$key][$id]) ? telegram_engagement_snapshot($cache[$key], (string)$id, (int)($item['posted_at'] ?? 0)) : null;
    }
    if (empty($item['board']) || empty($item['thread_id']) || empty($item['post_id'])) return null;
    $key = "dv:{$item['board']}/{$item['thread_id']}";
    $cache["cat:{$item['board']}"] ??= fetch_2ch_json($item['board'], null, [CURLOPT_TIMEOUT => 25])['threads'] ?? [];
    $cache[$key] ??= ($posts = dvach_thread_posts(fetch_2ch_json($item['board'], (string)$item['thread_id'], [CURLOPT_TIMEOUT => 20]))) ? dvach_thread_engagement($posts, $cache["cat:{$item['board']}"]) : [];
    return $cache[$key][(string)$item['post_id']] ?? null;
}

function report_auc(array $scores, array $labels) {
    [$sum, $pairs] = [0, 0];
    foreach ($scores as $i => $p) foreach ($scores as $j => $n) {
        if ($p === null || $n === null || !$labels[$i] || $labels[$j]) continue;
        $sum += $p > $n ? 1 : ($p == $n ? 0.5 : 0);
        $pairs++;
    }
    return $pairs ? $sum / $pairs : null;
}

/** Числовые признаки по снимку активности. */
function report_features(array $e) {
    $react = fn(array $set) => array_sum(array_filter($e['reactions'] ?? [], fn($k) => in_array(mb_substr($k, 0, 1), $set, true), ARRAY_FILTER_USE_KEY));
    if (($e['source'] ?? '') === 'telegram') {
        [$views, $total] = [(int)($e['views'] ?? 0), array_sum($e['reactions'] ?? [])];
        [$mv, $mr] = [$e['neighbors_median_views'] ?? null, $e['neighbors_median_reaction_rate'] ?? null];
        return ['views_rel' => $mv === null ? null : log(($views + 1) / ($mv + 1)), 'react_rate_rel' => $mr === null ? null : log(($total / max(1, $views) + 0.002) / ($mr + 0.002)),
            'react_total' => log($total + 1), 'laugh_share' => $total ? $react(['😁', '🤣', '😂', '😆']) / $total : 0,
            'love_share' => $total ? $react(['❤', '🔥', '💯', '🥰', '😍', '🤩', '👍', '👏', '🎉', '⚡', '🏆']) / $total : 0,
            'has_negative' => $react(['👎', '💩', '🤮', '🤡', '🥱', '😴', '😢', '😭', '😡']) > 0 ? 1 : 0, 'age_hours' => $e['age_hours'] ?? null];
    }
    if (($e['source'] ?? '') !== 'dvach') return [];
    [$posts, $hours] = [(int)($e['thread_posts'] ?? 0), (float)($e['thread_hours'] ?? 0)];
    return ['thread_size_rel' => empty($e['board_median_thread_posts']) ? null : log(($posts + 1) / ($e['board_median_thread_posts'] + 1)),
        'thread_velocity_rel' => $hours > 0 && isset($e['board_median_velocity']) ? log(($posts / $hours + 0.1) / ($e['board_median_velocity'] + 0.1)) : null,
        'thread_files_share' => $posts ? ($e['thread_files'] ?? 0) / $posts : null]
        + (empty($e['is_op']) ? ['replies' => log(($e['replies'] ?? 0) + 1), 'has_reply' => ($e['replies'] ?? 0) > 0 ? 1 : 0,
            'reply_share' => ($e['posts_after'] ?? 0) > 0 ? ($e['replies'] ?? 0) / $e['posts_after'] : 0, 'position' => $e['index'] ?? null] : []);   // у ОП-поста ответов почти как постов в треде
}

function engagement_report(array $files) {
    [$records, $total] = [[], 0];
    foreach ($files as $file) foreach (HISTORY_COLLECTIONS as $c) foreach (load_data($file, [])[$c] ?? [] as $r) {
        $total++;
        if (!is_array($r['engagement'] ?? null) || !empty($r['engagement']['backfilled']) || ($c === 'complaints' && ($r['complaint_reason'] ?? '') !== 'match_error')) continue;
        $records[] = ['label' => $c, 'source' => $r['engagement']['source'] ?? '', 'relevance' => $r['relevance'] ?? null, 'features' => report_features($r['engagement'])];
    }
    printf("Оценок всего: %d, со снимком активности: %d\n", $total, count($records));
    if (!$records) exit("Снимки появляются у записей, найденных парсером после включения сбора. Подождите, пока накопятся оценки.\n");
    $tasks = ['Годно: лайк против дизлайка' => [fn($r) => $r['label'] === 'liked', fn($r) => in_array($r['label'], ['liked', 'disliked'], true), false],
        'Годно: лайк против дизлайков и жалоб' => [fn($r) => $r['label'] === 'liked', fn($r) => $r['label'] !== 'undecided', false],
        'Не по теме: жалоба «ошибка подбора» против остального' => [fn($r) => $r['label'] === 'complaints', fn($r) => true, true]];
    foreach (['telegram' => 'Telegram', 'dvach' => 'Двач'] as $source => $name) {
        $of = array_values(array_filter($records, fn($r) => $r['source'] === $source));
        printf("\n################ %s: %d записей с оценкой ################\n", $name, count($of));
        foreach ($tasks as $title => [$positive, $include, $invert]) {
            $rows = array_values(array_filter($of, $include));
            $labels = array_map(fn($r) => $positive($r) ? 1 : 0, $rows);
            [$yes, $no] = [array_sum($labels), count($labels) - array_sum($labels)];
            if ($yes < 15 || $no < 15) { printf("\n== %s: мало данных (положительных %d, остальных %d; нужно по 15)\n", $title, $yes, $no); continue; }
            printf("\n== %s (положительных %d, остальных %d)\n", $title, $yes, $no);
            printf("  %-22s AUC %.2f   ← оценка модели «по теме», для сравнения\n", 'relevance', report_auc(array_map(fn($r) => $r['relevance'] === null ? null : ($invert ? 100 - $r['relevance'] : $r['relevance']), $rows), $labels));
            foreach (array_unique(array_merge(...array_map(fn($r) => array_keys($r['features']), $rows))) as $f) {
                if (($a = report_auc(array_map(fn($r) => $r['features'][$f] ?? null, $rows), $labels)) === null) continue;
                printf("  %-22s AUC %.2f%s\n", $f, $a, abs($a - 0.5) >= 0.1 ? ($a > 0.5 ? '  ↑ выше — чаще положительное' : '  ↓ выше — реже положительное') : '');
            }
        }
    }
    echo "\nAUC: 0.5 — угадывание. Пока в классе меньше ~100 примеров, различия до ±0.1 — шум. Признак полезен, если стабильно\nдержится дальше 0.6 (или 0.4) и не слабее relevance.\n";
}

/** Переоценка найденных Telegram-каналов (предложения, с --catalog — и автодобавленные). Без --apply — только таблица. */
function rescore_sources($with_catalog, $apply) {
    $db = load_source_db();
    $targets = [];
    foreach ($db['source_suggestions'] as $s) if (($s['platform'] ?? '') === PLATFORM_TELEGRAM) $targets[] = ['kind' => 'предложение', 'id' => $s['id'], 'topic' => $s['topic'] ?? DEFAULT_TOPIC];
    foreach ($with_catalog ? $db['telegram_channels'] : [] as $e) if (!empty($e['auto'])) $targets[] = ['kind' => 'каталог', 'id' => ltrim($e['id'], '@'), 'topic' => $e['topics'][0] ?? DEFAULT_TOPIC];
    printf("Проверяю каналов: %d (%s)%s\n\n", count($targets), $with_catalog ? 'предложения и автодобавленные' : 'предложения', $apply ? ', результат будет применён' : ', без изменений');
    $results = [];
    foreach ($targets as $t) {
        $label = sprintf('%-9s %-24s ', $t['kind'], mb_substr($t['id'], 0, 24));
        $why = null;
        if (!($info = telegram_channel_info($t['id'], 14))) $why = 'канала нет или он закрыт';
        else {
            $rate = source_view_rate($info['subscribers'], $info['post_views']);
            if (($dead = source_inactivity_reason(source_activity($info['post_times'])) ?? source_dead_audience_reason($info['subscribers'], $rate)) !== null) $why = "мёртвый: $dead";
            elseif (!($ev = ($posts = array_values(array_filter($info['posts'], fn($p) => !is_post_outdated($p['posted_at'] ?? 0)))) ? evaluate_source($info, $posts, $t['topic']) : null)) $why = 'нет постов с картинками';
        }
        if ($why) { $results[] = $t + ['keep' => false]; echo $label . $why . "\n"; continue; }
        $q = source_quality_score($ev['score'], $info['subscribers'], $rate);
        $results[] = $t + ['keep' => $keep = $q['score'] >= SOURCE_SUGGEST_SCORE, 'score' => $q['score'], 'audience' => ['subscribers' => $info['subscribers'], 'view_rate' => $rate, 'checked_at' => time()]];
        printf("%sпо теме %3d%% (картинки %s) %s → %3d%% — %s\n", $label, $q['content'], implode('/', $ev['images']), describe_source_audience($info['subscribers'], $rate), $q['score'], $keep ? 'оставить' : 'убрать');
    }
    $removed = count(array_filter($results, fn($r) => !$r['keep']));
    printf("\nИтого: оставить %d, убрать %d.\n", count($results) - $removed, $removed);
    if (!$apply) exit('Чтобы применить: php tools/cli.php rescore-sources' . ($with_catalog ? ' --catalog' : '') . " --apply\n");
    $by = fn($kind) => array_column(array_map(fn($r) => ['id' => strtolower($r['id'])] + $r, array_filter($results, fn($r) => $r['kind'] === $kind)), null, 'id');
    [$sugg, $cat] = [$by('предложение'), $by('каталог')];
    update_source_db(function ($db) use ($sugg, $cat) {
        $db['source_suggestions'] = array_values(array_filter(array_map(fn($s) => ($r = $sugg[strtolower($s['id'] ?? '')] ?? null) ? ($r['keep'] ? ['score' => $r['score']] + $s : null) : $s, $db['source_suggestions'])));
        $db['telegram_channels'] = array_values(array_filter(array_map(fn($e) => !empty($e['auto']) && ($r = $cat[strtolower(ltrim($e['id'], '@'))] ?? null)
            ? ($r['keep'] ? ['score' => $r['score'], 'audience' => $r['audience']] + $e : null) : $e, $db['telegram_channels'])));
        return $db;
    });
    echo "Применено.\n";
}
