<?php
// MAX, мессенджер VK (AUTOPOST_NETWORKS_PROMPT.md, часть B2). Проверено по dev.max.ru: базовый домен platform-api2.max.ru,
// токен в заголовке Authorization. НЕ проверено: где передаётся chat_id (query или тело) — выбран query; загрузка файлов не реализована,
// поэтому публикуется только текст. Сертификат: проверка TLS включена (CA Минцифры — если нужен, задаётся в системе).

const MAX_API = 'https://platform-api2.max.ru';
const MAX_TEXT_MAX = 4000;                          // лимит не проверен по документации: консервативное значение

/** Подключение: токен бота и чат (канал), куда бот добавлен админом. */
function max_connect(array $input): array {
    $token = trim((string)($input['token'] ?? ''));
    $chat_id = trim((string)($input['chat_id'] ?? ''));
    if ($token === '' || !preg_match('/^-?\d{1,20}$/', $chat_id)) throw new RuntimeException('Укажите токен бота MAX и id чата.');
    $chat = network_expect(network_http('GET', MAX_API . '/chats/' . $chat_id, ['Authorization: ' . $token]), [200], 'Чат не найден или бот в нём не админ');
    return ['ref' => $chat_id, 'title' => (string)($chat['title'] ?? 'MAX'), 'secrets' => ['token' => $token], 'meta' => []];
}

/** Публикация текста в чат: POST /messages с chat_id в query. */
function max_publish(array $channel, array $post): array {
    $text = (string)($post['text'] ?? '');
    if (trim($text) === '') throw new RuntimeException('MAX пока публикует только текст, а в посте текста нет.');
    if (mb_strlen($text) > MAX_TEXT_MAX) throw new RuntimeException('Текст длиннее ' . MAX_TEXT_MAX . ' символов.');
    $url = MAX_API . '/messages?chat_id=' . rawurlencode((string)$channel['ref']);
    $sent = network_expect(network_http('POST', $url, ['Authorization: ' . $channel['secrets']['token'], 'Content-Type: application/json'],
        json_encode(['text' => $text], JSON_UNESCAPED_UNICODE)), [200], 'Сообщение не отправлено');
    $id = (string)($sent['message']['body']['mid'] ?? $sent['message']['id'] ?? '');
    return ['url' => '', 'message_ids' => [$id]];
}
