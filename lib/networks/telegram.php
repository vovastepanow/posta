<?php
// Адаптер Telegram: тот же путь публикации, что был до ресурсов соцсетей (AUTOPOST_NETWORKS_PROMPT.md, часть A).

/** Публикация поста в Telegram-канал: опрос или медиа с текстом. Возвращает ['url', 'message_ids']. */
function telegram_network_publish(array $channel, array $post): array {
    if (!empty($post['poll'])) {
        $sent = telegram_publish_poll($channel, $post['poll']);
        return ['url' => $sent['url'], 'message_ids' => $sent['message_ids']];
    }
    $media = array_map(fn($f) => ['kind' => $f['kind'], 'path' => $f['path'], 'width' => $f['width'], 'height' => $f['height'], 'duration' => $f['duration']], $post['media'] ?? []);
    $sent = telegram_publish_prepared($channel, (string)($post['text'] ?? ''), $media);
    return ['url' => $sent['url'], 'message_ids' => $sent['message_ids']];
}
