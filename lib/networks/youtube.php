<?php
// YouTube Shorts (AUTOPOST_NETWORKS_PROMPT.md, часть C). OAuth Google, scope youtube.upload; видео — резюмируемой загрузкой.
// Без аудита проекта Google ролики уходят приватными (по умолчанию так и публикуем). Проверено по developers.google.com.

function youtube_oauth_authorize(string $state, string $redirect, string $verifier = ''): string {
    return 'https://accounts.google.com/o/oauth2/v2/auth?' . http_build_query(['client_id' => network_app_key('YOUTUBE_CLIENT_ID'), 'redirect_uri' => $redirect,
        'response_type' => 'code', 'scope' => 'https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.readonly',
        'access_type' => 'offline', 'prompt' => 'consent', 'state' => $state]);
}

function youtube_token_request(array $form): array {
    $t = network_expect(network_http('POST', 'https://oauth2.googleapis.com/token', ['Content-Type: application/x-www-form-urlencoded'], http_build_query($form + [
        'client_id' => network_app_key('YOUTUBE_CLIENT_ID'), 'client_secret' => network_app_key('YOUTUBE_CLIENT_SECRET')])), [200], 'Google не выдал токен');
    return ['access_token' => (string)$t['access_token'], 'refresh_token' => (string)($t['refresh_token'] ?? ''), 'expires_at' => network_expires_at($t)];
}

function youtube_oauth_exchange(string $code, string $redirect, string $verifier = ''): array {
    return youtube_token_request(['grant_type' => 'authorization_code', 'code' => $code, 'redirect_uri' => $redirect]);
}

function youtube_oauth_refresh(array $secrets): array {
    $fresh = youtube_token_request(['grant_type' => 'refresh_token', 'refresh_token' => $secrets['refresh_token']]);
    return ['refresh_token' => $secrets['refresh_token']] + $fresh;
}

/** Канал пользователя: ресурс — id канала. */
function youtube_oauth_identify(array $secrets): array {
    $r = network_expect(network_http('GET', 'https://www.googleapis.com/youtube/v3/channels?part=snippet&mine=true', ['Authorization: Bearer ' . $secrets['access_token']]), [200], 'Канал YouTube не получен');
    $channel = $r['items'][0] ?? null;
    if (!$channel) throw new RuntimeException('У аккаунта нет канала YouTube.');
    return ['ref' => (string)$channel['id'], 'title' => (string)($channel['snippet']['title'] ?? 'YouTube'), 'meta' => ['privacy' => 'private']];
}

/** Видео Shorts: сессия резюмируемой загрузки (Location), затем тело видео. Приватность — из meta (по умолчанию private). */
function youtube_publish(array $channel, array $post): array {
    $video = null;
    foreach ($post['media'] ?? [] as $f) if (($f['kind'] ?? '') === 'video') { $video = $f; break; }
    if ($video === null) throw new RuntimeException('YouTube публикует только видео.');
    $text = trim((string)($post['text'] ?? ''));
    [$title, $desc] = array_pad(explode("\n", $text, 2), 2, '');
    $bytes = (string)file_get_contents($video['path']);
    $auth = 'Authorization: Bearer ' . $channel['secrets']['access_token'];
    $meta = ['snippet' => ['title' => mb_substr(trim($title) ?: 'Shorts', 0, 100), 'description' => trim($desc)],
        'status' => ['privacyStatus' => $channel['meta']['privacy'] ?? 'private', 'selfDeclaredMadeForKids' => false]];
    $start = network_http('POST', 'https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&part=snippet,status',
        [$auth, 'Content-Type: application/json; charset=UTF-8', 'X-Upload-Content-Length: ' . strlen($bytes), 'X-Upload-Content-Type: video/mp4'], json_encode($meta, JSON_UNESCAPED_UNICODE));
    network_expect($start, [200], 'Загрузка видео не начата');
    $location = (string)($start['headers']['location'] ?? '');
    if ($location === '') throw new RuntimeException('YouTube не выдал адрес загрузки.');
    $done = network_expect(network_http('PUT', $location, ['Content-Type: video/mp4'], $bytes), [200, 201], 'Видео не загружено');
    $id = (string)($done['id'] ?? '');
    return ['url' => $id !== '' ? "https://youtube.com/shorts/{$id}" : '', 'message_ids' => [$id]];
}
