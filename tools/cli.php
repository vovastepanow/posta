<?php
// Утилиты: php tools/cli.php <команда> [аргументы]
//   adult-allow <id> on|off        — разрешить пользователю 18+ и «Жесть» (или запретить)
//   nightly-discovery [N]          — подбор источников для N популярных тем (cron: 0 4 * * *)
//   check-threads [--dry-run]      — убрать из пулов записи удалённых тредов Двача
//   backfill-proposals [id] [--force] — достроить медиа записей «Предложки» (запускается сайтом сам)
//   backfill-engagement            — дособрать активность (просмотры, ответы) у записей пулов
//   engagement-report [файлы]      — угадывает ли активность голоса пользователей (AUC)
//   rescore-sources [--catalog] [--apply] — переоценить найденные каналы по текущим правилам
//   seed-topics                    — засеять готовое дерево тем (lib/topic_tree.php), повторный запуск ничего не меняет
//   topic-merge <откуда> <куда>    — слить тему в другую: названия и запросы переходят, дочерние темы — к ней, исходная удаляется
//   discovery-origins [тема]       — откуда берутся предложения источников и как их решают: по виду находки, доля «да»
//   discovery-run <тема> <id>      — прогон автоподбора одной темы от имени пользователя; тему в его настройках не меняет
//   tag-content [N] [--dry-run]    — метки (темы дерева) записям без меток, по описанию ИИ; N запросов к модели (20)
//   phash-backfill [--dry-run]     — для записей пула и истории без phash считает отпечаток по локальному файлу
//   phash-report [N]               — печатает N самых близких пар разных картинок (по умолчанию 30)
//   migrate-json [--apply]         — остатки JSON (ключи Telegram, лимиты, горячие темы) в базу, старые JSON-файлы — в архив
//   balance-add <id> <₽> [заметка] — пополнить баланс пользователя (пока нет приёма платежей)
//   outbox-gc                      — удалить папки файлов отложенных постов, у которых уже нет поста в очереди; миниатюры старше 90 дней без ссылок
//   outbox-repair [--dry-run] [uid] — починить очередь: файлы поста из кэша или по ссылке, заглушка в подписи; --dry-run — только список
//   channels-migrate [--dry-run]   — перенести старый канал пользователя (connections.telegram) в таблицу каналов
//   login-bot-from-channel <id>    — подключить бот входа к токену бота канала пользователя
//   users-gc [--dry-run]           — удалить пустые профили (заход без куки) и брошенные дольше USER_RETENTION_DAYS
//   users-merge <из> <в> [--dry-run] — слить два профиля, один в другой
//   user-password <id|логин> [логин] — новый пароль профиля (печатается); с логином — заодно задать/сменить логин
//   ytdlp-queue [--clear-failed]   — очередь роликов (пользователь, страница, способ, попытки, когда повтор); --clear-failed снимает исчерпавшие попытки
//   media-cleanup [--dry-run] [--force] — очистить кэш медиа (без файлов, на которые ссылаются записи)
//   media-audit [id]                   — проверить состояние медиа (только чтение): файлы, старые пути, источники
//   media-repair [id] [--dry-run]      — исправить пути, пометить записи на перезагрузку медиа
//   leads-scan [<uid>|--all] [--log=<файл>] — проход радара лидов (один пользователь или всем с включённым), журнал в stdout и файл
//   history-dedupe [--dry-run]     — убрать из истории оценок повторы одной картинки (остаётся последняя оценка)
//   radar-owner [<uid>] [--remove]     — кто видит вкладку «Радар» (без uid — показать список)
//   channel-style <id> <канал>         — изучить стиль канала по его постам (сайт запускает сам, в фоне)
//   articles-refresh [id] [--limit=40] — перечитать старые записи новостей без статьи (статья, обложка, текст); без моделей
if (PHP_SAPI !== 'cli') { http_response_code(403); exit('Только из командной строки.'); }
require_once __DIR__ . '/../bootstrap.php';
require_once __DIR__ . '/../handlers/content.php';
set_time_limit(0);
$command = $argv[1] ?? '';
$args = array_values(array_filter(array_slice($argv, 2), fn($a) => !str_starts_with($a, '--')));
$flag = fn($name) => in_array("--$name", $argv, true);
$short = fn($uid) => substr($uid, 0, 8);

switch ($command) {
case 'nightly-discovery':
    $topics = popular_topics(max(1, (int)($args[0] ?? 5)));
    echo date('c') . ' Ночной подбор источников. Темы: ' . ($topics ? implode(', ', $topics) : 'нет') . "\n";
    topic_classify_pending(10);
    foreach ($topics as $topic) {
        topic_ensure($topic);
        topic_profile_ensure($topic);
        source_discovery_due(load_source_db(), $topic) ? run_source_discovery($topic) : print("Тема «{$topic}»: подбор недавно уже был — пропускаю.\n");
    }
    echo date('c') . " Готово.\n";
    break;

case 'check-threads':
    foreach (user_ids() as $uid) {
        $r = prune_dead_pool_items($uid, $flag('dry-run'), true, false);
        printf("%s: проверено тредов %d, удалённых %d, записей %s: %d\n", $short($uid), $r['threads'], $r['gone'], $flag('dry-run') ? 'к удалению' : 'убрано', $r['removed']);
    }
    break;

case 'backfill-proposals':
    foreach ($args ? [$args[0]] : user_ids() as $uid) {
        app_user_bind($uid);
        for ($total = 0; ($n = proposal_backfill_media(10)) > 0; $total += $n);
        proposal_progress_write(['running' => false] + proposal_progress_read());
        if ($total) echo $short($uid) . ": обновлено записей: $total\n";
        if ($dead = proposal_prune_dead_threads($flag('force'))) echo $short($uid) . ": скрыто записей из мёртвых тредов: $dead\n";
    }
    break;

case 'backfill-engagement':
    $cache = [];
    foreach (user_ids() as $uid) {
        $snapshots = [];
        foreach (pool_load($uid) as $item) {
            if (empty($item['engagement']) && ($s = engagement_backfill_item($item, $cache))) $snapshots[content_image_key($item)] = $s + ['backfilled' => true];
        }
        $applied = 0;
        pool_update(function ($pool) use ($snapshots, &$applied) {
            foreach ($pool as &$i) if (empty($i['engagement']) && isset($snapshots[$k = content_image_key($i)])) { $i['engagement'] = $snapshots[$k]; $applied++; }
            return $applied ? $pool : null;
        }, $uid);
        printf("%s: дособрано %d\n", $short($uid), $applied);
    }
    break;

case 'engagement-report':
    engagement_report($args ?: user_ids());
    break;

case 'rescore-sources':
    rescore_sources($flag('catalog'), $flag('apply'));
    break;

case 'migrate-media':
    // Без --apply только отчёт. Перед --apply: копия базы (sqlite3 data/posta.sqlite ".backup …")
    $r = media_migrate(ROOT . '/media_cache', $flag('apply'));
    echo ($r['applied'] ? 'Перенесено' : 'Будет перенесено') . ": файлов {$r['files']}" . ($r['applied'] ? ", перемещено {$r['moved']}" : '') . ", строк в базе {$r['rows']}\n";
    echo "Откуда: {$r['from']}\nКуда: {$r['to']}\n";
    if (!$r['applied']) echo "Для переноса запустите с --apply.\n";
    break;

case 'hot-topics':
    $hot = hot_topics(HOT_TOPICS_LIMIT, true);
    echo $hot ? "Горячие темы (" . count($hot) . " из " . HOT_TOPICS_LIMIT . "):\n" : "Горячих тем сейчас нет.\n";
    foreach ($hot as $t) echo "  {$t['title']} — {$t['score']}\n";
    break;

case 'taxonomy-check':   // проверка карты тем: без моделей и без записи в базу; код выхода 1 при ошибках
    $check = taxonomy_check();
    echo "Веток: {$check['summary']['веток']}, частных тем: {$check['summary']['частных тем']}\n";
    foreach ($check['summary']['по веткам'] as $bkey => $n) echo "  {$bkey}: {$n}\n";
    foreach ($check['errors'] as $line) echo "ОШИБКА: {$line}\n";
    echo $check['errors'] ? "Проверка не пройдена.\n" : "Проверка пройдена.\n";
    exit($check['errors'] ? 1 : 0);
case 'discovery-run':
    // Проверка автоподбора одной темы: флаг не даёт прогону бросить тему, если пользователь её сменил
    if (($args[0] ?? '') === '' || !is_valid_user_id($args[1] ?? '')) exit("Укажите тему и id пользователя: discovery-run <тема> <id>\n");
    app_user_bind($args[1]);
    $GLOBALS['discovery_cli_fixed_topic'] = true;
    run_source_discovery($args[0]);
    break;

case 'discovery-origins':
    $topic = $args[0] ?? null;
    $rows = discovery_origin_report(load_source_db(), $topic);
    echo 'Тема: ' . ($topic ?? 'все') . "\n";
    printf("%-22s %8s %6s %10s %6s %10s\n", 'Вид находки', 'найдено', 'да', 'может быть', 'нет', 'доля «да»');
    foreach ($rows as $kind => $r) {
        $decided = $r['approve'] + $r['maybe'] + $r['reject'];
        printf("%-22s %8d %6d %10d %6d %10s\n", DISCOVERY_ORIGIN_LABELS[$kind] ?? $kind, $r['found'], $r['approve'], $r['maybe'], $r['reject'], $decided ? round(100 * $r['approve'] / $decided) . '%' : '—');
    }
    if (!$rows) echo "Предложений и решений по теме нет.\n";
    break;

case 'topic-merge':
    $from = topic_lookup($args[0] ?? '');
    $to = topic_lookup($args[1] ?? '');
    if (!$from || !$to) exit("Тема не найдена: укажите название или синоним, например topic-merge «Фап контент» «Порно».\n");
    $merged = topic_merge($from['id'], $to['id']);
    echo "«{$from['title']}» слита в «{$merged['title']}»: синонимы — " . implode(', ', $merged['aliases']) . "\n";
    break;

case 'seed-topics':
    $c = topic_tree_seed();
    echo "Дерево тем: создано {$c['created']}, обновлено {$c['updated']}, перенесено {$c['moved']}, пропущено {$c['skipped']}\n";
    foreach (topic_tree() as $branch) {
        echo $branch['title'] . ($branch['nsfw'] ? ' [18+]' : '') . "\n";
        foreach ($branch['children'] as $child) echo "  " . $child['title'] . ($branch['nsfw'] ? ' [18+]' : '') . "\n";
    }
    break;

case 'channel-reach':   // фон календаря: охват канала по часам (t.me/s/<имя>), результат — в кэше на 12 часов
    [$name, $tz] = [$args[0] ?? '', $args[1] ?? date_default_timezone_get()];
    $reach = channel_reach_by_hour(['username' => $name, 'schedule' => ['tz' => $tz]]);
    echo $reach ? "Охват {$name}: постов {$reach['posts']}, лучшие часы " . implode(', ', $reach['best']) . "\n" : "Охват {$name}: мало данных\n";
    break;
case 'channel-history':   // фон календаря: посты канала за неделю (t.me/s/<имя>), результат — в кэше на 15 минут
    $name = $args[0] ?? '';
    echo is_valid_telegram_channel($name) ? "История {$name}: постов " . channel_history_refresh($name) . "\n" : "История: неверное имя канала\n";
    break;
case 'seed-news-sites':
    $c = news_sites_seed();
    echo "Новостные сайты: добавлено {$c['added']}, обновлено {$c['updated']}, тем добавлено {$c['topics']}\n";
    break;

case 'articles-refresh':   // статьи: старые записи новостей без статьи перечитываются со страницы (без моделей; не больше --limit страниц)
    $limit = 40;
    foreach ($argv as $a) if (preg_match('~^--limit=(\d+)$~', $a, $m)) $limit = max(1, (int)$m[1]);
    foreach ($args ? [$args[0]] : user_ids() as $uid) {
        app_user_bind($uid);
        $r = articles_refresh_user($limit);
        echo $short($uid) . ": статей получено: {$r['updated']}, из них текстовых (без картинки): {$r['text']}\n";
    }
    break;

case 'tag-content':
    tag_content_backfill(max(1, (int)($args[0] ?? 20)), $flag('dry-run'));
    break;

case 'migrate-json':
    // Последний шаг переезда: настройки сайта из JSON — в kv, старые JSON-файлы — в архив data/json-archive-<дата>/ (не удаляются)
    $apply = $flag('apply');
    echo $apply ? "Перенос остатков JSON → SQLite\n" : "Только отчёт; перенести — с --apply (перед этим: sqlite3 data/posta.sqlite \".backup …\")\n";
    foreach (['telegram_app.json' => 'telegram_app_keys', 'telegram_limits.json' => 'telegram_limits', 'hot_topics.json' => 'hot_topics'] as $file => $key) {
        $data = is_file(ROOT . "/$file") ? json_decode((string)file_get_contents(ROOT . "/$file"), true) : null;
        if (!is_array($data)) continue;
        $have = kv_get($key) !== null;
        echo "  $file → kv[$key]" . ($have ? ' (в базе уже есть — файл не нужен)' : '') . "\n";
        if ($apply && !$have) kv_update($key, fn() => $data);
    }
    // Профили, которые остались только в папках users/<id>/ (прежний импорт перебирал лишь тех, кто уже был в базе). Без оценок — мусор от заходов без куки.
    foreach (glob(USERS_DIR . '/*/history.json') ?: [] as $file) {
        $uid = basename(dirname($file));
        $dir = dirname($file);
        $history = load_data($file, []);
        if (!is_valid_user_id($uid) || db_value('SELECT 1 FROM users WHERE id = ?', [$uid]) || !array_sum(array_map('count', array_intersect_key($history, array_flip(HISTORY_COLLECTIONS))))) continue;
        echo "  профиль {$short($uid)} есть только в папке — переношу в базу (оценок: " . array_sum(array_map('count', array_intersect_key($history, array_flip(HISTORY_COLLECTIONS)))) . ")\n";
        if (!$apply) continue;
        db_tx(function () use ($uid, $dir, $history) {
            db_exec('INSERT INTO users (id, created_at, last_seen) VALUES (?, ?, ?)', [$uid, (int)filectime($dir), (int)filemtime("$dir/history.json")]);
            if ($settings = load_data("$dir/settings.json", [])) db_save_settings($settings, $uid);
            history_update(fn() => $history + empty_history(), $uid);
            if ($pool = load_data("$dir/pool.json", [])) pool_update(fn() => $pool, $uid);
            if ($connections = load_data("$dir/connections.json", [])) connections_update(fn() => $connections, $uid);
            foreach (array_reverse(load_data("$dir/published.json", [])) as $entry) published_add($entry, $uid);
        });
    }
    $archive = ROOT . '/data/json-archive-' . date('Y-m-d');
    $old = array_merge(glob(ROOT . '/*.json') ?: [], glob(ROOT . '/*.json.lock') ?: [], glob(USERS_DIR . '/*/*.json') ?: [], glob(USERS_DIR . '/*/*.lock') ?: [],
        glob(USERS_DIR . '/*/{last-seen,proposals-checked,threads-checked,parser-v3.durations}', GLOB_BRACE) ?: []);
    foreach ($old as $path) {
        $rel = substr($path, strlen(ROOT) + 1);
        echo "  в архив: $rel\n";
        if (!$apply) continue;
        @mkdir(dirname("$archive/$rel"), 0770, true);
        rename($path, "$archive/$rel") || print("    не удалось перенести $rel\n");
    }
    echo $apply ? "Готово. Архив: $archive\n" : '';
    break;

case 'adult-allow':
    [$uid, $mode] = [$args[0] ?? '', $args[1] ?? ''];
    if (!is_valid_user_id($uid) || !db_value('SELECT 1 FROM users WHERE id = ?', [$uid])) exit("Нет такого пользователя: $uid\n");
    if (!in_array($mode, ['on', 'off'], true)) exit("Укажите on или off: adult-allow <id> on|off\n");
    db_exec('UPDATE users SET adult_allowed = ? WHERE id = ?', [$mode === 'on' ? 1 : 0, $uid]);
    printf("%s: 18+ %s\n", $short($uid), $mode === 'on' ? 'разрешено' : 'запрещено');
    break;

case 'balance-add':
    [$uid, $rub] = [$args[0] ?? '', (float)($args[1] ?? 0)];
    if (!is_valid_user_id($uid) || !db_value('SELECT 1 FROM users WHERE id = ?', [$uid])) exit("Нет такого пользователя: $uid\n");
    printf("%s: на балансе %.2f ₽\n", $short($uid), kop_to_rub(billing_topup($uid, $rub, implode(' ', array_slice($args, 2)) ?: 'Пополнение вручную')));
    break;

case 'outbox-gc':
    $root = ROOT . '/data/outbox';
    $removed = 0;
    foreach (is_dir($root) ? scandir($root) : [] as $name) {
        if ($name === '.' || $name === '..' || !is_dir("$root/$name") || db_value('SELECT 1 FROM outbox WHERE id = ?', [$name])) continue;
        remove_dir("$root/$name");
        $removed++;
    }
    echo "Удалено папок без поста: $removed\n";
    echo "Удалено миниатюр старше " . THUMB_KEEP_DAYS . " дней без ссылок: " . thumb_gc() . "\n";
    break;

case 'outbox-repair':
    foreach (outbox_repair(!$flag('dry-run'), $args[0] ?? null) as $line) echo $line . "\n";
    echo $flag('dry-run') ? "(--dry-run: ничего не изменено)\n" : "Готово.\n";
    break;

case 'ytdlp-queue':
    $jobs = ytdlp_queue_list();
    if (in_array('--clear-failed', array_slice($argv, 2), true)) {
        $failed = array_filter($jobs, fn($j) => (int)$j['tries'] >= YTDLP_TRIES);
        foreach ($failed as $job) ytdlp_drop_job($job);
        echo 'Снято исчерпавших попытки: ' . count($failed) . "\n";
        $jobs = ytdlp_queue_list();
    }
    echo count($jobs) . " задач в очереди\n";
    foreach ($jobs as $job) {
        printf("%s  %-40s  %-5s  попыток %d  повтор %s\n", substr((string)$job['uid'], 0, 8), mb_substr((string)$job['page'], 0, 40), ytdlp_job_via($job),
            (int)$job['tries'], (int)$job['next_at'] > 0 ? date('H:i:s', (int)$job['next_at']) : 'сейчас');
    }
    break;

case 'channels-migrate':
    foreach (db_all('SELECT id, connections FROM users') as $u) {
        $t = db_unjson($u['connections'], [])['telegram'] ?? null;
        if (!is_array($t) || empty($t['chat_id']) || empty($t['token_enc'])) continue;
        $label = $short($u['id']) . ": канал «{$t['chat_title']}» (@{$t['chat_username']})";
        if (db_value('SELECT 1 FROM channels WHERE user_id = ? AND chat_id = ?', [$u['id'], (int)$t['chat_id']])) echo "$label уже есть\n";
        elseif ($flag('dry-run')) echo "$label перенесётся\n";
        elseif (channels_import_legacy($u['id'])) echo "$label перенесён\n";
        else echo "$label не перенесён: токен не расшифровался\n";
    }
    break;

case 'login-bot-from-channel':
    $uid = $args[0] ?? '';
    if (!is_valid_user_id($uid)) exit("Некорректный id: $uid\n");
    if (!db_value('SELECT 1 FROM users WHERE id = ?', [$uid])) exit("Нет такого пользователя: $uid\n");
    $conn = telegram_connection($uid);
    if (!$conn) exit("У пользователя нет подключённого Telegram-канала\n");
    try {
        $me = telegram_bot_request($conn['token'], 'getMe');
        $bot_username = $me['username'] ?? '';
        if (!$bot_username) exit("Не удалось получить имя бота\n");
        kv_update('telegram_login_bot', fn() => ['token_enc' => secret_encrypt($conn['token']), 'username' => $bot_username, 'bot_id' => $me['id'] ?? 0]);
        echo "Бот входа: @{$bot_username}\n";
    } catch (RuntimeException $e) {
        exit("Ошибка: " . $e->getMessage() . "\n");
    }
    break;

case 'user-password':
    $who = $args[0] ?? '';
    try {
        $uid = is_valid_user_id($who) ? $who : (string)db_value('SELECT id FROM users WHERE login = ?', [credentials_login_normalize($who)]);
        if (!$uid || !db_value('SELECT 1 FROM users WHERE id = ?', [$uid])) exit("Нет такого профиля: $who\n");
        $login = credentials_login_normalize($args[1] ?? (string)db_value('SELECT login FROM users WHERE id = ?', [$uid]) ?: exit("У профиля нет логина: укажите его вторым аргументом\n"));
        if (db_value('SELECT 1 FROM users WHERE login = ? AND id <> ?', [$login, $uid])) exit("Логин $login занят другим профилем\n");
    } catch (InvalidArgumentException $e) {
        exit('Ошибка: ' . $e->getMessage() . "\n");
    }
    $password = substr(strtr(base64_encode(random_bytes(12)), '+/', 'xy'), 0, 14);
    db_exec('UPDATE users SET login = ?, password_hash = ? WHERE id = ?', [$login, password_hash($password, PASSWORD_DEFAULT), $uid]);
    session_delete_all($uid);
    echo "Профиль $uid\nЛогин: $login\nПароль: $password\nВсе сессии профиля закрыты.\n";
    break;

case 'users-merge':
    [$from, $to] = [$args[0] ?? '', $args[1] ?? ''];
    if (!is_valid_user_id($from) || !is_valid_user_id($to)) exit("Некорректные id\n");
    if ($from === $to) exit("id должны быть разными\n");
    if (!db_value('SELECT 1 FROM users WHERE id = ?', [$from])) exit("Профиль $from не найден\n");
    if (!db_value('SELECT 1 FROM users WHERE id = ?', [$to])) exit("Профиль $to не найден\n");

    $count = fn($table, $uid) => (int)db_value("SELECT COUNT(*) FROM $table WHERE user_id = ?", [$uid]);
    // Сколько оценок добавится: как в user_merge — по коллекциям, без повторов картинки
    [$from_h, $to_h, $added] = [history_load($from), history_load($to), 0];
    foreach (HISTORY_COLLECTIONS as $c) {
        $have = array_flip(array_map('content_image_key', $to_h[$c] ?? []));
        foreach ($from_h[$c] ?? [] as $r) if (($k = content_image_key($r)) !== '' && !isset($have[$k])) $added++;
    }
    printf("%s → %s%s:\n  оценок у второго: %d, добавится: %d (у первого %d, остальные — повторы)\n  операций баланса: %d, опубликованных: %d, в очереди: %d\n",
        $short($from), $short($to), $flag('dry-run') ? ' (--dry-run, без изменений)' : '', $count('history_items', $to), $added, $count('history_items', $from),
        $count('ledger', $from), $count('published', $from), $count('outbox', $from));
    if ($flag('dry-run')) break;
    user_merge($from, $to);
    printf("Готово: у %s теперь %d оценок, профиль %s удалён.\n", $short($to), $count('history_items', $to), $short($from));
    break;

case 'channel-style':
    [$uid, $channel_id] = [$args[0] ?? '', $args[1] ?? ''];
    if (!is_valid_user_id($uid) || $channel_id === '') exit("Нужны id пользователя и id канала\n");
    app_user_bind($uid);
    try {
        channel_style_learn($channel_id, $uid);
        kv_update('channel_style_job:' . $channel_id, fn() => ['status' => 'done', 'at' => time()]);
        echo "Стиль канала изучен.\n";
    } catch (Throwable $e) {
        kv_update('channel_style_job:' . $channel_id, fn() => ['status' => 'error', 'at' => time(), 'message' => $e->getMessage()]);
        echo 'Не удалось: ' . $e->getMessage() . "\n";
    }
    break;

case 'users-gc':
    printf("%s профилей: %d\n", $flag('dry-run') ? 'Будет удалено' : 'Удалено', users_gc('', $flag('dry-run')));
    break;

case 'phash-backfill':
    $dry_run = $flag('dry-run');
    $backfill_fn = function (array $list) {
        $result = [];
        foreach ($list as $item) {
            if (!is_array($item) || ($item['phash'] ?? '') !== '') { $result[] = $item; continue; }
            $local_path = media_cache_path($item['image_local'] ?? '');
            if (!is_file($local_path)) { $result[] = $item; continue; }
            $bytes = file_get_contents($local_path);
            if ($phash = image_phash($bytes)) $item['phash'] = $phash;
            $result[] = $item;
        }
        return $result;
    };
    $computed = $skipped = 0;
    foreach (user_ids() as $uid) {
        pool_update(function ($pool) use ($backfill_fn, &$computed, &$skipped) {
            $before = array_filter($pool, fn($i) => is_array($i) && ($i['phash'] ?? '') === '');
            $after = $backfill_fn($pool);
            $changed = count($before) - count(array_filter($after, fn($i) => is_array($i) && ($i['phash'] ?? '') === ''));
            $computed += $changed;
            $skipped += count($before) - $changed;
            return $after !== $pool ? $after : null;
        }, $uid);
        history_update(function ($history) use ($backfill_fn, &$computed, &$skipped) {
            $updated = false;
            foreach (HISTORY_COLLECTIONS as $c) {
                $before = array_filter($history[$c] ?? [], fn($i) => is_array($i) && ($i['phash'] ?? '') === '');
                $history[$c] = $backfill_fn($history[$c] ?? []);
                $changed = count($before) - count(array_filter($history[$c], fn($i) => is_array($i) && ($i['phash'] ?? '') === ''));
                $computed += $changed;
                $skipped += count($before) - $changed;
                if ($changed > 0) $updated = true;
            }
            return $updated ? $history : null;
        }, $uid);
    }
    echo "посчитано $computed, без локального файла $skipped\n";
    break;

case 'phash-report':
    $limit = max(1, (int)($args[0] ?? 30));
    $hashes = [];
    foreach (user_ids() as $uid) {
        $records = array_merge(pool_load($uid), ...array_values(history_load($uid)));
        foreach ($records as $item) {
            if (!is_array($item) || ($item['phash'] ?? '') === '') continue;
            $phash = $item['phash'];
            if (!isset($hashes[$phash])) $hashes[$phash] = [];
            $hashes[$phash][] = $item;
        }
    }
    $pairs = [];
    $keys = array_keys($hashes);
    for ($i = 0; $i < count($keys); $i++) {
        for ($j = $i + 1; $j < count($keys); $j++) {
            $dist = phash_distance($keys[$i], $keys[$j]);
            if ($dist <= PHASH_SAME_DISTANCE + 15) {
                foreach ($hashes[$keys[$i]] as $item1) {
                    foreach ($hashes[$keys[$j]] as $item2) {
                        if (content_image_key($item1) !== content_image_key($item2)) {
                            $pairs[] = ['distance' => $dist, 'path1' => media_cache_path($item1['image_local'] ?? ''), 'path2' => media_cache_path($item2['image_local'] ?? ''), 'source1' => $item1['source'] ?? '', 'source2' => $item2['source'] ?? ''];
                        }
                    }
                }
            }
        }
    }
    usort($pairs, fn($a, $b) => $a['distance'] <=> $b['distance']);
    $pairs = array_slice($pairs, 0, $limit);
    foreach ($pairs as $p) {
        printf("%d  %s  %s  ← (%s vs %s)\n", $p['distance'], $p['path1'], $p['path2'], $p['source1'], $p['source2']);
    }
    break;

case 'media-cleanup':
    $pool = all_users_pool_items();
    $removed = media_cache_cleanup($pool, false, $flag('force'));
    if ($removed > 0 || $flag('dry-run')) {
        echo "Кэш медиа: " . ($flag('dry-run') ? 'будет удалено' : 'удалено') . " $removed файлов.\n";
    }
    break;

case 'media-audit':
    $users = $args ? [$args[0]] : user_ids();
    $total_ok = 0;
    $total_missing = 0;
    $total_old_path = 0;
    $recoverable = 0;
    foreach ($users as $uid) {
        app_user_bind($uid);
        $pool = pool_load($uid);
        $history = history_load($uid);
        $items = array_merge($pool, ...array_values($history));
        $uid_ok = $uid_missing = $uid_old = $uid_recoverable = 0;

        foreach ($items as $r) {
            if (!is_array($r)) continue;
            foreach ([$r, ...(array)($r['gallery'] ?? [])] as $e) {
                if (!is_array($e)) continue;
                foreach (['image_local', 'video_local'] as $field) {
                    $url = (string)($e[$field] ?? '');
                    if ($url === '') continue;

                    // Старый путь media_cache/...
                    if (preg_match('~^media_cache/([a-f0-9]{32}\.[a-z0-9]{2,5})$~', $url, $m)) {
                        $file_path = MEDIA_CACHE_DIR . '/' . $m[1];
                        if (is_file($file_path)) {
                            $uid_old++;
                        } else {
                            $uid_missing++;
                        }
                    } elseif (media_cache_existing($url) !== '') {
                        $uid_ok++;
                    } else {
                        $uid_missing++;
                    }
                }
            }
        }

        // Счётчик восстанавливаемых: записи из Telegram или Двача без файла
        foreach ($items as $r) {
            if (!is_array($r) || (!empty($r['image_local']) && !empty($r['video_local']))) continue;
            if (preg_match('~^https://t\.me/~', (string)($r['source_url'] ?? ''))) {
                $uid_recoverable++;
            } elseif (!empty($r['board']) && !empty($r['thread_id']) && !empty($r['post_id'])) {
                $uid_recoverable++;
            }
        }

        echo $short($uid) . ": файл на месте $uid_ok, файла нет $uid_missing, старый путь $uid_old\n";
        $total_ok += $uid_ok;
        $total_missing += $uid_missing;
        $total_old_path += $uid_old;
        $recoverable += $uid_recoverable;
    }
    if (count($users) > 1) {
        echo "Итого: файл на месте $total_ok, файла нет $total_missing, старый путь $total_old_path\n";
    }
    echo "Можно попробовать вернуть: $recoverable записей из источников (Telegram и Двач)\n";
    break;

case 'media-repair':
    $total = ['fixed' => 0, 'cleared' => 0, 'marked' => 0];
    foreach ($args ? [$args[0]] : user_ids() as $uid) {
        $st = media_repair_user($uid, $flag('dry-run'));
        foreach ($st as $k => $v) $total[$k] += $v;
        if (array_sum($st)) printf("%s: путей исправлено %d, пропавших ссылок очищено %d, помечено на загрузку %d\n", $short($uid), $st['fixed'], $st['cleared'], $st['marked']);
    }
    printf("%sИтого: путей исправлено %d, ссылок очищено %d, помечено на загрузку %d\n", $flag('dry-run') ? '(--dry-run, без изменений) ' : '', $total['fixed'], $total['cleared'], $total['marked']);
    break;

case 'leads-scan':
    if ($flag('all')) {
        // Все пользователи с включённым радаром
        $uids = array_filter(
            array_map(fn($row) => preg_match('/^lead_radar:(.+)$/', $row['key'], $m) ? $m[1] : null, db_all("SELECT key FROM kv WHERE key LIKE 'lead_radar:%'")),
            fn($uid) => (lead_radar_settings($uid)['enabled'] ?? false)
        );
    } else {
        $uids = $args ? [$args[0]] : [app_user_id()];
    }

    $log_arg = null;
    foreach ($argv as $arg) if (str_starts_with($arg, '--log=')) $log_arg = substr($arg, 6);
    foreach ($uids as $uid) {
        if (lead_radar_is_running($uid)) { printf("%s: уже идёт\n", $short($uid)); continue; }
        // Журнал шагов: в stdout и в файл (по умолчанию — тот, что читает админка), в начале прохода файл перезаписывается
        $log_file = $log_arg ?? lead_radar_log_path($uid);
        file_put_contents($log_file, '');
        $log = function ($line) use ($log_file) {
            echo $line . "\n";
            file_put_contents($log_file, (new DateTime('now', radar_log_timezone()))->format('H:i:s') . ' ' . $line . "\n", FILE_APPEND);
        };
        try {
            $result = lead_radar_run($uid, $log);
            if (isset($result['skipped'])) $log($short($uid) . ': ' . $result['skipped']);
        } catch (Exception $e) {
            $log($short($uid) . ': исключение: ' . $e->getMessage());
        }
    }
    break;

case 'history-dedupe':
    $dry = in_array('--dry-run', $argv, true);
    $total = 0;
    foreach (array_column(db_all('SELECT DISTINCT user_id FROM history_items'), 'user_id') as $uid) {
        $n = history_dedupe($uid, $dry);
        if ($n) echo "{$uid}: " . ($dry ? 'повторов' : 'удалено') . " {$n}\n";
        $total += $n;
    }
    echo ($dry ? 'Повторов всего: ' : 'Удалено повторов: ') . $total . "\n";
    break;

case 'radar-owner':
    require_once __DIR__ . '/../lib/radar_admin.php';
    if ($args) radar_owner_set($args[0], !$flag('remove'));
    echo 'Владельцы (видят вкладку «Радар»): ' . (implode(', ', radar_owners()) ?: 'нет') . "\n";
    break;

default:
    echo preg_replace('/^\/\/ ?/m', '', implode("\n", array_slice(file(__FILE__, FILE_IGNORE_NEW_LINES), 1, 24))) . "\n";
}

/**
 * Метки записям пулов и истории, найденным до появления меток: по описанию ИИ и подписи, умной моделью без картинки.
 * Одна картинка — один запрос на всех пользователей. Не больше $limit запросов за запуск.
 */
function tag_content_backfill($limit, $dry_run) {
    $untagged = fn($r) => is_array($r) && !isset($r['tags']) && !in_array(trim((string)($r['description'] ?? '')), ['', 'Описание не получено'], true);
    [$found, $asked, $left] = [[], 0, 0];   // ключ картинки => метки
    foreach (user_ids() as $uid) {
        foreach (array_merge(pool_load($uid), ...array_values(history_load($uid))) as $r) {
            if (!$untagged($r) || isset($found[$k = content_image_key($r)])) continue;
            if ($asked >= $limit) { $left++; continue; }
            $asked++;
            if (($tags = content_tags_from_text($r['description'], post_caption_for_ai($r))) === null) { echo "  модель не ответила: $k\n"; continue; }
            $found[$k] = $tags;
            printf("  %s\n    → %s\n", mb_strimwidth($r['description'], 0, 90, '…'), implode(', ', array_column(content_tags_public($tags), 'path')) ?: 'нет подходящих');
        }
    }
    printf("Размечено записей: %d из %d запросов.%s\n", count($found), $asked, $left ? " Без меток осталось ещё ~{$left} (повторите команду)." : '');
    if ($dry_run || !$found) return;
    $tag = function (array $list, &$n) use ($found) {
        foreach ($list as &$r) if (is_array($r) && !isset($r['tags']) && isset($found[$k = content_image_key($r)])) { $r['tags'] = $found[$k]; $n++; }
        unset($r);
        return $list;
    };
    $saved = 0;
    foreach (user_ids() as $uid) {
        pool_update(function ($pool) use ($tag, &$saved) { $n = 0; $pool = $tag($pool, $n); $saved += $n; return $n ? $pool : null; }, $uid);
        history_update(function ($history) use ($tag, &$saved) {
            $n = 0;
            foreach (HISTORY_COLLECTIONS as $c) $history[$c] = $tag((array)($history[$c] ?? []), $n);
            $saved += $n;
            return $n ? $history : null;
        }, $uid);
    }
    echo "Сохранено в пулах и истории: {$saved}.\n";
}

/** Снимок активности для записи, найденной до включения сбора (страница канала или тред заново). */
function engagement_backfill_item(array $item, array &$cache) {
    if (preg_match('~^https://t\.me/([A-Za-z0-9_]+)/(\d+)$~', source_post_url($item), $m)) {
        $key = "tg:{$m[1]}:" . intdiv($id = (int)$m[2], 8);
        $cache[$key] ??= ($html = http_get("https://t.me/s/{$m[1]}?before=" . ($id + 11), [CURLOPT_USERAGENT => BROWSER_UA, CURLOPT_TIMEOUT => 25])) ? telegram_page_engagement($html) : [];
        return isset($cache[$key][$id]) ? telegram_engagement_snapshot($cache[$key], (string)$id, (int)($item['posted_at'] ?? 0)) : null;
    }
    if (empty($item['board']) || empty($item['thread_id']) || empty($item['post_id'])) return null;
    $key = "dv:{$item['board']}/{$item['thread_id']}";
    $cache["cat:{$item['board']}"] ??= fetch_2ch_json($item['board'], null, [CURLOPT_TIMEOUT => 25])['threads'] ?? [];
    $cache[$key] ??= ($posts = dvach_thread_posts(fetch_2ch_json($item['board'], (string)$item['thread_id'], [CURLOPT_TIMEOUT => 20]))) ? dvach_thread_engagement($posts, $cache["cat:{$item['board']}"]) : [];
    return $cache[$key][(string)$item['post_id']] ?? null;
}

function report_auc(array $scores, array $labels) {
    [$sum, $pairs] = [0, 0];
    foreach ($scores as $i => $p) foreach ($scores as $j => $n) {
        if ($p === null || $n === null || !$labels[$i] || $labels[$j]) continue;
        $sum += $p > $n ? 1 : ($p == $n ? 0.5 : 0);
        $pairs++;
    }
    return $pairs ? $sum / $pairs : null;
}

/** Числовые признаки по снимку активности. */
function report_features(array $e) {
    $react = fn(array $set) => array_sum(array_filter($e['reactions'] ?? [], fn($k) => in_array(mb_substr($k, 0, 1), $set, true), ARRAY_FILTER_USE_KEY));
    if (($e['source'] ?? '') === 'telegram') {
        [$views, $total] = [(int)($e['views'] ?? 0), array_sum($e['reactions'] ?? [])];
        [$mv, $mr] = [$e['neighbors_median_views'] ?? null, $e['neighbors_median_reaction_rate'] ?? null];
        return ['views_rel' => $mv === null ? null : log(($views + 1) / ($mv + 1)), 'react_rate_rel' => $mr === null ? null : log(($total / max(1, $views) + 0.002) / ($mr + 0.002)),
            'react_total' => log($total + 1), 'laugh_share' => $total ? $react(['😁', '🤣', '😂', '😆']) / $total : 0,
            'love_share' => $total ? $react(['❤', '🔥', '💯', '🥰', '😍', '🤩', '👍', '👏', '🎉', '⚡', '🏆']) / $total : 0,
            'has_negative' => $react(['👎', '💩', '🤮', '🤡', '🥱', '😴', '😢', '😭', '😡']) > 0 ? 1 : 0, 'age_hours' => $e['age_hours'] ?? null];
    }
    if (($e['source'] ?? '') !== 'dvach') return [];
    [$posts, $hours] = [(int)($e['thread_posts'] ?? 0), (float)($e['thread_hours'] ?? 0)];
    return ['thread_size_rel' => empty($e['board_median_thread_posts']) ? null : log(($posts + 1) / ($e['board_median_thread_posts'] + 1)),
        'thread_velocity_rel' => $hours > 0 && isset($e['board_median_velocity']) ? log(($posts / $hours + 0.1) / ($e['board_median_velocity'] + 0.1)) : null,
        'thread_files_share' => $posts ? ($e['thread_files'] ?? 0) / $posts : null]
        + (empty($e['is_op']) ? ['replies' => log(($e['replies'] ?? 0) + 1), 'has_reply' => ($e['replies'] ?? 0) > 0 ? 1 : 0,
            'reply_share' => ($e['posts_after'] ?? 0) > 0 ? ($e['replies'] ?? 0) / $e['posts_after'] : 0, 'position' => $e['index'] ?? null] : []);   // у ОП-поста ответов почти как постов в треде
}

function engagement_report(array $uids) {
    [$records, $total] = [[], 0];
    foreach ($uids as $uid) foreach (HISTORY_COLLECTIONS as $c) foreach (history_load($uid)[$c] ?? [] as $r) {
        $total++;
        if (!is_array($r['engagement'] ?? null) || !empty($r['engagement']['backfilled']) || ($c === 'complaints' && ($r['complaint_reason'] ?? '') !== 'match_error')) continue;
        $records[] = ['label' => $c, 'source' => $r['engagement']['source'] ?? '', 'relevance' => $r['relevance'] ?? null, 'features' => report_features($r['engagement'])];
    }
    printf("Оценок всего: %d, со снимком активности: %d\n", $total, count($records));
    if (!$records) exit("Снимки появляются у записей, найденных парсером после включения сбора. Подождите, пока накопятся оценки.\n");
    $tasks = ['Годно: лайк против дизлайка' => [fn($r) => $r['label'] === 'liked', fn($r) => in_array($r['label'], ['liked', 'disliked'], true), false],
        'Годно: лайк против дизлайков и жалоб' => [fn($r) => $r['label'] === 'liked', fn($r) => $r['label'] !== 'undecided', false],
        'Не по теме: жалоба «ошибка подбора» против остального' => [fn($r) => $r['label'] === 'complaints', fn($r) => true, true]];
    foreach (['telegram' => 'Telegram', 'dvach' => 'Двач'] as $source => $name) {
        $of = array_values(array_filter($records, fn($r) => $r['source'] === $source));
        printf("\n################ %s: %d записей с оценкой ################\n", $name, count($of));
        foreach ($tasks as $title => [$positive, $include, $invert]) {
            $rows = array_values(array_filter($of, $include));
            $labels = array_map(fn($r) => $positive($r) ? 1 : 0, $rows);
            [$yes, $no] = [array_sum($labels), count($labels) - array_sum($labels)];
            if ($yes < 15 || $no < 15) { printf("\n== %s: мало данных (положительных %d, остальных %d; нужно по 15)\n", $title, $yes, $no); continue; }
            printf("\n== %s (положительных %d, остальных %d)\n", $title, $yes, $no);
            printf("  %-22s AUC %.2f   ← оценка модели «по теме», для сравнения\n", 'relevance', report_auc(array_map(fn($r) => $r['relevance'] === null ? null : ($invert ? 100 - $r['relevance'] : $r['relevance']), $rows), $labels));
            foreach (array_unique(array_merge(...array_map(fn($r) => array_keys($r['features']), $rows))) as $f) {
                if (($a = report_auc(array_map(fn($r) => $r['features'][$f] ?? null, $rows), $labels)) === null) continue;
                printf("  %-22s AUC %.2f%s\n", $f, $a, abs($a - 0.5) >= 0.1 ? ($a > 0.5 ? '  ↑ выше — чаще положительное' : '  ↓ выше — реже положительное') : '');
            }
        }
    }
    echo "\nAUC: 0.5 — угадывание. Пока в классе меньше ~100 примеров, различия до ±0.1 — шум. Признак полезен, если стабильно\nдержится дальше 0.6 (или 0.4) и не слабее relevance.\n";
}

/** Переоценка найденных Telegram-каналов (предложения, с --catalog — и автодобавленные). Без --apply — только таблица. */
function rescore_sources($with_catalog, $apply) {
    $db = load_source_db();
    $targets = [];
    foreach ($db['source_suggestions'] as $s) if (($s['platform'] ?? '') === PLATFORM_TELEGRAM) $targets[] = ['kind' => 'предложение', 'id' => $s['id'], 'topic' => $s['topic'] ?? DEFAULT_TOPIC];
    foreach ($with_catalog ? $db['telegram_channels'] : [] as $e) if (!empty($e['auto'])) $targets[] = ['kind' => 'каталог', 'id' => ltrim($e['id'], '@'), 'topic' => $e['topics'][0] ?? DEFAULT_TOPIC];
    printf("Проверяю каналов: %d (%s)%s\n\n", count($targets), $with_catalog ? 'предложения и автодобавленные' : 'предложения', $apply ? ', результат будет применён' : ', без изменений');
    $results = [];
    foreach ($targets as $t) {
        $label = sprintf('%-9s %-24s ', $t['kind'], mb_substr($t['id'], 0, 24));
        $why = null;
        if (!($info = telegram_channel_info($t['id'], 14))) $why = 'канала нет или он закрыт';
        else {
            $rate = source_view_rate($info['subscribers'], $info['post_views']);
            if (($dead = source_inactivity_reason(source_activity($info['post_times'])) ?? source_dead_audience_reason($info['subscribers'], $rate)) !== null) $why = "мёртвый: $dead";
            elseif (!($ev = ($posts = array_values(array_filter($info['posts'], fn($p) => !is_post_outdated($p['posted_at'] ?? 0)))) ? evaluate_source($info, $posts, $t['topic']) : null)) $why = 'нет постов с картинками';
        }
        if ($why) { $results[] = $t + ['keep' => false]; echo $label . $why . "\n"; continue; }
        $q = source_quality_score($ev['score'], $info['subscribers'], $rate);
        $results[] = $t + ['keep' => $keep = $q['score'] >= SOURCE_SUGGEST_SCORE, 'score' => $q['score'], 'audience' => ['subscribers' => $info['subscribers'], 'view_rate' => $rate, 'checked_at' => time()]];
        printf("%sпо теме %3d%% (%s) %s → %3d%% — %s\n", $label, $q['content'], source_evaluation_basis($ev), describe_source_audience($info['subscribers'], $rate), $q['score'], $keep ? 'оставить' : 'убрать');
    }
    $removed = count(array_filter($results, fn($r) => !$r['keep']));
    printf("\nИтого: оставить %d, убрать %d.\n", count($results) - $removed, $removed);
    if (!$apply) exit('Чтобы применить: php tools/cli.php rescore-sources' . ($with_catalog ? ' --catalog' : '') . " --apply\n");
    $by = fn($kind) => array_column(array_map(fn($r) => ['id' => strtolower($r['id'])] + $r, array_filter($results, fn($r) => $r['kind'] === $kind)), null, 'id');
    [$sugg, $cat] = [$by('предложение'), $by('каталог')];
    update_source_db(function ($db) use ($sugg, $cat) {
        $db['source_suggestions'] = array_values(array_filter(array_map(fn($s) => ($r = $sugg[strtolower($s['id'] ?? '')] ?? null) ? ($r['keep'] ? ['score' => $r['score']] + $s : null) : $s, $db['source_suggestions'])));
        $db['telegram_channels'] = array_values(array_filter(array_map(fn($e) => !empty($e['auto']) && ($r = $cat[strtolower(ltrim($e['id'], '@'))] ?? null)
            ? ($r['keep'] ? ['score' => $r['score'], 'audience' => $r['audience']] + $e : null) : $e, $db['telegram_channels'])));
        return $db;
    });
    echo "Применено.\n";
}

/** Часовой пояс сервера для журнала радара: в CLI PHP без date.timezone берёт UTC, а владелец смотрит на местные часы. */
function radar_log_timezone(): DateTimeZone {
    $name = trim((string)@file_get_contents('/etc/timezone'));
    try { return new DateTimeZone($name !== '' ? $name : date_default_timezone_get()); } catch (Throwable) { return new DateTimeZone('UTC'); }
}
