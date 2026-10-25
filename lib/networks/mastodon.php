<?php
// Mastodon (AUTOPOST_NETWORKS_PROMPT.md, часть B4). Токен пользователя с write:statuses и write:media; проверено по docs.joinmastodon.org.

/** Адрес инстанса: только https://имя-узла, без пути. */
function mastodon_instance_url(string $input): string {
    $host = strtolower(trim(preg_replace('~^https?://~i', '', trim($input)), '/'));
    if (!preg_match('/^[a-z0-9.-]{3,253}$/', $host) || !str_contains($host, '.') || filter_var($host, FILTER_VALIDATE_IP)) throw new RuntimeException('Укажите адрес сервера Mastodon, например mastodon.social.');
    return 'https://' . $host;
}

/** Подключение: проверка токена, ресурс = инстанс и id аккаунта; лимит текста — из настроек инстанса (по умолчанию 500). */
function mastodon_connect(array $input): array {
    $instance = mastodon_instance_url((string)($input['instance'] ?? ''));
    $token = trim((string)($input['token'] ?? ''));
    if ($token === '') throw new RuntimeException('Вставьте токен доступа Mastodon.');
    $me = network_expect(network_http('GET', $instance . '/api/v1/accounts/verify_credentials', ['Authorization: Bearer ' . $token]), [200], 'Токен не принят');
    $conf = network_expect(network_http('GET', $instance . '/api/v2/instance', []), [200], 'Настройки сервера не получены');
    return ['ref' => substr($instance, 8) . '/' . $me['id'], 'title' => (string)($me['display_name'] ?: $me['acct'] ?? ''), 'secrets' => ['token' => $token],
        'meta' => ['instance' => $instance, 'acct' => '@' . ($me['acct'] ?? ''), 'max_chars' => (int)($conf['configuration']['statuses']['max_characters'] ?? 500)]];
}

/** Публикация: файлы — POST /api/v2/media (202 — ждём GET /api/v1/media/{id}), затем POST /api/v1/statuses. */
function mastodon_publish(array $channel, array $post): array {
    $instance = (string)($channel['meta']['instance'] ?? '');
    $auth = ['Authorization: Bearer ' . ($channel['secrets']['token'] ?? '')];
    $text = (string)($post['text'] ?? '');
    $limit = (int)($channel['meta']['max_chars'] ?? 500);
    if (mb_strlen($text) > $limit) throw new RuntimeException("Текст длиннее {$limit} символов: Mastodon его не примет.");
    $media_ids = [];
    foreach ($post['media'] ?? [] as $f) {
        $r = network_http('POST', $instance . '/api/v2/media', $auth, ['file' => new CURLFile($f['path'])]);
        $id = (string)(network_expect($r, [200, 202], 'Файл не загружен')['id'] ?? '');
        if ($id === '') throw new RuntimeException('Файл не загружен: сервер не вернул номер.');
        for ($try = 0; $r['status'] === 202 && $try < 30; $try++) {
            if (!isset($GLOBALS['network_http_fake'])) sleep(1);
            $r = network_http('GET', "{$instance}/api/v1/media/{$id}", $auth);
            if ($r['status'] === 200) break;
        }
        if ($r['status'] !== 200) throw new RuntimeException('Файл не обработан Mastodon.');
        $media_ids[] = $id;
    }
    $params = ['status' => $text, 'visibility' => 'public'] + ($media_ids ? ['media_ids' => $media_ids] : []);
    $sent = network_expect(network_http('POST', $instance . '/api/v1/statuses', [...$auth, 'Content-Type: application/x-www-form-urlencoded'], http_build_query($params)), [200], 'Пост не опубликован');
    return ['url' => (string)($sent['url'] ?? $sent['uri'] ?? ''), 'message_ids' => [(string)($sent['id'] ?? '')]];
}
