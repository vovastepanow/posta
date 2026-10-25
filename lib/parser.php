<?php
// Сборщик (cron_parser.php): посты всех источников темы → очередь кандидатов → быстрая оценка пачками → полный анализ лучших → пул.

function post_caption_for_ai($item) { $t = trim((string)($item['text'] ?? '')); return $t === EMPTY_POST_TEXT ? '' : $t; }

/** Метка для журнала: «[источник](ссылка)» — сайт делает её ссылкой. */
function candidate_log_label($item) {
    $url = (string)($item['source_url'] ?? '');
    return "[{$item['source']}]" . (is_trusted_source_url($url) ? "($url)" : '');
}

// ---- Сбор постов ----

/** Посты Двача со свежих тредов доски (без закреплённых, бесконечных, закрытых; при «актуальных» — только живые). С темой — треды по ней отбираются (IMAGEBOARD_CONTEXT_HASHTAGS_PROMPT.md, часть A). */
function parse_dvach($board, ?string $topic = null) {
    $catalog = dvach_json_cached($board, null);
    if (!is_array($catalog['threads'] ?? null)) return [];
    $threads = array_filter($catalog['threads'], fn($t) => empty($t['sticky']) && empty($t['endless']) && empty($t['closed']));   // все треды: флаги «живые» — только при показе
    $batch = [];
    foreach (array_slice(array_values($threads), 0, DVACH_THREADS_PER_BOARD) as $t) {
        if (!is_valid_dvach_id($thread = (string)($t['num'] ?? ''))) continue;
        $batch[] = ['thread' => $thread, 'title' => dvach_thread_title($t['subject'] ?? '', $thread), 'subject' => $t, 'posts' => dvach_thread_posts(dvach_json_cached($board, $thread))];
    }
    $gates = $topic === null ? [] : imageboard_thread_scores($topic, $board, array_map(fn($b) => [
        'id' => $b['thread'], 'title' => clean_html_text($b['subject']['subject'] ?? ''), 'op' => isset($b['posts'][0]) ? dvach_post_text($b['posts'][0]) : ''], $batch));
    $out = [];
    foreach ($batch as $b) {
        $gate = imageboard_thread_gate($gates[$b['thread']] ?? null);
        $thread = $b['thread'];
        if ($gate === 'skip') { cli_echo("-> Двач /$board/: тред «{$b['title']}» не по теме (" . $gates[$thread] . ') — пропуск.'); continue; }
        $op = $b['posts'][0] ?? null;
        $op_text = $op ? dvach_post_text($op) : '';
        $engagement = dvach_thread_engagement($b['posts'], $catalog['threads']);
        foreach (array_slice($b['posts'], 0, DVACH_POSTS_PER_THREAD) as $post) {
            if (!($files = dvach_post_media($board, $post))) continue;
            $text = dvach_post_text($post);
            $id = (string)($post['num'] ?? '');
            $out[] = ['source_key' => "dvach:$board", 'posted_at' => (int)($post['timestamp'] ?? 0), 'board' => $board, 'thread_id' => $thread, 'post_id' => $id,
                'source' => PLATFORM_DVACH . " /$board/ · " . $b['title'], 'source_url' => dvach_post_url($board, $thread, $id),
                'image' => $files[0]['image'], 'images' => [$files[0]['image']], 'video' => $files[0]['video'], 'captions' => [$text], 'text' => $text,
                'engagement' => $engagement[$id] ?? null, 'thread_title' => mb_substr(clean_html_text($b['subject']['subject'] ?? '') ?: $b['title'], 0, THREAD_TITLE_MAX), 'thread_op' => $id === $thread ? '' : mb_substr($op_text, 0, THREAD_OP_MAX),
                'is_op' => $id === $thread, 'thread_trust' => $gate === 'trust'] + (count($files) > 1 ? ['gallery' => $files] : []);
            if (count($out) >= 200) return $out;
        }
    }
    return $out;
}

/** Канал или паблик каталога с учётом активности. [посты, обновлённая запись каталога|null, событие|null]. */
/** Пропускать посты замороженного источника: при «Только актуальные источники» или если источник удалён. */
function source_frozen_skip(bool $active_only, $gone): bool { return $active_only || $gone !== null; }

/** Сбор одного источника каталога. $active_only — настройка «Только актуальные источники»: замороженные не собираются. */
function collect_catalog_source($platform, array $entry, bool $active_only = true) {
    if (($id = ltrim(trim((string)($entry['id'] ?? '')), '@')) === '') return [[], null, null];
    $label = match ($platform) { PLATFORM_TELEGRAM => "@$id", PLATFORM_RSS => (string)parse_url($id, PHP_URL_HOST), PLATFORM_OK => "ok:$id", PLATFORM_TIKTOK => "tiktok:$id", default => $id };
    if (is_source_frozen($entry)) return [[], null, 'skipped_frozen'];   // заморожены неактивные: перепроверяются по своему расписанию
    $source = ['id' => $id, 'name' => source_display_name($platform, $entry['name'] ?? $id)];
    cli_echo("Парсинг {$platform} {$label}...");
    $gone = null;
    try {
        if ($platform === PLATFORM_TELEGRAM) {
            if (!($html = fetch_telegram_channel_page($id))) { cli_echo("-> {$platform} {$label}: страница не загрузилась."); return [[], null, null]; }
            $posts = telegram_posts_from_html($html, $source, TELEGRAM_POSTS_PER_CHANNEL, topics_accept_text($entry['topics'] ?? []));
            $facts = telegram_page_facts($html);
            [$times, $subs] = [$facts['post_times'], $facts['subscribers']];
            $rate = source_view_rate($subs, $facts['post_views']);
            if (!str_contains($html, 'data-post="')) $gone = 'канала нет или он закрыт';
        } elseif ($platform === PLATFORM_RSS) {
            // Блоги берут и текстовые посты; новостным сайтам картинка нужна из статьи — там текстовые посты не нужны
            $with_text = topics_accept_text($entry['topics'] ?? []) && empty($entry['news']);
            $posts = news_enrich_posts(rss_fetch_posts($id, RSS_POSTS_PER_FEED, $source['name'], true, $with_text), $entry);   // без картинки — дообогатить со страницы статьи
            [$times, $subs, $rate] = [array_column($posts, 'posted_at'), 0, null];
        } elseif ($platform === PLATFORM_OK) {
            $posts = ok_fetch_posts($id, OK_POSTS_PER_GROUP, $source['name']);
            [$times, $subs, $rate] = [array_column($posts, 'posted_at'), 0, null];
        } elseif ($platform === PLATFORM_TIKTOK) {
            $posts = tiktok_fetch_posts($id, TIKTOK_POSTS_PER_ACCOUNT, $source['name']);   // одна выборка yt-dlp на аккаунт за проход
            [$times, $subs, $rate] = [array_column($posts, 'posted_at'), 0, null];
        } else {
            $posts = build_vk_preview_posts($source, VK_POSTS_PER_PUBLIC, topics_accept_text($entry['topics'] ?? []));
            [$times, $subs, $rate] = [array_column($posts, 'posted_at'), 0, null];
        }
    } catch (Throwable $e) {
        cli_echo("-> {$platform} {$label}: " . $e->getMessage());
        return [[], null, null];
    }
    $activity = source_activity($times);
    [$updated, $event] = apply_source_activity($entry, $activity, null, $gone ?? source_dead_audience_reason($subs, $rate));
    if ($subs > 0) $updated['audience'] = ['subscribers' => $subs, 'view_rate' => $rate, 'checked_at' => time()];
    if ($platform === PLATFORM_TELEGRAM && preg_match('~<meta property="og:image" content="([^"]+)"~', $html, $m)) $updated['avatar'] = html_entity_decode($m[1]);
    if ($preview = source_preview_images($posts)) $updated['preview'] = $preview;
    if ($event === 'frozen' || $event === 'still_frozen') {
        $skip = source_frozen_skip($active_only, $gone);
        cli_echo("-> {$platform} {$label}: " . ($event === 'frozen' ? 'заморожен' : 'всё ещё не активен') . " ({$updated['frozen_reason']}), перепроверка через " . SOURCE_FROZEN_RECHECK_DAYS . ' дн'
            . ($skip ? '.' : ' — «Только актуальные источники» выключено, посты всё равно берутся.'));
        if ($skip) return [[], $updated, $event];
    }
    if ($event === 'revived') cli_echo("-> {$platform} {$label}: ожил и разморожен (" . describe_source_activity($activity) . ').');
    $fresh = array_filter($posts, fn($p) => !news_post_outdated($p, $entry));
    // Большое видео без файла показать нечем — кроме Telegram при подключённом личном аккаунте: файл докачает он (video_via = tgu)
    $fresh = $platform === PLATFORM_TELEGRAM ? telegram_big_videos_via_account(array_values($fresh)) : array_values($fresh);
    $playable = array_values(array_filter($fresh, fn($p) => empty($p['video_unavailable'])));
    [$old, $hidden] = [count($posts) - count($fresh), count($fresh) - count($playable)];
    cli_echo("-> {$platform} {$label}: найдено медиа-постов: " . count($playable) . ($old ? ", устаревших пропущено: $old" : '') . ($hidden ? ", больших видео без файла пропущено: $hidden" : '')
        . ' (' . describe_source_activity($activity) . ($subs > 0 ? '; ' . describe_source_audience($subs, $rate) : '') . ').');
    $key = source_id_for($platform, $id);
    return [array_map(fn($p) => ['source_key' => $key, 'source_url' => $p['url'] ?? '', 'video' => $p['video'] ?? ''] + $p, $playable), $updated, $event];
}

/** Ставки источников для порядка сбора. Источники по оценкам не выключаются: плохие идут ниже, но берутся — контент собирается отовсюду. */
function user_rates_for_run(array $rates, string $topic): array {
    return $rates;
}

/** Длинный список досок обходится порциями: половина — лучшие по прошлым запускам, остальное — случайные. */
function dvach_boards_this_run(array $boards, $topic) {
    if (count($boards) <= DVACH_BOARDS_PER_RUN) return $boards;
    $rates = user_rates_for_run(combined_source_rates(source_pass_rates(topic_verdict_stats($topic)), $topic), $topic);
    $rate = fn($b, $default) => $rates["dvach:$b"] ?? $default;
    $always = array_values(array_intersect(DVACH_ALWAYS_BOARDS, $boards));
    $proven = array_values(array_filter(array_diff($boards, $always), fn($b) => $rate($b, 0.0) >= 0.2));
    usort($proven, fn($a, $b) => $rate($b, 0) <=> $rate($a, 0));
    $chosen = [...$always, ...array_slice($proven, 0, intdiv(DVACH_BOARDS_PER_RUN, 2))];
    $rest = array_values(array_diff($boards, $chosen));
    shuffle($rest);
    $chosen = array_merge($chosen, array_slice($rest, 0, DVACH_BOARDS_PER_RUN - count($chosen)));
    cli_echo('Двач: в списке ' . count($boards) . ' досок, в этот запуск — ' . count($chosen) . ' (лучшие по прошлым запускам и случайные, остальные — в следующие запуски).');
    return $chosen;
}

const SUGGESTION_POSTS_MAX = 12;   // находки автоподбора в проход, пока у темы мало своих источников — по кругу по площадкам

/** Строка журнала для источника: YouTube выделен из RSS по типу ленты. */
function platform_label(string $platform, array $entry): string {
    if ($platform === PLATFORM_RSS && in_array(rss_feed_kind((string)($entry['id'] ?? '')), ['YouTube', 'YouTube Shorts'], true)) return 'youtube';
    return match ($platform) {
        PLATFORM_TELEGRAM => 'telegram', PLATFORM_VK => 'vk', PLATFORM_RSS => 'rss', PLATFORM_OK => 'ok', PLATFORM_TIKTOK => 'tiktok',
        PLATFORM_DVACH => 'dvach', PLATFORM_4CHAN => 'fourchan', default => $platform,
    };
}

/** Название площадки для журнала (ключ из platform_label). */
function platform_name(string $label): string {
    return ['telegram' => 'Telegram', 'dvach' => 'Двач', 'fourchan' => '4chan', 'youtube' => 'YouTube', 'vk' => 'VK', 'tiktok' => 'TikTok', 'ok' => 'ОК', 'rss' => 'RSS'][$label] ?? $label;
}

function platform_report_init(): array {
    return array_fill_keys(['telegram', 'vk', 'youtube', 'tiktok', 'ok', 'rss', 'dvach', 'fourchan'], ['sources' => 0, 'with' => 0, 'posts' => 0]);
}

/** Итог по всем площадкам одной строкой: постов и источников с постами; у пустых — причина (журнал должен объяснять пустоту). */
function platform_summary_line(array $rep, bool $vk): string {
    $names = array_fill_keys(['telegram', 'dvach', 'fourchan', 'youtube', 'vk', 'tiktok', 'ok', 'rss'], null);
    foreach ($names as $key => $_) $names[$key] = platform_name($key);
    $unit = ['telegram' => 'каналов', 'youtube' => 'каналов', 'rss' => 'лент', 'dvach' => 'доски', 'fourchan' => 'доски'];
    $parts = [];
    foreach ($names as $key => $name) {
        $r = $rep[$key];
        if ($r['posts'] > 0) {
            $parts[] = match ($key) {
                'dvach' => "{$name} {$r['posts']} (" . $r['with'] . ' ' . ($r['with'] % 10 === 1 && $r['with'] % 100 !== 11 ? 'доска' : 'доски') . ')',
                'fourchan' => "{$name} {$r['posts']}",
                default => "{$name} {$r['posts']} постов (" . $r['with'] . ' ' . ($unit[$key] ?? 'источников') . ')',
            };
            continue;
        }
        $reason = match (true) {
            $key === 'vk' && !$vk => 'нет ключа',
            $key === 'rss' => 'нет лент по теме',
            in_array($key, ['dvach', 'fourchan'], true) => 'нет досок по теме',
            $r['sources'] > 0 => 'источники есть, постов нет',
            default => 'нет источников по теме',
        };
        $parts[] = "{$name} 0 — {$reason}";
    }
    return 'Площадки: ' . implode(' · ', $parts) . '.';
}

/** Посты всех источников темы по площадкам. */
function collect_posts_by_platform(array $db, $topic) {
    $GLOBALS['news_enrich_left'] = NEWS_ENRICH_PER_PASS;   // бюджет страниц статей на проход
    $by = [PLATFORM_TELEGRAM => [], PLATFORM_VK => [], PLATFORM_RSS => [], PLATFORM_OK => [], PLATFORM_TIKTOK => [], PLATFORM_DVACH => [], PLATFORM_4CHAN => []];
    fourchan_begin_pass();
    $stats = array_fill_keys(['other_topic', 'skipped_frozen', 'frozen', 'still_frozen', 'revived'], 0);
    $updates = [];
    $rep = platform_report_init();   // итог по площадкам для журнала
    if (!($boards = dvach_boards_for_topic($topic))) cli_echo('Подходящих теме досок Двача нет — Двач пропущен.');
    foreach (dvach_boards_this_run($boards, $topic) as $board) {
        cli_echo("Парсинг Двача /$board/...");
        try { $found = parse_dvach($board, $topic); } catch (Throwable $e) { cli_echo("-> Двач /$board/: " . $e->getMessage()); $found = []; }   // сбой одной доски не рвёт проход по остальным площадкам
        array_push($by[PLATFORM_DVACH], ...$found);
        $rep['dvach']['sources']++;
        $rep['dvach']['with'] += $found ? 1 : 0;
        $rep['dvach']['posts'] += count($found);
        cli_echo("-> Двач /$board/: найдено медиа-постов: " . count($found));
    }
    // 4chan: доски по теме, не больше FOURCHAN_BOARDS_PER_RUN; итог по площадке — в журнале
    try { $chan_boards = fourchan_boards_for_topic($topic); } catch (Throwable $e) { cli_echo('4chan: ' . $e->getMessage()); $chan_boards = []; }
    if (!$chan_boards) cli_echo('Подходящих теме досок 4chan нет — 4chan пропущен.');
    $chan_threads = 0;
    foreach (fourchan_boards_this_run($chan_boards) as $board) {
        cli_echo("Парсинг 4chan /$board/...");
        try { $found = parse_fourchan($board, FOURCHAN_THREADS_PER_BOARD, $topic); } catch (Throwable $e) { cli_echo("-> 4chan /$board/: " . $e->getMessage()); $found = []; }
        array_push($by[PLATFORM_4CHAN], ...$found);
        $rep['fourchan']['sources']++;
        $rep['fourchan']['with'] += $found ? 1 : 0;
        $rep['fourchan']['posts'] += count($found);
        $chan_threads += count(array_unique(array_column($found, 'thread_id')));
        cli_echo("-> 4chan /$board/: найдено медиа-постов: " . count($found));
    }
    if ($chan_boards) cli_echo('4chan: ' . min(count($chan_boards), FOURCHAN_BOARDS_PER_RUN) . ' доски, ' . $chan_threads . ' тредов с медиа, '
        . count($by[PLATFORM_4CHAN]) . ' постов с медиа, ' . ($GLOBALS['fourchan_stats']['api'] ?? 0) . ' запросов к API (из кэша: ' . ($GLOBALS['fourchan_stats']['api_cached'] ?? 0) . ').');
    try { $vk = vk_can_read(); } catch (Throwable $e) { cli_echo('VK: ' . $e->getMessage()); $vk = false; }
    if (!$vk) cli_echo('VK: нет ключа — паблики VK пропущены (добавьте сервисный ключ в config.local.php).');
    $active_only = !empty(content_filters()['active_sources_only']);   // настройка пользователя; по умолчанию включена
    // Бережный опрос: в сеть — не больше SOURCE_FETCHES_PER_PASS самых просроченных источников, остальные — из кэша kv (source_poll.php)
    $now = time();
    $poll = [];
    foreach (CATALOG_KEYS as $platform => $key) {
        if ($platform === PLATFORM_VK && !$vk) continue;
        foreach ($db[$key] as $entry) if (catalog_entry_matches_topic($entry, $topic) && !is_source_frozen($entry)) $poll[$platform . '|' . strtolower((string)$entry['id'])] = $entry;
    }
    $cached = [];
    foreach ($poll as $pkey => $entry) if (kv_get(source_poll_cache_key(strstr($pkey, '|', true), $entry['id']), null) !== null) $cached[$pkey] = true;
    $fetch = array_flip(source_poll_fetch_set($poll, $cached, $now));
    $fetched = $from_cache = 0;
    $next_min = null;   // через сколько минут самый ранний из тех, что сейчас из кэша, снова просрочен
    foreach (CATALOG_KEYS as $platform => $key) {
        if ($platform === PLATFORM_VK && !$vk) continue;
        foreach ($db[$key] as $entry) {
            if (!catalog_entry_matches_topic($entry, $topic)) { $stats['other_topic']++; continue; }
            $label = platform_label($platform, $entry);
            $rep[$label]['sources']++;
            $pkey = $platform . '|' . strtolower((string)$entry['id']);
            $hit = isset($poll[$pkey]) && !isset($fetch[$pkey]) ? kv_get(source_poll_cache_key($platform, $entry['id']), null) : null;
            if ($hit !== null) {
                $posts = (array)($hit['posts'] ?? []);
                $updated = $event = null;
                $from_cache++;
                $left = source_poll_interval($entry['activity'] ?? null) - ($now - (int)($entry['activity']['checked_at'] ?? 0));
                $next_min = $next_min === null ? $left : min($next_min, $left);
            } else {
                [$posts, $updated, $event] = collect_catalog_source($platform, $entry, $active_only);
                if ($posts) kv_update(source_poll_cache_key($platform, $entry['id']), fn() => ['at' => time(), 'posts' => $posts]);
                if (isset($poll[$pkey])) $fetched++;
            }
            if ($event !== null) $stats[$event]++;
            if ($updated !== null) $updates[$key . ':' . strtolower((string)$entry['id'])] = $updated;
            if ($posts) $rep[$label]['with']++;
            $rep[$label]['posts'] += count($posts);
            array_push($by[$platform], ...$posts);
        }
    }
    save_catalog_entry_updates($updates);
    if ($poll) cli_echo("Опрошено {$fetched} из " . count($poll) . ' источников, остальные из кэша' . ($from_cache && $next_min !== null ? ' (следующий через ~' . max(1, (int)ceil($next_min / 60)) . ' мин)' : '') . '.');
    // У новой темы мало своих источников: пока пользователь не разобрал находки подбора, посты берутся и из них.
    // Поровну по площадкам: сначала по две лучших с каждой, потом по кругу; внутри площадки — по score.
    if (topic_lacks_sources($db, $topic)) {
        $by_platform = [];
        foreach ($db['source_suggestions'] as $s) {
            $platform = $s['platform'] ?? '';
            if (!isset(CATALOG_KEYS[$platform]) || ($platform === PLATFORM_VK && !$vk) || !topic_same((string)($s['topic'] ?? ''), $topic) || (int)($s['score'] ?? 0) < SOURCE_SUGGEST_SCORE) continue;
            $by_platform[$platform][] = $s;
        }
        $lists = [];
        foreach (CATALOG_KEYS as $platform => $_) {
            if (empty($by_platform[$platform])) continue;
            usort($by_platform[$platform], fn($a, $b) => ($b['score'] ?? 0) <=> ($a['score'] ?? 0));
            $lists[] = $by_platform[$platform];
        }
        $picked = discovery_balance($lists, SUGGESTION_POSTS_MAX, 2);
        foreach ($picked as $s) {
            $platform = $s['platform'];
            $name = preg_replace('/^(Telegram|ВКонтакте|RSS|Одноклассники|TikTok) \| /u', '', (string)($s['name'] ?? $s['id']));
            [$posts] = collect_catalog_source($platform, ['id' => $s['id'], 'name' => $name, 'topics' => [$topic]], $active_only);
            $label = platform_label($platform, ['id' => $s['id']]);
            if ($posts) $rep[$label]['with']++;
            $rep[$label]['posts'] += count($posts);
            array_push($by[$platform], ...$posts);
        }
        if ($picked) {
            $by_label = [];
            foreach ($picked as $s) { $label = platform_label($s['platform'], ['id' => $s['id']]); $by_label[$label] = ($by_label[$label] ?? 0) + 1; }
            $names = array_map(fn($label, $n) => platform_name($label) . " {$n}", array_keys($by_label), $by_label);
            cli_echo('Своих источников у темы мало — временно беру посты из ' . count($picked) . ' находок автоподбора (' . implode(', ', $names) . '; решение по ним — на вкладке «Источники»).');
        }
    }
    // Поиск постов встроенными поисками (POST_SEARCH_PROMPT.md): идёт рядом с источниками, в пределах бюджета прохода
    $search = post_search_collect($topic, app_user_bind() !== '' ? app_user_id() : null);
    array_push($by[PLATFORM_RSS], ...$search['by'][PLATFORM_RSS]);
    array_push($by[PLATFORM_TELEGRAM], ...$search['by'][PLATFORM_TELEGRAM]);
    cli_echo('Поиск постов: YouTube ' . count($search['by'][PLATFORM_RSS]) . ' (запросов ' . $search['done']['youtube'] . '), Telegram '
        . count($search['by'][PLATFORM_TELEGRAM]) . ' (запросов ' . $search['done']['telegram'] . ') — после фильтра по словам.');
    $labels = ['other_topic' => 'другой темы', 'skipped_frozen' => 'заморожено (ждут перепроверки)', 'frozen' => 'заморожено сейчас', 'revived' => 'ожило'];
    $notes = array_map(fn($k) => "{$labels[$k]}: {$stats[$k]}", array_keys(array_filter(array_intersect_key($stats, $labels))));
    if ($notes) cli_echo('Источники: ' . implode(', ', $notes) . '.');
    cli_echo(platform_summary_line($rep, $vk));
    return $by;
}

// ---- Кандидаты ----

/** Один кандидат на пост: оценивается первая картинка, остальные — в галерею. */
function expand_post_images($posts) {
    $out = [];
    foreach ($posts as $post) {
        $images = array_values(array_unique(array_filter((array)($post['images'] ?? []) ?: [$post['image'] ?? null], 'is_string')));
        if (!$images) continue;
        $c = ['image' => $images[0], 'images' => [$images[0]], 'captions' => [$post['captions'][0] ?? ($post['text'] ?? '')]] + $post;
        if (count($post['gallery'] ?? []) > 1) $c['gallery'] = $post['gallery'];
        elseif (count($images) > 1) $c['gallery'] = array_map(fn($i) => ['image' => $i, 'video' => ''], $images);
        $out[] = $c;
    }
    return $out;
}

/** Ключ поста (ссылка на пост): после склейки альбомов первой картинкой мог стать другой файл. */
function candidate_post_key(array $item) {
    $url = (string)($item['source_url'] ?? $item['url'] ?? '');
    if ($url === '' && !empty($item['board']) && !empty($item['thread_id']) && !empty($item['post_id'])) $url = dvach_post_url($item['board'], $item['thread_id'], $item['post_id']);
    return $url === '' ? '' : 'post:' . preg_replace('~[?#].*$~', '', $url) . (str_contains($url, '2ch.hk') ? '#' . ($item['post_id'] ?? '') : '');
}

/** Ключ текстового поста без картинки: text: + sha1 нормализованного текста (без ссылок, эмодзи и регистра). '' — текста мало. */
function candidate_text_key(array $item): string {
    if (($item['image'] ?? '') !== '' || empty($item['text_post'])) return '';
    $text = mb_strtolower((string)($item['text'] ?? ''));
    $text = preg_replace(['~https?://\S+~u', '~[^\p{L}\p{N}\s]+~u', '~\s+~u'], [' ', ' ', ' '], $text);
    $text = trim((string)$text);
    return mb_strlen($text) >= 20 ? 'text:' . sha1($text) : '';
}

/** Ключи записей: картинки (главная и галерея) и пост. */
function record_known_keys(array $records) {
    $known = [];
    foreach ($records as $r) {
        if (!is_array($r)) continue;
        foreach ([content_image_key($r), ...array_column((array)($r['gallery'] ?? []), 'image')] as $img) if (is_string($img) && $img !== '') $known[$img] = true;
        if (($post = candidate_post_key($r)) !== '') $known[$post] = true;
        if (($tkey = candidate_text_key($r)) !== '') $known[$tkey] = true;
        // Новости: слова заголовка за последние сутки — для поиска дублей с других сайтов
        if (!empty($r['news_words']) && time() - (int)($r['pooled_at'] ?? $r['posted_at'] ?? 0) <= NEWS_DUPLICATE_HOURS * 3600) $known['newsw:' . implode(' ', $r['news_words'])] = true;
    }
    return $known;
}

// Пул и история живут в SQLite (db_access.php): парсер читает и пишет их теми же функциями, что и сайт.
function history_records() {
    $history = history_load();
    return array_merge(...array_map(fn($c) => (array)($history[$c] ?? []), HISTORY_COLLECTIONS));
}

function collect_known_images() { return record_known_keys(array_merge(pool_load(), history_records())); }

/** Отпечатки записей пула и истории: [phash => подпись источника] — с чем сравнивать новых кандидатов. */
function collect_known_hashes() {
    $hashes = [];
    foreach (array_merge(pool_load(), history_records()) as $r) {
        if (!is_array($r)) continue;
        $phash = (string)($r['phash'] ?? '');
        if ($phash !== '') $hashes[$phash] = $r['source'] ?? '';
    }
    return $hashes;
}

function candidate_is_known(array $item, array $known) {
    foreach ([$item['image'] ?? '', ...array_column((array)($item['gallery'] ?? []), 'image')] as $img) if ($img !== '' && isset($known[$img])) return true;
    if (($tkey = candidate_text_key($item)) !== '' && isset($known[$tkey])) return true;
    if (!empty($item['news_words'])) {   // та же новость с другого сайта
        foreach (array_keys($known) as $key) {
            if (is_string($key) && str_starts_with($key, 'newsw:') && news_similar($item['news_words'], explode(' ', substr($key, 6)))) return true;
        }
    }
    return ($post = candidate_post_key($item)) !== '' && isset($known[$post]);
}

/** Убирает из пула записи, чей пост уже оценён. */
function prune_pool_already_rated() {
    $known = record_known_keys(history_records());
    $before = pool_load();
    $after = pool_update(function ($pool) use ($known) {
        $kept = array_values(array_filter($pool, fn($i) => !is_array($i) || !candidate_is_known($i, $known)));
        return count($kept) !== count($pool) ? $kept : null;
    });
    media_cache_release_removed($before, $after);
    return count($before) - count($after);
}

/** Добавляет запись в пул в транзакции (лучшие первыми). false — уже в пуле или оценена. */
/**
 * Добавляет запись в пул. Если пул темы полон (CONTENT_POOL_TARGET записей, включая просроченные),
 * вытесняется самая старая просроченная запись этой темы: только тогда её медиа удаляется.
 * Если просроченных нет, полный пул не трогаем и запись не добавляется.
 */
function add_item_to_pool_safely($item) {
    $added = false;
    $item += ['pooled_at' => time()];   // по этой метке запись просрочивается (POOL_MAX_AGE_SECONDS)
    $before = pool_load();
    $after = pool_update(function ($pool) use ($item, &$added) {
        if (candidate_is_known($item, collect_known_images())) return null;
        $topic = topic_norm($item['topic'] ?? '');
        $same = array_keys(array_filter($pool, fn($i) => is_array($i) && topic_norm($i['topic'] ?? '') === $topic));
        if (count($same) >= CONTENT_POOL_TARGET) {
            $expired = array_filter($same, fn($k) => !pool_item_is_fresh($pool[$k]));
            if (!$expired) return null;
            usort($expired, fn($a, $b) => ($pool[$a]['pooled_at'] ?? 0) <=> ($pool[$b]['pooled_at'] ?? 0));
            unset($pool[$expired[0]]);
            $pool = array_values($pool);
        }
        $pool[] = $item;
        $pool = pool_order($pool);   // порядок показа — по свежести, источники чередуются; card_score тут не решает порядок (FRESH_ORDER_POLITE_FETCH_PROMPT.md, часть A)
        $added = true;
        return $pool;
    });
    media_cache_release_removed($before, $after);   // медиа вытесненной записи — только сейчас
    if ($added) pool_items_enqueue_videos([$item], app_user_id());   // ролик — фоном, не здесь
    if ($added) translate_warm_cache($item);   // перевод — заранее, в кэш (AUTOTRANSLATE_PROMPT.md, часть C)
    return $added;
}

/** Если видео есть только в галерее, оно становится главным файлом записи. */
function post_item_video_promote(array &$item) {
    if (trim((string)($item['video'] ?? '')) !== '') return true;
    foreach ((array)($item['gallery'] ?? []) as $f) {
        if (trim((string)($f['video'] ?? '')) === '') continue;
        [$item['image'], $item['video']] = [$f['image'] ?? $item['image'], $f['video']];
        return true;
    }
    return false;
}

/** Очередь: без виденных, устаревших, отсеянных раньше и отключённых источников; лучшие источники первыми, площадки чередуются. До 500. */
function build_candidate_queue($by_platform, $seen, array $rejected = [], array $rates = [], array &$skipped = [], array $blocked = [], ?array $format = null, array $filters = []) {
    $skipped = ['outdated' => 0, 'rejected_before' => 0, 'disabled_sources' => 0, 'blocked_platform' => 0, 'format' => 0, 'filter' => 0];
    $unique = [];
    $sampled = [];   // площадки, которые пользователь режет по теме: в полный анализ 1 из QUALITY_PLATFORM_SAMPLE
    foreach ($by_platform as $posts) foreach (expand_post_images($posts) as $item) {
        if (!content_passes_media_filters($item, $filters)) { $skipped['filter']++; continue; }   // фильтры пользователя «Только с медиа» / «Только медиа»: в анализ не идут
        $ukey = ($item['image'] ?? '') !== '' ? $item['image'] : candidate_text_key($item);   // текстовый пост — по ключу текста
        if ($ukey === '') { if ($format !== null && ($item['image'] ?? '') === '') $skipped['format']++; continue; }   // без картинки и без текста нужной длины
        if (candidate_is_known($item, $seen) || isset($unique[$ukey])) continue;
        $source = content_source_key($item);
        $reason = match (true) {
            is_post_outdated($item['posted_at'] ?? 0, null, content_max_age_days(str_starts_with($source, 'dvach:'))) => 'outdated',
            isset($rejected[$item['image']]) => 'rejected_before',
            $format !== null && (($item['image'] ?? '') === '' && $format['media'] === 'required') => 'format',   // без картинки — тема требует картинку
            $format !== null && $format['text'] === 'required' && mb_strlen(trim((string)($item['text'] ?? ''))) < $format['min_text'] => 'format',   // текст короче минимума
            in_array(explode(':', $source)[0], $blocked, true) && ($sampled[explode(':', $source)[0]] = ($sampled[explode(':', $source)[0]] ?? 0) + 1) % QUALITY_PLATFORM_SAMPLE !== 1 => 'blocked_platform',
            default => null
        };
        if ($reason) $skipped[$reason]++; else $unique[$ukey] = $item;
    }
    // Сначала каналы, где запись по теме реально проходит в пул (итог полного анализа); холодные каналы — в конец.
    // Внутри канала — самые горячие по реакциям и просмотрам, при равенстве — свежие
    $all = array_values($unique);
    // Уточнение: записи, где оно упомянуто в тексте, — на полный анализ первыми (TOPIC_QUERY_PROMPT.md, часть 4)
    $query = current_query();
    $named = fn($item) => $query !== '' && query_text_hit((string)($item['text'] ?? ''), $query) ? 1 : 0;
    $chance = function ($item) use ($rates) {
        [$p, $t] = $rates[content_source_key($item)] ?? [0, 0];
        $rate = ($p + 1) / ($t + 2);   // сглаживание: без данных — нейтрально
        return [$t >= SOURCE_VERDICT_MIN && $rate < SOURCE_VERDICT_LOW, $rate];
    };
    usort($all, function ($a, $b) use ($chance, $named) {
        [$cold_a, $rate_a] = $chance($a);
        [$cold_b, $rate_b] = $chance($b);
        return ($named($b) <=> $named($a)) ?: (!empty($b['thread_trust']) <=> !empty($a['thread_trust'])) ?: ($cold_a <=> $cold_b) ?: ($rate_b <=> $rate_a) ?: content_hotness($b) <=> content_hotness($a) ?: ($b['posted_at'] ?? 0) <=> ($a['posted_at'] ?? 0);
    });
    return array_slice(interleave_by_source($all), 0, 500);
}

/**
 * Кандидатов по кругу из источников: по одному с каждого по очереди. Порядок источников — как первое появление в списке
 * (по доле прошедших), внутри источника — тот же порядок. Пачки из одного источника в анализе не будет.
 */
function interleave_by_source(array $items): array {
    $by_source = [];
    foreach ($items as $item) $by_source[content_source_key($item)][] = $item;
    $out = [];
    for ($round = 0; $by_source; $round++) {
        foreach ($by_source as $key => $list) {
            $out[] = $list[$round];
            if (count($list) === $round + 1) unset($by_source[$key]);
        }
    }
    return $out;
}

/** Горячесть поста: просмотры, реакции и ответы на час жизни. Нет данных — 0. */
function content_hotness($item) {
    $e = is_array($item['engagement'] ?? null) ? $item['engagement'] : [];
    $views = (int)($e['views'] ?? $item['post_views'] ?? 0);
    $reactions = is_array($e['reactions'] ?? null) ? array_sum($e['reactions']) : 0;
    $hours = max(1, (time() - (int)($item['posted_at'] ?? 0)) / 3600);
    return ($views + 5 * $reactions + 10 * (int)($e['replies'] ?? 0)) / ($hours + 2);
}

// ---- Кэши оценок ----

function screened_scores_for_topic(array $cache, $topic) {
    $topic = mb_strtolower($topic);
    $filtered = array_filter($cache, fn($e) => mb_strtolower((string)($e['topic'] ?? '')) === $topic);
    return array_column($filtered, 'score', 'url');
}

/** Сглаженная доля прошедших быструю оценку по источникам. */
function source_pass_rates(array $verdicts) {
    return array_map(fn($c) => ($c[0] + 1) / ($c[1] + 2), $verdicts);   // [прошло, оценено] → сглаженная доля; вердикты полного анализа (PRESCREEN_SOFTEN_PROMPT.md, часть 2)
}

/** Быстрая оценка 2B отбрасывает запись, только если та «мусор» (score — P(мусор) в процентах). Нет оценки — не отбрасываем. */
function prescreen_is_junk($score, array $item): bool {
    return $score !== null && $score >= prescreen_junk_threshold_for($item);
}

/** Промахи подряд по каналу за запуск: полный анализ не принял запись. Идёт в PRESCREEN_SOURCE_MISS_LIMIT. */
function source_miss_note($source, $passed) {
    $GLOBALS['source_misses'] ??= [];
    $GLOBALS['source_misses'][$source] = $passed ? 0 : ($GLOBALS['source_misses'][$source] ?? 0) + 1;
}

/** Итог полного анализа по каналу и теме: записали оценку; прошла ли в пул (порог RELEVANCE_THRESHOLD). Окно — SOURCE_VERDICT_WINDOW последних. */
function source_verdict_record($topic, $source, $passed) {
    if ($topic === '' || $source === '') return;
    db_tx(function () use ($topic, $source, $passed) {
        $row = db_one('SELECT passed, total FROM source_verdicts WHERE topic = ? AND source = ?', [$topic, $source]);
        [$p, $t] = $row ? [(int)$row['passed'], (int)$row['total']] : [0, 0];
        if ($t >= SOURCE_VERDICT_WINDOW) {   // старые оценки уходят пропорционально: окно скользит
            $p = (int)round($p * (SOURCE_VERDICT_WINDOW - 1) / SOURCE_VERDICT_WINDOW);
            $t = SOURCE_VERDICT_WINDOW - 1;
        }
        db_exec('INSERT OR REPLACE INTO source_verdicts (topic, source, passed, total, at) VALUES (?, ?, ?, ?, ?)',
            [$topic, $source, $p + ($passed ? 1 : 0), $t + 1, time()]);
    });
}

/** [источник => [прошло в пул, оценено]] по теме. */
function topic_verdict_stats($topic) {
    $out = [];
    foreach (db_all('SELECT source, passed, total FROM source_verdicts WHERE topic = ?', [$topic]) as $r) $out[$r['source']] = [(int)$r['passed'], (int)$r['total']];
    return $out;
}

/** Добавляет в кэш и оставляет $limit самых свежих записей. */
function cache_merge(array $new, $limit) {
    if (!$new) return;
    db_tx(function () use ($new, $limit) {
        foreach ($new as $url => $data) {
            db_exec('INSERT OR REPLACE INTO screened_cache (url, topic, source, score, phash, at, ver) VALUES (?, ?, ?, ?, ?, ?, ?)',
                    [$url, $data['topic'] ?? '', $data['source'] ?? '', $data['score'] ?? 0, $data['phash'] ?? '', time(), SCREENED_CACHE_VERSION]);
        }
        db_exec('DELETE FROM screened_cache WHERE at < ?', [time() - SCREENED_CACHE_DAYS * DAY_SECONDS]);
        $count = db_value('SELECT COUNT(*) FROM screened_cache');
        if ($count > $limit) {
            $excess = $count - $limit;
            db_exec('DELETE FROM screened_cache WHERE url IN (SELECT url FROM screened_cache ORDER BY at ASC LIMIT ?)', [$excess]);
        }
    });
}

function analysis_cache_key($item, $topic) {
    $t = mb_strtolower(trim($topic));
    // Для тем из SUBTOPIC_CACHE_TOPICS ключ с суффиксом: старые оценки до правок части A–B не находятся; остальные темы — без изменений
    $suffix = in_array($t, SUBTOPIC_CACHE_TOPICS, true) ? '|subtopic2' : '';
    return sha1(ANALYSIS_PROMPT_VERSION . '|' . (content_image_key($item) ?: candidate_text_key($item)) . '|' . $t . $suffix);
}   // текст без картинки — по тексту

function analysis_cache_get($item, $topic) {
    $key = analysis_cache_key($item, $topic);
    $row = db_one('SELECT analysis, at FROM analysis_cache WHERE key = ? AND at > ?',
                  [$key, time() - ANALYSIS_CACHE_DAYS * DAY_SECONDS]);
    return $row ? db_unjson($row['analysis'], null) : null;
}

// ---- Оценка ----

function fetch_candidate_poster($item) {
    if (($local = media_cache_path($item['image'])) !== null) $bytes = is_file($local) ? file_get_contents($local) : '';   // обложка уже в кэше (TikTok): адрес относительный
    elseif (str_starts_with((string)$item['image'], FOURCHAN_IMG . '/')) $bytes = fourchan_fetch_media($item['image']);   // 4chan: заголовки браузера, Referer и пауза
    else $bytes = http_get($item['image']) ?: http_get($item['image'], [CURLOPT_TIMEOUT => 25]);
    if (!$bytes) { cli_echo('-> Превью не скачалось, пропуск.'); return null; }
    try { validate_image_bytes($bytes); } catch (Throwable $e) { cli_echo('-> Пропуск: ' . $e->getMessage()); return null; }
    return $bytes;
}

/**
 * Быстрая оценка пачки. Возвращает прошедших, лучшие первыми: [['item', 'poster', 'score']].
 * $ctx: stats, misses (промахи подряд по источникам), cached (оценки темы из кэша), new_cache, passed_total.
 */
function prescreen_batch(array $batch, $topic, array &$ctx, $deadline) {
    $passed = [];
    $to_score = [];  // кандидаты на параллельную оценку

    // Фильтруем и готовим кандидатов. Быстрая оценка 2B видит только картинку и подпись: тему не видит (PRESCREEN_SOFTEN_PROMPT.md)
    foreach ($batch as $pos => $item) {
        if (time() > $deadline || !parser_session_alive()) break;
        $source = content_source_key($item);
        if (!empty($ctx['passed_total']) && ($GLOBALS['source_misses'][$source] ?? 0) >= PRESCREEN_SOURCE_MISS_LIMIT) { $ctx['stats']['source_skipped']++; continue; }
        if (($poster = fetch_candidate_poster($item)) === null) continue;

        $hash = image_phash($poster) ?? '';
        if ($hash !== '' && ($seen = phash_find($hash, $ctx['known_hashes'])) !== null) {
            $ctx['stats']['duplicates']++;
            cli_echo('-> ' . candidate_log_label($item) . ': повтор уже виденной картинки' . ($ctx['known_hashes'][$seen] !== '' ? " (из «{$ctx['known_hashes'][$seen]}»)" : '') . ' — пропуск.');
            continue;
        }
        if ($hash !== '') $ctx['known_hashes'][$hash] = $item['source'] ?? '';

        $row = ['item' => $item, 'poster' => $poster, 'pos' => $pos];
        if (($cached = $ctx['cached'][$item['image']] ?? null) !== null) {
            // Из кэша — обрабатываем сразу
            if (prescreen_is_junk($cached, $item)) $ctx['stats']['rejected']++;
            else { $ctx['passed_total'] = ($ctx['passed_total'] ?? 0) + 1; $passed[] = $row + ['score' => $cached]; }
        } elseif ($hash !== '' && ($screened_score = phash_find($hash, $ctx['screened_hashes'])) !== null) {
            // Нашлась быстрая оценка копии в экране - используй её score
            if (prescreen_is_junk($ctx['screened_hashes'][$screened_score], $item)) $ctx['stats']['rejected']++;
            else { $ctx['passed_total'] = ($ctx['passed_total'] ?? 0) + 1; $passed[] = $row + ['score' => $ctx['screened_hashes'][$screened_score]]; }
        } else {
            // Нужна оценка — добавляем в параллельную батч
            $to_score[] = $row + ['source' => $source, 'phash' => $hash];
        }
    }

    // Параллельная быстрая оценка (curl_multi для одновременных запросов к Ollama)
    if ($to_score) {
        cli_echo('Параллельная быстрая оценка ' . count($to_score) . ' кандидатов (до ' . min(5, count($to_score)) . ' одновременно)...');
        $scored = parallel_quick_scores($to_score, $topic);
        foreach ($scored as $idx => $quick) {
            if (!isset($to_score[$idx])) continue;
            $item = $to_score[$idx]['item'];
            $source = $to_score[$idx]['source'];
            $row = ['item' => $item, 'poster' => $to_score[$idx]['poster'], 'pos' => $to_score[$idx]['pos']];
            cli_echo('-> ' . candidate_log_label($item) . ': ' . ($quick['score'] === null ? 'ошибка оценки, идёт на полный анализ' : ($quick['score'] >= prescreen_junk_threshold_for($item) ? "мусор {$quick['score']}% — пропуск" : "мусора {$quick['score']}%")));

            if ($quick['score'] === null) {   // без ответа не отбрасываем: решает полный анализ
                $passed[] = $row + ['score' => null];
                continue;
            }
            $ctx['new_cache'][$item['image']] = ['score' => $quick['score'], 'topic' => $topic, 'source' => $source, 'phash' => $to_score[$idx]['phash'] ?? '', 'at' => time()];
            $ctx['stats']['prescreened']++;

            if (prescreen_is_junk($quick['score'], $item)) {
                $ctx['stats']['rejected']++;
            } else {
                $ctx['passed_total'] = ($ctx['passed_total'] ?? 0) + 1;
                $passed[] = $row + ['score' => $quick['score']];
            }
        }
    }
    usort($passed, fn($a, $b) => $a['pos'] <=> $b['pos']);   // порядок очереди сохраняется: её уже выстроили по источникам и свежести
    return $passed;
}

/**
 * Быстрая оценка пачки: первая попытка («да/нет») уходит всем сразу, до 5 одновременно (ollama_generate_many).
 * Записи без ответа доспрашиваются последовательно через quick_junk_score (вторая попытка — число).
 */
function parallel_quick_scores(array $candidates, $topic) {
    if (!$candidates) return [];
    ollama_prepare_model(prescreen_model());
    // Подпись и название треда — свои у каждой записи (раньше брали подпись первой записи пачки)
    $captions = array_map(fn($c) => post_caption_for_ai($c['item']), $candidates);
    $threads = array_map(fn($c) => imageboard_item($c['item']) ? (string)($c['item']['thread_title'] ?? '') : '', $candidates);
    $payloads = array_map(fn($c, $i) => quick_junk_payload($c['poster'], $captions[$i], $threads[$i]), $candidates, array_keys($candidates));
    $answers = ollama_generate_many($payloads, 120, 5);
    return array_map(function ($answer, $c, $i) use ($captions, $threads) {
        $score = $answer ? (yes_probability_score($answer['logprobs'] ?? []) ?? parse_yes_no_score($answer['response'] ?? '')) : null;
        return $score !== null
            ? ['score' => $score, 'seconds' => null, 'error' => '', 'raw' => '', 'detail' => '']
            : quick_junk_score($c['poster'], $captions[$i], $threads[$i]);
    }, $answers, $candidates, array_keys($candidates));
}

/** Порядок текстовых записей в полном анализе: сначала с ключевыми словами темы, потом по лайкам (выше — раньше). */
function text_candidates_priority(array $items, $topic): array {
    $words = discovery_topic_keywords($topic);
    $score = fn($item) => ($words && array_filter($words, fn($w) => mb_stripos((string)($item['text'] ?? ''), $w) !== false) ? 1000000 : 0) + (int)($item['likes'] ?? 0);
    usort($items, fn($a, $b) => $score($b) <=> $score($a));
    return $items;
}

/**
 * Пост без картинки (TOPICS_SEARCH_PROMPT.md, часть E): оценка текста умной моделью, те же проверки, что у картинки, запись без картинки.
 * Вызывается только при включённом сборе текста (text_posts_enabled()).
 */
function analyze_text_candidate($item, $history, $topic) {
    if (!text_posts_enabled() || ($text = analysis_text($item)) === '') return false;   // статья — с заголовком первым
    $GLOBALS['text_analyses_left'] ??= TEXT_ANALYSES_PER_PASS;
    if ($GLOBALS['text_analyses_left'] <= 0) {   // лимит текстовых анализов за проход: запись отложена до следующего прохода
        $GLOBALS['text_analyses_deferred'] = ($GLOBALS['text_analyses_deferred'] ?? 0) + 1;
        cli_echo('-> Текстовых: проанализировано ' . TEXT_ANALYSES_PER_PASS . ', отложено ' . $GLOBALS['text_analyses_deferred'] . ' (лимит за проход).');
        return false;
    }
    $GLOBALS['text_analyses_left']--;
    cli_echo('Полный анализ текстовой записи ' . candidate_log_label($item) . ' (без картинки)');
    try {
        if (($a = analysis_cache_get($item, $topic)) !== null) {
            cli_echo('-> Анализ взят из общего кэша: эту запись по этой теме уже оценивали.');
        } else {
            try { $a = analyze_text_batch([$text], $topic)[0] ?? null; }
            catch (Throwable $e) { cli_echo('-> ИИ не вернул оценку текста: ' . $e->getMessage()); return false; }
            if ($a === null) { cli_echo('-> ИИ не ответил по этой записи, пропускаю.'); return false; }
            db_tx(function () use ($item, $topic, $a) {
                db_exec('INSERT OR REPLACE INTO analysis_cache (key, analysis, at) VALUES (?, ?, ?)', [analysis_cache_key($item, $topic), db_json($a), time()]);
            });
        }
        if (is_spam_content($text, $a['description'])) { cli_echo('-> Отброшено: явный маркер рекламы или спама.'); return false; }
        $verdict = "соответствие теме {$a['relevance']}%";
        $a = quality_tag_penalty($a, $topic);
        $passed_analysis = (int)$a['relevance'] >= RELEVANCE_THRESHOLD;
        source_verdict_record($topic, content_source_key($item), $passed_analysis);   // итог полного анализа — для порядка каналов (часть F)
        source_miss_note(content_source_key($item), $passed_analysis);
        if ($reason = pool_admission($a, $item, $topic)) { cli_echo("-> Отброшено: {$reason} ({$verdict})."); return false; }
        if (content_adult_blocked(array_merge($item, ['tags' => $a['tags'] ?? []]), $topic)) { cli_echo('-> Отклонено: 18+ или «Жесть» не по теме.'); return false; }
        if (search_topic_key() !== $topic) { cli_echo('-> Тема поиска сменилась — запись по старой теме не сохраняю.'); return false; }
        cli_echo("-> Принято: {$verdict}.");
        $record = ['description' => $a['description'] ?: 'Описание не получено', 'why_relevant' => $a['why_relevant'] ?: 'Нейросеть не сформулировала объяснение.',
            'relevance' => $a['relevance'], 'rating' => null, 'format' => $a['format'] ?? 'text', 'tags' => $a['tags'] ?? [], 'entity' => $a['entity'] ?? '', 'source_hashtags' => post_source_hashtags($item), 'topic' => $topic,
            'image_local' => '', 'phash' => ''] + $item;
        $record['image'] = '';
        $record['images'] = [];
        $record['captions'] = [$text];
        return add_item_to_pool_safely($record);
    } catch (Throwable $e) {
        cli_echo('-> Текстовая запись пропущена: ' . $e->getMessage());
        return false;
    }
}

/** Полный анализ кандидата (видео — раскадровкой), порог, кэш медиа и запись в пул. true — добавлено. */
function analyze_candidate($item, $poster, $history, $topic) {
    if (($item['image'] ?? '') === '') return analyze_text_candidate($item, $history, $topic);   // пост без картинки — оценка по тексту
    $is_video = ($item['video'] ?? '') !== '';
    cli_echo('Полный анализ ' . candidate_log_label($item) . ' ' . ($is_video ? 'видео' : 'картинка') . " {$item['image']}");
    $video = $is_video ? video_analysis_input($item['video'], $poster) : null;
    try {
        $frames = $video['frames'] ?? [$poster];
        $storyboard = $video !== null && count($frames) > 1;
        $context = analysis_context_block($item);   // тред имиджборды или статья: блок перед «Запись:», подпись тогда уже внутри блока
        $caption = $context !== '' ? '' : post_caption_for_ai($item);
        cli_echo('-> ' . ($storyboard ? 'Раскадровка видео уходит' : 'Картинка уходит') . " в vision-модель: описание и оценка по теме «{$topic}»...");
        if (($a = analysis_cache_get($item, $topic)) !== null) {
            cli_echo('-> Анализ взят из общего кэша: эту запись по этой теме уже оценивали.');
        } else {
            try { $a = analyze_content_with_vision($frames, $caption, $history, $topic, $storyboard ? $video : null, PARSER_THINKING, $context); }
            catch (Throwable $e) { cli_echo('-> ИИ не вернул корректную оценку: ' . $e->getMessage()); return false; }
            db_tx(function () use ($item, $topic, $a) {
                $key = analysis_cache_key($item, $topic);
                db_exec('INSERT OR REPLACE INTO analysis_cache (key, analysis, at) VALUES (?, ?, ?)',
                        [$key, db_json($a), time()]);
                db_exec('DELETE FROM analysis_cache WHERE at < ?', [time() - ANALYSIS_CACHE_DAYS * DAY_SECONDS]);
                $count = db_value('SELECT COUNT(*) FROM analysis_cache');
                if ($count > ANALYSIS_CACHE_LIMIT) {
                    $excess = $count - ANALYSIS_CACHE_LIMIT;
                    db_exec('DELETE FROM analysis_cache WHERE key IN (SELECT key FROM analysis_cache ORDER BY at ASC LIMIT ?)', [$excess]);
                }
            });
        }
        if (is_spam_content($caption, $a['description'])) { cli_echo('-> Отброшено: явный маркер рекламы или спама.'); return false; }
        $verdict = "соответствие теме {$a['relevance']}%";
        $a = quality_tag_penalty($a, $topic);   // частная тема без своей метки — штраф (часть D)
        $passed_analysis = (int)$a['relevance'] >= RELEVANCE_THRESHOLD;
        source_verdict_record($topic, content_source_key($item), $passed_analysis);   // итог полного анализа — для порядка каналов (часть F)
        source_miss_note(content_source_key($item), $passed_analysis);
        if ($reason = pool_admission($a, $item, $topic)) { cli_echo("-> Отброшено: {$reason} ({$verdict})."); return false; }
        if (content_adult_blocked(array_merge($item, ['tags' => $a['tags'] ?? []]), $topic)) { cli_echo('-> Отклонено: 18+ или «Жесть» не по теме.'); return false; }
        cli_echo("-> Принято: {$verdict}." . (($tags = content_tags_public($a['tags'] ?? [])) ? ' Метки: ' . implode(', ', array_column($tags, 'path')) . '.' : ''));
        if (search_topic_key() !== $topic) { cli_echo('-> Тема поиска сменилась — запись по старой теме не сохраняю.'); return false; }
        $item = ['description' => $a['description'] ?: 'Описание не получено', 'why_relevant' => $a['why_relevant'] ?: 'Нейросеть не сформулировала объяснение.',
            'relevance' => $a['relevance'], 'rating' => null, 'format' => $a['format'] ?? '', 'tags' => $a['tags'] ?? [], 'entity' => $a['entity'] ?? '', 'source_hashtags' => post_source_hashtags($item), 'topic' => $topic, 'image_local' => media_cache_store_image($item['image'], $poster), 'phash' => image_phash($poster) ?? ''] + $item;
        if ($is_video) {
            $item['video_local'] = !empty($video['file']) ? cache_downloaded_video($item['video'], $video['file']) : media_cache_store_video($item['video']);
            cli_echo($item['video_local'] !== '' ? '-> Видео сохранено в кэш сайта.' : '-> Видео не удалось сохранить в кэш, на сайте будет исходная ссылка.');
        }
        if (!empty($item['gallery'])) $item['gallery'] = gallery_cache_files($item['gallery'], $item['image']);
        if ($item['image_local'] === '') cli_echo('-> Картинку не удалось сохранить в кэш, на сайте будет исходная ссылка.');
        $added = add_item_to_pool_safely($item);
        cli_echo($added ? '[!] Запись сохранена и выведена на сайт.' : '-> Запись уже в пуле или оценена пользователем, пропускаю.');
        return $added;
    } finally {
        if (!empty($video['file'])) @unlink($video['file']);
    }
}

function collector_stop_reason($started, $analyzed, $added) {
    return match (true) {
        !parser_session_alive() => PARSER_SESSION_ENDED,
        time() - $started > PARSER_MAX_SECONDS => 'Достигнут лимит времени одного запуска.',
        $analyzed >= CONTENT_ANALYZE_PER_RUN => 'Достигнут лимит полных анализов за запуск.',
        $added >= CONTENT_ADDED_PER_RUN => 'Достигнут лимит новых записей за запуск.',
        pool_topic_count(search_topic_key()) >= CONTENT_POOL_TARGET => 'Пул заполнен до ' . CONTENT_POOL_TARGET . ' записей.',
        default => null
    };
}

/**
 * Один проход: уборка, тема и её профиль, подбор (если источников нет), сбор, пачки «быстрая оценка → анализ лучших».
 * Возвращает true, если тема или фильтры сменились и сбор нужно начать заново.
 */
/** Устанавливает фазу парсера с информацией о теме поиска. */
function parser_phase_with_topic($phase, $topic = null) {
    $topic = $topic ?? current_topic();
    $display_topic = $topic ? " — тема: «{$topic}»" : '';
    parser_phase($phase . $display_topic);
}

function run_collector() {
    $GLOBALS['text_analyses_left'] = TEXT_ANALYSES_PER_PASS;   // лимит текстовых анализов — на проход
    $GLOBALS['text_analyses_deferred'] = 0;
    $started = time();
    $topic = current_topic();
    ytdlp_kick_worker();   // будильник очереди роликов: воркер мог умереть после неудачи — проход парсера его поднимает
    parser_progress(['deadline' => $started + PARSER_MAX_SECONDS], true);
    cli_echo('');  // пустая строка для визуального разделения
    cli_echo('╔════════════════════════════════════════════════════════════════════════╗');
    cli_echo('║ ⏱️  НАЧИНАЕТСЯ ПОДБОР КОНТЕНТА                                           ║');
    cli_echo('║ Тема: «' . str_pad($topic, 62) . ' ║');
    cli_echo('║ Максимальное время ожидания: ' . (PARSER_MAX_SECONDS / 60) . ' минут                        ║');
    cli_echo('╚════════════════════════════════════════════════════════════════════════╝');
    cli_echo('');
    parser_phase_with_topic('сбор постов', $topic);
    mirror_collect_user(app_user_id());   // зеркала: новые посты личных источников без оценки моделью
    if ($n = media_cache_cleanup(all_users_pool_items())) cli_echo("Кэш медиа: удалено устаревших файлов: $n.");
    if ($n = prune_pool_already_rated()) cli_echo("Пул: убрано записей, пост которых уже оценён: $n.");
    pool_stamp_legacy();
    cache_pool_galleries();
    prune_dead_pool_items();
    $history = load_history();
    $seen = collect_known_images();
    $topic = current_topic();
    $key = search_topic_key();   // одна тема или «первая + вторая»
    if (($query = current_query()) !== '') cli_echo("Уточнение: «{$query}» — ищу в теме «{$topic}» только про это.");
    cli_echo('Модели: быстрая оценка — ' . prescreen_model() . ', полный анализ — ' . analysis_model() . (PARSER_THINKING ? ' (с размышлениями)' : ' (без размышлений)') . (fast_only_mode() ? ' (режим «только быстрая модель»)' : '') . '.');
    topic_ensure($topic);
    topic_classify_pending(1, $topic);
    topic_profile_ensure($topic);
    dvach_boards_for_topic($topic);
    $due = source_discovery_due(load_source_db(), $topic);
    $discovered = false;
    if ($due && topic_lacks_sources(load_source_db(), $topic)) {
        $own = topic_own_sources_count(load_source_db(), $topic);
        cli_echo("У темы «{$topic}» " . ($own ? "всего {$own} своих источника" : 'пока нет своих источников') . ' — сначала ищу новые.');
        run_source_discovery($topic);
        [$due, $discovered] = [false, true];
        if (current_topic() !== $topic) return true;
    }
    if ($reason = collector_stop_reason($started, 0, 0)) {
        cli_echo($reason);
        if ($due && parser_session_alive()) run_source_discovery($topic);
        return false;
    }
    // Уточнение: мало своих источников у пары — подбор по запросу (TOPIC_QUERY_PROMPT.md, часть 5), в пределах суточной квоты
    if (topic_query_of($key) !== '' && query_own_sources_count(load_source_db(), $key) < TOPIC_QUERY_MIN_SOURCES
        && query_discovery_quota_take(app_user_id(), $key)) run_query_discovery($key);
    $cache = screened_cache_load();
    $scores = screened_scores_for_topic($cache, $key);
    $key_lower = mb_strtolower($key);
    $screened_hashes = array_column(array_filter($cache, fn($e) => mb_strtolower((string)($e['topic'] ?? '')) === $key_lower && ($e['phash'] ?? '') !== ''), 'score', 'phash');
    $GLOBALS['source_misses'] = [];   // промахи подряд — за этот запуск
    $skipped = [];
    $history = history_load();
    $blocked = [];   // площадки по жалобам не режем (Двач в мемах и т. п.): контент берётся отовсюду, отбирает проверка по теме
    $candidates = build_candidate_queue(collect_posts_by_platform(load_source_db(), $key), $seen, array_filter($scores, fn($s) => $s >= PRESCREEN_JUNK_P * 100),
        topic_verdict_stats($key), $skipped, $blocked, topic_format($key), content_filters());
    cli_echo("📌 Тема: «{$topic}». Кандидатов к проверке: " . count($candidates) . '.'
        . ($skipped['rejected_before'] ? " Уже отсеяны в прошлых запусках: {$skipped['rejected_before']}." : '')
        . ($skipped['outdated'] ? " Устаревших: {$skipped['outdated']}." : '')
        . ($skipped['disabled_sources'] ? " Пропущено из источников, которые пользователи оценивают плохо: {$skipped['disabled_sources']}." : '')
        . ($skipped['format'] ? " Отсеяно форматом темы: {$skipped['format']}." : '')
        . ($skipped['filter'] ? " Отсеяно фильтрами «Только с медиа» / «Только медиа»: {$skipped['filter']}." : '')
        . ($skipped['blocked_platform'] ? " Площадки, которые режутся по теме (жалоб ≥ " . (int)(QUALITY_PLATFORM_COMPLAINT_SHARE * 100) . "%): в полный анализ 1 из " . QUALITY_PLATFORM_SAMPLE . ", пропущено: {$skipped['blocked_platform']}." : '')
        . ' Быстрая оценка по картинке ' . PRESCREEN_MAX_SIDE . ' px: отсев мусора при уверенности от ' . (int)round(PRESCREEN_JUNK_P * 100) . '%, тему не проверяет.');
    $ctx = ['stats' => ['prescreened' => 0, 'rejected' => 0, 'source_skipped' => 0, 'duplicates' => 0], 'cached' => $scores, 'new_cache' => [], 'known_hashes' => collect_known_hashes(), 'screened_hashes' => $screened_hashes];
    [$analyzed, $added, $stop, $restart, $queue, $done] = [0, 0, null, false, [], 0];
    // Полоса прогресса на сайте: сбор кончится, когда разобрана очередь или набран лимит записей (столько не хватает пулу)
    parser_progress(['total' => count($candidates), 'queue_at' => time(), 'need' => max(1, min(CONTENT_ADDED_PER_RUN, CONTENT_POOL_TARGET - pool_topic_count($key)))]);
    $last_progress = time();
    $batches = array_chunk($candidates, PRESCREEN_BATCH);
    foreach ($batches as $i => $batch) {
        $now = time();
        // Показываем прогресс примерно каждые 30 секунд
        if ($now - $last_progress >= 30) {
            $elapsed = round(($now - $started) / 60, 1);
            $percent = count($batches) > 0 ? (int)(($i + 1) / count($batches) * 100) : 0;
            $remaining_max = PARSER_MAX_SECONDS - ($now - $started);
            $remaining_min = (int)($remaining_max / 60);
            $remaining_sec = $remaining_max % 60;
            $remaining_text = $remaining_min > 0 ? "{$remaining_min}м {$remaining_sec}с" : "{$remaining_sec}с";
            cli_echo("⏳ Прогресс: {$percent}% ({$i}/" . count($batches) . " пачек). Прошло: {$elapsed} мин. Осталось до лимита: ≈ {$remaining_text}");
            $last_progress = $now;
        }
        if ($stop = collector_stop_reason($started, $analyzed, $added)) break;
        // Тема перечитывается для каждой пачки: смена темы на сайте действует сразу (фильтры сбор не затрагивают)
        $stop = current_topic() !== $topic ? 'Тема поиска изменилась на «' . current_topic() . '» — начинаю заново с новой темой.'
            : (search_topic_key() !== $key ? 'Вторая тема изменилась — собираю заново.' : null);
        if ($restart = $stop !== null) break;
        // Посты без картинки (текст, часть E) мимо быстрой оценки по картинке — сразу на полный анализ по тексту
        foreach (text_candidates_priority(array_values(array_filter($batch, fn($i) => ($i['image'] ?? '') === '')), $topic) as $text_item) $queue[] = ['item' => $text_item, 'poster' => null];
        $batch = array_values(array_filter($batch, fn($i) => ($i['image'] ?? '') !== ''));
        parser_phase_with_topic('быстрая оценка', $topic);
        $passed = $batch ? prescreen_batch($batch, $key, $ctx, $started + PARSER_MAX_SECONDS) : [];
        cache_merge($ctx['new_cache'], SCREENED_CACHE_LIMIT);
        $ctx['new_cache'] = [];
        parser_progress(['done' => $done += count($batch) - count($passed)]);   // отсеянные быстрой оценкой разобраны (текстовые — в полном анализе)
        // Прошедшие быструю оценку копятся и уходят на полный анализ блоком: каждая смена модели — перезагрузка в видеопамять
        array_push($queue, ...$passed);
        if (count($queue) < ANALYZE_BLOCK && $i < count($batches) - 1) continue;
        parser_phase_with_topic('полный анализ', $topic);
        foreach ($queue as $c) {
            if ($stop = collector_stop_reason($started, $analyzed, $added)) break 2;
            $analyzed++;
            if (analyze_candidate($c['item'], $c['poster'], $history = load_history(), $key)) $added++;
            parser_progress(['done' => ++$done, 'analyzed' => $analyzed, 'added' => $added]);
        }
        $queue = [];
    }
    cache_merge($ctx['new_cache'], SCREENED_CACHE_LIMIT);
    parser_progress(['done' => count($candidates)]);   // проверка окончена (в том числе по лимиту), дальше — только итоги и поиск источников
    if (!$restart) parser_expected_seconds(time() - $started);   // по ней оценивается время следующих проходов, пока нет своей скорости
    if ($stop) cli_echo($stop);
    $s = $ctx['stats'];
    $elapsed_min = round((time() - $started) / 60, 1);
    cli_echo('');
    cli_echo('╔════════════════════════════════════════════════════════════════════════╗');
    cli_echo('║ ✅ ПОДБОР КОНТЕНТА ЗАВЕРШЁН                                              ║');
    cli_echo('║ Тема: «' . str_pad($topic, 62) . ' ║');
    cli_echo('║ Время выполнения: ' . str_pad($elapsed_min . ' мин', 50) . ' ║');
    cli_echo('║ Добавлено в пул: ' . str_pad($added . ' постов', 52) . ' ║');
    cli_echo('║ Проанализировано: ' . str_pad($analyzed . ' кандидатов', 49) . ' ║');
    cli_echo('║ Отклонено: ' . str_pad($s['rejected'] . ' (по релевантности)', 50) . ' ║');
    cli_echo('║ Повторов пропущено: ' . str_pad($s['duplicates'] ?? 0, 48) . ' ║');
    cli_echo('╚════════════════════════════════════════════════════════════════════════╝');
    cli_echo('');
    // Поиск постов закончился, а пул темы не наполнен: ищем новые источники (не чаще DISCOVERY_SHORTAGE_INTERVAL) и, если нашлись, собираем заново
    if (!$restart && !$discovered && !$added && pool_topic_count($key) < CONTENT_POOL_TARGET && parser_session_alive() && current_topic() === $topic && !topic_same(DEFAULT_TOPIC, $topic)
        && time() - discovery_last_run($db = load_source_db(), $topic) >= DISCOVERY_SHORTAGE_INTERVAL) {
        cli_echo('Из источников темы нечего оценивать — ищу новые источники и соберу заново.');
        $before = topic_own_sources_count($db, $topic);
        run_source_discovery($topic);
        return topic_own_sources_count(load_source_db(), $topic) > $before;
    }
    if ($due && !$restart && parser_session_alive()) run_source_discovery($topic);
    return $restart;
}
