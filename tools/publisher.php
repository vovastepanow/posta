<?php
// Публикатор очереди Posta: cron раз в минуту (см. README) или фоновый запуск со страницы вкладки.
// Одна копия за раз, не дольше 50 секунд: следующий запуск подхватит остальное.
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('publisher.php запускается только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
set_time_limit(60);
users_ensure_root();
$lock = try_lock(USERS_DIR . '/.publisher.lock');
if (!$lock) exit;   // уже работает другой запуск
$result = outbox_process_due(time(), 'outbox_send_post', time() + 50);
if ($result['sent'] || $result['failed']) echo date('c') . " отправлено {$result['sent']}, ошибок {$result['failed']}\n";
ytdlp_kick_worker();   // будильник очереди роликов (публикатор запускается регулярно)
flock($lock, LOCK_UN);
fclose($lock);
