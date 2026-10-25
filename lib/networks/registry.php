<?php
// text_limit: [с файлами (подпись), без файлов (текст)]. Реестр соцсетей для автопостинга (AUTOPOST_NETWORKS_PROMPT.md, часть A). Соцсеть: подпись, префикс цели в редакторе,
// лимиты, статус и, если публикация есть, функция publish(array $channel, array $post): array → ['url', 'message_ids'].
// Статусы: ready — публикует; planned — адаптер ещё не написан; needs_keys — нужен ключ приложения владельца;
// unavailable — открытого API публикации нет (пояснение в интерфейсе).

/** Все соцсети по ключу платформы. Публикация подключается по мере готовности адаптеров. */
function networks_registry(): array {
    static $registry = null;
    if ($registry !== null) return $registry;
    $planned = fn($title, $prefix, $note = '') => ['title' => $title, 'prefix' => $prefix, 'status' => 'planned', 'note' => $note];
    $keys = fn($title, $prefix, $note) => ['title' => $title, 'prefix' => $prefix, 'status' => 'needs_keys', 'note' => $note];
    $unavailable = fn($title, $note) => ['title' => $title, 'prefix' => '', 'status' => 'unavailable', 'note' => $note];
    $registry = [
        'telegram' => ['title' => 'Telegram', 'prefix' => 'tg', 'status' => 'ready', 'note' => '', 'text_limit' => [1024, 4096], 'media_max' => 10,
            'kinds' => ['photo', 'video', 'animation'], 'poll' => true, 'publish' => 'telegram_network_publish'],
        'vk' => ['title' => 'ВКонтакте (группа)', 'prefix' => 'vk', 'status' => 'beta', 'note' => 'Токен администратора с правами wall, photos, groups; API не сверен с документацией (часть B1).',
            'text_limit' => [16384, 16384], 'media_max' => 10, 'kinds' => ['photo'], 'max_bytes' => ['photo' => 50000000], 'docs' => 'unverified',
            'fields' => [['name' => 'token', 'label' => 'Токен администратора (wall, photos, groups, offline)', 'secret' => true], ['name' => 'group', 'label' => 'Адрес или id сообщества']],
            'connect' => 'vk_connect', 'publish' => 'vk_publish'],
        'max' => ['title' => 'MAX', 'prefix' => 'max', 'status' => 'beta', 'note' => 'Бот MAX в чате, токен из «MAX для бизнеса»; пока только текст (часть B2).',
            'text_limit' => [4000, 4000], 'media_max' => 0, 'kinds' => [], 'docs' => 'partly', 'connect' => 'max_connect', 'publish' => 'max_publish',
            'fields' => [['name' => 'token', 'label' => 'Токен бота (MAX для бизнеса)', 'secret' => true], ['name' => 'chat_id', 'label' => 'id чата, где бот админ']]],
        'bluesky' => ['title' => 'Bluesky', 'prefix' => 'bsky', 'status' => 'beta', 'note' => 'Пароль приложения Bluesky (часть B3).',
            'text_limit' => [300, 300], 'media_max' => 4, 'kinds' => ['photo'], 'max_bytes' => ['photo' => 20000000], 'docs' => 'unverified',
            'fields' => [['name' => 'identifier', 'label' => 'Ник (handle), например name.bsky.social'], ['name' => 'password', 'label' => 'Пароль приложения (не основной)', 'secret' => true]],
            'connect' => 'bluesky_connect', 'publish' => 'bluesky_publish'],
        'mastodon' => ['title' => 'Mastodon', 'prefix' => 'mastodon', 'status' => 'ready', 'note' => 'Токен доступа (write:statuses, write:media) и адрес сервера (часть B4).',
            'text_limit' => [500, 500], 'media_max' => 4, 'kinds' => ['photo', 'animation', 'video'], 'max_bytes' => ['photo' => 16000000, 'video' => 99000000], 'docs' => 'ok',
            'fields' => [['name' => 'instance', 'label' => 'Сервер, например mastodon.social'], ['name' => 'token', 'label' => 'Токен доступа (write:statuses, write:media)', 'secret' => true]],
            'connect' => 'mastodon_connect', 'publish' => 'mastodon_publish'],
        'discord' => ['title' => 'Discord', 'prefix' => 'discord', 'status' => 'ready', 'note' => 'Вебхук канала (часть B5).',
            'text_limit' => [2000, 2000], 'media_max' => 10, 'kinds' => ['photo', 'animation', 'video'], 'max_bytes' => ['photo' => 10000000, 'animation' => 10000000, 'video' => 10000000], 'docs' => 'ok',
            'fields' => [['name' => 'webhook', 'label' => 'Адрес вебхука канала', 'secret' => true]],
            'connect' => 'discord_connect', 'publish' => 'discord_publish'],
        'dzen' => ['title' => 'Дзен', 'prefix' => '', 'status' => 'manual', 'note' => 'Публичного API публикации нет. Посты Telegram-канала выходят в Дзене сами: Дзен-студия → настройки канала → код для Telegram → бот @zen_sync_bot админом в канал → /sync. Шаги не сверены со справкой Дзена.'],
        'ok' => ['title' => 'Одноклассники (группа)', 'prefix' => 'ok', 'status' => 'needs_keys', 'note' => 'Ключ приложения OK и постоянный токен группы; только текст (часть C).',
            'text_limit' => [4000, 4000], 'media_max' => 0, 'kinds' => [], 'docs' => 'partly', 'app_keys' => ['OK_APP_KEY'], 'connect' => 'ok_connect', 'publish' => 'ok_publish',
            'fields' => [['name' => 'access_token', 'label' => 'Постоянный токен приложения', 'secret' => true], ['name' => 'session_secret_key', 'label' => 'Секретный ключ сессии', 'secret' => true], ['name' => 'gid', 'label' => 'id группы']]],
        'youtube' => ['title' => 'YouTube Shorts', 'prefix' => 'yt', 'status' => 'needs_keys', 'note' => 'Проект Google и экран согласия; без аудита ролики приватные. Только видео (часть C).',
            'text_limit' => [5000, 5000], 'media_max' => 1, 'kinds' => ['video'], 'max_bytes' => ['video' => 200000000], 'docs' => 'ok', 'app_keys' => ['YOUTUBE_CLIENT_ID', 'YOUTUBE_CLIENT_SECRET'],
            'oauth' => ['authorize' => 'youtube_oauth_authorize', 'exchange' => 'youtube_oauth_exchange', 'identify' => 'youtube_oauth_identify', 'refresh' => 'youtube_oauth_refresh'], 'publish' => 'youtube_publish'],
        'pinterest' => ['title' => 'Pinterest', 'prefix' => 'pin', 'status' => 'needs_keys', 'note' => 'Приложение Pinterest; пин всегда с картинкой (часть C).',
            'text_limit' => [800, 800], 'media_max' => 1, 'kinds' => ['photo'], 'max_bytes' => ['photo' => 20000000], 'docs' => 'ok', 'app_keys' => ['PINTEREST_CLIENT_ID', 'PINTEREST_CLIENT_SECRET'],
            'oauth' => ['authorize' => 'pinterest_oauth_authorize', 'exchange' => 'pinterest_oauth_exchange', 'identify' => 'pinterest_oauth_identify', 'refresh' => 'pinterest_oauth_refresh'], 'publish' => 'pinterest_publish'],
        'tumblr' => ['title' => 'Tumblr', 'prefix' => 'tumblr', 'status' => 'needs_keys', 'note' => 'Приложение Tumblr, OAuth 2; текстовые посты (часть C).',
            'text_limit' => [4096, 4096], 'media_max' => 0, 'kinds' => [], 'docs' => 'ok', 'app_keys' => ['TUMBLR_CLIENT_ID', 'TUMBLR_CLIENT_SECRET'],
            'oauth' => ['authorize' => 'tumblr_oauth_authorize', 'exchange' => 'tumblr_oauth_exchange', 'identify' => 'tumblr_oauth_identify', 'refresh' => 'tumblr_oauth_refresh'], 'publish' => 'tumblr_publish'],
        'linkedin' => ['title' => 'LinkedIn (профиль)', 'prefix' => 'li', 'status' => 'needs_keys', 'note' => 'Приложение LinkedIn с продуктом «Share on LinkedIn»; токен на 60 дней, без обновления (часть C).',
            'text_limit' => [3000, 3000], 'media_max' => 0, 'kinds' => [], 'docs' => 'partly', 'app_keys' => ['LINKEDIN_CLIENT_ID', 'LINKEDIN_CLIENT_SECRET'],
            'oauth' => ['authorize' => 'linkedin_oauth_authorize', 'exchange' => 'linkedin_oauth_exchange', 'identify' => 'linkedin_oauth_identify'], 'publish' => 'linkedin_publish'],
        'threads' => $keys('Threads', 'threads', 'Приложение Meta; картинка по публичной ссылке (часть C).'),
        'instagram' => $keys('Instagram', 'ig', 'Meta (Instagram, Facebook) признана экстремистской и запрещена в РФ. Приложение Meta, профессиональный аккаунт (часть C).'),
        'facebook' => $keys('Facebook (страница)', 'fb', 'Meta (Instagram, Facebook) признана экстремистской и запрещена в РФ. Приложение Meta (часть C).'),
        'x' => ['title' => 'X (Twitter)', 'prefix' => 'x', 'status' => 'needs_keys', 'note' => 'Платный доступ к API: бесплатного тарифа нет, посты с URL дороже (часть C).',
            'text_limit' => [280, 280], 'media_max' => 0, 'kinds' => [], 'docs' => 'partly', 'app_keys' => ['X_CLIENT_ID', 'X_CLIENT_SECRET'],
            'oauth' => ['authorize' => 'x_oauth_authorize', 'exchange' => 'x_oauth_exchange', 'identify' => 'x_oauth_identify', 'refresh' => 'x_oauth_refresh', 'pkce' => true], 'publish' => 'x_publish'],
        'reddit' => ['title' => 'Reddit', 'prefix' => 'reddit', 'status' => 'needs_keys', 'note' => 'Приложение Reddit; постим только в свой профиль, текстом (часть C).',
            'text_limit' => [40000, 40000], 'media_max' => 0, 'kinds' => [], 'docs' => 'partly', 'app_keys' => ['REDDIT_CLIENT_ID', 'REDDIT_CLIENT_SECRET'],
            'oauth' => ['authorize' => 'reddit_oauth_authorize', 'exchange' => 'reddit_oauth_exchange', 'identify' => 'reddit_oauth_identify', 'refresh' => 'reddit_oauth_refresh'], 'publish' => 'reddit_publish'],
        'tiktok' => $unavailable('TikTok', 'Публикация доступна только после аудита приложения TikTok: без проверки видно «только мне».'),
        'rutube' => $unavailable('Rutube', 'Открытого API публикации нет.'),
        'vk_clips' => $unavailable('ВК Клипы', 'Отдельной ленты с открытым API публикации нет.'),
        'whatsapp' => $unavailable('WhatsApp-каналы', 'Открытого API публикации в каналы нет.'),
        'viber' => $unavailable('Viber', 'Открытого API публикации нет.'),
        'likee' => $unavailable('Likee', 'Открытого API публикации нет.'),
        'yappy' => $unavailable('Яппи', 'Открытого API публикации нет.'),
    ];
    // Сеть с ключами приложения становится «бета», когда ключи заданы в config.local.php
    foreach ($registry as $key => $network) {
        if (!empty($network['app_keys'])) $registry[$key]['status'] = network_keys_present($network['app_keys']) ? 'beta' : 'needs_keys';
    }
    return $registry;
}

/** Адаптер публикации для платформы канала; соцсеть без публикации — исключение с понятным текстом. */
function network_for(string $platform): array {
    $network = networks_registry()[$platform] ?? null;
    if ($network === null) throw new RuntimeException('Неизвестная соцсеть: ' . $platform . '.');
    if (empty($network['publish']) || !function_exists($network['publish'])) throw new RuntimeException($network['title'] . ' пока не публикует: ' . $network['note']);
    return $network;
}

/**
 * Проверка до скачивания и биллинга: текст по лимиту ресурса, виды файлов по реестру (только по полю kind, размер ещё не известен).
 * Возвращает ['kept' => сколько файлов уйдёт, 'skipped' => тексты пропусков]. Исключение — если не уйдёт ни файл, ни текст.
 */
function network_preflight(string $platform, string $text, array $items): array {
    $network = networks_registry()[$platform] ?? null;
    if ($network === null) throw new RuntimeException('Неизвестная соцсеть: ' . $platform . '.');
    $limit = network_text_limit($platform, (bool)$items);
    if (mb_strlen($text) > $limit) throw new RuntimeException("{$network['title']}: текст длиннее {$limit} символов — сократите его.");
    $kept = 0;
    $skipped = [];
    foreach ($items as $item) {
        $kind = (string)($item['kind'] ?? '');
        $name = $kind === 'video' ? 'видео' : ($kind === 'animation' ? 'GIF' : 'картинка');
        if (!in_array($kind, $network['kinds'] ?? [], true)) { $skipped[] = "{$name}: {$network['title']} такой вид не принимает"; continue; }
        if ($kept >= (int)($network['media_max'] ?? 1)) { $skipped[] = "{$name}: больше " . ($network['media_max'] ?? 1) . " файлов для {$network['title']}"; continue; }
        $kept++;
    }
    if ($kept === 0 && trim($text) === '') throw new RuntimeException("{$network['title']}: в посте нет ни подходящих файлов, ни текста.");
    return ['kept' => $kept, 'skipped' => $skipped];
}

/** Лимит текста соцсети: подпись к файлам или текст без файлов. */
function network_text_limit(string $platform, bool $with_media): int {
    return (int)(networks_registry()[$platform]['text_limit'][$with_media ? 0 : 1] ?? 4096);
}

/** Реестр для интерфейса: без функций, только то, что показываем. */
function networks_public(): array {
    return array_map(fn($n) => array_intersect_key($n, array_flip(['title', 'prefix', 'status', 'note', 'text_limit', 'media_max', 'kinds', 'poll', 'fields', 'docs'])) + ['oauth' => !empty($n['oauth'])], networks_registry());
}
