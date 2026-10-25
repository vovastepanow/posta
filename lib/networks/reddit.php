<?php
// Reddit (AUTOPOST_NETWORKS_PROMPT.md, часть C). Вход OAuth2, scope identity submit, duration=permanent (есть refresh_token).
// Публикация — только в свой профиль (sr = u_<имя>), текстовые посты. Проверено по архивному коду reddit-archive; живая документация недоступна.
const REDDIT_UA = 'web:posta:1.0 (автопостинг)';

function reddit_oauth_authorize(string $state, string $redirect, string $verifier = ''): string {
    return 'https://www.reddit.com/api/v1/authorize?' . http_build_query(['client_id' => network_app_key('REDDIT_CLIENT_ID'), 'response_type' => 'code', 'state' => $state,
        'redirect_uri' => $redirect, 'duration' => 'permanent', 'scope' => 'identity submit']);
}

/** Запрос токена (обмен кода или обновление): Basic-авторизация приложением. */
function reddit_token_request(array $form): array {
    $basic = base64_encode(network_app_key('REDDIT_CLIENT_ID') . ':' . network_app_key('REDDIT_CLIENT_SECRET'));
    $r = network_http('POST', 'https://www.reddit.com/api/v1/access_token', ['Authorization: Basic ' . $basic, 'User-Agent: ' . REDDIT_UA, 'Content-Type: application/x-www-form-urlencoded'], http_build_query($form));
    $t = network_expect($r, [200], 'Reddit не выдал токен');
    if (isset($t['error'])) throw new RuntimeException('Reddit не выдал токен: ' . $t['error'] . '.');
    return ['access_token' => (string)$t['access_token'], 'refresh_token' => (string)($t['refresh_token'] ?? ''), 'expires_at' => network_expires_at($t)];
}

function reddit_oauth_exchange(string $code, string $redirect, string $verifier = ''): array {
    return reddit_token_request(['grant_type' => 'authorization_code', 'code' => $code, 'redirect_uri' => $redirect]);
}

function reddit_oauth_refresh(array $secrets): array {
    $fresh = reddit_token_request(['grant_type' => 'refresh_token', 'refresh_token' => $secrets['refresh_token']]);
    return ['refresh_token' => $secrets['refresh_token']] + $fresh;
}

/** Аккаунт: имя пользователя; ресурс — имя, публикация — в его профиль u_<имя>. */
function reddit_oauth_identify(array $secrets): array {
    $me = network_expect(network_http('GET', 'https://oauth.reddit.com/api/v1/me', ['Authorization: bearer ' . $secrets['access_token'], 'User-Agent: ' . REDDIT_UA]), [200], 'Аккаунт Reddit не получен');
    $name = (string)($me['name'] ?? '');
    if ($name === '') throw new RuntimeException('Аккаунт Reddit не определён.');
    return ['ref' => $name, 'title' => 'u/' . $name, 'meta' => ['subreddit' => 'u_' . strtolower($name)]];
}

/** Текстовый пост в профиль: первая строка — заголовок (до 300 символов), остальное — текст. */
function reddit_publish(array $channel, array $post): array {
    $text = trim((string)($post['text'] ?? ''));
    if ($text === '') throw new RuntimeException('Reddit: пост без текста.');
    [$title, $body] = array_pad(explode("\n", $text, 2), 2, '');
    $title = mb_substr(trim($title), 0, 300);
    $form = ['api_type' => 'json', 'kind' => 'self', 'sr' => (string)($channel['meta']['subreddit'] ?? ''), 'title' => $title, 'text' => trim($body)];
    $r = network_expect(network_http('POST', 'https://oauth.reddit.com/api/submit', ['Authorization: bearer ' . $channel['secrets']['access_token'], 'User-Agent: ' . REDDIT_UA,
        'Content-Type: application/x-www-form-urlencoded'], http_build_query($form)), [200], 'Пост не опубликован');
    $errors = $r['json']['errors'] ?? [];
    if ($errors) throw new RuntimeException('Reddit: ' . mb_substr((string)implode('; ', array_map(fn($e) => is_array($e) ? implode(' ', $e) : (string)$e, $errors)), 0, 200) . '.');
    return ['url' => (string)($r['json']['data']['url'] ?? ''), 'message_ids' => [(string)($r['json']['data']['name'] ?? '')]];
}
