<?php
// Календарь v4, «Заполнить дыры»: план из «Предложки» на свободные слоты расписания и постановка плана в очередь.
// План — чистая функция (calendar_plan_pick); применение ставит посты той же подготовкой и очередью, что обычная публикация.

const FILL_MAX_POSTS = 21;          // за один раз
const FILL_MAX_VIDEO_RUN = 2;       // не больше стольких видео подряд в одном канале

/**
 * Свободные слоты расписания канала в [from, to) по времени (не раньше now+минута), без занятых очередью ±60 с.
 * Возвращает список unix-времён по возрастанию.
 */
function calendar_free_slots(array $channel, array $queue, int $from, int $to, int $now): array {
    $schedule = channel_schedule($channel);
    $tz = new DateTimeZone($schedule['tz']);
    $taken = array_map(fn($p) => (int)($p['publish_at'] ?? 0), array_filter($queue, fn($p) => ($p['channel'] ?? '') === $channel['id']));
    $earliest = max($from, $now + MIN_SECONDS_AHEAD);
    $out = [];
    for ($day = (new DateTime('@' . $from))->setTimezone($tz)->setTime(0, 0); $day->getTimestamp() < $to; $day->modify('+1 day')) {
        foreach ($schedule['slots'][(int)$day->format('N')] ?? [] as $hhmm) {
            [$h, $m] = array_map('intval', explode(':', $hhmm));
            $at = (clone $day)->setTime($h, $m)->getTimestamp();
            if ($at < $earliest || $at >= $to) continue;
            if (array_filter($taken, fn($t) => abs($t - $at) <= 60)) continue;
            $out[] = $at;
        }
    }
    sort($out);
    return $out;
}

/** Минимум вперёд для поста из плана (как у публикации в очередь). */
const MIN_SECONDS_AHEAD = 60;

/**
 * Подбор постов на слоты: слоты уже в порядке «сначала лучшие часы»; для каждого — лучший по relevance пост,
 * без повтора в этом канале (history: опубликованное и очередь), без одинаковых картинок в плане, без трёх видео подряд.
 * slots: [{at, channel: [...], best: bool}]; candidates: записи Предложки; возвращает [{slot_at, channel, image, text, relevance, format}].
 */
function calendar_plan_pick(array $slots, array $candidates, array $history, int $limit = FILL_MAX_POSTS): array {
    usort($candidates, fn($a, $b) => ($b['relevance'] ?? 0) <=> ($a['relevance'] ?? 0));
    $used = [];
    $plan = [];
    $video_run = [];   // канал => подряд видео
    foreach ($slots as $slot) {
        if (count($plan) >= $limit) break;
        $channel = $slot['channel'];
        foreach ($candidates as $i => $cand) {
            $image = (string)($cand['image'] ?? $cand['display_image'] ?? '');
            if ($image === '' || isset($used[$image])) continue;
            $is_video = ($cand['format'] ?? '') === 'video';
            if ($is_video && ($video_run[$channel['id']] ?? 0) >= FILL_MAX_VIDEO_RUN) continue;
            if (calendar_duplicate_of($cand, (string)$channel['id'], $history) !== null) continue;
            $used[$image] = true;
            $video_run[$channel['id']] = $is_video ? ($video_run[$channel['id']] ?? 0) + 1 : 0;
            $plan[] = ['slot_at' => (int)$slot['at'], 'channel' => (string)$channel['id'], 'channel_title' => (string)($channel['title'] ?? ''),
                'image' => $image, 'display' => (string)($cand['display_image'] ?? $image), 'text' => post_caption_clean($cand['text'] ?? ''),
                'relevance' => (int)($cand['relevance'] ?? 0), 'format' => (string)($cand['format'] ?? ''),
                'best' => (bool)$slot['best']];
            unset($candidates[$i]);
            break;
        }
    }
    return $plan;
}

/**
 * План для календаря: свободные слоты каналов в [from, to), лучшие часы канала первыми; при $include_rest — и остальные.
 * Предложка берётся как для календаря (api('proposals')), история — опубликованное и очередь.
 */
function calendar_fill_plan($uid, array $channels, int $from, int $to, bool $include_rest, array $pool, array $queue, int $now): array {
    $history = calendar_history($uid, $queue);
    $best = [];
    $slots = [];
    foreach ($channels as $channel) {
        $reach = channel_reach_cached($channel);
        $best_hours = $reach['best'] ?? [];
        $tz = new DateTimeZone(channel_schedule($channel)['tz']);
        foreach (calendar_free_slots($channel, $queue, $from, $to, $now) as $at) {
            $hour = (int)(new DateTime('@' . $at))->setTimezone($tz)->format('G');
            $is_best = in_array($hour, $best_hours, true);
            if ($is_best || $include_rest) $slots[] = ['at' => $at, 'channel' => $channel, 'best' => $is_best];
        }
    }
    usort($slots, fn($a, $b) => [!$b['best'], $a['at']] <=> [!$a['best'], $b['at']]);   // лучшие часы первыми, по времени
    return calendar_plan_pick($slots, $pool, $history);
}

/** История для повторов: опубликованное за REPEAT_DAYS и посты очереди (их картинки — по адресам источников). */
function calendar_history($uid, array $queue): array {
    $queued = array_map(fn($p) => [
        'channel' => (string)($p['channel'] ?? ''), 'text' => (string)($p['text'] ?? ''), 'at' => (int)($p['publish_at'] ?? 0), 'url' => '',
        'media_src' => media_sources($p['media'] ?? []),
    ], $queue);
    return [...$queued, ...calendar_published_recent($uid)];
}

/** Своя копия файла записи, если она есть: ссылки Telegram CDN живут недолго, по ним поздно скачивать. Иначе исходный адрес. */
function calendar_fill_media_src(string $image, array $pool, array $history): string {
    return media_cache_existing(calendar_fill_record($image, $pool, $history)['image_local'] ?? '') ?: $image;
}

/** Запись пула или «Предложки» по картинке (для медиа из кэша и хэштегов); null — не нашлась. */
function calendar_fill_record(string $image, array $pool, array $history): ?array {
    foreach ([$pool, ...array_map(fn($c) => $history[$c] ?? [], PROPOSAL_COLLECTIONS)] as $records) {
        foreach ($records as $r) if (is_array($r) && content_image_key($r) === $image) return $r;
    }
    return null;
}

/**
 * Применение плана: каждый пост — той же подготовкой медиа и очередью, что обычная публикация (мимо публикатора сразу).
 * items: [{slot_at, channels: [id…] (или channel), image, text}]. Пост на несколько ресурсов — одна группа (общий group).
 * Возвращает ['posted' => [id…], 'errors' => [['slot_at', 'channel', 'error']…]]: частичный успех — не ошибка всего запроса.
 */
function calendar_fill_apply($uid, array $items): array {
    if (count($items) > FILL_MAX_POSTS) throw new RuntimeException('За раз ставится не больше ' . FILL_MAX_POSTS . ' постов.');
    $result = ['posted' => [], 'errors' => []];
    [$pool, $history] = [pool_load(), history_load()];
    foreach ($items as $item) {
        $at = (int)($item['slot_at'] ?? 0);
        $group = bin2hex(random_bytes(6));
        $channels = array_values(array_unique(array_map('strval', (array)($item['channels'] ?? [$item['channel'] ?? '']))));
        foreach ($channels as $channel_id) {
            $work = null;
            try {
                if ($at < time() + MIN_SECONDS_AHEAD) throw new RuntimeException('Слот уже прошёл — выберите другой.');
                $target = publish_channel_resolve($channel_id, $uid);
                if (isset($target['error'])) throw new RuntimeException($target['error']);
                $bot = $target['channel'];
                $record = calendar_fill_record((string)($item['image'] ?? ''), $pool, $history);
                $text = text_with_hashtags(trim((string)($item['text'] ?? '')), $record ? channel_hashtags($bot, post_hashtags($record)) : [], TELEGRAM_CAPTION_LIMIT);
                if ($denied = billing_post_denied(true, $uid)) throw new RuntimeException($denied);
                if ($denied = publish_media_denied([['src' => (string)($item['image'] ?? '')]])) throw new RuntimeException($denied);
                telegram_check_text($text, 1);
                $work = telegram_work_dir();
                $prepared = telegram_prepare_media([['src' => calendar_fill_media_src((string)($item['image'] ?? ''), $pool, $history), 'name' => 'фото']], [], $work, 'bot');
                $response = outbox_queue_post($uid, $bot, $text, $prepared, $at, $at, $work, null, null, $group);
                $work = null;   // файлы уже в папке поста
                billing_post_used($uid);
                $result['posted'][] = $response['id'] ?? '';
            } catch (RuntimeException $e) {
                $result['errors'][] = ['slot_at' => $at, 'channel' => $channel_id, 'error' => $e->getMessage()];
            } finally {
                if ($work !== null) remove_dir($work);
            }
        }
    }
    return $result;
}
