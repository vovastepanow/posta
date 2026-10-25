<?php
// Воркер роликов (TikTok, YouTube Shorts, большие видео Telegram): одна задача за раз, не больше одного скачивания на сервер.
// Запускается из парсера, публикатора и обработчиков статуса; cron не нужен. Ждёт ближайшую задачу (повтор через 60 с),
// живёт не дольше YTDLP_WORKER_LIFE; дальше задачи подхватит следующий запуск (будильники — ytdlp_kick_worker).
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('ytdlp_worker.php запускается только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';

ytdlp_ensure_cache_dir();
if (!($lock = try_lock(YTDLP_CACHE_DIR . '/.worker.lock'))) exit;   // другой воркер уже работает
$started = time();
$life_end = $started + YTDLP_WORKER_LIFE;
while (time() < $life_end) {
    $job = ytdlp_next_job_within(YTDLP_WAIT_SECONDS);
    if ($job === null) break;
    $waited = false;
    // Ждём ближайшую задачу по секундам: если в очереди появилась более ранняя — берём её
    while ($job !== null && (int)$job['next_at'] > time()) {
        if (time() >= $life_end) break 2;
        sleep(1);
        $waited = true;
        $job = ytdlp_next_job_within(YTDLP_WAIT_SECONDS);
    }
    if ($job === null) break;
    if (!$waited && time() - $started >= YTDLP_WORKER_SECONDS) break;   // готовую задачу уже не начинаем: следующий запуск
    ytdlp_run_job($job);
}
flock($lock, LOCK_UN);
fclose($lock);
if (ytdlp_next_job_within(YTDLP_WAIT_SECONDS) !== null) ytdlp_kick_worker();   // задача в ближайшем окне осталась — будим себя снова
