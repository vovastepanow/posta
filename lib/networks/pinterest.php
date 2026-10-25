<?php
// Pinterest (AUTOPOST_NETWORKS_PROMPT.md, часть C). OAuth2, scope boards:read pins:write; пин — всегда с картинкой (image_base64).
// Проверено по OpenAPI-спецификации pinterest/api-description; страницы developers.pinterest.com недоступны.

function pinterest_oauth_authorize(string $state, string $redirect, string $verifier = ''): string {
    return 'https://www.pinterest.com/oauth/?' . http_build_query(['client_id' => network_app_key('PINTEREST_CLIENT_ID'), 'redirect_uri' => $redirect,
        'response_type' => 'code', 'scope' => 'boards:read,pins:write', 'state' => $state]);
}

function pinterest_token_request(array $form): array {
    $basic = base64_encode(network_app_key('PINTEREST_CLIENT_ID') . ':' . network_app_key('PINTEREST_CLIENT_SECRET'));
    $t = network_expect(network_http('POST', 'https://api.pinterest.com/v5/oauth/token', ['Authorization: Basic ' . $basic, 'Content-Type: application/x-www-form-urlencoded'],
        http_build_query($form + ['continuous_refresh' => 'true'])), [200], 'Pinterest не выдал токен');
    return ['access_token' => (string)$t['access_token'], 'refresh_token' => (string)($t['refresh_token'] ?? ''), 'expires_at' => network_expires_at($t)];
}

function pinterest_oauth_exchange(string $code, string $redirect, string $verifier = ''): array {
    return pinterest_token_request(['grant_type' => 'authorization_code', 'code' => $code, 'redirect_uri' => $redirect]);
}

function pinterest_oauth_refresh(array $secrets): array {
    return pinterest_token_request(['grant_type' => 'refresh_token', 'refresh_token' => $secrets['refresh_token']]);
}

/** Аккаунт и первая доска: в неё идут пины (meta.board_id). */
function pinterest_oauth_identify(array $secrets): array {
    $me = network_expect(network_http('GET', 'https://api.pinterest.com/v5/user_account', ['Authorization: Bearer ' . $secrets['access_token']]), [200], 'Аккаунт Pinterest не получен');
    $boards = network_expect(network_http('GET', 'https://api.pinterest.com/v5/boards', ['Authorization: Bearer ' . $secrets['access_token']]), [200], 'Доски Pinterest не получены');
    $board = $boards['items'][0] ?? null;
    if (!$board) throw new RuntimeException('У аккаунта Pinterest нет досок: создайте доску и подключите снова.');
    return ['ref' => (string)($me['username'] ?? ''), 'title' => '@' . ($me['username'] ?? ''), 'meta' => ['board_id' => (string)$board['id']]];
}

function pinterest_publish(array $channel, array $post): array {
    $photo = null;
    foreach ($post['media'] ?? [] as $f) if (($f['kind'] ?? '') === 'photo') { $photo = $f; break; }
    if ($photo === null) throw new RuntimeException('Pinterest публикует пины только с картинкой.');
    $text = trim((string)($post['text'] ?? ''));
    [$title, $desc] = array_pad(explode("\n", $text, 2), 2, '');
    $info = @getimagesize((string)$photo['path']);
    $body = ['board_id' => (string)$channel['meta']['board_id'], 'title' => mb_substr(trim($title) ?: 'Пин', 0, 100), 'description' => mb_substr(trim($desc), 0, 800),
        'media_source' => ['source_type' => 'image_base64', 'content_type' => $info['mime'] ?? 'image/jpeg', 'data' => base64_encode((string)file_get_contents($photo['path']))]];
    $r = network_expect(network_http('POST', 'https://api.pinterest.com/v5/pins', ['Authorization: Bearer ' . $channel['secrets']['access_token'], 'Content-Type: application/json'],
        json_encode($body, JSON_UNESCAPED_UNICODE)), [201], 'Пин не опубликован');
    $id = (string)($r['id'] ?? '');
    return ['url' => $id !== '' ? "https://www.pinterest.com/pin/{$id}/" : '', 'message_ids' => [$id]];
}
