<?php
// Новостные сайты (RSS): дообогащение записей со страницы статьи, свежесть по сайту, дубли одной новости с разных сайтов
// и подпись «заголовок + анонс + ссылка на источник». Полный текст статьи в пост не попадает.

const RSS_ENRICH_PER_FEED = 10;         // страниц статей на одну ленту за проход
const NEWS_ENRICH_PER_PASS = 40;        // страниц статей за весь проход парсера
const NEWS_DUPLICATE_SHARE = 0.6;       // доля общих значимых слов, при которой новость — дубль
const NEWS_DUPLICATE_HOURS = 48;
const NEWS_KEY_WORDS = 6;
const NEWS_STOPWORDS = ['это', 'как', 'что', 'при', 'для', 'над', 'под', 'про', 'его', 'её', 'их', 'или', 'так', 'уже', 'еще', 'ещё', 'там', 'тут', 'the', 'and', 'for', 'with', 'from', 'that', 'this'];

/** Значимые слова заголовка по порядку: без знаков, чисел и служебных слов; не короче трёх букв. */
function news_story_words(string $title): array {
    $words = preg_split('/[^\p{L}]+/u', mb_strtolower($title), -1, PREG_SPLIT_NO_EMPTY) ?: [];
    return array_values(array_unique(array_filter($words, fn($w) => mb_strlen($w) >= 3 && !in_array($w, NEWS_STOPWORDS, true))));
}

/** Ключ новости: первые шесть значимых слов, отсортированные по алфавиту (для отладки и сравнения точных совпадений). */
function news_story_key(string $title): string {
    $words = array_slice(news_story_words($title), 0, NEWS_KEY_WORDS);
    sort($words);
    return implode(' ', $words);
}

/** Схожи ли две новости: доля общих слов (Жаккар) не меньше NEWS_DUPLICATE_SHARE. */
function news_similar(array $a, array $b): bool {
    // Сравниваем основы (пять букв): «ставка» и «ставку» — одно слово
    $a = array_unique(array_map(fn($w) => mb_substr($w, 0, 5), $a));
    $b = array_unique(array_map(fn($w) => mb_substr($w, 0, 5), $b));
    if (!$a || !$b) return false;
    $common = count(array_intersect($a, $b));
    return $common / count(array_unique([...$a, ...$b])) >= NEWS_DUPLICATE_SHARE;
}

/** Нужна ли странице статьи загрузка: нет картинки или анонс короче ARTICLE_SHORT_TEXT. */
function news_needs_article(array $post): bool {
    if (!empty($post['enriched'])) return false;   // уже дочитана: повторный запрос страницы не нужен
    return ($post['image'] ?? '') === '' || mb_strlen((string)($post['text'] ?? '')) < ARTICLE_SHORT_TEXT;
}

/** Запись старше допустимого: у сайта — свой max_age_hours, у остальных — CONTENT_MAX_AGE_DAYS. */
function news_post_outdated(array $post, array $entry): bool {
    if (empty($entry['max_age_hours'])) return is_post_outdated($post['posted_at'] ?? 0, null, content_max_age_days());
    $at = (int)($post['posted_at'] ?? 0);
    return $at > 0 && time() - $at > (int)$entry['max_age_hours'] * 3600;
}

/**
 * Записи ленты: самые свежие сначала дообогащаются (не больше RSS_ENRICH_PER_FEED на ленту и NEWS_ENRICH_PER_PASS на проход),
 * без картинки после этого отбрасываются. Записи новостного сайта получают метку и слова заголовка для дублей.
 */
function news_enrich_posts(array $posts, array $entry): array {
    usort($posts, fn($a, $b) => (int)($b['posted_at'] ?? 0) <=> (int)($a['posted_at'] ?? 0));
    $budget = RSS_ENRICH_PER_FEED;
    $out = [];
    foreach ($posts as $post) {
        if (article_image_is_stub((string)($post['image'] ?? ''))) {   // обложка-заглушка с заголовком — не картинка
            $post['image'] = '';
            $post['images'] = [];
        }
        if (news_needs_article($post) && $budget > 0 && ($GLOBALS['news_enrich_left'] ?? NEWS_ENRICH_PER_PASS) > 0) {
            $budget--;
            $GLOBALS['news_enrich_left'] = ($GLOBALS['news_enrich_left'] ?? NEWS_ENRICH_PER_PASS) - 1;
            $post = article_enrich($post);
        }
        // Конвейер работает с картинками. Статья без настоящей обложки — текстовая, если тема допускает текстовые посты
        if (($post['image'] ?? '') === '' && !(!empty($post['article']) && topics_accept_text($entry['topics'] ?? []))) continue;
        if (!empty($entry['news'])) {
            $post['news'] = true;
            $post['news_site'] = (string)parse_url((string)($post['url'] ?? $entry['id'] ?? ''), PHP_URL_HOST);
            $post['news_words'] = array_slice(news_story_words(trim((string)strtok((string)$post['text'], "\n"))), 0, NEWS_KEY_WORDS);   // ключевые слова заголовка
        }
        $out[] = $post;
    }
    return $out;
}

/**
 * Подпись новости: заголовок, выжимка (анонс, не больше трёх предложений) и строка «Источник: сайт» со ссылкой.
 * Ссылку на источник не удаляем: она добавляется здесь же.
 */
function news_caption(array $post): string {
    $lines = array_values(array_filter(array_map('trim', explode("\n", (string)($post['text'] ?? ''))), fn($l) => $l !== '' && $l !== EMPTY_POST_TEXT));
    $title = $lines[0] ?? '';
    $excerpt = mb_substr(trim(implode(' ', array_slice($lines, 1))), 0, 400);
    $sentences = preg_split('/(?<=[.!?])\s+/u', $excerpt, 4) ?: [];
    $excerpt = implode(' ', array_slice($sentences, 0, 3));
    $site = (string)($post['news_site'] ?? parse_url((string)($post['url'] ?? ''), PHP_URL_HOST));
    $url = (string)($post['url'] ?? '');
    return trim(($title !== '' ? "**{$title}**\n" : '') . ($excerpt !== '' ? "{$excerpt}\n" : '') . "Источник: {$site}" . ($url !== '' ? " — {$url}" : ''));
}

/** Ссылка на источник есть в тексте поста. */
function news_has_source_link(string $text, string $url): bool {
    return $url !== '' && str_contains($text, $url);
}

/** Выжимка новости по запросу пользователя: 2–3 предложения своими словами по тексту статьи. Полный текст не возвращается. */
function news_excerpt(string $url, $uid = null): string {
    if (!str_starts_with($url, 'https://') || filter_var($url, FILTER_VALIDATE_URL) === false) throw new RuntimeException('Нужна ссылка на статью https://.');
    $uid = $uid ?: app_user_id();
    $article = article_enrich(['url' => $url]);
    $body = trim((string)($article['article_text'] ?? ''));
    if ($body === '') throw new RuntimeException('Не удалось прочитать статью, выжимка не сделана. Напишите текст вручную.');
    caption_quota_take($uid);
    $prompt = "Напиши выжимку новости из текста ниже: 2–3 коротких предложения по-русски, своими словами, без цитат и без вступления."
        . " Не копируй фразы из текста. Ответь только выжимкой.\n\nТекст статьи:\n" . mb_substr($body, 0, 4000);
    $text = trim(ai_generate_local(smart_model(), $prompt, [], null, ['temperature' => 0.3, 'num_predict' => 240], false));
    $text = trim($text, " \t\n\r\"«»");
    if ($text === '') throw new RuntimeException('Модель не написала выжимку: попробуйте ещё раз.');
    return mb_substr($text, 0, 400);
}

/**
 * Перечитать одну запись новости со страницы (мимо кэша): текст, картинка, статья, счётчики.
 * Возвращает запись и признак, что статья получена (true) — для статистики articles-refresh.
 */
function news_refresh_item(array $item): array {
    $fresh = article_enrich($item, true);
    $fresh['enriched'] = true;
    return $fresh;
}

/**
 * Старые записи новостей (пул и история) без статьи — перечитать. Не больше $limit страниц за запуск.
 * Возвращает: updated — сколько записей получили статью, text — сколько из них стали текстовыми (без картинки).
 */
function articles_refresh_user(int $limit): array {
    $stats = ['updated' => 0, 'text' => 0, 'left' => $limit];
    $due = fn(array $i) => !empty($i['news']) && empty($i['article']) && str_starts_with((string)($i['url'] ?? ''), 'https://');
    $refresh = function (array $i) use (&$stats, $due) {
        if (!$due($i) || $stats['left'] <= 0) return $i;
        $stats['left']--;
        $fresh = news_refresh_item($i);
        if (!empty($fresh['article'])) {
            $stats['updated']++;
            if (($fresh['image'] ?? '') === '') $stats['text']++;
        }
        return $fresh;
    };
    // Сетевые запросы — вне транзакции: запись в базу короткая, в ней уже готовые данные
    $pool = pool_load();
    $fresh_pool = array_map(fn($i) => is_array($i) ? $refresh($i) : $i, $pool);
    if ($fresh_pool !== $pool) pool_update(fn() => $fresh_pool);
    $history = history_load();
    $fresh_history = $history;
    foreach (HISTORY_COLLECTIONS as $c) $fresh_history[$c] = array_map(fn($i) => is_array($i) ? $refresh($i) : $i, (array)($history[$c] ?? []));
    if ($fresh_history !== $history) history_update(fn() => $fresh_history);
    return $stats;
}
