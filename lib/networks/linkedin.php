<?php
// LinkedIn, профиль (AUTOPOST_NETWORKS_PROMPT.md, часть C). OAuth2 со scope openid profile w_member_social; токен 60 дней, без обновления.
// Публикация — Posts API /rest/posts, текст. Проверено по Microsoft Learn (Posts API обновлён 2026-05-13).
const LINKEDIN_VERSION = '202511';                  // версия API; старые снимаются — проверить перед включением

function linkedin_oauth_authorize(string $state, string $redirect, string $verifier = ''): string {
    return 'https://www.linkedin.com/oauth/v2/authorization?' . http_build_query(['response_type' => 'code', 'client_id' => network_app_key('LINKEDIN_CLIENT_ID'),
        'redirect_uri' => $redirect, 'state' => $state, 'scope' => 'openid profile w_member_social']);
}

function linkedin_oauth_exchange(string $code, string $redirect, string $verifier = ''): array {
    $t = network_expect(network_http('POST', 'https://www.linkedin.com/oauth/v2/accessToken', ['Content-Type: application/x-www-form-urlencoded'], http_build_query([
        'grant_type' => 'authorization_code', 'code' => $code, 'client_id' => network_app_key('LINKEDIN_CLIENT_ID'),
        'client_secret' => network_app_key('LINKEDIN_CLIENT_SECRET'), 'redirect_uri' => $redirect])), [200], 'LinkedIn не выдал токен');
    return ['access_token' => (string)$t['access_token'], 'expires_at' => network_expires_at($t)];
}

function linkedin_oauth_identify(array $secrets): array {
    $me = network_expect(network_http('GET', 'https://api.linkedin.com/v2/userinfo', ['Authorization: Bearer ' . $secrets['access_token']]), [200], 'Аккаунт LinkedIn не получен');
    $sub = (string)($me['sub'] ?? '');
    if ($sub === '') throw new RuntimeException('Аккаунт LinkedIn не определён.');
    return ['ref' => $sub, 'title' => (string)($me['name'] ?? 'LinkedIn'), 'meta' => []];
}

function linkedin_publish(array $channel, array $post): array {
    $text = (string)($post['text'] ?? '');
    if (trim($text) === '') throw new RuntimeException('LinkedIn: пост без текста.');
    $body = ['author' => 'urn:li:person:' . $channel['ref'], 'commentary' => $text, 'visibility' => 'PUBLIC',
        'distribution' => ['feedDistribution' => 'MAIN_FEED', 'targetEntities' => [], 'thirdPartyDistributionChannels' => []],
        'lifecycleState' => 'PUBLISHED', 'isReshareDisabledByAuthor' => false];
    $r = network_http('POST', 'https://api.linkedin.com/rest/posts', ['Authorization: Bearer ' . $channel['secrets']['access_token'], 'Linkedin-Version: ' . LINKEDIN_VERSION,
        'X-Restli-Protocol-Version: 2.0.0', 'Content-Type: application/json'], json_encode($body, JSON_UNESCAPED_UNICODE));
    network_expect($r, [201], 'Пост не опубликован');
    $urn = (string)($r['headers']['x-restli-id'] ?? '');
    return ['url' => $urn !== '' ? 'https://www.linkedin.com/feed/update/' . $urn : '', 'message_ids' => [$urn]];
}
