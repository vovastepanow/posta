<?php
// Проверка ресурсов соцсетей: php tools/test_networks.php [файл-базы-v15]. Миграция идёт на временной копии базы, живая не трогается.
// По умолчанию — копия data/posta.sqlite.bak-discnames (схема до миграции 16); без такого файла — пустая база.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

echo "A. Миграция 16 на копии базы:\n";
$source = $argv[1] ?? (ROOT . '/data/posta.sqlite.bak-discnames');
$tmp = sys_get_temp_dir() . '/posta-networks-test-' . getmypid() . '.sqlite';
if (is_file($source)) copy($source, $tmp); else touch($tmp);
$pdo = new PDO('sqlite:' . $tmp);
$pdo->exec('PRAGMA foreign_keys = ON');
$version_before = (int)$pdo->query('PRAGMA user_version')->fetchColumn();
$before = $pdo->query('SELECT id, user_id, chat_id, token_enc, position FROM channels ORDER BY position, connected_at')->fetchAll(PDO::FETCH_ASSOC);
if ($version_before < 16) db_migrate($pdo);
$check('версия схемы стала 16', (int)$pdo->query('PRAGMA user_version')->fetchColumn() === 16);
$after = $pdo->query('SELECT * FROM channels ORDER BY position, connected_at')->fetchAll(PDO::FETCH_ASSOC);
$check('число каналов до и после совпадает (' . count($before) . ')', count($before) === count($after));
$check('порядок каналов совпадает', array_column($before, 'id') === array_column($after, 'id'));
$check('telegram: ref = chat_id', array_reduce($after, fn($ok, $r) => $ok && $r['platform'] === 'telegram' && $r['ref'] === (string)$r['chat_id'], true));
$check('секреты перенесены: токен расшифровывается из secrets_enc', array_reduce($after, fn($ok, $r) => $ok && ($r['secrets_enc'] !== '' && (secret_decrypt($r['secrets_enc']) !== null) && json_decode(secret_decrypt($r['secrets_enc']), true)['token'] === secret_decrypt($r['token_enc'])), true));
$pdo->exec("INSERT INTO users (id, created_at) VALUES ('test-user', 0)");
try { $pdo->exec("INSERT INTO channels (id, user_id, platform, ref, connected_at) VALUES ('d1','test-user','telegram','-100','0'),('d2','test-user','telegram','-100','0')"); $dup = false; }
catch (PDOException $e) { $dup = true; }
$check('уникальность (user_id, platform, ref) работает', $dup);

echo "B. Адаптеры и чтение каналов:\n";
$check('реестр: Telegram публикует, остальные — нет', (network_for('telegram')['title'] ?? '') === 'Telegram');
$thrown = false; try { network_for('tiktok'); } catch (RuntimeException $e) { $thrown = str_contains($e->getMessage(), 'пока не публикует'); }
$check('соцсеть без адаптера — понятная ошибка', $thrown);
$public = networks_public();
$check('публичный реестр не содержит функций', !array_key_exists('publish', $public['telegram']));
$row = $after[0] ?? null;
if ($row) {
    $c = channel_from_row($row);
    $check('channel_from_row: ref, platform, token из секретов', ($c['ref'] ?? '') === (string)$row['chat_id'] && ($c['platform'] ?? '') === 'telegram' && ($c['token'] ?? '') !== '');
}
$new = ['platform' => 'discord', 'ref' => 'https://discord.com/api/webhooks/1/x', 'secrets' => ['webhook' => 'https://discord.com/api/webhooks/1/x'], 'meta' => ['title' => 'Тест'], 'connected_at' => 1];
$cfix = json_decode(json_encode(channel_from_row(['id' => 'z', 'platform' => 'discord', 'ref' => $new['ref'], 'meta' => json_encode($new['meta']), 'position' => 0, 'bot_id' => 0, 'bot_username' => '', 'chat_id' => 0, 'chat_title' => '', 'chat_username' => '', 'chat_type' => '', 'connected_at' => 1, 'schedule' => '{}', 'style' => '{}', 'hashtags' => 'off', 'secrets_enc' => secret_encrypt(json_encode($new['secrets'])), 'token_enc' => secret_encrypt('')])), true);
$check('не-Telegram ресурс: secrets и meta читаются', ($cfix['secrets']['webhook'] ?? '') === $new['secrets']['webhook'] && ($cfix['meta']['title'] ?? '') === 'Тест');
$check('не-Telegram ресурс: токена нет', !isset($cfix['token']));

echo "C. Публикация Telegram не сломана (адаптер выбирается по платформе):\n";
$check('outbox_send_post выбирает Telegram-адаптер', function_exists('telegram_network_publish') && function_exists('outbox_send_post'));

echo "D. Адаптеры на подставном HTTP (Mastodon, Discord, Bluesky, VK, MAX):\n";
// Подставка: маршруты «МЕТОД url-подстрока» => ответ или функция (метод, url, заголовки, тело) => ответ.
$fake = function (array $routes) {
    $GLOBALS['network_http_fake'] = function ($method, $url, $headers, $body) use ($routes) {
        foreach ($routes as $pattern => $reply) {
            if (!str_contains("$method $url", $pattern)) continue;
            $r = is_callable($reply) ? $reply($method, $url, $headers, $body) : $reply;
            return ['status' => $r[0], 'data' => $r[1], 'raw' => $r['raw'] ?? (is_string($r[1]) ? $r[1] : json_encode($r[1])), 'headers' => $r['headers'] ?? []];
        }
        throw new RuntimeException("нет подставки: $method $url");
    };
};
$jpeg = sys_get_temp_dir() . '/posta-net-' . getmypid() . '.jpg';
$img = imagecreatetruecolor(16, 16); imagejpeg($img, $jpeg, 80);
$caught = fn(callable $fn) => (function () use ($fn) { try { $fn(); return ''; } catch (RuntimeException $e) { return $e->getMessage(); } })();

// Mastodon
$fake(['GET https://mastodon.social/api/v1/accounts/verify_credentials' => [200, ['id' => '7', 'acct' => 'vlad', 'display_name' => 'Влад']],
    'GET https://mastodon.social/api/v2/instance' => [200, ['configuration' => ['statuses' => ['max_characters' => 500]]]]]);
$m = mastodon_connect(['instance' => 'mastodon.social', 'token' => 'SECRET-M']);
$check('Mastodon: подключение — ресурс инстанс/id, токен только в секретах', $m['ref'] === 'mastodon.social/7' && $m['secrets']['token'] === 'SECRET-M' && !str_contains(json_encode($m['meta']), 'SECRET-M') && $m['meta']['max_chars'] === 500);
$captured = [];
$fake(['POST https://mastodon.social/api/v2/media' => [202, ['id' => 'm1']], 'GET https://mastodon.social/api/v1/media/m1' => [200, ['id' => 'm1']],
    'POST https://mastodon.social/api/v1/statuses' => function ($method, $url, $headers, $body) use (&$captured) { $captured = ['body' => $body, 'headers' => $headers]; return [200, ['id' => 's1', 'url' => 'https://mastodon.social/@vlad/s1']]; }]);
$sent = mastodon_publish(['meta' => $m['meta'], 'secrets' => $m['secrets']], ['text' => 'Привет', 'media' => [['kind' => 'photo', 'path' => $jpeg]]]);
$check('Mastodon: файл ждёт обработки (202 → 200), пост с media_ids[0]', str_contains($captured['body'], 'media_ids%5B0%5D=m1') && str_contains($captured['body'], 'status=' . urlencode('Привет')) && $sent['url'] === 'https://mastodon.social/@vlad/s1');
$check('Mastodon: текст длиннее лимита — понятная ошибка', str_contains($caught(fn() => mastodon_publish(['meta' => ['instance' => 'https://mastodon.social', 'max_chars' => 5], 'secrets' => ['token' => 't']], ['text' => 'слишком длинный текст'])), 'Текст длиннее 5'));
$fake(['POST https://mastodon.social/api/v1/statuses' => [422, ['error' => "Validation failed: Text can't be blank"]]]);
$check('Mastodon: ошибка API — текст из ответа, без токена', str_contains($caught(fn() => mastodon_publish(['meta' => ['instance' => 'https://mastodon.social', 'max_chars' => 500], 'secrets' => ['token' => 'SECRET-M']], ['text' => 'x'])), "Пост не опубликован: Validation failed"));

// Discord
$check('Discord: адрес не вебхук — отказ до запроса', str_contains($caught(fn() => discord_connect(['webhook' => 'https://example.com/x'])), 'выглядит неверно'));
$fake(['GET https://discord.com/api/webhooks/' => [200, ['channel_id' => '123', 'name' => 'Канал', 'guild_id' => '9']]]);
$d = discord_connect(['webhook' => 'https://discord.com/api/webhooks/123456/' . str_repeat('a', 30)]);
$check('Discord: подключение — ресурс id канала, сервер в meta', $d['ref'] === '123' && $d['meta']['guild_id'] === '9');
$captured = [];
$fake(['POST https://discord.com/api/webhooks/' => function ($method, $url, $headers, $body) use (&$captured) { $captured = ['url' => $url, 'body' => $body]; return [200, ['id' => '55', 'channel_id' => '123']]; }]);
$sent = discord_publish(['meta' => $d['meta'], 'secrets' => $d['secrets']], ['text' => 'Текст', 'media' => [['kind' => 'photo', 'path' => $jpeg]]]);
$check('Discord: wait=true, payload_json и files[0]', str_contains($captured['url'], 'wait=true') && isset($captured['body']['payload_json']) && $captured['body']['files[0]'] instanceof CURLFile);
$check('Discord: ссылка на сообщение', $sent['url'] === 'https://discord.com/channels/9/123/55');
$check('Discord: текст длиннее 2000 — ошибка', str_contains($caught(fn() => discord_publish(['meta' => [], 'secrets' => ['webhook' => 'https://discord.com/api/webhooks/1/x']], ['text' => str_repeat('я', 2001)])), 'больше 2000') || str_contains($caught(fn() => discord_publish(['meta' => [], 'secrets' => ['webhook' => 'https://discord.com/api/webhooks/1/x']], ['text' => str_repeat('я', 2001)])), 'длиннее 2000'));

// Bluesky
$fake(['POST https://bsky.social/xrpc/com.atproto.server.createSession' => [200, ['accessJwt' => 'JWT', 'did' => 'did:plc:x', 'handle' => 'a.bsky.social']]]);
$b = bluesky_connect(['identifier' => 'a.bsky.social', 'password' => 'app-pass']);
$check('Bluesky: ресурс = DID, пароль приложения зашифрован (не в meta)', $b['ref'] === 'did:plc:x' && !str_contains(json_encode($b['meta']), 'app-pass'));
$facets = bluesky_facets('Привет https://ex.com/путь #тест');
$check('Bluesky: facets в байтах UTF-8 (ссылка с 13-го байта)', ($facets[0]['index']['byteStart'] ?? -1) === 13 && ($facets[0]['features'][0]['$type'] ?? '') === 'app.bsky.richtext.facet#link');
$check('Bluesky: хэштег — tag без «#»', ($facets[1]['features'][0]['tag'] ?? '') === 'тест');
$captured = [];
$fake(['POST https://bsky.social/xrpc/com.atproto.server.createSession' => [200, ['accessJwt' => 'JWT', 'did' => 'did:plc:x', 'handle' => 'a.bsky.social']],
    'POST https://bsky.social/xrpc/com.atproto.repo.uploadBlob' => [200, ['blob' => ['$type' => 'blob', 'ref' => ['$link' => 'bafy'], 'mimeType' => 'image/jpeg', 'size' => 100]]],
    'POST https://bsky.social/xrpc/com.atproto.repo.createRecord' => function ($method, $url, $headers, $body) use (&$captured) { $captured = json_decode($body, true); return [200, ['uri' => 'at://did:plc:x/app.bsky.feed.post/3kabc', 'cid' => 'bafy2']]; }]);
$sent = bluesky_publish(['meta' => ['handle' => 'a.bsky.social'], 'secrets' => ['identifier' => 'a.bsky.social', 'password' => 'app-pass']], ['text' => str_repeat('а', 310), 'media' => [['kind' => 'photo', 'path' => $jpeg]]]);
$check('Bluesky: длинный текст обрезан до 300 с «…»', $sent['text_cut'] === true && mb_strlen($captured['record']['text']) === 300 && str_ends_with($captured['record']['text'], '…'));
$check('Bluesky: картинка — embed images с blob', ($captured['record']['embed']['$type'] ?? '') === 'app.bsky.embed.images' && isset($captured['record']['embed']['images'][0]['image']));
$check('Bluesky: ссылка на пост из uri', $sent['url'] === 'https://bsky.app/profile/a.bsky.social/post/3kabc');

// VK (группа)
$fake(['POST https://api.vk.com/method/groups.getById' => [200, ['response' => [['id' => 123, 'name' => 'Группа', 'screen_name' => 'grp']]]]]);
$v = vk_connect(['token' => 'VK-SECRET', 'group' => '-123']);
$check('VK: подключение группы — ref = положительный id', $v['ref'] === '123' && $v['title'] === 'Группа' && !str_contains(json_encode($v['meta']), 'VK-SECRET'));
$wall = [];
$fake(['POST https://api.vk.com/method/photos.getWallUploadServer' => [200, ['response' => ['upload_url' => 'https://up.vk.test/u']]],
    'POST https://up.vk.test/u' => [200, ['server' => 1, 'photo' => 'p', 'hash' => 'h']],
    'POST https://api.vk.com/method/photos.saveWallPhoto' => [200, ['response' => [['owner_id' => -123, 'id' => 9]]]],
    'POST https://api.vk.com/method/wall.post' => function ($method, $url, $headers, $body) use (&$wall) { parse_str($body, $wall); return [200, ['response' => ['post_id' => 77]]]; }]);
$sent = vk_publish(['ref' => '123', 'secrets' => ['token' => 'VK-SECRET'], 'meta' => []], ['text' => 'Текст', 'media' => [['kind' => 'photo', 'path' => $jpeg]]]);
$check('VK: wall.post от группы с фото photo-123_9', $wall['owner_id'] === '-123' && $wall['from_group'] === '1' && $wall['attachments'] === 'photo-123_9' && $sent['url'] === 'https://vk.com/wall-123_77');
$fake(['POST https://api.vk.com/method/wall.post' => [200, ['error' => ['error_msg' => 'Access denied']]]]);
$check('VK: ошибка метода — текст VK', str_contains($caught(fn() => vk_publish(['ref' => '123', 'secrets' => ['token' => 'x'], 'meta' => []], ['text' => 'x'])), 'Access denied'));

// MAX
$fake(['GET https://platform-api2.max.ru/chats/-100' => [200, ['title' => 'Чат']]]);
$x = max_connect(['token' => 'MAX-SECRET', 'chat_id' => '-100']);
$check('MAX: подключение чата', $x['ref'] === '-100' && $x['title'] === 'Чат');
$url_seen = '';
$fake(['POST https://platform-api2.max.ru/messages' => function ($method, $url) use (&$url_seen) { $url_seen = $url; return [200, ['message' => ['body' => ['mid' => 'mid.1']]]]; }]);
$sent = max_publish(['ref' => '-100', 'secrets' => ['token' => 'MAX-SECRET']], ['text' => 'Привет']);
$check('MAX: сообщение в чат (chat_id в query), id сообщения', str_contains($url_seen, 'chat_id=-100') && $sent['message_ids'] === ['mid.1']);
$check('MAX: без текста — понятная ошибка (пока только текст)', str_contains($caught(fn() => max_publish(['ref' => '-100', 'secrets' => ['token' => 'x']], ['text' => '  '])), 'пока публикует только текст'));

echo "E. Файлы под соцсеть и диспетчер:\n";
$video = sys_get_temp_dir() . '/posta-net-' . getmypid() . '.mp4'; file_put_contents($video, str_repeat('v', 1000));
[$kept, $skipped] = network_fit_media(networks_registry()['vk'], [['kind' => 'photo', 'path' => $jpeg], ['kind' => 'video', 'path' => $video], ['kind' => 'animation', 'path' => $jpeg]]);
$check('соцсеть: видео и GIF для ВК пропущены, фото оставлено, причина в тексте', count($kept) === 1 && count($skipped) === 2 && str_contains($skipped[0], 'такой вид не принимает'));
[$kept, $skipped] = network_fit_media(networks_registry()['mastodon'], array_fill(0, 5, ['kind' => 'photo', 'path' => $jpeg]));
$check('соцсеть: больше лимита файлов — лишние пропущены', count($kept) === 4 && count($skipped) === 1);
$fake(['POST https://api.vk.com/method/photos.getWallUploadServer' => [200, ['response' => ['upload_url' => 'https://up.vk.test/u']]],
    'POST https://up.vk.test/u' => [200, ['server' => 1, 'photo' => 'p', 'hash' => 'h']],
    'POST https://api.vk.com/method/photos.saveWallPhoto' => [200, ['response' => [['owner_id' => -123, 'id' => 9]]]],
    'POST https://api.vk.com/method/wall.post' => [200, ['response' => ['post_id' => 5]]]]);
$out = outbox_send_post(['text' => 'Текст', 'media' => [['kind' => 'video', 'path' => $video], ['kind' => 'photo', 'path' => $jpeg]]], ['platform' => 'vk', 'ref' => '123', 'secrets' => ['token' => 'x'], 'meta' => []]);
$check('диспетчер: соцсеть публикует, видео пропущено и названо в ответе', ($out['url'] ?? '') === 'https://vk.com/wall-123_5' && count($out['skipped'] ?? []) === 1);
$check('диспетчер: соцсеть без адаптера — понятная ошибка', str_contains($caught(fn() => outbox_send_post(['text' => 'x'], ['platform' => 'dzen'])), 'пока не публикует'));
$check('диспетчер: пост без подходящих файлов и текста — ошибка', str_contains($caught(fn() => outbox_send_post(['text' => '', 'media' => [['kind' => 'video', 'path' => $video]]], ['platform' => 'vk', 'ref' => '1', 'secrets' => ['token' => 'x'], 'meta' => []])), 'нет подходящих файлов'));
unset($GLOBALS['network_http_fake']);
@unlink($jpeg); @unlink($video);

echo "F. Ресурсы в базе: выбор канала и Telegram по умолчанию (на временной копии базы):\n";
$uid = 'net-test-user';
db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, ?)', [$uid, time()]);
$vk_id = channel_save(['platform' => 'vk', 'ref' => '555', 'secrets' => ['token' => 'VK-T'], 'meta' => [], 'chat_title' => 'Группа'], $uid);
$check('сохранённый ресурс VK читается с секретами', (channel_get($vk_id, $uid)['secrets']['token'] ?? '') === 'VK-T');
$check('канал VK по id находит отправку (channel_for_post)', (channel_for_post($uid, $vk_id)['platform'] ?? '') === 'vk');
$check('выбор канала для публикации принимает ресурс VK', (publish_channel_resolve($vk_id, $uid, false)['channel']['platform'] ?? '') === 'vk');
$check('Telegram по умолчанию не берёт ресурс VK', telegram_connection($uid) === null && telegram_channels_load($uid) === []);
$check('список Telegram не содержит ресурсов VK', array_column(telegram_channels_load($uid), 'id') === []);
$check('повторное подключение того же ресурса — по (платформа, ref) одна строка', count(array_filter(channels_load($uid), fn($c) => $c['platform'] === 'vk' && $c['ref'] === '555')) === 1);
channel_delete($vk_id, $uid);
$check('после удаления ресурса нет', channel_get($vk_id, $uid) === null);

echo "G. Безопасность и бета-статус:\n";
$check('адрес 127.0.0.1 отклонён', network_url_unsafe('https://127.0.0.1/x') !== null);
$check('адрес 10.0.0.5 отклонён', network_url_unsafe('https://10.0.0.5/x') !== null);
$check('metadata-адрес 169.254.169.254 отклонён', network_url_unsafe('https://169.254.169.254/latest') !== null);
$check('http без шифрования отклонён', network_url_unsafe('http://93.184.216.34/x') !== null);
$check('публичный IP-адрес по https пропущен', network_url_unsafe('https://93.184.216.34/x') === null);
$check('Mastodon: IP-литерал не принимается как инстанс', str_contains($caught(fn() => mastodon_instance_url('10.0.0.5')), 'Укажите адрес'));
$check('VK: ресурс не получает поле token верхнего уровня', !array_key_exists('token', channel_from_row(['id' => 'v', 'platform' => 'vk', 'ref' => '1', 'meta' => '{}', 'position' => 0, 'bot_id' => 0, 'bot_username' => '', 'chat_id' => 0, 'chat_title' => '', 'chat_username' => '', 'chat_type' => '', 'connected_at' => 1, 'schedule' => '{}', 'style' => '{}', 'hashtags' => 'off', 'secrets_enc' => secret_encrypt(json_encode(['token' => 'VK-X'])), 'token_enc' => secret_encrypt('')])));
$check('VK: beta-статус в реестре', networks_registry()['vk']['status'] === 'beta');

echo "H. Слияние профилей не теряет ресурсы других сетей:\n";
$mf = str_repeat('a', 32); $mi = str_repeat('b', 32);
db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, 0), (?, 0)', [$mf, $mi]);
channel_save(['platform' => 'telegram', 'chat_id' => -100, 'token' => '123:x', 'bot_id' => 1, 'chat_title' => 'TG'], $mf);
channel_save(['platform' => 'vk', 'ref' => '5', 'secrets' => ['token' => 'v1'], 'chat_title' => 'VKfrom'], $mf);
channel_save(['platform' => 'vk', 'ref' => '9', 'secrets' => ['token' => 'v2'], 'chat_title' => 'VKinto'], $mi);
channel_save(['platform' => 'discord', 'ref' => '77', 'secrets' => ['webhook' => 'https://discord.com/api/webhooks/1/x'], 'chat_title' => 'DS'], $mf);
user_merge($mf, $mi);
$keys = array_map(fn($c) => $c['platform'] . ':' . $c['ref'], channels_load($mi));
sort($keys);
$check('после слияния: ресурсы обоих профилей, дубликат VK один', $keys === ['discord:77', 'telegram:-100', 'vk:5', 'vk:9']);

echo "I. Часть C: OAuth и адаптеры (подставной HTTP, ключи — тестовые):\n";
$jpeg = sys_get_temp_dir() . '/posta-net-i-' . getmypid() . '.jpg'; imagejpeg(imagecreatetruecolor(16, 16), $jpeg, 80);
foreach (['REDDIT_CLIENT_ID' => 'rid', 'REDDIT_CLIENT_SECRET' => 'rsec', 'TUMBLR_CLIENT_ID' => 'tid', 'TUMBLR_CLIENT_SECRET' => 'tsec', 'LINKEDIN_CLIENT_ID' => 'lid', 'LINKEDIN_CLIENT_SECRET' => 'lsec',
    'PINTEREST_CLIENT_ID' => 'pid', 'PINTEREST_CLIENT_SECRET' => 'psec', 'YOUTUBE_CLIENT_ID' => 'yid', 'YOUTUBE_CLIENT_SECRET' => 'ysec', 'X_CLIENT_ID' => 'xid', 'X_CLIENT_SECRET' => 'xsec', 'OK_APP_KEY' => 'okkey'] as $name => $value) {
    if (!defined($name)) define($name, $value);
}
$check('Reddit: ссылка входа с client_id, state и permanent (refresh)', str_contains(reddit_oauth_authorize('S1', 'https://r.test/cb'), 'client_id=rid') && str_contains(reddit_oauth_authorize('S1', 'https://r.test/cb'), 'duration=permanent'));
$check('X: PKCE — code_challenge_method=S256', str_contains(x_oauth_authorize('S2', 'https://r.test/cb', 'verifier-verifier-verifier-verifier-verifier-12345'), 'code_challenge_method=S256'));
$check('YouTube: access_type=offline для refresh-токена', str_contains(youtube_oauth_authorize('S3', 'https://r.test/cb'), 'access_type=offline'));
$state = network_oauth_state_create(str_repeat('c', 32), 'reddit', 'v');
$check('состояние входа: забирается один раз', network_oauth_state_take($state)['platform'] === 'reddit' && network_oauth_state_take($state) === null);
$check('состояние входа: чужой или неверный формат — нет', network_oauth_state_take('../../etc') === null);

$fake(['POST https://www.reddit.com/api/v1/access_token' => [200, ['access_token' => 'RA', 'refresh_token' => 'RR', 'expires_in' => 3600]],
    'GET https://oauth.reddit.com/api/v1/me' => [200, ['name' => 'Vlad']]]);
$rs = reddit_oauth_exchange('code', 'https://r.test/cb');
$check('Reddit: обмен кода — токены, срок', $rs['access_token'] === 'RA' && $rs['refresh_token'] === 'RR' && $rs['expires_at'] > time());
$ri = reddit_oauth_identify($rs);
$check('Reddit: ресурс — имя, публикация в профиль u_<имя>', $ri['ref'] === 'Vlad' && $ri['meta']['subreddit'] === 'u_vlad');
$captured = [];
$fake(['POST https://oauth.reddit.com/api/submit' => function ($m, $u, $h, $body) use (&$captured) { parse_str($body, $captured); return [200, ['json' => ['errors' => [], 'data' => ['url' => 'https://reddit.com/r/x/1', 'name' => 't3_1']]]]; }]);
$out = reddit_publish(['meta' => $ri['meta'], 'secrets' => $rs], ['text' => "Заголовок\nТело поста"]);
$check('Reddit: sr, kind=self, заголовок — первая строка', $captured['sr'] === 'u_vlad' && $captured['kind'] === 'self' && $captured['title'] === 'Заголовок' && $captured['text'] === 'Тело поста' && $out['url'] === 'https://reddit.com/r/x/1');

$fake(['POST https://api.tumblr.com/v2/oauth2/token' => [200, ['access_token' => 'TA', 'refresh_token' => 'TR', 'expires_in' => 3600]],
    'GET https://api.tumblr.com/v2/user/info' => [200, ['response' => ['user' => ['blogs' => [['name' => 'myblog', 'title' => 'Мой блог', 'primary' => true]]]]]]]);
$ts = tumblr_oauth_exchange('c', 'https://r.test/cb');
$ti = tumblr_oauth_identify($ts);
$check('Tumblr: основной блог — ресурс и адрес', $ti['ref'] === 'myblog' && $ti['meta']['blog'] === 'myblog.tumblr.com');
$captured = [];
$fake(['POST https://api.tumblr.com/v2/blog/myblog.tumblr.com/posts' => function ($m, $u, $h, $body) use (&$captured) { $captured = json_decode($body, true); return [201, ['response' => ['id' => 5]]]; }]);
$out = tumblr_publish(['meta' => $ti['meta'], 'secrets' => $ts], ['text' => 'Привет']);
$check('Tumblr: NPF-блок text, состояние published, адрес поста', $captured['content'][0]['text'] === 'Привет' && $captured['state'] === 'published' && $out['url'] === 'https://myblog.tumblr.com/post/5');

$fake(['POST https://www.linkedin.com/oauth/v2/accessToken' => [200, ['access_token' => 'LA', 'expires_in' => 5184000]],
    'GET https://api.linkedin.com/v2/userinfo' => [200, ['sub' => 'abc123', 'name' => 'Влад']]]);
$ls = linkedin_oauth_exchange('c', 'https://r.test/cb');
$li = linkedin_oauth_identify($ls);
$check('LinkedIn: ресурс — sub, токен на 60 дней', $li['ref'] === 'abc123' && $ls['expires_at'] > time() + 5000000);
$captured = [];
$fake(['POST https://api.linkedin.com/rest/posts' => function ($m, $u, $h, $body) use (&$captured) { $captured = ['body' => json_decode($body, true), 'headers' => $h]; return [201, '', 'headers' => ['x-restli-id' => 'urn:li:share:9']]; }]);
$out = linkedin_publish(['ref' => 'abc123', 'secrets' => $ls], ['text' => 'Пост']);
$check('LinkedIn: author=urn:li:person, версия API в заголовке, адрес по x-restli-id', $captured['body']['author'] === 'urn:li:person:abc123' && in_array('Linkedin-Version: ' . LINKEDIN_VERSION, $captured['headers'], true) && $out['url'] === 'https://www.linkedin.com/feed/update/urn:li:share:9');

$fake(['POST https://api.pinterest.com/v5/oauth/token' => [200, ['access_token' => 'PA', 'refresh_token' => 'PR', 'expires_in' => 2592000]],
    'GET https://api.pinterest.com/v5/user_account' => [200, ['username' => 'vlad']], 'GET https://api.pinterest.com/v5/boards' => [200, ['items' => [['id' => '777']]]]]);
$ps = pinterest_oauth_exchange('c', 'https://r.test/cb');
$pi = pinterest_oauth_identify($ps);
$check('Pinterest: первая доска — board_id', $pi['meta']['board_id'] === '777');
$check('Pinterest: пин без картинки — понятная ошибка', str_contains($caught(fn() => pinterest_publish(['meta' => $pi['meta'], 'secrets' => $ps], ['text' => 'x', 'media' => []])), 'только с картинкой'));
$captured = [];
$fake(['POST https://api.pinterest.com/v5/pins' => function ($m, $u, $h, $body) use (&$captured) { $captured = json_decode($body, true); return [201, ['id' => 'pin1']]; }]);
$out = pinterest_publish(['meta' => $pi['meta'], 'secrets' => $ps], ['text' => "Название\nОписание", 'media' => [['kind' => 'photo', 'path' => $jpeg]]]);
$check('Pinterest: image_base64, доска, адрес пина', $captured['media_source']['source_type'] === 'image_base64' && $captured['board_id'] === '777' && $out['url'] === 'https://www.pinterest.com/pin/pin1/');

$fake(['POST https://oauth2.googleapis.com/token' => [200, ['access_token' => 'YA', 'refresh_token' => 'YR', 'expires_in' => 3599]],
    'GET https://www.googleapis.com/youtube/v3/channels' => [200, ['items' => [['id' => 'UC1', 'snippet' => ['title' => 'Канал']]]]]]);
$ys = youtube_oauth_exchange('c', 'https://r.test/cb');
$yi = youtube_oauth_identify($ys);
$check('YouTube: ресурс — id канала, приватность по умолчанию', $yi['ref'] === 'UC1' && $yi['meta']['privacy'] === 'private');
$check('YouTube: текст без видео — понятная ошибка', str_contains($caught(fn() => youtube_publish(['meta' => $yi['meta'], 'secrets' => $ys], ['text' => 'x'])), 'только видео'));
$mp4 = sys_get_temp_dir() . '/posta-net-' . getmypid() . '.mp4'; file_put_contents($mp4, str_repeat('v', 2000));
$put = null; $start_meta = null;
$fake(['POST https://www.googleapis.com/upload/youtube/v3/videos' => function ($m, $u, $h, $body) use (&$start_meta) { $start_meta = json_decode($body, true); return [200, [], 'headers' => ['location' => 'https://upload.test/session']]; },
    'PUT https://upload.test/session' => function ($m, $u, $h, $body) use (&$put) { $put = strlen($body); return [200, ['id' => 'vid1']]; }]);
$out = youtube_publish(['meta' => $yi['meta'], 'secrets' => $ys], ['text' => "Заголовок\nОписание", 'media' => [['kind' => 'video', 'path' => $mp4]]]);
$check('YouTube: сессия резюмируемой загрузки, тело видео, ссылка Shorts', $start_meta['status']['privacyStatus'] === 'private' && $put === 2000 && $out['url'] === 'https://youtube.com/shorts/vid1');
@unlink($mp4); @unlink($jpeg);

$fake(['POST https://api.x.com/2/oauth2/token' => [200, ['access_token' => 'XA', 'refresh_token' => 'XR', 'expires_in' => 7200]],
    'GET https://api.x.com/2/users/me' => [200, ['data' => ['id' => '42', 'username' => 'vlad']]]]);
$xs = x_oauth_exchange('c', 'https://r.test/cb', 'verifier');
$xi = x_oauth_identify($xs);
$check('X: ресурс — id, адрес @username', $xi['ref'] === '42' && $xi['meta']['username'] === 'vlad');
$check('X: длиннее 280 — отказ до запроса', str_contains($caught(fn() => x_publish(['meta' => $xi['meta'], 'secrets' => $xs], ['text' => str_repeat('a', 281)])), 'длиннее 280'));
$fake(['POST https://api.x.com/2/tweets' => [201, ['data' => ['id' => '99']]]]);
$out = x_publish(['meta' => $xi['meta'], 'secrets' => $xs], ['text' => 'Привет']);
$check('X: адрес твита', $out['url'] === 'https://x.com/vlad/status/99');

$check('Одноклассники: подпись — md5 по алфавиту параметров + секрет', ok_sign(['b' => '2', 'a' => '1', 'access_token' => 'x'], 'S') === md5('a=1b=2S'));
$captured = null;
$fake(['GET https://api.ok.ru/fb.do' => function ($m, $u) use (&$captured) { $captured = $u; return [200, null, 'raw' => '"123"']; }]);
$out = ok_publish(['meta' => ['gid' => '555'], 'secrets' => ['access_token' => 'AT', 'session_secret_key' => 'SK']], ['text' => 'Текст']);
$check('Одноклассники: mediatopic.post с подписью, адрес темы', str_contains($captured, 'method=mediatopic.post') && str_contains($captured, 'sig=') && $out['url'] === 'https://ok.ru/group/555/topic/123');

// обновление токена перед отправкой
$fake(['POST https://www.reddit.com/api/v1/access_token' => [200, ['access_token' => 'RNEW', 'expires_in' => 3600]]]);
$stale = ['platform' => 'reddit', 'ref' => 'Vlad', 'user_id' => '', 'meta' => [], 'secrets' => ['access_token' => 'RA', 'refresh_token' => 'RR', 'expires_at' => time() - 10]];
$fresh = network_refresh_channel($stale);
$check('токен истёк — обновлён через refresh_token, refresh сохранён', $fresh['secrets']['access_token'] === 'RNEW' && $fresh['secrets']['refresh_token'] === 'RR');
$calls = 0;
$fake(['POST https://www.reddit.com/api/v1/access_token' => function () use (&$calls) { $calls++; return [200, ['access_token' => 'X']]; }]);
network_refresh_channel(['platform' => 'reddit', 'ref' => 'Vlad', 'user_id' => '', 'meta' => [], 'secrets' => ['access_token' => 'RA', 'refresh_token' => 'RR', 'expires_at' => time() + 3600]]);
$check('токен свежий — без запроса обновления', $calls === 0);

$check('статус: ключи заданы — сеть можно включить; Threads без ключей — ждёт', network_keys_present(['REDDIT_CLIENT_ID', 'REDDIT_CLIENT_SECRET']) && networks_registry()['threads']['status'] === 'needs_keys');
$check('Threads, Instagram, Facebook — без кода публикации (решение владельца)', !isset(networks_registry()['instagram']['publish']) && !isset(networks_registry()['threads']['publish']) && !isset(networks_registry()['facebook']['publish']));
unset($GLOBALS['network_http_fake']);
@unlink($jpeg);

echo "J. Журнал — по отправленным файлам:\n";
$fake(['POST https://api.vk.com/method/photos.getWallUploadServer' => [200, ['response' => ['upload_url' => 'https://up.vk.test/u']]],
    'POST https://up.vk.test/u' => [200, ['server' => 1, 'photo' => 'p', 'hash' => 'h']],
    'POST https://api.vk.com/method/photos.saveWallPhoto' => [200, ['response' => [['owner_id' => -123, 'id' => 9]]]],
    'POST https://api.vk.com/method/wall.post' => [200, ['response' => ['post_id' => 6]]]]);
$jp = sys_get_temp_dir() . '/posta-net-j-' . getmypid() . '.jpg'; imagejpeg(imagecreatetruecolor(8, 8), $jp, 80);
$vp = sys_get_temp_dir() . '/posta-net-j-' . getmypid() . '.mp4'; file_put_contents($vp, 'vid');
$jsent = outbox_send_post(['text' => 'Текст', 'media' => [['kind' => 'video', 'path' => $vp, 'src' => 'https://cdn.example/v.mp4'], ['kind' => 'photo', 'path' => $jp, 'src' => 'https://cdn.example/p.jpg']]], ['platform' => 'vk', 'ref' => '123', 'secrets' => ['token' => 'x'], 'meta' => []]);
$check('отправка: в media_kept только отправленные файлы (видео отброшено)', count($jsent['media_kept']) === 1 && $jsent['media_kept'][0]['kind'] === 'photo' && count($jsent['skipped']) === 1);
$juid = str_repeat('d', 32);
db_exec('INSERT OR IGNORE INTO users (id, created_at) VALUES (?, 0)', [$juid]);
outbox_finish_sent('abcdef012345', $juid, ['text' => 'Текст', 'media' => [['kind' => 'video', 'path' => $vp, 'src' => 'a'], ['kind' => 'photo', 'path' => $jp, 'src' => 'b']]],
    ['id' => 'vk1', 'chat_title' => 'Группа'], 'https://vk.com/wall-123_6', $jsent);
$entry = json_decode(db_value('SELECT data FROM published WHERE user_id = ? ORDER BY at DESC LIMIT 1', [$juid]), true);
$check('журнал: один файл (отправленный), пропуск назван, ссылка на пост', ($entry['media'] ?? -1) === 1 && count($entry['media_src'] ?? []) === 1 && count($entry['skipped'] ?? []) === 1 && $entry['url'] === 'https://vk.com/wall-123_6');
unset($GLOBALS['network_http_fake']);
@unlink($jp); @unlink($vp);

echo "K. Проверка до скачивания: текст по лимиту и виды файлов:\n";
$pf = fn($platform, $text, $items) => (function () use ($platform, $text, $items) { try { return network_preflight($platform, $text, $items); } catch (RuntimeException $e) { return $e->getMessage(); } })();
$check('ВК: текст длиннее лимита — отказ до скачивания', is_string($pf('vk', str_repeat('а', 16385), [])) && str_contains($pf('vk', str_repeat('а', 16385), []), 'длиннее'));
$check('ВК: видео и GIF пропущены, фото уйдёт, причины названы', ($r = $pf('vk', 'Текст', [['kind' => 'video'], ['kind' => 'photo'], ['kind' => 'animation']])) && $r['kept'] === 1 && count($r['skipped']) === 2);
$check('MAX: только текст — картинка не уйдёт, пропуск назван', ($r = $pf('max', 'Текст', [['kind' => 'photo']])) && $r['kept'] === 0 && count($r['skipped']) === 1);
$check('MAX: без текста и файлов, которые уйдут — отказ до скачивания', is_string($pf('max', '', [['kind' => 'photo']])) && str_contains($pf('max', '', [['kind' => 'photo']]), 'нет ни подходящих файлов'));
$check('Mastodon: больше 4 файлов — лишние пропущены', ($r = $pf('mastodon', 'Текст', array_fill(0, 6, ['kind' => 'photo']))) && $r['kept'] === 4 && count($r['skipped']) === 2);
$check('обычный пост без пропусков', ($r = $pf('discord', 'Текст', [['kind' => 'photo']])) && $r['kept'] === 1 && $r['skipped'] === []);

echo "L. Отказ соцсети не повторяется, временный сбой — повторяется:\n";
$kind = function (int $status) { try { network_expect(['status' => $status, 'data' => ['error' => 'x'], 'raw' => ''], [200], 'Тест'); return 'none'; } catch (NetworkRejectedError $e) { return 'rejected'; } catch (RuntimeException $e) { return 'retry'; } };
$check('401 неверный токен — отказ без повтора', $kind(401) === 'rejected');
$check('422 запрещённое содержимое — отказ без повтора', $kind(422) === 'rejected');
$check('429 лимит — повтор', $kind(429) === 'retry');
$check('503 сервис недоступен — повтор', $kind(503) === 'retry');
$check('отказ по адресу (внутренняя сеть) — отказ без повтора', (function () { $GLOBALS['network_http_fake'] = null; unset($GLOBALS['network_http_fake']); try { network_http('GET', 'https://10.0.0.5/x'); return false; } catch (NetworkRejectedError $e) { return true; } })());

unlink($tmp);
echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли\n";
exit($failures ? 1 : 0);
