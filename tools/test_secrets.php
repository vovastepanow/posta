<?php
// Проверка шифрования секретов с привязкой (AAD). Запускать на копии базы: POSTA_DB_FILE=<копия> POSTA_MEDIA_CACHE_DIR=<tmp> php tools/test_secrets.php
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
if (MEDIA_CACHE_DIR === '/var/lib/posta/media') exit("Тест запущен с настоящей папкой медиа: задайте POSTA_MEDIA_CACHE_DIR\n");

$failures = 0;
$check = function ($name, $ok) use (&$failures) { echo ($ok ? 'OK ' : 'FAIL ') . $name . "\n"; if (!$ok) $failures++; };

echo "1. Формат:\n";
$e = secret_encrypt('тайна', 'a|b');
$check('формат 2 с версией ключа', str_starts_with($e, '2.' . SECRET_KEY_VERSION . '.'));
$check('та же привязка — расшифровывается', secret_decrypt($e, 'a|b') === 'тайна');
$check('другая привязка — отказ', secret_decrypt($e, 'a|c') === null);
$check('без привязки — отказ', secret_decrypt($e) === null);
$check('подмена байта — отказ', secret_decrypt(substr($e, 0, -3) . 'AAA', 'a|b') === null);
$check('другая версия ключа — отказ', secret_decrypt('2.9.' . explode('.', $e, 3)[2], 'a|b') === null);
$old = secret_encrypt('старый');
$check('старый формат без привязки читается, привязка игнорируется', secret_decrypt($old) === 'старый' && secret_decrypt($old, 'x') === 'старый');

echo "\n2. Ресурсы:\n";
$mk = function () { $u = bin2hex(random_bytes(16)); db_exec('INSERT INTO users (id, created_at) VALUES (?, ?)', [$u, time()]); return $u; };
$u1 = $mk(); $u2 = $mk();
$id = channel_save(['platform' => 'mastodon', 'ref' => 'x@y', 'secrets' => ['token' => 'T1']], $u1);
$row = db_one('SELECT * FROM channels WHERE id = ?', [$id]);
$check('новая строка: секреты в формате 2', str_starts_with($row['secrets_enc'], '2.') && str_starts_with($row['token_enc'], '2.'));
$check('секреты читаются', channel_get($id, $u1)['secrets']['token'] === 'T1');
$id2 = channel_save(['platform' => 'mastodon', 'ref' => 'x@y', 'secrets' => ['token' => 'T2']], $u1);
$check('повторное сохранение — тот же ресурс, новые секреты', $id2 === $id && channel_get($id, $u1)['secrets']['token'] === 'T2');
db_exec('UPDATE channels SET secrets_enc = ? WHERE id = ?', [secret_encrypt(json_encode(['token' => 'EVIL'])), $id]);
$legacy_ok = channel_get($id, $u1)['secrets']['token'] === 'EVIL';
$other = channel_save(['platform' => 'mastodon', 'ref' => 'other', 'secrets' => ['token' => 'T3']], $u1);
db_exec('UPDATE channels SET secrets_enc = ? WHERE id = ?', [db_value('SELECT secrets_enc FROM channels WHERE id = ?', [$other]), $id]);
$check('секрет из чужой записи не расшифровывается', channel_get($id, $u1)['secrets'] === []);
$check('старый формат без привязки по-прежнему читается', $legacy_ok);

echo "\n3. Перешифровка и слияние:\n";
db_exec('UPDATE channels SET secrets_enc = ?, token_enc = ? WHERE id = ?', [secret_encrypt(json_encode(['token' => 'OLD'])), secret_encrypt('tg'), $id]);
$check('сухой прогон считает и не пишет', channels_reseal_secrets(false) >= 1 && !str_starts_with(db_value('SELECT secrets_enc FROM channels WHERE id = ?', [$id]), '2.'));
channels_reseal_secrets(true);
$check('после перешифровки формат 2, значение то же', str_starts_with(db_value('SELECT secrets_enc FROM channels WHERE id = ?', [$id]), '2.') && channel_get($id, $u1)['secrets']['token'] === 'OLD');
$check('повторная перешифровка ничего не делает', channels_reseal_secrets(false) === 0);
channel_move_to_user($id, $u1, $u2, 0);
$check('перенос владельцу: секреты читаются под новым пользователем', channel_get($id, $u2)['secrets']['token'] === 'OLD');

foreach ([$u1, $u2] as $u) db_exec('DELETE FROM users WHERE id = ?', [$u]);
echo $failures ? "\nПровалено: $failures\n" : "\nВсе проверки прошли\n";
exit($failures ? 1 : 0);
