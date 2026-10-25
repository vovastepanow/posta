<?php
// Tumblr (AUTOPOST_NETWORKS_PROMPT.md, часть C). OAuth2, scope basic write offline_access; публикация NPF на api.tumblr.com.
// Текстовые посты. Проверено по репозиторию tumblr/docs; страница tumblr.com/docs недоступна.

function tumblr_oauth_authorize(string $state, string $redirect, string $verifier = ''): string {
    return 'https://www.tumblr.com/oauth2/authorize?' . http_build_query(['client_id' => network_app_key('TUMBLR_CLIENT_ID'), 'response_type' => 'code',
        'scope' => 'basic write offline_access', 'state' => $state, 'redirect_uri' => $redirect]);
}

function tumblr_token_request(array $form): array {
    $form += ['client_id' => network_app_key('TUMBLR_CLIENT_ID'), 'client_secret' => network_app_key('TUMBLR_CLIENT_SECRET')];
    $t = network_expect(network_http('POST', 'https://api.tumblr.com/v2/oauth2/token', ['Content-Type: application/x-www-form-urlencoded'], http_build_query($form)), [200], 'Tumblr не выдал токен');
    return ['access_token' => (string)$t['access_token'], 'refresh_token' => (string)($t['refresh_token'] ?? ''), 'expires_at' => network_expires_at($t)];
}

function tumblr_oauth_exchange(string $code, string $redirect, string $verifier = ''): array {
    return tumblr_token_request(['grant_type' => 'authorization_code', 'code' => $code, 'redirect_uri' => $redirect]);
}

function tumblr_oauth_refresh(array $secrets): array {
    return tumblr_token_request(['grant_type' => 'refresh_token', 'refresh_token' => $secrets['refresh_token']]);
}

/** Основной блог пользователя: ресурс — его имя, адрес блога — <имя>.tumblr.com. */
function tumblr_oauth_identify(array $secrets): array {
    $info = network_expect(network_http('GET', 'https://api.tumblr.com/v2/user/info', ['Authorization: Bearer ' . $secrets['access_token']]), [200], 'Блоги Tumblr не получены');
    $blogs = (array)($info['response']['user']['blogs'] ?? []);
    $blog = null;
    foreach ($blogs as $b) if (!empty($b['primary'])) $blog = $b;
    $blog ??= $blogs[0] ?? null;
    if (!$blog) throw new RuntimeException('У аккаунта Tumblr нет блогов.');
    return ['ref' => (string)$blog['name'], 'title' => (string)($blog['title'] ?? $blog['name']), 'meta' => ['blog' => (string)$blog['name'] . '.tumblr.com']];
}

function tumblr_publish(array $channel, array $post): array {
    $text = (string)($post['text'] ?? '');
    if (trim($text) === '') throw new RuntimeException('Tumblr: пост без текста.');
    $blog = (string)($channel['meta']['blog'] ?? '');
    $body = json_encode(['content' => [['type' => 'text', 'text' => $text]], 'state' => 'published'], JSON_UNESCAPED_UNICODE);
    $r = network_expect(network_http('POST', "https://api.tumblr.com/v2/blog/{$blog}/posts", ['Authorization: Bearer ' . $channel['secrets']['access_token'], 'Content-Type: application/json'], $body), [201], 'Пост не опубликован');
    $id = (string)($r['response']['id'] ?? '');
    return ['url' => $id !== '' ? "https://{$blog}/post/{$id}" : '', 'message_ids' => [$id]];
}
