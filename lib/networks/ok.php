<?php
// Одноклассники, группа (AUTOPOST_NETWORKS_PROMPT.md, часть C). Вход — постоянный токен приложения (его выдаёт настройка приложения OK),
// без OAuth-потока. Подпись запроса: md5 от параметров по алфавиту (без access_token и session_key) + session_secret_key.
// Метод mediatopic.post и формат подписи — по apiok.ru (частично проверено). Загрузка фото не сделана: только текст.

function ok_sign(array $params, string $secret): string {
    ksort($params);
    $plain = '';
    foreach ($params as $k => $v) if (!in_array($k, ['access_token', 'session_key', 'sig'], true)) $plain .= $k . '=' . $v;
    return md5($plain . $secret);
}

/** Вызов метода OK (fb.do): подписанный запрос. */
function ok_call(string $method, array $params, string $access_token, string $session_secret): array {
    $params += ['application_key' => network_app_key('OK_APP_KEY'), 'method' => $method, 'format' => 'json', 'access_token' => $access_token];
    $params['sig'] = ok_sign($params, $session_secret);
    return network_expect(network_http('GET', 'https://api.ok.ru/fb.do?' . http_build_query($params), []), [200], 'Одноклассники: ' . $method);
}

/** Подключение: токен приложения, секрет сессии и id группы; проверка — текущий пользователь. */
function ok_connect(array $input): array {
    $token = trim((string)($input['access_token'] ?? ''));
    $secret = trim((string)($input['session_secret_key'] ?? ''));
    $gid = trim((string)($input['gid'] ?? ''));
    if ($token === '' || $secret === '' || !preg_match('/^\d{1,20}$/', $gid)) throw new RuntimeException('Укажите токен, секретный ключ сессии и id группы.');
    $me = ok_call('users.getCurrentUser', [], $token, $secret);
    return ['ref' => $gid, 'title' => trim((string)($me['name'] ?? 'Одноклассники')) ?: 'Одноклассники',
        'secrets' => ['access_token' => $token, 'session_secret_key' => $secret], 'meta' => ['gid' => $gid]];
}

/** Публикация в группу: тема типа GROUP_THEME с текстом. */
function ok_publish(array $channel, array $post): array {
    $text = trim((string)($post['text'] ?? ''));
    if ($text === '') throw new RuntimeException('Одноклассники: пост без текста.');
    $attachment = json_encode(['media' => [['type' => 'text', 'text' => $text]]], JSON_UNESCAPED_UNICODE);
    // Ответ метода — строка с id темы (не объект), поэтому читаем сырой ответ
    $params = ['application_key' => network_app_key('OK_APP_KEY'), 'method' => 'mediatopic.post', 'format' => 'json', 'type' => 'GROUP_THEME',
        'gid' => (string)$channel['meta']['gid'], 'attachment' => $attachment, 'access_token' => $channel['secrets']['access_token']];
    $params['sig'] = ok_sign($params, (string)$channel['secrets']['session_secret_key']);
    $r = network_http('GET', 'https://api.ok.ru/fb.do?' . http_build_query($params), []);
    if ($r['status'] !== 200) throw new RuntimeException('Пост не опубликован: код ' . $r['status'] . '.');
    $id = trim((string)$r['raw'], "\" \n");
    return ['url' => $id !== '' ? "https://ok.ru/group/{$channel['meta']['gid']}/topic/{$id}" : '', 'message_ids' => [$id]];
}
