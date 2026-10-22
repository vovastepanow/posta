#!/usr/bin/env php
<?php
// tools/ollama_worker.php — запуск радара лидов для всех пользователей с включённым радаром.
// Используется из cron раз в 10 минут: */10 * * * * /usr/bin/php /path/to/tools/cli.php leads-scan --all

if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

// Все пользователи с включённым радаром
$uids = array_filter(
    array_map(fn($row) => preg_match('/^lead_radar:(.+)$/', $row['key'], $m) ? $m[1] : null, db_all("SELECT key FROM kv WHERE key LIKE 'lead_radar:%'")),
    fn($uid) => (lead_radar_settings($uid)['enabled'] ?? false)
);

foreach ($uids as $uid) {
    try {
        lead_radar_run($uid);
    } catch (Exception $e) {
        // Ошибка одного пользователя не должна остановить остальных
    }
}
