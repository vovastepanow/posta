<?php
// ВКонтакте: сообщество (AUTOPOST_NETWORKS_PROMPT.md, часть B1). ВНИМАНИЕ: dev.vk.com недоступен из этой сессии —
// имена методов, версия API и права токена не сверены по документации. Нужен токен администратора с правами wall, photos, groups, offline.

const VK_API = 'https://api.vk.com/method/';
const VK_API_VERSION = '5.199';                     // проверить по документации перед включением
const VK_TEXT_MAX = 16384;
const VK_ATTACH_MAX = 10;

/** Вызов метода VK: параметры формой, ошибка API — исключение с текстом VK. */
function vk_call(string $method, array $params, string $token): array {
    $params += ['access_token' => $token, 'v' => VK_API_VERSION];
    $r = network_http('POST', VK_API . $method, ['Content-Type: application/x-www-form-urlencoded'], http_build_query($params));
    $data = is_array($r['data']) ? $r['data'] : [];
    if (isset($data['error'])) throw new RuntimeException('ВКонтакте: ' . mb_substr((string)($data['error']['error_msg'] ?? 'ошибка'), 0, 200) . '.');
    return (array)($data['response'] ?? []);
}

/** Группы, где пользователь — администратор (для выбора в интерфейсе). */
function vk_admin_groups(string $token): array {
    $items = vk_call('groups.get', ['filter' => 'admin', 'extended' => 1], $token)['items'] ?? [];
    return array_map(fn($g) => ['id' => (int)$g['id'], 'name' => (string)($g['name'] ?? '')], $items);
}

/** Подключение сообщества: токен и id (или короткое имя) группы; ресурс = положительный id группы. */
function vk_connect(array $input): array {
    $token = trim((string)($input['token'] ?? ''));
    $group = ltrim(trim((string)($input['group'] ?? '')), '-');
    if ($token === '' || $group === '') throw new RuntimeException('Укажите токен и адрес сообщества.');
    $found = vk_call('groups.getById', ['group_id' => $group], $token);
    $info = $found[0] ?? $found['groups'][0] ?? null;
    if (!$info) throw new RuntimeException('Сообщество не найдено или токен не даёт к нему доступа.');
    return ['ref' => (string)$info['id'], 'title' => (string)($info['name'] ?? ''), 'secrets' => ['token' => $token],
        'meta' => ['screen_name' => (string)($info['screen_name'] ?? '')]];
}

/** Фото поста: upload-сервер сообщества, загрузка, сохранение на стене → attachment photo{owner}_{id}. */
function vk_photo_attachment(int $group_id, string $path, string $token): string {
    $upload = vk_call('photos.getWallUploadServer', ['group_id' => $group_id], $token)['upload_url'] ?? '';
    if ($upload === '') throw new RuntimeException('ВКонтакте не дал адрес для загрузки фото.');
    $sent = network_http('POST', $upload, [], ['photo' => new CURLFile($path)]);
    $file = is_array($sent['data']) ? $sent['data'] : [];
    $saved = vk_call('photos.saveWallPhoto', ['group_id' => $group_id, 'photo' => $file['photo'] ?? '', 'server' => $file['server'] ?? '', 'hash' => $file['hash'] ?? ''], $token);
    $photo = $saved[0] ?? null;
    if (!$photo) throw new RuntimeException('Фото не сохранилось на стене.');
    return "photo{$photo['owner_id']}_{$photo['id']}";
}

/** Публикация на стену сообщества (от имени группы): текст, до 10 вложений (фото; видео и GIF пока не публикуем). */
function vk_publish(array $channel, array $post): array {
    $text = (string)($post['text'] ?? '');
    if (mb_strlen($text) > VK_TEXT_MAX) throw new RuntimeException('Текст длиннее ' . VK_TEXT_MAX . ' символов: ВКонтакте его не примет.');
    $token = (string)($channel['secrets']['token'] ?? '');
    $group_id = (int)$channel['ref'];
    $attachments = [];
    foreach ($post['media'] ?? [] as $f) $attachments[] = vk_photo_attachment($group_id, (string)$f['path'], $token);
    $params = ['owner_id' => -$group_id, 'from_group' => 1, 'message' => $text];
    if ($attachments) $params['attachments'] = implode(',', array_slice($attachments, 0, VK_ATTACH_MAX));
    $posted = vk_call('wall.post', $params, $token);
    $post_id = (int)($posted['post_id'] ?? 0);
    return ['url' => $post_id ? "https://vk.com/wall-{$group_id}_{$post_id}" : '', 'message_ids' => [(string)$post_id]];
}
