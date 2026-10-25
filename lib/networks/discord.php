<?php
// Discord: вебхук канала (AUTOPOST_NETWORKS_PROMPT.md, часть B5). Проверено по docs (discord.com/developers, Execute Webhook).

/** Подключение: GET вебхука (проверка и название канала), ресурс = id канала. */
function discord_connect(array $input): array {
    $url = trim((string)($input['webhook'] ?? ''));
    if (!preg_match('~^https://(?:discord|discordapp)\.com/api/webhooks/(\d{5,25})/[\w-]{20,200}$~', $url)) throw new RuntimeException('Адрес вебхука Discord выглядит неверно.');
    $hook = network_expect(network_http('GET', $url, []), [200], 'Вебхук не принят');
    return ['ref' => (string)($hook['channel_id'] ?? ''), 'title' => (string)($hook['name'] ?? 'Discord'), 'secrets' => ['webhook' => $url],
        'meta' => ['guild_id' => (string)($hook['guild_id'] ?? ''), 'channel_id' => (string)($hook['channel_id'] ?? '')]];
}

/** Публикация: текст до 2000 символов и файлы как files[n] (multipart), ждём ответа с wait=true. */
function discord_publish(array $channel, array $post): array {
    $text = (string)($post['text'] ?? '');
    if (mb_strlen($text) > 2000) throw new RuntimeException('Текст длиннее 2000 символов: Discord его не примет.');
    $body = ['payload_json' => json_encode(['content' => $text], JSON_UNESCAPED_UNICODE)];
    foreach (array_values($post['media'] ?? []) as $i => $f) $body["files[{$i}]"] = new CURLFile($f['path']);
    $url = $channel['secrets']['webhook'] . (str_contains((string)$channel['secrets']['webhook'], '?') ? '&' : '?') . 'wait=true';
    $sent = network_expect(network_http('POST', $url, [], $body), [200], 'Пост не опубликован');
    $guild = (string)($channel['meta']['guild_id'] ?? '');
    $channel_id = (string)($sent['channel_id'] ?? $channel['meta']['channel_id'] ?? '');
    return ['url' => $guild !== '' ? "https://discord.com/channels/{$guild}/{$channel_id}/" . ($sent['id'] ?? '') : '', 'message_ids' => [(string)($sent['id'] ?? '')]];
}
