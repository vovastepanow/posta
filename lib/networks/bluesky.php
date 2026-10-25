<?php
// Bluesky (AUTOPOST_NETWORKS_PROMPT.md, часть B3). Вход — пароль приложения (не основной), каждая публикация — свежая сессия.
// Не проверено по документации в этой сессии (docs.bsky.app перенаправляет, текст не открылся): эндпоинты — по знанию API AT Protocol.

const BLUESKY_PDS = 'https://bsky.social';           // сервер по умолчанию; у других серверов — meta.pds
const BLUESKY_BLOB_MAX = 950000;                     // картинка для загрузки: чуть меньше лимита 1 МБ
const BLUESKY_TEXT_MAX = 300;                        // граффем; без intl считаем кодовые точки — это не больше граффем, значит безопасно

/** Подключение: вход по паролю приложения, ресурс = DID. */
function bluesky_connect(array $input): array {
    $identifier = trim((string)($input['identifier'] ?? ''));
    $password = trim((string)($input['password'] ?? ''));
    if ($identifier === '' || $password === '') throw new RuntimeException('Укажите ник Bluesky и пароль приложения.');
    $session = bluesky_session(BLUESKY_PDS, $identifier, $password);
    return ['ref' => (string)$session['did'], 'title' => '@' . $session['handle'], 'secrets' => ['identifier' => $identifier, 'password' => $password],
        'meta' => ['handle' => (string)$session['handle'], 'pds' => BLUESKY_PDS]];
}

/** Вход: accessJwt и DID. */
function bluesky_session(string $pds, string $identifier, string $password): array {
    $r = network_http('POST', $pds . '/xrpc/com.atproto.server.createSession', ['Content-Type: application/json'],
        json_encode(['identifier' => $identifier, 'password' => $password]));
    return network_expect($r, [200], 'Вход в Bluesky не удался (нужен пароль приложения)');
}

/** Ссылки и хэштеги для facets: смещения — байты UTF-8 (PREG_OFFSET_CAPTURE отдаёт байты). */
function bluesky_facets(string $text): array {
    $facets = [];
    preg_match_all('~https?://[^\s<>"\']+~u', $text, $links, PREG_OFFSET_CAPTURE);
    foreach ($links[0] as [$url, $start]) {
        $facets[] = ['index' => ['byteStart' => $start, 'byteEnd' => $start + strlen($url)], 'features' => [['$type' => 'app.bsky.richtext.facet#link', 'uri' => $url]]];
    }
    preg_match_all('~(?<=\s|^)#([\p{L}\p{N}_]{1,60})~u', $text, $tags, PREG_OFFSET_CAPTURE);
    foreach ($tags[1] as $i => [$tag, $start]) {
        $whole = $tags[0][$i][0];
        $facets[] = ['index' => ['byteStart' => $tags[0][$i][1], 'byteEnd' => $tags[0][$i][1] + strlen($whole)], 'features' => [['$type' => 'app.bsky.richtext.facet#tag', 'tag' => $tag]]];
    }
    return $facets;
}

/** Картинка до BLOB_MAX: сжатие JPEG, если нужно; иначе null (файл пропускаем). */
function bluesky_image_bytes(string $path): ?array {
    $bytes = (string)file_get_contents($path);
    foreach ([2048, 1600, 1200] as $side) {
        if (strlen($bytes) <= BLUESKY_BLOB_MAX) break;
        $bytes = image_shrink($bytes, $side, 80, true);
    }
    if (strlen($bytes) > BLUESKY_BLOB_MAX) return null;
    $info = @getimagesizefromstring($bytes);
    return ['bytes' => $bytes, 'mime' => $info['mime'] ?? 'image/jpeg'];
}

/** Публикация: вход, загрузка картинок (uploadBlob), запись app.bsky.feed.post через createRecord. */
function bluesky_publish(array $channel, array $post): array {
    $pds = (string)($channel['meta']['pds'] ?? BLUESKY_PDS);
    $session = bluesky_session($pds, (string)($channel['secrets']['identifier'] ?? ''), (string)($channel['secrets']['password'] ?? ''));
    $auth = 'Authorization: Bearer ' . $session['accessJwt'];
    $text = (string)($post['text'] ?? '');
    $cut = mb_strlen($text) > BLUESKY_TEXT_MAX;
    if ($cut) $text = mb_substr($text, 0, BLUESKY_TEXT_MAX - 1) . '…';
    $images = [];
    foreach ($post['media'] ?? [] as $f) {
        $img = bluesky_image_bytes((string)$f['path']);
        if ($img === null) continue;
        $blob = network_expect(network_http('POST', $pds . '/xrpc/com.atproto.repo.uploadBlob', [$auth, 'Content-Type: ' . $img['mime']], $img['bytes']), [200], 'Картинка не загружена')['blob'] ?? null;
        if ($blob) $images[] = ['alt' => '', 'image' => $blob];
    }
    $record = ['$type' => 'app.bsky.feed.post', 'text' => $text, 'createdAt' => gmdate('Y-m-d\TH:i:s\Z')];
    if ($facets = bluesky_facets($text)) $record['facets'] = $facets;
    if ($images) $record['embed'] = ['$type' => 'app.bsky.embed.images', 'images' => array_slice($images, 0, 4)];
    $body = json_encode(['repo' => $session['did'], 'collection' => 'app.bsky.feed.post', 'record' => $record], JSON_UNESCAPED_UNICODE);
    $made = network_expect(network_http('POST', $pds . '/xrpc/com.atproto.repo.createRecord', [$auth, 'Content-Type: application/json'], $body), [200], 'Пост не опубликован');
    $rkey = substr(strrchr((string)($made['uri'] ?? ''), '/') ?: '/', 1);
    return ['url' => $rkey !== '' ? 'https://bsky.app/profile/' . ($channel['meta']['handle'] ?? $session['did']) . "/post/{$rkey}" : '', 'message_ids' => [(string)($made['cid'] ?? '')], 'text_cut' => $cut];
}
