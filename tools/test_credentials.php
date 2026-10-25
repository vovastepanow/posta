<?php
// Проверка своего входа (логин + пароль). Только на копии базы — см. CREDENTIALS_FINISH_PROMPT.md, часть A.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
$fails = 0;
$ok = function ($cond, $name) use (&$fails) { echo ($cond ? 'OK   ' : 'FAIL ') . $name . "\n"; if (!$cond) $fails++; };
$throws = function ($fn, $class) { try { $fn(); return false; } catch (Throwable $e) { return $e instanceof $class ? $e->getMessage() : get_class($e) . ': ' . $e->getMessage(); } };
// В вебе пользователь привязан к запросу; в CLI — привязываем сами перед входом
function login_as($uid, ...$a) { app_user_bind($uid); return credentials_login($uid, ...$a); }

echo 'версия базы: ', db_value('pragma user_version'), "\n";
$n = 'credentials_login_normalize';
$ok($n('8 (999) 123-45-67') === '+79991234567', 'телефон 8… → +7');
$ok($n('9991234567') === '+79991234567', 'телефон 10 цифр → +7');
$ok($n('+380 50 123 4567') === '+380501234567', 'телефон с +');
$ok($n(' Name@Mail.RU ') === 'name@mail.ru', 'почта строчными');
$ok($n('Vova_S') === 'vova_s', 'ник');
$ok(is_string($throws(fn() => $n('a@b'), InvalidArgumentException::class)), 'плохая почта');
$ok(is_string($throws(fn() => $n('12345'), InvalidArgumentException::class)), 'короткий телефон');
$ok(is_string($throws(fn() => $n('ник'), InvalidArgumentException::class)), 'кириллический ник');

$A = bin2hex(random_bytes(16)); $sidA = session_create($A);
$ok(!user_has_identity($A), 'A анонимный');
$ok(is_string($throws(fn() => credentials_set($A, $sidA, 'tester@example.com', '1234567'), InvalidArgumentException::class)), 'короткий пароль отклонён');
credentials_set($A, $sidA, 'Tester@Example.com', 'secret-pass-1');
$ok(db_value('SELECT login FROM users WHERE id = ?', [$A]) === 'tester@example.com', 'логин сохранён');
$ok(user_has_identity($A), 'A теперь вошедший');
$ok(auth_public($A)['logged_in'] && auth_public($A)['has_password'], 'auth_public');
$ok(is_string($throws(fn() => credentials_set($A, $sidA, 'tester@example.com', 'new-pass-22', 'wrong'), InvalidArgumentException::class)), 'смена без верного текущего — нет');
$sidA2 = session_create($A);
credentials_set($A, $sidA, 'tester@example.com', 'new-pass-22', 'secret-pass-1');
$ok(session_user($sidA) === $A && session_user($sidA2) === '', 'смена пароля: текущая сессия жива, другая закрыта');
$B = bin2hex(random_bytes(16)); $sidB = session_create($B);
$ok(is_string($throws(fn() => credentials_set($B, $sidB, 'TESTER@example.com', 'whatever-123'), DomainException::class)), 'занятый логин');

$C = bin2hex(random_bytes(16)); $sidC = session_create($C);
$ok(is_string($throws(fn() => login_as($C, $sidC, 'tester@example.com', 'secret-pass-1'), InvalidArgumentException::class)), 'старый пароль не подходит');
$ok(is_string($throws(fn() => login_as($C, $sidC, 'nobody@example.com', 'x'), InvalidArgumentException::class)), 'несуществующий логин — та же ошибка');
$ok(login_as($C, $sidC, ' TESTER@example.com', 'new-pass-22') === $A, 'вход вернул A');
$ok(!db_value('SELECT 1 FROM users WHERE id = ?', [$C]), 'пустой C удалён');
$ok(session_user($sidC) === '', 'старая сессия C закрыта');

$D = bin2hex(random_bytes(16)); $sidD = session_create($D);
db_exec('INSERT INTO ledger (user_id, at, amount, kind) VALUES (?, ?, ?, ?)', [$D, time(), 100, 'test']);
$bal = fn($u) => (int)db_value('SELECT COALESCE(SUM(amount),0) FROM ledger WHERE user_id = ?', [$u]);
$before = $bal($A);
login_as($D, $sidD, 'tester@example.com', 'new-pass-22');
$ok($bal($A) === $before + 100 && !db_value('SELECT 1 FROM users WHERE id = ?', [$D]), 'данные D влиты в A, D удалён');

$E = bin2hex(random_bytes(16)); $sidE = session_create($E);
credentials_set($E, $sidE, '+7 900 000-00-01', 'pass-eeee-1');
login_as($E, $sidE, '89000000001', 'pass-eeee-1');
$ok((bool)db_value('SELECT 1 FROM users WHERE id = ?', [$E]), 'вход в свой же профиль — профиль цел');
login_as($E, '', 'tester@example.com', 'new-pass-22');
$ok(db_value('SELECT login FROM users WHERE id = ?', [$E]) === '+79000000001', 'вошедший E при входе в A не влит и не удалён');

$ok(session_resolve('', $A)['uid'] !== $A, 'старая кука вошедшего → новый профиль');

$F = bin2hex(random_bytes(16)); session_create($F);
for ($i = 0; $i < 10; $i++) $throws(fn() => login_as($F, '', 'tester@example.com', 'bad'), InvalidArgumentException::class);
$m = $throws(fn() => login_as($F, '', 'tester@example.com', 'new-pass-22'), DomainException::class);
$ok(is_string($m) && str_contains($m, 'Слишком много'), 'после 10 неудач — блок даже с верным паролем');

$G = bin2hex(random_bytes(16)); $H = bin2hex(random_bytes(16)); session_create($G); session_create($H);
credentials_set($G, '', 'merge_me', 'pass-gggg-1');
user_merge($G, $H);
$ok(db_value('SELECT login FROM users WHERE id = ?', [$H]) === 'merge_me', 'user_merge переносит логин');

// Вошедший профиль не удаляется за неактивность, анонимный — удаляется
$K = bin2hex(random_bytes(16)); session_create($K);
credentials_set($K, '', 'keep_me', 'pass-kkkk-1');
$L = bin2hex(random_bytes(16)); session_create($L);
foreach ([$K, $L] as $u) db_exec('UPDATE users SET last_seen = ?, created_at = ? WHERE id = ?', [time() - 400 * DAY_SECONDS, time() - 400 * DAY_SECONDS, $u]);
users_gc();
$ok((bool)db_value('SELECT 1 FROM users WHERE id = ?', [$K]), 'вошедший профиль не удалён за неактивность');
$ok(!db_value('SELECT 1 FROM users WHERE id = ?', [$L]), 'анонимный старый профиль удалён');

echo "Провалов: $fails\n";
exit($fails ? 1 : 0);
