<?php
// Автоперевод текста постов на русский через MyMemory (Google Translate с этого сервера блокируется капчей).
// Кэш общий (kv), квота сервиса учитывается до конца суток UTC. Сетевой вызов подменяется для тестов: $GLOBALS['translate_transport'].

const TRANSLATE_MAX_CHARS = 2000;        // больше не переводим: остаток остаётся оригиналом
const TRANSLATE_CHUNK_CHARS = 450;       // кусок на один запрос к сервису
const TRANSLATE_TIMEOUT_SECONDS = 4;     // тайм-аут одного запроса
const TRANSLATE_RETRY_SECONDS = 3600;    // сетевую неудачу повторяем не раньше чем через час

/** Грубое определение языка без сети: 'ru' — переводить нечего; 'uk'/'be' — переводим; '?' — определит сервис; null — букв мало. */
function text_language_guess(string $text): ?string {
    $clean = preg_replace(['~https?://\S+~u', '~[@#]\S+~u'], ' ', $text);
    $letters = preg_match_all('/\p{L}/u', $clean);
    if ($letters < 12) return null;
    $cyrillic = preg_match_all('/\p{Cyrillic}/u', $clean);
    if ($cyrillic / $letters < 0.6) return '?';
    if (preg_match('/[іїєґІЇЄҐ]/u', $clean)) return 'uk';
    if (preg_match('/[ўЎ]/u', $clean)) return 'be';
    return 'ru';
}

/** Название языка в родительном падеже для пометки «Переведено с …». */
function language_name_genitive(string $code): string {
    $names = ['en' => 'английского', 'de' => 'немецкого', 'uk' => 'украинского', 'be' => 'белорусского', 'es' => 'испанского', 'fr' => 'французского',
        'it' => 'итальянского', 'pt' => 'португальского', 'pl' => 'польского', 'tr' => 'турецкого', 'ja' => 'японского', 'zh' => 'китайского',
        'ko' => 'корейского', 'ar' => 'арабского', 'nl' => 'нидерландского', 'sv' => 'шведского', 'cs' => 'чешского', 'sr' => 'сербского',
        'bg' => 'болгарского', 'ka' => 'грузинского', 'kk' => 'казахского', 'fi' => 'финского', 'ro' => 'румынского', 'hu' => 'венгерского',
        'el' => 'греческого', 'he' => 'иврита', 'hi' => 'хинди', 'th' => 'тайского', 'vi' => 'вьетнамского', 'id' => 'индонезийского'];
    return $names[strtolower($code)] ?? 'другого языка';
}

/** Сетевой вызов по умолчанию: GET с тайм-аутом; возвращает ['status', 'body']. */
function translate_http_get(string $url): array {
    $ch = curl_init($url);
    curl_setopt_array($ch, [CURLOPT_RETURNTRANSFER => true, CURLOPT_TIMEOUT => TRANSLATE_TIMEOUT_SECONDS,
        CURLOPT_CONNECTTIMEOUT => TRANSLATE_TIMEOUT_SECONDS, CURLOPT_USERAGENT => HTTP_UA]);
    $body = curl_exec($ch);
    $status = (int)curl_getinfo($ch, CURLINFO_HTTP_CODE);
    curl_close($ch);
    return ['status' => $status, 'body' => is_string($body) ? $body : ''];
}

function translate_transport(): callable {
    return $GLOBALS['translate_transport'] ?? 'translate_http_get';
}

/** Журнал перевода: одна строка в консоли CLI и в лог PHP. */
function translate_log(string $line): void {
    cli_echo('-> Перевод: ' . $line);
    error_log('Перевод: ' . $line);
}

/** Конец суток UTC — до него квота сервиса считается исчерпанной. */
function translate_quota_until(): int {
    return strtotime(gmdate('Y-m-d', time() + 86400) . ' 00:00:00 UTC');
}

/** Ссылки, @упоминания и #хэштеги заменяются плейсхолдерами, чтобы сервис их не портил; возвращает [текст, [метка => оригинал]]. */
function translate_protect(string $text): array {
    $map = [];
    $protected = preg_replace_callback('~https?://\S+|@\w+|#\w+~u', function ($m) use (&$map) {
        $token = 'ZXQ' . count($map) . 'ZXQ';
        $map[$token] = $m[0];
        return $token;
    }, $text);
    return [$protected, $map];
}

/** Куски не длиннее $max: по абзацам, длинный абзац — по предложениям, длинное предложение — по словам. Возвращает [[кусок, перед_ним], …]. */
function translate_chunks(string $text, int $max = TRANSLATE_CHUNK_CHARS): array {
    $chunks = [];
    $paragraphs = array_values(array_filter(array_map('trim', preg_split('/\n\s*\n/u', trim($text))), 'strlen'));
    foreach ($paragraphs as $paragraph) {
        $join = $chunks ? "\n\n" : '';
        $buf = null;
        foreach (preg_split('/(?<=[.!?…])\s+/u', $paragraph, -1, PREG_SPLIT_NO_EMPTY) as $sentence) {
            foreach (translate_split_words($sentence, $max) as $piece) {
                if ($buf !== null && mb_strlen($buf . ' ' . $piece) <= $max) { $buf .= ' ' . $piece; continue; }
                if ($buf !== null) { $chunks[] = [$buf, $join]; $join = ' '; }
                $buf = $piece;
            }
        }
        if ($buf !== null) $chunks[] = [$buf, $join];
    }
    return $chunks;
}

/** Предложение, которое длиннее $max, режется по словам (слово целиком не рвётся, если помещается). */
function translate_split_words(string $sentence, int $max): array {
    if (mb_strlen($sentence) <= $max) return [$sentence];
    $pieces = [];
    $buf = '';
    foreach (preg_split('/\s+/u', $sentence, -1, PREG_SPLIT_NO_EMPTY) as $word) {
        if ($buf !== '' && mb_strlen($buf . ' ' . $word) > $max) { $pieces[] = $buf; $buf = ''; }
        $buf = $buf === '' ? $word : $buf . ' ' . $word;
    }
    if ($buf !== '') $pieces[] = $buf;
    return $pieces;
}

/** Нормализация для кэша и для сравнения «перевод совпал с исходником». */
function translate_normalize(string $text): string {
    return mb_strtolower(preg_replace('/\s+/u', ' ', trim($text)));
}

/**
 * Перевод на русский. Возвращает ['text', 'lang'] или null: русский текст, квота, сетевая ошибка, превышен бюджет, перевод не
 * отличается от исходника. Кэш общий: и успехи, и «не переводится» запоминаются.
 */
function translate_to_russian(string $text, float $budget = 6.0): ?array {
    $text = mb_substr(trim($text), 0, TRANSLATE_MAX_CHARS);
    $guess = text_language_guess($text);
    if ($text === '' || $guess === null || $guess === 'ru') return null;
    if ($guess === 'uk' && ua_filter_on()) return null;   // украинский не переводим и не пускаем дальше (UA_FILTER_PROMPT.md, B10)
    $key = 'translate:' . md5(translate_normalize($text));
    $hit = kv_get($key);
    if (is_array($hit)) {
        if (!empty($hit['ok'])) return ['text' => $hit['text'], 'lang' => $hit['lang']];
        if (($hit['retry_at'] ?? 0) > time()) return null;   // неудача: не повторяем
    }
    if ((int)kv_get('translate_quota_until', 0) > time()) {
        translate_log('квота исчерпана до ' . gmdate('d.m H:i', (int)kv_get('translate_quota_until')) . ' UTC, сеть не используется');
        return null;
    }
    [$protected, $map] = translate_protect($text);
    $started = microtime(true);
    $parts = [];
    $lang = $guess === '?' ? null : $guess;
    $transport = translate_transport();
    foreach (translate_chunks($protected) as [$chunk, $join]) {
        if (microtime(true) - $started > $budget) {
            translate_log('не уложились в ' . $budget . ' с, показываем оригинал');
            return null;
        }
        $query = ['q' => $chunk, 'langpair' => 'autodetect|ru'] + (TRANSLATE_EMAIL !== '' ? ['de' => TRANSLATE_EMAIL] : []);
        $response = $transport('https://api.mymemory.translated.net/get?' . http_build_query($query));
        $data = json_decode((string)($response['body'] ?? ''), true);
        $status = (int)($data['responseStatus'] ?? $response['status'] ?? 0);
        if (!empty($data['quotaFinished']) || in_array($status, [403, 429], true)) {
            kv_update('translate_quota_until', fn() => translate_quota_until());
            translate_log('квота сервиса исчерпана до ' . gmdate('d.m H:i', translate_quota_until()) . ' UTC');
            return null;
        }
        $translated = $data['responseData']['translatedText'] ?? '';
        if ($status !== 200 || !is_string($translated) || trim($translated) === '') {
            kv_update($key, fn() => ['retry_at' => time() + TRANSLATE_RETRY_SECONDS]);
            translate_log("сеть: ответ {$status}, повтор через час");
            return null;
        }
        $detected = strtolower((string)($data['responseData']['detectedLanguage'] ?? ''));
        if ($lang === null && $detected !== '') $lang = $detected;
        if ($detected === 'ru') {   // сервис сказал «русский» — это не перевод
            kv_update($key, fn() => ['retry_at' => PHP_INT_MAX]);
            translate_log('текст уже по-русски (определил сервис)');
            return null;
        }
        $parts[] = $join . $translated;
    }
    $out = implode('', $parts);
    foreach ($map as $token => $original) {
        if (!str_contains($out, $token)) {   // ссылка или упоминание потерялись — не показываем искажённый текст
            kv_update($key, fn() => ['retry_at' => PHP_INT_MAX]);
            translate_log('сервис испортил ссылку или упоминание, показываем оригинал');
            return null;
        }
        $out = str_replace($token, $original, $out);
    }
    if (translate_normalize($out) === translate_normalize($text)) {
        kv_update($key, fn() => ['retry_at' => PHP_INT_MAX]);
        return null;
    }
    $lang = $lang ?: $guess;
    kv_update($key, fn() => ['ok' => true, 'text' => $out, 'lang' => $lang]);
    translate_log(sprintf('%s → ru, %d симв., %.1f с, сеть', $lang, mb_strlen($text), microtime(true) - $started));
    return ['text' => $out, 'lang' => $lang];
}

/** Показ записи: текст переводится, если галочка включена; оригинал — в text_original. Пул и история не меняются. */
function translate_display(array $response): array {
    if (!load_settings()['auto_translate']) return $response;
    $text = trim((string)($response['text'] ?? ''));
    if ($text === '' || $text === EMPTY_POST_TEXT) return $response;
    try {
        $translated = translate_to_russian($text, 6.0);
    } catch (Throwable $e) {
        translate_log('ошибка перевода: ' . $e->getMessage());
        return $response;
    }
    if ($translated === null) return $response;
    $response['text_original'] = $response['text'];
    $response['text'] = $translated['text'];
    if (!empty($response['captions'])) $response['captions'][0] = $translated['text'];
    $response['translated_from'] = $translated['lang'];
    $response['translated_note'] = 'Переведено с ' . language_name_genitive($translated['lang']);
    return $response;
}

/** Прогрев кэша перевода сразу после добавления записи в пул (результат в запись не пишем; ошибки не роняют парсер). */
function translate_warm_cache(array $item): void {
    try {
        if (load_settings()['auto_translate'] && trim((string)($item['text'] ?? '')) !== '') translate_to_russian((string)$item['text'], 4.0);
    } catch (Throwable $e) {
        translate_log('прогрев не удался: ' . $e->getMessage());
    }
}
