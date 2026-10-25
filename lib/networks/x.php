<?php
// X (Twitter), API v2 (AUTOPOST_NETWORKS_PROMPT.md, часть C). OAuth2 с PKCE (S256), scope tweet.write users.read tweet.read offline.access.
// Текстовые посты. Лимит длины не подтверждён документацией — 280 символов как консервативная граница. Проверено по docs.x.com.
const X_TEXT_MAX = 280;

function x_oauth_authorize(string $state, string $redirect, string $verifier = ''): string {
    $challenge = rtrim(strtr(base64_encode(hash('sha256', $verifier, true)), '+/', '-_'), '=');
    return 'https://x.com/i/oauth2/authorize?' . http_build_query(['response_type' => 'code', 'client_id' => network_app_key('X_CLIENT_ID'), 'redirect_uri' => $redirect,
        'scope' => 'tweet.write users.read tweet.read offline.access', 'state' => $state, 'code_challenge' => $challenge, 'code_challenge_method' => 'S256']);
}

function x_token_request(array $form): array {
    $basic = base64_encode(network_app_key('X_CLIENT_ID') . ':' . network_app_key('X_CLIENT_SECRET'));
    $t = network_expect(network_http('POST', 'https://api.x.com/2/oauth2/token', ['Authorization: Basic ' . $basic, 'Content-Type: application/x-www-form-urlencoded'],
        http_build_query($form)), [200], 'X не выдал токен');
    return ['access_token' => (string)$t['access_token'], 'refresh_token' => (string)($t['refresh_token'] ?? ''), 'expires_at' => network_expires_at($t)];
}

function x_oauth_exchange(string $code, string $redirect, string $verifier = ''): array {
    return x_token_request(['grant_type' => 'authorization_code', 'code' => $code, 'redirect_uri' => $redirect, 'code_verifier' => $verifier, 'client_id' => network_app_key('X_CLIENT_ID')]);
}

function x_oauth_refresh(array $secrets): array {
    $fresh = x_token_request(['grant_type' => 'refresh_token', 'refresh_token' => $secrets['refresh_token'], 'client_id' => network_app_key('X_CLIENT_ID')]);
    return ['refresh_token' => $secrets['refresh_token']] + $fresh;
}

function x_oauth_identify(array $secrets): array {
    $me = network_expect(network_http('GET', 'https://api.x.com/2/users/me', ['Authorization: Bearer ' . $secrets['access_token']]), [200], 'Аккаунт X не получен');
    $user = $me['data'] ?? [];
    if (empty($user['id'])) throw new RuntimeException('Аккаунт X не определён.');
    return ['ref' => (string)$user['id'], 'title' => '@' . ($user['username'] ?? ''), 'meta' => ['username' => (string)($user['username'] ?? '')]];
}

function x_publish(array $channel, array $post): array {
    $text = (string)($post['text'] ?? '');
    if (trim($text) === '') throw new RuntimeException('X: пост без текста.');
    if (mb_strlen($text) > X_TEXT_MAX) throw new RuntimeException('Текст длиннее ' . X_TEXT_MAX . ' символов: X его не примет.');
    $r = network_expect(network_http('POST', 'https://api.x.com/2/tweets', ['Authorization: Bearer ' . $channel['secrets']['access_token'], 'Content-Type: application/json'],
        json_encode(['text' => $text], JSON_UNESCAPED_UNICODE)), [201], 'Твит не опубликован');
    $id = (string)($r['data']['id'] ?? '');
    $user = (string)($channel['meta']['username'] ?? 'i');
    return ['url' => $id !== '' ? "https://x.com/{$user}/status/{$id}" : '', 'message_ids' => [$id]];
}
